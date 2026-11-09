package client

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

// Remote is a saved remote server.
type Remote struct {
	URL         string    `yaml:"url"`
	User        string    `yaml:"user"`
	Fingerprint string    `yaml:"fingerprint,omitempty"`
	Token       string    `yaml:"token,omitempty"`
	Expires     time.Time `yaml:"expires,omitempty"`
}

// Remotes is the client configuration (~/.config/minato/remotes.yml).
type Remotes struct {
	path    string
	Current string             `yaml:"current"`
	Servers map[string]*Remote `yaml:"servers"`
}

// RemotesPath returns the client configuration path.
func RemotesPath() string {
	if p := os.Getenv("MINATO_REMOTES"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "minato", "remotes.yml")
}

// LoadRemotes reads the client configuration.
func LoadRemotes() (*Remotes, error) {
	r := &Remotes{path: RemotesPath(), Servers: map[string]*Remote{}}
	data, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, r); err != nil {
		return nil, fmt.Errorf("parse %s: %w", r.path, err)
	}
	if r.Servers == nil {
		r.Servers = map[string]*Remote{}
	}
	return r, nil
}

// Save writes the configuration with mode 0600 (it holds tokens).
func (r *Remotes) Save() error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(r)
	if err != nil {
		return err
	}
	return os.WriteFile(r.path, data, 0o600)
}

// Get returns a remote by name, or the current one when name is empty.
func (r *Remotes) Get(name string) (string, *Remote, error) {
	if name == "" {
		name = r.Current
	}
	if name == "" {
		return "", nil, errors.New("no remote selected")
	}
	rem, ok := r.Servers[name]
	if !ok {
		return "", nil, fmt.Errorf("unknown remote %q (see `minato remote ls`)", name)
	}
	return name, rem, nil
}

// Names returns the sorted remote names.
func (r *Remotes) Names() []string {
	out := make([]string, 0, len(r.Servers))
	for n := range r.Servers {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Client builds an API client for a remote.
func (rem *Remote) Client() *Client { return NewRemote(rem.URL, rem.Fingerprint, rem.Token) }
