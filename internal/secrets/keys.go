package secrets

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
	"filippo.io/age/agessh"
)

// ParseRecipient accepts age (age1...) and SSH public keys (ssh-ed25519, ssh-rsa).
func ParseRecipient(s string) (age.Recipient, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "ssh-") {
		return agessh.ParseRecipient(s)
	}
	rs, err := age.ParseRecipients(strings.NewReader(s))
	if err != nil {
		return nil, fmt.Errorf("invalid recipient %q: %w", s, err)
	}
	if len(rs) != 1 {
		return nil, fmt.Errorf("invalid recipient %q", s)
	}
	return rs[0], nil
}

// ParseRecipients parses a list of recipients.
func ParseRecipients(list []string) ([]age.Recipient, error) {
	if len(list) == 0 {
		return nil, errors.New("no recipients configured (add secrets.recipients to .minato.yml or pass --recipient)")
	}
	out := make([]age.Recipient, 0, len(list))
	for _, s := range list {
		r, err := ParseRecipient(s)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// ParseIdentityData parses an age identity file or an unencrypted SSH private key.
func ParseIdentityData(data []byte) ([]age.Identity, error) {
	if bytes.Contains(data, []byte("PRIVATE KEY-----")) {
		id, err := agessh.ParseIdentity(data)
		if err != nil {
			return nil, err
		}
		return []age.Identity{id}, nil
	}
	return age.ParseIdentities(bytes.NewReader(data))
}

// DefaultIdentityFiles returns the files searched for identities, in order.
func DefaultIdentityFiles(extra ...string) []string {
	var files []string
	if f := os.Getenv("MINATO_AGE_KEY_FILE"); f != "" {
		files = append(files, f)
	}
	files = append(files, extra...)
	if cfg, err := os.UserConfigDir(); err == nil {
		files = append(files, filepath.Join(cfg, "minato", "age.key"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		files = append(files, filepath.Join(home, ".config", "minato", "age.key"))
	}
	return files
}

// LoadIdentities loads identities from MINATO_AGE_KEY and every readable file.
// Missing files are skipped. It returns an error only if nothing was found.
func LoadIdentities(files ...string) ([]age.Identity, error) {
	var ids []age.Identity
	if raw := os.Getenv("MINATO_AGE_KEY"); raw != "" {
		parsed, err := ParseIdentityData([]byte(raw))
		if err != nil {
			return nil, fmt.Errorf("MINATO_AGE_KEY: %w", err)
		}
		ids = append(ids, parsed...)
	}
	seen := map[string]bool{}
	for _, f := range files {
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		parsed, err := ParseIdentityData(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		ids = append(ids, parsed...)
	}
	if len(ids) == 0 {
		return nil, errors.New("no age identity found (set MINATO_AGE_KEY_FILE, MINATO_AGE_KEY or run `minato secrets keygen`)")
	}
	return ids, nil
}

// GenerateKey writes a new X25519 identity to path (mode 0600) and returns
// its public recipient.
func GenerateKey(path string) (string, error) {
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("%s already exists", path)
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	content := fmt.Sprintf("# minato age key\n# public key: %s\n%s\n", id.Recipient(), id)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", err
	}
	return id.Recipient().String(), nil
}

// PublicKeys returns the public recipients of the X25519 identities in ids.
func PublicKeys(ids []age.Identity) []string {
	var out []string
	for _, id := range ids {
		if x, ok := id.(*age.X25519Identity); ok {
			out = append(out, x.Recipient().String())
		}
	}
	return out
}

// RandomValue returns a random URL safe string of length n.
func RandomValue(n int) string {
	if n <= 0 {
		n = 32
	}
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return base64.RawURLEncoding.EncodeToString(buf)[:n]
}
