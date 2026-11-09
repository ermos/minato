package daemon

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ermos/minato/internal/docker"
)

const maxHookBody = 1 << 20

// registerWebhooks adds the public webhook endpoints:
//
//	POST /hooks/sync              git pull + reconcile every stack
//	POST /hooks/{stack}/{hook}    hook declared in the stack minato.yml
//	GET  /healthz                 liveness
//
// Add ?wait=1 to run synchronously and get the result (useful in CI).
func (d *Daemon) registerWebhooks(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("POST /hooks/sync", d.handleSyncHook)
	mux.HandleFunc("POST /hooks/{stack}/{hook}", d.handleStackHook)
}

func (d *Daemon) handleSyncHook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxHookBody))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	cfg := d.cfg.Webhooks
	if !authorized(r, body, cfg.Token, cfg.Secret) {
		d.log.Warn("unauthorized webhook", "path", r.URL.Path, "remote", r.RemoteAddr)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if ignored, reason := d.ignoredGitEvent(r, body); ignored {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": reason})
		return
	}
	force := r.URL.Query().Get("force") == "1"
	if !wantWait(r) {
		d.Trigger(force)
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
		return
	}
	report, err := d.Sync(r.Context(), force)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	code := http.StatusOK
	if len(report.Failed) > 0 {
		code = http.StatusInternalServerError
	}
	writeJSON(w, code, report)
}

func (d *Daemon) handleStackHook(w http.ResponseWriter, r *http.Request) {
	name, hookName := r.PathValue("stack"), r.PathValue("hook")
	body, err := io.ReadAll(io.LimitReader(r.Body, maxHookBody))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	b := d.Bundle(name)
	if b == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	hook, ok := b.Webhooks[hookName]
	if !ok || !authorized(r, body, hook.Token, hook.Secret) {
		d.log.Warn("unauthorized webhook", "path", r.URL.Path, "remote", r.RemoteAddr)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	services := hook.Services
	if q := r.URL.Query().Get("services"); q != "" {
		requested := strings.Split(q, ",")
		for _, s := range requested {
			if len(hook.Services) > 0 && !slices.Contains(hook.Services, s) {
				http.Error(w, "service "+s+" not allowed by this hook", http.StatusForbidden)
				return
			}
		}
		services = requested
	}

	action := docker.Action(hook.Action)
	run := func(ctx context.Context) (string, error) {
		// Pick up the latest commit first so hooks always act on git HEAD.
		if _, err := d.Sync(ctx, false); err != nil {
			return "", err
		}
		return d.Action(ctx, name, action, services)
	}
	d.log.Info("webhook triggered", "stack", name, "hook", hookName, "action", action, "services", services)
	if !wantWait(r) {
		go func() {
			ctx, cancel := context.WithTimeout(d.baseCtx, 30*time.Minute)
			defer cancel()
			if _, err := run(ctx); err != nil {
				d.log.Error("webhook action failed", "stack", name, "hook", hookName, "err", err)
			}
		}()
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
		return
	}
	out, err := run(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ActionResponse{Output: out, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ActionResponse{Output: out})
}

// ignoredGitEvent filters forge events that must not trigger a sync: pings
// and pushes to another branch.
func (d *Daemon) ignoredGitEvent(r *http.Request, body []byte) (bool, string) {
	event := r.Header.Get("X-GitHub-Event") + r.Header.Get("X-Gitea-Event") + r.Header.Get("X-Gitlab-Event")
	if event == "ping" {
		return true, "ping"
	}
	var payload struct {
		Ref string `json:"ref"`
	}
	if json.Unmarshal(body, &payload) == nil && strings.HasPrefix(payload.Ref, "refs/") {
		if payload.Ref != "refs/heads/"+d.cfg.Repo.Branch {
			return true, "ref " + payload.Ref + " is not the tracked branch"
		}
	}
	return false, ""
}

// authorized accepts either a matching token (Authorization bearer,
// X-Minato-Token, X-Gitlab-Token or ?token=) or a valid HMAC SHA-256 signature
// (X-Hub-Signature-256 for GitHub, X-Gitea-Signature / X-Gogs-Signature).
func authorized(r *http.Request, body []byte, token, secret string) bool {
	if token != "" {
		for _, candidate := range []string{
			strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "),
			r.Header.Get("X-Minato-Token"),
			r.Header.Get("X-Gitlab-Token"),
			r.URL.Query().Get("token"),
		} {
			if candidate != "" && subtle.ConstantTimeCompare([]byte(candidate), []byte(token)) == 1 {
				return true
			}
		}
	}
	if secret != "" {
		sig := strings.TrimPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256=")
		if sig == "" {
			sig = r.Header.Get("X-Gitea-Signature")
		}
		if sig == "" {
			sig = r.Header.Get("X-Gogs-Signature")
		}
		got, err := hex.DecodeString(sig)
		if err == nil && len(got) > 0 {
			m := hmac.New(sha256.New, []byte(secret))
			m.Write(body)
			if hmac.Equal(got, m.Sum(nil)) {
				return true
			}
		}
	}
	return false
}

func wantWait(r *http.Request) bool {
	v := r.URL.Query().Get("wait")
	return v == "1" || v == "true"
}
