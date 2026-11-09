package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/ermos/minato/internal/config"
	"github.com/ermos/minato/internal/secrets"
	"github.com/ermos/minato/internal/stack"
)

// SecretsChange is the body of POST /v1/stacks/{name}/secrets. Everything is
// applied in a single commit.
type SecretsChange struct {
	Set map[string]string `json:"set,omitempty"`
	// Random generates values of the given length.
	Random map[string]int `json:"random,omitempty"`
	Unset  []string       `json:"unset,omitempty"`
	// Rotate generates a new data key wrapped for the current recipients.
	Rotate bool `json:"rotate,omitempty"`
}

// SecretsInfo describes the secrets of a stack.
type SecretsInfo struct {
	Stack      string            `json:"stack"`
	File       string            `json:"file"`
	Keys       []string          `json:"keys"`
	Recipients []string          `json:"recipients"`
	Values     map[string]string `json:"values,omitempty"`
	Rev        string            `json:"rev,omitempty"`
}

func (d *Daemon) loadRepoStack(name string) (*stack.Stack, *config.RepoSpec, error) {
	spec, err := config.LoadRepoSpec(d.cfg.Repo.Dir)
	if err != nil {
		return nil, nil, err
	}
	if !servicePat.MatchString(name) || strings.HasPrefix(name, ".") {
		return nil, nil, fmt.Errorf("invalid stack name %q", name)
	}
	st, err := stack.Load(filepath.Join(d.cfg.Repo.Dir, spec.StacksDir, name))
	if err != nil {
		return nil, nil, fmt.Errorf("stack %s: %w", name, err)
	}
	return st, spec, nil
}

// ReadSecrets lists the secrets of a stack, with values when asked.
func (d *Daemon) ReadSecrets(name string, values bool) (*SecretsInfo, error) {
	st, _, err := d.loadRepoStack(name)
	if err != nil {
		return nil, err
	}
	info := &SecretsInfo{Stack: name, File: st.Spec.Secrets, Keys: []string{}}
	path := st.SecretsPath()
	if path == "" {
		return info, nil
	}
	e, err := secrets.ReadFile(path)
	if err != nil {
		return nil, err
	}
	info.Keys, info.Recipients = e.Keys(), e.Meta.Recipients
	if values {
		s, err := secrets.Open(e, d.ids)
		if err != nil {
			return nil, err
		}
		info.Values = s.Values()
	}
	return info, nil
}

// recipients returns the repository recipients plus the server key, which
// must always be able to decrypt.
func (d *Daemon) recipients(spec *config.RepoSpec) []string {
	out := append([]string(nil), spec.Secrets.Recipients...)
	for _, pub := range secrets.PublicKeys(d.ids) {
		if !slices.Contains(out, pub) {
			out = append(out, pub)
		}
	}
	return out
}

// ChangeSecrets edits the secret file of a stack in the git repository,
// commits and pushes it, then triggers a reconcile.
func (d *Daemon) ChangeSecrets(ctx context.Context, user, name string, ch SecretsChange) (*SecretsInfo, error) {
	if len(d.ids) == 0 {
		return nil, errors.New("the server has no age key")
	}
	d.syncMu.Lock()
	defer d.syncMu.Unlock()

	if _, err := d.repo.Sync(ctx); err != nil {
		return nil, err
	}
	st, spec, err := d.loadRepoStack(name)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(st.Dir, st.Spec.Secrets)
	recipients := d.recipients(spec)

	var s *secrets.Secrets
	e, err := secrets.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if s, err = secrets.New(recipients); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		if s, err = secrets.Open(e, d.ids); err != nil {
			return nil, fmt.Errorf("the server key cannot decrypt %s: %w", st.Spec.Secrets, err)
		}
	}

	var summary []string
	for k, v := range ch.Set {
		if err := s.Set(k, v); err != nil {
			return nil, err
		}
		summary = append(summary, "set "+k)
	}
	for k, n := range ch.Random {
		if err := s.Set(k, secrets.RandomValue(n)); err != nil {
			return nil, err
		}
		summary = append(summary, "generate "+k)
	}
	for _, k := range ch.Unset {
		if !s.Delete(k) {
			return nil, fmt.Errorf("secret %s not found", k)
		}
		summary = append(summary, "unset "+k)
	}
	if ch.Rotate {
		if err := s.Rotate(recipients); err != nil {
			return nil, err
		}
		summary = append(summary, "rotate data key")
	}
	if len(summary) == 0 {
		return nil, errors.New("nothing to change")
	}
	sort.Strings(summary)

	data, err := s.Marshal()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return nil, err
	}
	rel, _ := filepath.Rel(d.cfg.Repo.Dir, path)
	msg := fmt.Sprintf("chore(secrets): update %s secrets\n\n%s\n\nDone by %s through minato.", name, strings.Join(summary, "\n"), user)
	host, _ := os.Hostname()
	rev, err := d.repo.CommitAndPush(ctx, rel, msg, user, user+"@"+host)
	if err != nil {
		// Drop the local commit so the checkout matches the remote again.
		_, _ = d.repo.Sync(ctx)
		return nil, err
	}
	d.log.Info("secrets changed", "user", user, "stack", name, "changes", summary, "rev", short(rev))
	d.Trigger(false)

	keys := make([]string, 0)
	for k := range s.Values() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return &SecretsInfo{Stack: name, File: st.Spec.Secrets, Keys: keys, Recipients: s.Recipients(), Rev: rev}, nil
}
