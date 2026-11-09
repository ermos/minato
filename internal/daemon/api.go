package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"github.com/ermos/minato/internal/auth"
	"github.com/ermos/minato/internal/config"
	"github.com/ermos/minato/internal/docker"
	"github.com/ermos/minato/internal/secrets"
	"github.com/ermos/minato/internal/stack"
	"github.com/ermos/minato/internal/tlsutil"
)

var (
	credentialsRe = regexp.MustCompile(`://[^/@\s]+@`)
	servicePat    = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

// StatusResponse is returned by GET /v1/status.
type StatusResponse struct {
	State
	Stacks []StackState `json:"stacks"`
}

// ActionRequest is the body of POST /v1/stacks/{name}/{action}.
type ActionRequest struct {
	Services []string `json:"services"`
}

// ActionResponse is returned by actions and errors.
type ActionResponse struct {
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}

// SyncRequest is the body of POST /v1/sync.
type SyncRequest struct {
	Force bool `json:"force"`
}

// HookInfo describes a webhook endpoint.
type HookInfo struct {
	Stack    string   `json:"stack"`
	Name     string   `json:"name"`
	Action   string   `json:"action"`
	Services []string `json:"services,omitempty"`
	Path     string   `json:"path"`
	URL      string   `json:"url,omitempty"`
}

// Info is returned by GET /v1/info, without authentication.
type Info struct {
	Version     string   `json:"version"`
	Recipients  []string `json:"recipients"`
	Password    bool     `json:"password_login"`
	TLS         string   `json:"tls"`
	PublicURL   string   `json:"public_url,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`
}

// Identity is the caller of an API request.
type Identity struct {
	Name string    `json:"name"`
	Role auth.Role `json:"role"`
}

type identityKey struct{}

func identity(ctx context.Context) Identity {
	if id, ok := ctx.Value(identityKey{}).(Identity); ok {
		return id
	}
	return Identity{Name: "root", Role: auth.RoleAdmin}
}

type route struct {
	pattern string
	role    auth.Role
	handler http.HandlerFunc
}

// routes are served on the local socket (no auth) and on the public server
// (session token with at least the given role).
func (d *Daemon) routes() []route {
	return []route{
		{"GET /v1/status", auth.RoleViewer, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, StatusResponse{State: d.Status(), Stacks: d.Stacks()})
		}},
		{"GET /v1/hooks", auth.RoleViewer, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, d.Hooks())
		}},
		{"GET /v1/whoami", auth.RoleViewer, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, identity(r.Context()))
		}},
		{"POST /v1/sync", auth.RoleOperator, func(w http.ResponseWriter, r *http.Request) {
			var req SyncRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			report, err := d.Sync(r.Context(), req.Force)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, report)
		}},
		{"POST /v1/stacks/{name}/{action}", auth.RoleOperator, d.handleAction},
		{"GET /v1/stacks/{name}/compose", auth.RoleAdmin, func(w http.ResponseWriter, r *http.Request) {
			b := d.Bundle(r.PathValue("name"))
			if b == nil {
				writeError(w, http.StatusNotFound, errors.New("unknown stack"))
				return
			}
			w.Header().Set("Content-Type", "text/yaml")
			_, _ = w.Write(b.Files[b.ComposePath].Data)
		}},
		{"GET /v1/stacks/{name}/logs", auth.RoleViewer, d.handleLogs},
		{"GET /v1/stacks/{name}/ps", auth.RoleViewer, d.handlePs},
		{"GET /v1/stacks/{name}/secrets", auth.RoleViewer, d.handleSecretsList},
		{"GET /v1/stacks/{name}/secrets/values", auth.RoleAdmin, d.handleSecretsValues},
		{"POST /v1/stacks/{name}/secrets", auth.RoleOperator, d.handleSecretsChange},
	}
}

