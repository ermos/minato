// Package config holds the server configuration (/etc/minato/config.yml)
// and the repository level configuration (.minato.yml at the repo root).
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultPath     = "/etc/minato/config.yml"
	DefaultDataDir  = "/var/lib/minato"
	DefaultSocket   = "/run/minato/minato.sock"
	DefaultAgeKey   = "/etc/minato/age.key"
	DefaultUsers    = "/etc/minato/users.yml"
	DefaultListen   = ":7373"
	RepoFileName    = ".minato.yml"
	DefaultStackDir = "stacks"
)

// Config is the daemon configuration.
type Config struct {
	Repo     Repo     `yaml:"repo"`
	DataDir  string   `yaml:"data_dir"`
	Socket   string   `yaml:"socket"`
	AgeKey   string   `yaml:"age_key"`
	Prune    bool     `yaml:"prune"`
	Server   Server   `yaml:"server"`
	Auth     Auth     `yaml:"auth"`
	Webhooks Webhooks `yaml:"webhooks"`
	Notify   Notify   `yaml:"notify"`
	Docker   Docker   `yaml:"docker"`
}

// Repo describes the git repository holding the stacks.
type Repo struct {
	// URL of the git repository. Empty means Dir is used as is (no git sync).
	URL    string `yaml:"url"`
	Branch string `yaml:"branch"`
	// SSHKey is a private key used for git over SSH.
	SSHKey string `yaml:"ssh_key"`
	// Dir is the local checkout. Defaults to <data_dir>/repo.
	Dir string `yaml:"dir"`
	// Poll interval. 0 disables polling (webhooks only).
	Poll time.Duration `yaml:"poll"`
}

// TLS modes of the public server.
const (
	TLSSelfSigned = "self-signed"
	TLSAcme       = "acme"
	TLSFiles      = "files"
	TLSOff        = "off"
)

// Server is the public endpoint serving the remote API and the webhooks.
type Server struct {
	// Listen address, empty disables the public server.
	Listen string `yaml:"listen"`
	// Domain the server is reachable on (minato.example.com). Required for acme.
	Domain string `yaml:"domain"`
	// TLS mode: self-signed (default, clients pin the fingerprint), acme
	// (Let's Encrypt, needs ports 80 and 443), files (cert_file/key_file) or
	// off (plain HTTP behind a reverse proxy doing TLS).
	TLS        string `yaml:"tls"`
	CertFile   string `yaml:"cert_file"`
	KeyFile    string `yaml:"key_file"`
	AcmeEmail  string `yaml:"acme_email"`
	PublicURL  string `yaml:"public_url"`
	TrustProxy bool   `yaml:"trust_proxy"`
}

// Auth configures remote users.
type Auth struct {
	// UsersFile lists users, their role, password hash and SSH keys.
	UsersFile string `yaml:"users_file"`
	// Password enables login with a password. Disable it to only allow SSH keys.
	Password *bool `yaml:"password"`
	// SSHCAs are trusted SSH certificate authorities (public keys). A user can
	// log in with a certificate signed by one of them whose principals
	// contain the user name.
	SSHCAs []string `yaml:"ssh_cas"`
	// SessionTTL is the lifetime of a login.
	SessionTTL time.Duration `yaml:"session_ttl"`
}

// PasswordEnabled reports whether password login is allowed.
func (a Auth) PasswordEnabled() bool { return a.Password == nil || *a.Password }

// Webhooks configures the global sync webhook.
type Webhooks struct {
	// Token protects the global /hooks/sync endpoint (bearer, header or query).
	Token string `yaml:"token"`
	// Secret validates GitHub/Gitea HMAC signatures on /hooks/sync.
	Secret string `yaml:"secret"`
}

// Notify sends a JSON message on deployments (Slack and Discord compatible).
type Notify struct {
	URL string `yaml:"url"`
	// On lists events: success, failure. Defaults to failure only.
	On []string `yaml:"on"`
}

