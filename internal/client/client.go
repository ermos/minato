// Package client talks to the minato daemon, either on the local unix socket
// (no authentication) or remotely over HTTPS with a session token.
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ermos/minato/internal/daemon"
	"github.com/ermos/minato/internal/tlsutil"
)

// Client is a daemon API client.
type Client struct {
	base   string
	where  string
	token  string
	remote bool
	http   *http.Client
}

// NewSocket creates a client for the local unix socket.
func NewSocket(socket string) *Client {
	return &Client{
		base:  "http://minato",
		where: socket,
		http: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			},
		}},
	}
}

// NewRemote creates a client for a remote server. When fingerprint is set the
// server certificate is pinned instead of being verified against system CAs.
func NewRemote(rawURL, fingerprint, token string) *Client {
	return &Client{
		base:   strings.TrimSuffix(rawURL, "/"),
		where:  rawURL,
		token:  token,
		remote: true,
		http: &http.Client{Transport: &http.Transport{
			TLSClientConfig: TLSConfig(fingerprint),
			Proxy:           http.ProxyFromEnvironment,
		}},
	}
}

// TLSConfig returns a TLS configuration pinning fingerprint when set.
func TLSConfig(fingerprint string) *tls.Config {
	if fingerprint == "" {
		return &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // replaced by the pinning check below
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("no server certificate")
			}
			if got := tlsutil.Fingerprint(cs.PeerCertificates[0].Raw); got != fingerprint {
				return fmt.Errorf("server certificate changed: expected %s, got %s (possible man in the middle)", fingerprint, got)
			}
			return nil
		},
	}
}

// ProbeCertificate connects to a https URL and returns the certificate
// fingerprint, and whether the certificate is trusted by the system.
func ProbeCertificate(rawURL string) (fingerprint string, trusted bool, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", false, err
	}
	if u.Scheme != "https" {
		return "", true, nil
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "443")
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if conn, err := tls.DialWithDialer(dialer, "tcp", host, &tls.Config{ServerName: u.Hostname()}); err == nil {
		defer func() { _ = conn.Close() }()
		return tlsutil.Fingerprint(conn.ConnectionState().PeerCertificates[0].Raw), true, nil
	}
	conn, err := tls.DialWithDialer(dialer, "tcp", host, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		return "", false, err
	}
	defer func() { _ = conn.Close() }()
	return tlsutil.Fingerprint(conn.ConnectionState().PeerCertificates[0].Raw), false, nil
}

// IsRemote reports whether the client talks to a remote server.
func (c *Client) IsRemote() bool { return c.remote }

// Where describes the endpoint.
func (c *Client) Where() string { return c.where }

// APIError is an error returned by the daemon.
type APIError struct {
	Status  int
	Message string
	Output  string
}

func (e *APIError) Error() string { return e.Message }

func (c *Client) request(method, path string, in any) (*http.Response, error) {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if !c.remote {
			if errors.Is(err, os.ErrPermission) {
				return nil, fmt.Errorf("permission denied on %s (use sudo, or `minato login` to go through the remote API)", c.where)
			}
			return nil, fmt.Errorf("cannot reach the daemon on %s (is it running? for remote access run `minato login`): %w", c.where, err)
		}
		return nil, fmt.Errorf("cannot reach %s: %w", c.where, err)
	}
	if resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		data, _ := io.ReadAll(resp.Body)
		var e daemon.ActionResponse
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return nil, &APIError{Status: resp.StatusCode, Message: e.Error, Output: e.Output}
		}
		return nil, &APIError{Status: resp.StatusCode, Message: resp.Status}
	}
	return resp, nil
}

func (c *Client) do(method, path string, in, out any) error {
	resp, err := c.request(method, path, in)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	switch o := out.(type) {
	case nil:
		return nil
	case *string:
		*o = string(data)
		return nil
	default:
		return json.Unmarshal(data, out)
	}
}

func stackPath(stack, suffix string) string {
	return "/v1/stacks/" + url.PathEscape(stack) + suffix
}

