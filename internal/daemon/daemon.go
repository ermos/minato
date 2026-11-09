// Package daemon implements the minato reconciliation loop, the local API
// and the webhook server.
package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"filippo.io/age"

	"github.com/ermos/minato/internal/auth"
	"github.com/ermos/minato/internal/config"
	"github.com/ermos/minato/internal/docker"
	"github.com/ermos/minato/internal/gitsync"
	"github.com/ermos/minato/internal/render"
	"github.com/ermos/minato/internal/secrets"
	"github.com/ermos/minato/internal/stack"
)

// Daemon reconciles the git repository with docker.
type Daemon struct {
	cfg    *config.Config
	log    *slog.Logger
	docker *docker.Client
	repo   *gitsync.Repo
	ids    []age.Identity

	syncMu     sync.Mutex
	stackLocks sync.Map

	mu      sync.RWMutex
	state   *State
	bundles map[string]*render.Bundle

	trigger chan bool
	baseCtx context.Context

	users    *auth.Users
	sessions *auth.Sessions
	sshAuth  *auth.SSHVerifier
	limiter  *auth.Limiter
}

// Version is reported by /v1/info.
var Version = "dev"

// New creates a daemon.
func New(cfg *config.Config, log *slog.Logger) (*Daemon, error) {
	state, err := loadState(cfg.StatePath())
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	// Secrets are optional: a repository without secret files needs no key.
	ids, err := secrets.LoadIdentities(cfg.AgeKey)
	if err != nil {
		log.Warn("no age identity loaded, stacks with secrets will fail", "key", cfg.AgeKey)
	}
	users, err := auth.LoadUsers(cfg.Auth.UsersFile)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	sessions, err := auth.LoadSessions(cfg.SessionsPath())
	if err != nil {
		return nil, fmt.Errorf("load sessions: %w", err)
	}
	cas, err := auth.ParseCAs(cfg.Auth.SSHCAs)
	if err != nil {
		return nil, err
	}
	return &Daemon{
		users:    users,
		sessions: sessions,
		sshAuth:  &auth.SSHVerifier{Challenges: auth.NewChallenges(), CAs: cas},
		limiter:  auth.NewLimiter(10, 15*time.Minute),
		cfg:      cfg,
		log:      log,
		docker:   &docker.Client{Bin: cfg.Docker.Bin},
		repo:     &gitsync.Repo{URL: cfg.Repo.URL, Branch: cfg.Repo.Branch, Dir: cfg.Repo.Dir, SSHKey: cfg.Repo.SSHKey},
		ids:      ids,
		state:    state,
		bundles:  map[string]*render.Bundle{},
		trigger:  make(chan bool, 1),
	}, nil
}

// Run starts the API, the webhook server and the reconcile loop until ctx is
// cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	d.baseCtx = ctx
	if err := os.MkdirAll(d.cfg.StacksDir(), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(d.cfg.DataDir, 0o700); err != nil {
		return err
	}

	errc := make(chan error, 2)
	api, err := d.startAPI()
	if err != nil {
		return err
	}
	go func() { errc <- serve(api.srv, api.ln) }()
	var public *http.Server
	if d.cfg.Server.Listen != "" {
		var start func() error
		if public, start, err = d.startPublic(); err != nil {
			return err
		}
		go func() { errc <- start() }()
	}
	d.log.Info("minato daemon started", "repo", redactURL(d.cfg.Repo.URL), "branch", d.cfg.Repo.Branch, "poll", d.cfg.Repo.Poll)

	d.Trigger(false)
	var tick <-chan time.Time
	if d.cfg.Repo.Poll > 0 {
		t := time.NewTicker(d.cfg.Repo.Poll)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = api.srv.Shutdown(shutdown)
			if public != nil {
				_ = public.Shutdown(shutdown)
			}
			return nil
		case err := <-errc:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
		case <-tick:
			_, _ = d.Sync(ctx, false)
		case force := <-d.trigger:
			_, _ = d.Sync(ctx, force)
		}
	}
}

// Trigger asks the loop for a sync without waiting for it.
func (d *Daemon) Trigger(force bool) {
	select {
	case d.trigger <- force:
	default:
	}
}

// SyncReport summarizes a sync.
type SyncReport struct {
	Rev      string   `json:"rev"`
	Deployed []string `json:"deployed,omitempty"`
	Failed   []string `json:"failed,omitempty"`
	Removed  []string `json:"removed,omitempty"`
	Errors   []string `json:"errors,omitempty"`
}