// Docker holds docker CLI settings.
type Docker struct {
	Bin string `yaml:"bin"`
}

// Load reads the configuration file and applies defaults.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("config %s not found, run `minato server init` first", path)
		}
		return nil, err
	}
	c := &Config{}
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	c.applyDefaults()
	if c.Repo.URL == "" && c.Repo.Dir == "" {
		return nil, errors.New("repo.url or repo.dir must be set")
	}
	return c, nil
}

func (c *Config) applyDefaults() {
	if c.DataDir == "" {
		c.DataDir = DefaultDataDir
	}
	if c.Socket == "" {
		c.Socket = DefaultSocket
	}
	if c.AgeKey == "" {
		c.AgeKey = DefaultAgeKey
	}
	if c.Repo.Branch == "" {
		c.Repo.Branch = "main"
	}
	if c.Repo.Dir == "" {
		c.Repo.Dir = filepath.Join(c.DataDir, "repo")
	}
	if c.Server.TLS == "" {
		c.Server.TLS = TLSSelfSigned
	}
	if c.Server.PublicURL == "" && c.Server.Listen != "" {
		c.Server.PublicURL = c.defaultPublicURL()
	}
	if c.Auth.UsersFile == "" {
		c.Auth.UsersFile = DefaultUsers
	}
	if c.Auth.SessionTTL == 0 {
		c.Auth.SessionTTL = 30 * 24 * time.Hour
	}
	if c.Docker.Bin == "" {
		c.Docker.Bin = "docker"
	}
	if len(c.Notify.On) == 0 {
		c.Notify.On = []string{"failure"}
	}
}

func (c *Config) defaultPublicURL() string {
	scheme := "https"
	if c.Server.TLS == TLSOff {
		scheme = "http"
	}
	host := c.Server.Domain
	if host == "" {
		host, _ = os.Hostname()
	}
	_, port, _ := net.SplitHostPort(c.Server.Listen)
	if c.Server.TLS == TLSAcme || (c.Server.TLS == TLSOff && c.Server.Domain != "") || port == "" || port == "443" {
		if c.Server.TLS == TLSOff && c.Server.Domain != "" {
			scheme = "https"
		}
		return scheme + "://" + host
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

// TLSDir holds the self-signed certificate and the ACME cache.
func (c *Config) TLSDir() string { return filepath.Join(c.DataDir, "tls") }

// SessionsPath stores remote sessions.
func (c *Config) SessionsPath() string { return filepath.Join(c.DataDir, "sessions.json") }

// StacksDir is where rendered stacks are written.
func (c *Config) StacksDir() string { return filepath.Join(c.DataDir, "stacks") }

// StatePath is the daemon state file.
func (c *Config) StatePath() string { return filepath.Join(c.DataDir, "state.json") }

// RepoSpec is the optional .minato.yml at the root of the git repository.
type RepoSpec struct {
	// StacksDir is the folder holding one sub folder per stack.
	StacksDir string `yaml:"stacks_dir"`
	Secrets   struct {
		// Recipients able to decrypt the secret files: the server key and
		// every developer or CI key.
		Recipients []string `yaml:"recipients"`
	} `yaml:"secrets"`
}

// LoadRepoSpec reads <root>/.minato.yml. A missing file returns defaults.
func LoadRepoSpec(root string) (*RepoSpec, error) {
	s := &RepoSpec{}
	data, err := os.ReadFile(filepath.Join(root, RepoFileName))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if err := yaml.Unmarshal(data, s); err != nil {
			return nil, fmt.Errorf("parse %s: %w", RepoFileName, err)
		}
	}
	if s.StacksDir == "" {
		s.StacksDir = DefaultStackDir
	}
	return s, nil
}

// FindRepoRoot walks up from dir looking for .minato.yml or .git.
func FindRepoRoot(dir string) (string, bool) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for {
		for _, marker := range []string{RepoFileName, ".git"} {
			if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
				return dir, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}
