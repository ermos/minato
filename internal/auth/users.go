// Package auth implements remote access: users, roles, password and SSH
// login, and sessions.
package auth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
)

// Role grants a set of permissions.
type Role string

const (
	// RoleViewer reads status, hooks, logs and secret names.
	RoleViewer Role = "viewer"
	// RoleOperator also syncs, deploys and writes or rotates secrets.
	RoleOperator Role = "operator"
	// RoleAdmin also reads secret values and rendered compose files.
	RoleAdmin Role = "admin"
)

var rank = map[Role]int{RoleViewer: 1, RoleOperator: 2, RoleAdmin: 3}

// ParseRole validates a role.
func ParseRole(s string) (Role, error) {
	if _, ok := rank[Role(s)]; !ok {
		return "", fmt.Errorf("unknown role %q (viewer, operator, admin)", s)
	}
	return Role(s), nil
}

// Allows reports whether r includes the permissions of required.
func (r Role) Allows(required Role) bool { return rank[r] >= rank[required] }

var userPattern = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}$`)

// User is a remote user.
type User struct {
	Name         string   `yaml:"name"`
	Role         Role     `yaml:"role"`
	PasswordHash string   `yaml:"password,omitempty"`
	SSHKeys      []string `yaml:"ssh_keys,omitempty"`
}

// CheckPassword verifies a password.
func (u *User) CheckPassword(password string) bool {
	if u.PasswordHash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) == nil
}

// SetPassword hashes and stores a password. Empty removes it.
func (u *User) SetPassword(password string) error {
	if password == "" {
		u.PasswordHash = ""
		return nil
	}
	if len(password) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return err
	}
	u.PasswordHash = string(h)
	return nil
}

// AddSSHKey adds an authorized public key (authorized_keys format).
func (u *User) AddSSHKey(line string) error {
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return fmt.Errorf("invalid ssh public key: %w", err)
	}
	normalized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	for _, k := range u.SSHKeys {
		if keyBody(k) == keyBody(normalized) {
			return errors.New("key already present")
		}
	}
	comment := ""
	if fields := strings.Fields(line); len(fields) > 2 {
		comment = " " + strings.Join(fields[2:], " ")
	}
	u.SSHKeys = append(u.SSHKeys, normalized+comment)
	return nil
}

// RemoveSSHKey removes keys whose fingerprint or comment matches.
func (u *User) RemoveSSHKey(match string) bool {
	var kept []string
	for _, k := range u.SSHKeys {
		pub, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(k))
		if err == nil && (ssh.FingerprintSHA256(pub) == match || comment == match) {
			continue
		}
		kept = append(kept, k)
	}
	removed := len(kept) != len(u.SSHKeys)
	u.SSHKeys = kept
	return removed
}

// HasSSHKey reports whether pub is one of the user's authorized keys.
func (u *User) HasSSHKey(pub ssh.PublicKey) bool {
	want := string(pub.Marshal())
	for _, k := range u.SSHKeys {
		parsed, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k))
		if err == nil && string(parsed.Marshal()) == want {
			return true
		}
	}
	return false
}

func keyBody(line string) string {
	f := strings.Fields(line)
	if len(f) < 2 {
		return line
	}
	return f[1]
}

// Users is the users file.
type Users struct {
	path  string
	mu    sync.Mutex
	mtime time.Time
	List  []*User `yaml:"users"`
}

// LoadUsers reads the users file. A missing file means no user.
func LoadUsers(path string) (*Users, error) {
	u := &Users{path: path}
	return u, u.reload()
}

func (u *Users) reload() error {
	info, err := os.Stat(u.path)
	if errors.Is(err, os.ErrNotExist) {
		u.List, u.mtime = nil, time.Time{}
		return nil
	}
	if err != nil {
		return err
	}
	if info.ModTime().Equal(u.mtime) {
		return nil
	}
	data, err := os.ReadFile(u.path)
	if err != nil {
		return err
	}
	var parsed struct {
		List []*User `yaml:"users"`
	}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("parse %s: %w", u.path, err)
	}
	u.List, u.mtime = parsed.List, info.ModTime()
	return nil
}

// Get returns a copy of a user, reloading the file when it changed.
func (u *Users) Get(name string) (*User, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.reload(); err != nil {
		return nil, false
	}
	for _, usr := range u.List {
		if usr.Name == name {
			c := *usr
			return &c, true
		}
	}
	return nil, false
}

// Find returns the stored user for edition.
func (u *Users) Find(name string) *User {
	for _, usr := range u.List {
		if usr.Name == name {
			return usr
		}
	}
	return nil
}

// Add creates a user.
func (u *Users) Add(name string, role Role) (*User, error) {
	if !userPattern.MatchString(name) {
		return nil, fmt.Errorf("invalid user name %q", name)
	}
	if u.Find(name) != nil {
		return nil, fmt.Errorf("user %s already exists", name)
	}
	usr := &User{Name: name, Role: role}
	u.List = append(u.List, usr)
	return usr, nil
}

// Remove deletes a user.
func (u *Users) Remove(name string) bool {
	for i, usr := range u.List {
		if usr.Name == name {
			u.List = slices.Delete(u.List, i, i+1)
			return true
		}
	}
	return false
}

// Save writes the users file (mode 0600).
func (u *Users) Save() error {
	sort.Slice(u.List, func(i, j int) bool { return u.List[i].Name < u.List[j].Name })
	data, err := yaml.Marshal(struct {
		List []*User `yaml:"users"`
	}{u.List})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(u.path), 0o700); err != nil {
		return err
	}
	header := "# Minato remote users. Manage with: minato user --help\n"
	return os.WriteFile(u.path, append([]byte(header), data...), 0o600)
}