func (d *Daemon) handleAction(w http.ResponseWriter, r *http.Request) {
	action, err := docker.ParseAction(r.PathValue("action"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req ActionRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	d.log.Info("api action", "user", identity(r.Context()).Name, "stack", r.PathValue("name"), "action", action, "services", req.Services)
	out, err := d.Action(r.Context(), r.PathValue("name"), action, req.Services)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ActionResponse{Output: out, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ActionResponse{Output: out})
}

func (d *Daemon) stackState(name string) (StackState, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	s, ok := d.state.Stacks[name]
	if !ok || s.Project == "" {
		return StackState{}, false
	}
	return *s, true
}

func (d *Daemon) handleLogs(w http.ResponseWriter, r *http.Request) {
	st, ok := d.stackState(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("unknown stack"))
		return
	}
	q := r.URL.Query()
	tail := q.Get("tail")
	if _, err := strconv.Atoi(tail); err != nil {
		tail = "200"
	}
	service := q.Get("service")
	if service != "" && !servicePat.MatchString(service) {
		writeError(w, http.StatusBadRequest, errors.New("invalid service"))
		return
	}
	args := []string{"--tail", tail}
	if q.Get("follow") == "1" {
		args = append(args, "-f")
	}
	if st.Mode == stack.ModeSwarm {
		if service == "" {
			writeError(w, http.StatusBadRequest, errors.New("swarm mode: a service is required"))
			return
		}
		args = append(append([]string{"service", "logs"}, args...), st.Project+"_"+service)
	} else {
		args = append([]string{"compose", "-p", st.Project, "logs"}, args...)
		if service != "" {
			args = append(args, service)
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fw := &flushWriter{w: w}
	if f, ok := w.(http.Flusher); ok {
		fw.f = f
	}
	_ = d.docker.Exec(r.Context(), nil, fw, args...)
}

func (d *Daemon) handlePs(w http.ResponseWriter, r *http.Request) {
	st, ok := d.stackState(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("unknown stack"))
		return
	}
	args := []string{"compose", "-p", st.Project, "ps", "-a"}
	if st.Mode == stack.ModeSwarm {
		args = []string{"stack", "ps", st.Project}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_ = d.docker.Exec(r.Context(), nil, w, args...)
}

func (d *Daemon) handleSecretsList(w http.ResponseWriter, r *http.Request) {
	info, err := d.ReadSecrets(r.PathValue("name"), false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (d *Daemon) handleSecretsValues(w http.ResponseWriter, r *http.Request) {
	info, err := d.ReadSecrets(r.PathValue("name"), true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	d.log.Info("secret values read", "user", identity(r.Context()).Name, "stack", r.PathValue("name"))
	writeJSON(w, http.StatusOK, info)
}

func (d *Daemon) handleSecretsChange(w http.ResponseWriter, r *http.Request) {
	var ch SecretsChange
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&ch); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	info, err := d.ChangeSecrets(r.Context(), identity(r.Context()).Name, r.PathValue("name"), ch)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

type apiServer struct {
	srv *http.Server
	ln  net.Listener
}

// startAPI listens on the unix socket. Access is limited to root (0600) and
// needs no authentication: being root on the server is enough.
func (d *Daemon) startAPI() (*apiServer, error) {
	if err := os.MkdirAll(filepath.Dir(d.cfg.Socket), 0o755); err != nil {
		return nil, err
	}
	_ = os.Remove(d.cfg.Socket)
	ln, err := net.Listen("unix", d.cfg.Socket)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(d.cfg.Socket, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/info", d.handleInfo)
	for _, rt := range d.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	return &apiServer{srv: &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}, ln: ln}, nil
}

// publicHandler serves the remote API (authenticated) and the webhooks.
func (d *Daemon) publicHandler() http.Handler {
	mux := http.NewServeMux()
	d.registerWebhooks(mux)
	mux.HandleFunc("GET /v1/info", d.handleInfo)
	mux.HandleFunc("POST /v1/auth/login", d.handlePasswordLogin)
	mux.HandleFunc("POST /v1/auth/challenge", d.handleChallenge)
	mux.HandleFunc("POST /v1/auth/ssh", d.handleSSHLogin)
	mux.HandleFunc("POST /v1/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		d.sessions.Revoke(bearer(r))
		w.WriteHeader(http.StatusNoContent)
	})
	for _, rt := range d.routes() {
		mux.HandleFunc(rt.pattern, d.requireRole(rt.role, rt.handler))
	}
	return mux
}

// startPublic prepares the public server according to the TLS mode.
func (d *Daemon) startPublic() (*http.Server, func() error, error) {
	sc := d.cfg.Server
	srv := &http.Server{Addr: sc.Listen, Handler: d.publicHandler(), ReadHeaderTimeout: 10 * time.Second}
	var start func() error
	switch sc.TLS {
	case config.TLSSelfSigned:
		host, _ := os.Hostname()
		fp, err := tlsutil.EnsureSelfSigned(d.cfg.TLSDir(), sc.Domain, host)
		if err != nil {
			return nil, nil, err
		}
		cert, key := tlsutil.SelfSignedPaths(d.cfg.TLSDir())
		start = func() error { return srv.ListenAndServeTLS(cert, key) }
		d.log.Info("public server listening (self-signed TLS)", "addr", sc.Listen, "url", sc.PublicURL, "fingerprint", fp)
	case config.TLSFiles:
		start = func() error { return srv.ListenAndServeTLS(sc.CertFile, sc.KeyFile) }
		d.log.Info("public server listening (TLS)", "addr", sc.Listen, "url", sc.PublicURL)
	case config.TLSAcme:
		if sc.Domain == "" {
			return nil, nil, errors.New("server.domain is required with tls: acme")
		}
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(sc.Domain),
			Cache:      autocert.DirCache(filepath.Join(d.cfg.TLSDir(), "acme")),
			Email:      sc.AcmeEmail,
		}
		srv.TLSConfig = m.TLSConfig()
		go func() {
			if err := http.ListenAndServe(":80", m.HTTPHandler(nil)); err != nil {
				d.log.Warn("acme http-01 listener unavailable, relying on tls-alpn-01", "err", err)
			}
		}()
		start = func() error { return srv.ListenAndServeTLS("", "") }
		d.log.Info("public server listening (Let's Encrypt)", "addr", sc.Listen, "domain", sc.Domain)
	case config.TLSOff:
		start = srv.ListenAndServe
		d.log.Info("public server listening (plain HTTP, keep it behind a TLS reverse proxy)", "addr", sc.Listen, "url", sc.PublicURL)
	default:
		return nil, nil, fmt.Errorf("unknown server.tls %q", sc.TLS)
	}
	return srv, start, nil
}

func (d *Daemon) handleInfo(w http.ResponseWriter, r *http.Request) {
	info := Info{
		Version:    Version,
		Recipients: secrets.PublicKeys(d.ids),
		Password:   d.cfg.Auth.PasswordEnabled(),
		TLS:        d.cfg.Server.TLS,
		PublicURL:  d.cfg.Server.PublicURL,
	}
	if d.cfg.Server.TLS == config.TLSSelfSigned {
		info.Fingerprint, _ = tlsutil.EnsureSelfSigned(d.cfg.TLSDir())
	}
	writeJSON(w, http.StatusOK, info)
}

// requireRole authenticates a bearer session and checks the role.
func (d *Daemon) requireRole(role auth.Role, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := d.sessions.Lookup(bearer(r))
		if !ok {
			writeError(w, http.StatusUnauthorized, errors.New("not logged in or session expired, run `minato login`"))
			return
		}
		u, ok := d.users.Get(sess.User)
		if !ok {
			writeError(w, http.StatusUnauthorized, errors.New("user no longer exists"))
			return
		}
		if !u.Role.Allows(role) {
			writeError(w, http.StatusForbidden, fmt.Errorf("role %s required (you are %s)", role, u.Role))
			return
		}
		ctx := context.WithValue(r.Context(), identityKey{}, Identity{Name: u.Name, Role: u.Role})
		next(w, r.WithContext(ctx))
	}
}

// LoginResponse is returned on successful login.
type LoginResponse struct {
	Token   string    `json:"token"`
	User    string    `json:"user"`
	Role    auth.Role `json:"role"`
	Expires time.Time `json:"expires"`
}

// PasswordLogin is the body of POST /v1/auth/login.
type PasswordLogin struct {
	User     string `json:"user"`
	Password string `json:"password"`
}

// SSHLogin is the body of POST /v1/auth/ssh. Keys and signatures use the SSH
// wire format.
type SSHLogin struct {
	User      string `json:"user"`
	Nonce     string `json:"nonce"`
	PublicKey []byte `json:"public_key"`
	Signature []byte `json:"signature"`
}

func (d *Daemon) handlePasswordLogin(w http.ResponseWriter, r *http.Request) {
	ip := d.clientIP(r)
	if d.limiter.Blocked(ip) {
		writeError(w, http.StatusTooManyRequests, errors.New("too many failed attempts, try later"))
		return
	}
	if !d.cfg.Auth.PasswordEnabled() {
		writeError(w, http.StatusForbidden, errors.New("password login is disabled on this server, use an SSH key"))
		return
	}
	var req PasswordLogin
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	u, ok := d.users.Get(req.User)
	if !ok || !u.CheckPassword(req.Password) {
		d.loginFailed(w, ip, req.User, "password")
		return
	}
	d.loginOK(w, ip, u, "password")
}

func (d *Daemon) handleChallenge(w http.ResponseWriter, r *http.Request) {
	var req struct {
		User string `json:"user"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil || req.User == "" {
		writeError(w, http.StatusBadRequest, errors.New("user is required"))
		return
	}
	// A nonce is issued for unknown users too, so users cannot be enumerated.
	writeJSON(w, http.StatusOK, map[string]string{"nonce": d.sshAuth.Challenges.Issue(req.User)})
}

func (d *Daemon) handleSSHLogin(w http.ResponseWriter, r *http.Request) {
	ip := d.clientIP(r)
	if d.limiter.Blocked(ip) {
		writeError(w, http.StatusTooManyRequests, errors.New("too many failed attempts, try later"))
		return
	}
	var req SSHLogin
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	u, ok := d.users.Get(req.User)
	if !ok {
		d.loginFailed(w, ip, req.User, "ssh")
		return
	}
	if err := d.sshAuth.Verify(u, req.Nonce, req.PublicKey, req.Signature); err != nil {
		d.log.Debug("ssh login rejected", "user", req.User, "err", err)
		d.loginFailed(w, ip, req.User, "ssh")
		return
	}
	d.loginOK(w, ip, u, "ssh")
}

func (d *Daemon) loginFailed(w http.ResponseWriter, ip, user, method string) {
	d.limiter.Fail(ip)
	d.log.Warn("login failed", "user", user, "method", method, "remote", ip)
	time.Sleep(300 * time.Millisecond)
	writeError(w, http.StatusUnauthorized, errors.New("invalid credentials"))
}

func (d *Daemon) loginOK(w http.ResponseWriter, ip string, u *auth.User, method string) {
	d.limiter.Reset(ip)
	ttl := d.cfg.Auth.SessionTTL
	token, err := d.sessions.Create(auth.Session{User: u.Name, Method: method, Remote: ip}, ttl)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	d.log.Info("login", "user", u.Name, "method", method, "remote", ip)
	writeJSON(w, http.StatusOK, LoginResponse{Token: token, User: u.Name, Role: u.Role, Expires: time.Now().Add(ttl)})
}

func (d *Daemon) clientIP(r *http.Request) string {
	if d.cfg.Server.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func bearer(r *http.Request) string {
	if t, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return strings.TrimSpace(t)
	}
	return ""
}

// Hooks lists every webhook endpoint (tokens are never exposed).
func (d *Daemon) Hooks() []HookInfo {
	d.mu.RLock()
	defer d.mu.RUnlock()
	base := d.cfg.Server.PublicURL
	out := []HookInfo{{Stack: "*", Name: "sync", Action: "git sync + reconcile", Path: "/hooks/sync"}}
	for name, b := range d.bundles {
		for hook, h := range b.Webhooks {
			out = append(out, HookInfo{
				Stack: name, Name: hook, Action: h.Action, Services: h.Services,
				Path: "/hooks/" + name + "/" + hook,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	for i := range out {
		if base != "" {
			out[i].URL = base + out[i].Path
		}
	}
	return out
}

type flushWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func (fw *flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if fw.f != nil {
		fw.f.Flush()
	}
	return n, err
}

func serve(srv *http.Server, ln net.Listener) error {
	err := srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, ActionResponse{Error: err.Error()})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
