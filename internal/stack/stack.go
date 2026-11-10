// Package stack loads stack definitions from the git repository.
//
// A stack is a folder containing a compose file and an optional minato.yml:
//
//	stacks/blog/
//	  compose.yml
//	  minato.yml            stack settings, variables, webhooks
//	  secrets.enc.yml    encrypted secrets (minato secrets edit)
//	  files/             files mounted into containers, *.tmpl are rendered
package stack

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	SpecFile           = "minato.yml"
	DefaultSecretsFile = "secrets.enc.yml"
	DefaultFilesDir    = "files"

	ModeCompose = "compose"
	ModeSwarm   = "swarm"
)

var (
	composeCandidates = []string{"compose.yml", "compose.yaml", "docker-compose.yml", "docker-compose.yaml"}
	namePattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
)

// Spec is the content of minato.yml.
type Spec struct {
	// Mode is compose (default) or swarm.
	Mode string `yaml:"mode"`
	// Project overrides the compose project / swarm stack name.
	Project string `yaml:"project"`
	// Compose file name, auto detected by default.
	Compose string `yaml:"compose"`
	// Disabled stacks are taken down and never deployed.
	Disabled bool `yaml:"disabled"`
	// Vars are free values usable as ${vars.NAME}. They may use ${secrets.*},
	// ${env.*} and ${stack.*}.
	Vars map[string]string `yaml:"vars"`
	// Env is exported to docker compose, usable as ${NAME} in the compose
	// file. Values may use every namespace.
	Env map[string]string `yaml:"env"`
	// Secrets file, defaults to secrets.enc.yml when present.
	Secrets  string             `yaml:"secrets"`
	Files    Files              `yaml:"files"`
	Deploy   Deploy             `yaml:"deploy"`
	Webhooks map[string]Webhook `yaml:"webhooks"`
}

// Files configures the attached files folder.
type Files struct {
	Dir string `yaml:"dir"`
	// Templates enables rendering of *.tmpl files (default true).
	Templates *bool `yaml:"templates"`
	// OnChange is applied to services mounting a changed file:
	// restart (default), recreate or none.
	OnChange string `yaml:"on_change"`
}

// Deploy configures how the stack is reconciled.
type Deploy struct {
	// Auto deploys on every git change (default true). When false the stack
	// is only deployed by the CLI or a webhook.
	Auto *bool `yaml:"auto"`
	// Pull policy on deploy: missing (default), always or never.
	Pull string `yaml:"pull"`
	// Wait for services to be healthy / converged.
	Wait bool `yaml:"wait"`
	// Rollout lists the compose services updated without downtime, keyed by
	// service name: the new container starts next to the old one, which is
	// only stopped once the new one is ready.
	Rollout map[string]Rollout `yaml:"rollout"`
}

// Rollout configures the rolling update of a compose service.
type Rollout struct {
	// Probe is an HTTP check minato runs against the new container. Without
	// it, the healthcheck of the container is used when it has one, otherwise
	// the container only has to stay up for 10 seconds.
	Probe *Probe `yaml:"probe"`
	// Timeout for the new container to be ready (default 2m).
	Timeout string `yaml:"timeout"`
	// Drain is the time both versions run once the new one is ready, so a
	// reverse proxy routes to it before the old one stops (default 5s).
	Drain string `yaml:"drain"`

	timeout, drain time.Duration
}

// TimeoutDuration returns the parsed timeout.
func (r Rollout) TimeoutDuration() time.Duration { return r.timeout }

// DrainDuration returns the parsed drain time.
func (r Rollout) DrainDuration() time.Duration { return r.drain }

// Probe is an HTTP GET on the container IP, ready on a 2xx answer.
type Probe struct {
	Port int    `yaml:"port"`
	Path string `yaml:"path"`
}

// Webhook is a stack level hook exposed on /hooks/<stack>/<name>.
type Webhook struct {
	// Action: deploy, pull, restart, recreate.
	Action string `yaml:"action"`
	// Services restricts the action. Empty means the whole stack.
	Services []string `yaml:"services"`
	// Token required in the Authorization bearer, X-Minato-Token header or
	// ?token= query. Usually ${secrets.SOMETHING}.
	Token string `yaml:"token"`
	// Secret validates an HMAC SHA-256 signature (GitHub, Gitea) instead.
	Secret string `yaml:"secret"`
}

// Stack is a loaded stack.
type Stack struct {
	Name        string
	Dir         string
	ComposeFile string
	Spec        Spec
}

// Project returns the compose project / swarm stack name.
func (s *Stack) Project() string {
	if s.Spec.Project != "" {
		return s.Spec.Project
	}
	return s.Name
}

// AutoDeploy reports whether git changes are deployed automatically.
func (s *Stack) AutoDeploy() bool { return s.Spec.Deploy.Auto == nil || *s.Spec.Deploy.Auto }

// Templates reports whether *.tmpl files are rendered.
func (s *Stack) Templates() bool { return s.Spec.Files.Templates == nil || *s.Spec.Files.Templates }

// FilesDir returns the files folder name relative to the stack.
func (s *Stack) FilesDir() string { return s.Spec.Files.Dir }