// Info returns public server information.
func (c *Client) Info() (*daemon.Info, error) {
	var i daemon.Info
	return &i, c.do(http.MethodGet, "/v1/info", nil, &i)
}

// Whoami returns the authenticated identity.
func (c *Client) Whoami() (*daemon.Identity, error) {
	var i daemon.Identity
	return &i, c.do(http.MethodGet, "/v1/whoami", nil, &i)
}

// PasswordLogin opens a session with a password.
func (c *Client) PasswordLogin(user, password string) (*daemon.LoginResponse, error) {
	var r daemon.LoginResponse
	return &r, c.do(http.MethodPost, "/v1/auth/login", daemon.PasswordLogin{User: user, Password: password}, &r)
}

// Challenge asks a nonce for SSH login.
func (c *Client) Challenge(user string) (string, error) {
	var r struct {
		Nonce string `json:"nonce"`
	}
	return r.Nonce, c.do(http.MethodPost, "/v1/auth/challenge", map[string]string{"user": user}, &r)
}

// SSHLogin opens a session with a signed challenge.
func (c *Client) SSHLogin(req daemon.SSHLogin) (*daemon.LoginResponse, error) {
	var r daemon.LoginResponse
	return &r, c.do(http.MethodPost, "/v1/auth/ssh", req, &r)
}

// Logout revokes the session.
func (c *Client) Logout() error { return c.do(http.MethodPost, "/v1/auth/logout", nil, nil) }

// Status returns the daemon status.
func (c *Client) Status() (*daemon.StatusResponse, error) {
	var s daemon.StatusResponse
	return &s, c.do(http.MethodGet, "/v1/status", nil, &s)
}

// Sync triggers a git sync and reconciliation.
func (c *Client) Sync(force bool) (*daemon.SyncReport, error) {
	var r daemon.SyncReport
	return &r, c.do(http.MethodPost, "/v1/sync", daemon.SyncRequest{Force: force}, &r)
}

// Action runs an action on a stack.
func (c *Client) Action(stack, action string, services []string) (*daemon.ActionResponse, error) {
	var r daemon.ActionResponse
	err := c.do(http.MethodPost, stackPath(stack, "/"+action), daemon.ActionRequest{Services: services}, &r)
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		r.Output = apiErr.Output
	}
	return &r, err
}

// Compose returns the rendered compose file of a stack.
func (c *Client) Compose(stack string) (string, error) {
	var s string
	return s, c.do(http.MethodGet, stackPath(stack, "/compose"), nil, &s)
}

// Hooks lists webhook endpoints.
func (c *Client) Hooks() ([]daemon.HookInfo, error) {
	var h []daemon.HookInfo
	return h, c.do(http.MethodGet, "/v1/hooks", nil, &h)
}

// Logs streams the logs of a stack to w.
func (c *Client) Logs(stack, service string, tail int, follow bool, w io.Writer) error {
	q := url.Values{"tail": {strconv.Itoa(tail)}}
	if service != "" {
		q.Set("service", service)
	}
	if follow {
		q.Set("follow", "1")
	}
	resp, err := c.request(http.MethodGet, stackPath(stack, "/logs?"+q.Encode()), nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, err = io.Copy(w, resp.Body)
	return err
}

// Ps returns the containers of a stack.
func (c *Client) Ps(stack string) (string, error) {
	var s string
	return s, c.do(http.MethodGet, stackPath(stack, "/ps"), nil, &s)
}

// Secrets lists the secret names of a stack, with values when asked (admin).
func (c *Client) Secrets(stack string, values bool) (*daemon.SecretsInfo, error) {
	var s daemon.SecretsInfo
	suffix := "/secrets"
	if values {
		suffix += "/values"
	}
	return &s, c.do(http.MethodGet, stackPath(stack, suffix), nil, &s)
}

// ChangeSecrets edits secrets on the server, which commits and pushes them.
func (c *Client) ChangeSecrets(stack string, ch daemon.SecretsChange) (*daemon.SecretsInfo, error) {
	var s daemon.SecretsInfo
	return &s, c.do(http.MethodPost, stackPath(stack, "/secrets"), ch, &s)
}
