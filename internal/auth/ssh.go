package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const challengeTTL = time.Minute

// SignedData is what the client signs with its SSH key to log in. It binds
// the nonce to the user so a signature cannot be replayed for someone else.
func SignedData(user, nonce string) []byte {
	return []byte("minato-login-v1\x00" + user + "\x00" + nonce)
}

// Challenges issues single use nonces for SSH login.
type Challenges struct {
	mu sync.Mutex
	m  map[string]challenge
}

type challenge struct {
	user    string
	expires time.Time
}

// NewChallenges creates a nonce store.
func NewChallenges() *Challenges { return &Challenges{m: map[string]challenge{}} }

// Issue returns a new nonce for user.
func (c *Challenges) Issue(user string) string {
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	nonce := base64.RawURLEncoding.EncodeToString(raw)
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, v := range c.m {
		if now.After(v.expires) {
			delete(c.m, k)
		}
	}
	c.m[nonce] = challenge{user: user, expires: now.Add(challengeTTL)}
	return nonce
}

// consume validates and deletes a nonce.
func (c *Challenges) consume(user, nonce string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.m[nonce]
	delete(c.m, nonce)
	return ok && ch.user == user && time.Now().Before(ch.expires)
}

// SSHVerifier checks SSH login proofs.
type SSHVerifier struct {
	Challenges *Challenges
	// CAs are trusted user certificate authorities.
	CAs []ssh.PublicKey
}

// ParseCAs parses authorized_keys formatted CA keys.
func ParseCAs(lines []string) ([]ssh.PublicKey, error) {
	var out []ssh.PublicKey
	for _, l := range lines {
		k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(l))
		if err != nil {
			return nil, fmt.Errorf("invalid ssh CA %q: %w", l, err)
		}
		out = append(out, k)
	}
	return out, nil
}

// Verify checks that sig is a signature of the challenge by a key allowed
// for user: one of its authorized keys, or a certificate signed by a trusted
// CA whose principals include the user name.
func (v *SSHVerifier) Verify(u *User, nonce string, pubWire, sigWire []byte) error {
	if !v.Challenges.consume(u.Name, nonce) {
		return errors.New("invalid or expired challenge")
	}
	pub, err := ssh.ParsePublicKey(pubWire)
	if err != nil {
		return fmt.Errorf("invalid public key: %w", err)
	}
	if cert, ok := pub.(*ssh.Certificate); ok {
		checker := ssh.CertChecker{
			IsUserAuthority: func(auth ssh.PublicKey) bool {
				for _, ca := range v.CAs {
					if string(ca.Marshal()) == string(auth.Marshal()) {
						return true
					}
				}
				return false
			},
		}
		if cert.CertType != ssh.UserCert {
			return errors.New("not a user certificate")
		}
		if !checker.IsUserAuthority(cert.SignatureKey) {
			return errors.New("certificate not signed by a trusted CA")
		}
		if err := checker.CheckCert(u.Name, cert); err != nil {
			return err
		}
	} else if !u.HasSSHKey(pub) {
		return errors.New("key not authorized for this user")
	}
	sig := new(ssh.Signature)
	if err := ssh.Unmarshal(sigWire, sig); err != nil {
		return fmt.Errorf("invalid signature: %w", err)
	}
	return pub.Verify(SignedData(u.Name, nonce), sig)
}

// Limiter slows down brute force: after max failures in window, an address
// is blocked until the window expires.
type Limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	m      map[string][]time.Time
}

// NewLimiter creates a limiter.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{max: max, window: window, m: map[string][]time.Time{}}
}

// Blocked reports whether key exceeded the allowed failures.
func (l *Limiter) Blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(key)) >= l.max
}

// Fail records a failure.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.m[key] = append(l.recent(key), time.Now())
}

// Reset clears failures after a success.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, key)
}

func (l *Limiter) recent(key string) []time.Time {
	cutoff := time.Now().Add(-l.window)
	var out []time.Time
	for _, t := range l.m[key] {
		if t.After(cutoff) {
			out = append(out, t)
		}
	}
	return out
}