// Sync pulls the repository and reconciles every stack. With force, every
// stack is redeployed even without change.
func (d *Daemon) Sync(ctx context.Context, force bool) (*SyncReport, error) {
	d.syncMu.Lock()
	defer d.syncMu.Unlock()
	report := &SyncReport{}

	rev, err := d.repo.Sync(ctx)
	if err != nil {
		d.log.Error("git sync failed", "err", err)
		d.mu.Lock()
		d.state.LastSync, d.state.LastSyncError = time.Now(), err.Error()
		d.saveLocked()
		d.mu.Unlock()
		return nil, err
	}
	report.Rev = rev

	repoSpec, err := config.LoadRepoSpec(d.cfg.Repo.Dir)
	if err != nil {
		return nil, err
	}
	stacks, loadErrs, err := stack.Discover(filepath.Join(d.cfg.Repo.Dir, repoSpec.StacksDir))
	if err != nil {
		return nil, err
	}

	present := map[string]bool{}
	for name, e := range loadErrs {
		present[name] = true
		report.Errors = append(report.Errors, e.Error())
		d.setStatus(name, StatusError, e.Error())
		d.log.Error("invalid stack", "stack", name, "err", e)
	}

	for _, st := range stacks {
		present[st.Name] = true
		b, err := render.Build(st, render.Options{
			OutDir:        filepath.Join(d.cfg.StacksDir(), st.Name),
			Identities:    d.ids,
			HostEnv:       hostEnv(),
			SharedSecrets: repoSpec.SharedSecretsPath(d.cfg.Repo.Dir),
		})
		if err != nil {
			report.Failed = append(report.Failed, st.Name)
			d.setStatus(st.Name, StatusError, err.Error())
			d.log.Error("render failed", "stack", st.Name, "err", err)
			d.notify(st.Name, false, err.Error(), rev)
			continue
		}
		d.mu.Lock()
		d.bundles[st.Name] = b
		prev := d.stackStateLocked(st)
		d.mu.Unlock()

		if st.Spec.Disabled {
			if prev.Hash != "" || prev.Status == StatusDeployed {
				d.log.Info("taking down disabled stack", "stack", st.Name)
				if _, err := d.run(ctx, b, docker.ActionDown, nil); err != nil {
					d.setStatus(st.Name, StatusError, err.Error())
					continue
				}
			}
			d.update(st.Name, func(s *StackState) { s.Status, s.Hash, s.Error = StatusDisabled, "", "" })
			continue
		}

		needed := force || prev.Hash != b.Hash || prev.Status == StatusError
		if !needed {
			continue
		}
		if !force && !st.AutoDeploy() {
			d.update(st.Name, func(s *StackState) { s.Status = StatusOutOfSync })
			continue
		}
		if err := d.deploy(ctx, b, rev); err != nil {
			report.Failed = append(report.Failed, st.Name)
		} else {
			report.Deployed = append(report.Deployed, st.Name)
		}
	}

	report.Removed = d.handleRemoved(ctx, present)

	d.mu.Lock()
	d.state.Rev, d.state.LastSync, d.state.LastSyncError = rev, time.Now(), ""
	d.saveLocked()
	d.mu.Unlock()
	d.log.Info("sync done", "rev", short(rev), "deployed", report.Deployed, "failed", report.Failed)
	return report, nil
}

func (d *Daemon) handleRemoved(ctx context.Context, present map[string]bool) []string {
	d.mu.RLock()
	var gone []StackState
	for name, s := range d.state.Stacks {
		if !present[name] && s.Status != StatusOrphaned {
			gone = append(gone, *s)
		}
	}
	d.mu.RUnlock()

	var removed []string
	for _, s := range gone {
		if !d.cfg.Prune {
			d.log.Warn("stack removed from git, left running (prune disabled)", "stack", s.Name)
			d.update(s.Name, func(st *StackState) { st.Status = StatusOrphaned })
			continue
		}
		d.log.Info("pruning stack removed from git", "stack", s.Name)
		var out bytes.Buffer
		var err error
		if s.Mode == stack.ModeSwarm {
			err = d.docker.Exec(ctx, nil, &out, "stack", "rm", s.Project)
		} else {
			err = d.docker.Exec(ctx, nil, &out, "compose", "-p", s.Project, "down", "--remove-orphans")
		}
		if err != nil {
			d.setStatus(s.Name, StatusError, err.Error()+"\n"+out.String())
			continue
		}
		_ = os.RemoveAll(filepath.Join(d.cfg.StacksDir(), s.Name))
		d.mu.Lock()
		delete(d.state.Stacks, s.Name)
		delete(d.bundles, s.Name)
		d.saveLocked()
		d.mu.Unlock()
		removed = append(removed, s.Name)
	}
	return removed
}

// deploy writes and deploys a bundle, then restarts services whose mounted
// files changed.
func (d *Daemon) deploy(ctx context.Context, b *render.Bundle, rev string) error {
	unlock := d.lock(b.Stack.Name)
	defer unlock()
	name := b.Stack.Name
	d.log.Info("deploying", "stack", name, "rev", short(rev))

	var out bytes.Buffer
	wr, err := b.Write()
	if err == nil {
		err = d.docker.Run(ctx, b, docker.ActionDeploy, nil, &out)
	}
	if err == nil && !wr.Fresh && b.Stack.Spec.Files.OnChange != "none" {
		if affected := b.ServicesUsing(wr.Changed); len(affected) > 0 {
			action := docker.Action(b.Stack.Spec.Files.OnChange)
			d.log.Info("files changed, "+string(action)+"ing services", "stack", name, "services", affected)
			err = d.docker.Run(ctx, b, action, affected, &out)
		}
	}
	if err != nil {
		msg := err.Error() + "\n" + tail(out.String(), 2000)
		d.log.Error("deploy failed", "stack", name, "err", err, "output", tail(out.String(), 2000))
		d.setStatus(name, StatusError, msg)
		d.notify(name, false, msg, rev)
		return err
	}
	d.update(name, func(s *StackState) {
		s.Status, s.Error, s.Hash, s.Rev, s.DeployedAt = StatusDeployed, "", b.Hash, rev, time.Now()
	})
	d.notify(name, true, "deployed", rev)
	return nil
}