// SecretsPath returns the absolute path of the secrets file, or "" if none.
func (s *Stack) SecretsPath() string {
	p := filepath.Join(s.Dir, s.Spec.Secrets)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// Load loads the stack in dir.
func Load(dir string) (*Stack, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	st := &Stack{Name: filepath.Base(dir), Dir: dir}
	data, err := os.ReadFile(filepath.Join(dir, SpecFile))
	switch {
	case err == nil:
		if err := yaml.Unmarshal(data, &st.Spec); err != nil {
			return nil, fmt.Errorf("%s/%s: %w", st.Name, SpecFile, err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	if err := st.applyDefaults(); err != nil {
		return nil, fmt.Errorf("stack %s: %w", st.Name, err)
	}
	return st, nil
}

func (s *Stack) applyDefaults() error {
	sp := &s.Spec
	if !namePattern.MatchString(s.Project()) {
		return fmt.Errorf("invalid name %q (lowercase letters, digits, - and _)", s.Project())
	}
	switch sp.Mode {
	case "":
		sp.Mode = ModeCompose
	case ModeCompose, ModeSwarm:
	default:
		return fmt.Errorf("unknown mode %q", sp.Mode)
	}
	if sp.Compose == "" {
		for _, c := range composeCandidates {
			if _, err := os.Stat(filepath.Join(s.Dir, c)); err == nil {
				sp.Compose = c
				break
			}
		}
	}
	if sp.Compose == "" {
		return errors.New("no compose file found")
	}
	s.ComposeFile = filepath.Join(s.Dir, sp.Compose)
	if _, err := os.Stat(s.ComposeFile); err != nil {
		return fmt.Errorf("compose file: %w", err)
	}
	if sp.Secrets == "" {
		sp.Secrets = DefaultSecretsFile
	}
	if sp.Files.Dir == "" {
		sp.Files.Dir = DefaultFilesDir
	}
	switch sp.Files.OnChange {
	case "":
		sp.Files.OnChange = "restart"
	case "restart", "recreate", "none":
	default:
		return fmt.Errorf("files.on_change: unknown value %q", sp.Files.OnChange)
	}
	switch sp.Deploy.Pull {
	case "":
		sp.Deploy.Pull = "missing"
	case "missing", "always", "never":
	default:
		return fmt.Errorf("deploy.pull: unknown value %q", sp.Deploy.Pull)
	}
	if len(sp.Deploy.Rollout) > 0 && sp.Mode == ModeSwarm {
		return errors.New("deploy.rollout is for compose mode, swarm rolls out with deploy.update_config in the compose file")
	}
	for svc, r := range sp.Deploy.Rollout {
		var err error
		if r.timeout, err = duration(r.Timeout, 2*time.Minute); err != nil {
			return fmt.Errorf("deploy.rollout.%s.timeout: %w", svc, err)
		}
		if r.drain, err = duration(r.Drain, 5*time.Second); err != nil {
			return fmt.Errorf("deploy.rollout.%s.drain: %w", svc, err)
		}
		if r.Probe != nil {
			if r.Probe.Port < 1 || r.Probe.Port > 65535 {
				return fmt.Errorf("deploy.rollout.%s.probe.port: invalid port %d", svc, r.Probe.Port)
			}
			if r.Probe.Path == "" {
				r.Probe.Path = "/"
			}
			if !strings.HasPrefix(r.Probe.Path, "/") {
				return fmt.Errorf("deploy.rollout.%s.probe.path must start with /", svc)
			}
		}
		sp.Deploy.Rollout[svc] = r
	}
	for name, h := range sp.Webhooks {
		if !namePattern.MatchString(name) {
			return fmt.Errorf("webhook %q: invalid name", name)
		}
		switch h.Action {
		case "deploy", "pull", "restart", "recreate":
		default:
			return fmt.Errorf("webhook %q: unknown action %q", name, h.Action)
		}
		if h.Token == "" && h.Secret == "" {
			return fmt.Errorf("webhook %q: token or secret is required", name)
		}
	}
	return nil
}

// duration parses s, or returns def when s is empty.
func duration(s string, def time.Duration) (time.Duration, error) {
	if s == "" {
		return def, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return d, nil
}

// Discover loads every stack under root (one folder per stack). Folders
// without compose file nor minato.yml are ignored. Invalid stacks are returned in
// errs keyed by folder name, so callers never mistake them for removed stacks.
func Discover(root string) (stacks []*Stack, errs map[string]error, err error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, nil, fmt.Errorf("read stacks dir: %w", err)
	}
	errs = map[string]error{}
	seen := map[string]string{}
	for _, e := range entries {
		if !e.IsDir() || e.Name()[0] == '.' || e.Name()[0] == '_' {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if !hasComposeOrSpec(dir) {
			continue
		}
		st, err := Load(dir)
		if err != nil {
			errs[e.Name()] = err
			continue
		}
		if other, dup := seen[st.Project()]; dup {
			errs[e.Name()] = fmt.Errorf("stacks %s and %s share project name %q", other, st.Name, st.Project())
			continue
		}
		seen[st.Project()] = st.Name
		stacks = append(stacks, st)
	}
	sort.Slice(stacks, func(i, j int) bool { return stacks[i].Name < stacks[j].Name })
	return stacks, errs, nil
}

func hasComposeOrSpec(dir string) bool {
	for _, c := range append([]string{SpecFile}, composeCandidates...) {
		if _, err := os.Stat(filepath.Join(dir, c)); err == nil {
			return true
		}
	}
	return false
}