// Action runs an action on a stack using its latest rendered version.
func (d *Daemon) Action(ctx context.Context, name string, action docker.Action, services []string) (string, error) {
	d.mu.RLock()
	b := d.bundles[name]
	rev := d.state.Rev
	d.mu.RUnlock()
	if b == nil {
		return "", fmt.Errorf("unknown or invalid stack %q", name)
	}
	if action == docker.ActionDeploy && len(services) == 0 {
		err := d.deploy(ctx, b, rev)
		d.saveState()
		if err != nil {
			d.mu.RLock()
			msg := d.state.Stacks[name].Error
			d.mu.RUnlock()
			return msg, err
		}
		return "deployed " + name, nil
	}
	unlock := d.lock(name)
	defer unlock()
	out, err := d.run(ctx, b, action, services)
	if err != nil {
		d.notify(name, false, fmt.Sprintf("%s failed: %v", action, err), rev)
		return out, err
	}
	d.update(name, func(s *StackState) {
		switch {
		case action == docker.ActionDown:
			s.Status, s.Hash = StatusDown, ""
		case len(services) == 0 && action != docker.ActionRestart:
			s.Status, s.Error, s.Hash, s.Rev, s.DeployedAt = StatusDeployed, "", b.Hash, rev, time.Now()
		}
	})
	d.saveState()
	d.log.Info("action done", "stack", name, "action", action, "services", services)
	return out, nil
}

// run writes the bundle when needed and executes the action.
func (d *Daemon) run(ctx context.Context, b *render.Bundle, action docker.Action, services []string) (string, error) {
	var out bytes.Buffer
	if action != docker.ActionRestart && action != docker.ActionDown {
		if _, err := b.Write(); err != nil {
			return "", err
		}
	}
	err := d.docker.Run(ctx, b, action, services, &out)
	return out.String(), err
}

// Bundle returns the latest rendered version of a stack.
func (d *Daemon) Bundle(name string) *render.Bundle {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.bundles[name]
}

// Status returns a snapshot of the state.
func (d *Daemon) Status() State {
	d.mu.RLock()
	defer d.mu.RUnlock()
	s := *d.state
	s.Stacks = nil
	return s
}

// Stacks returns the stack states sorted by name.
func (d *Daemon) Stacks() []StackState {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.state.sortedStacks()
}

func (d *Daemon) lock(name string) func() {
	m, _ := d.stackLocks.LoadOrStore(name, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (d *Daemon) stackStateLocked(st *stack.Stack) StackState {
	s, ok := d.state.Stacks[st.Name]
	if !ok {
		s = &StackState{Name: st.Name}
		d.state.Stacks[st.Name] = s
	}
	s.Project, s.Mode, s.AutoDeploy = st.Project(), st.Spec.Mode, st.AutoDeploy()
	return *s
}

func (d *Daemon) update(name string, fn func(*StackState)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s, ok := d.state.Stacks[name]
	if !ok {
		s = &StackState{Name: name}
		d.state.Stacks[name] = s
	}
	fn(s)
}

func (d *Daemon) setStatus(name, status, msg string) {
	d.update(name, func(s *StackState) { s.Status, s.Error = status, msg })
}

func (d *Daemon) saveState() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.saveLocked()
}

func (d *Daemon) saveLocked() {
	if err := d.state.save(d.cfg.StatePath()); err != nil {
		d.log.Error("save state", "err", err)
	}
}

// notify posts a Slack/Discord compatible message.
func (d *Daemon) notify(stackName string, ok bool, msg, rev string) {
	n := d.cfg.Notify
	event := "failure"
	icon := "❌"
	if ok {
		event, icon = "success", "✅"
	}
	if n.URL == "" || !slices.Contains(n.On, event) {
		return
	}
	text := fmt.Sprintf("%s minato %s (%s): %s", icon, stackName, short(rev), tail(msg, 1500))
	body, _ := json.Marshal(map[string]string{
		"text": text, "content": text, "stack": stackName, "status": event, "rev": rev,
	})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			d.log.Warn("notification failed", "err", err)
			return
		}
		_ = resp.Body.Close()
	}()
}

func hostEnv() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return env
}

func short(rev string) string {
	if len(rev) > 8 {
		return rev[:8]
	}
	return rev
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return "..." + s[len(s)-n:]
	}
	return s
}

func redactURL(u string) string { return credentialsRe.ReplaceAllString(u, "://***@") }
