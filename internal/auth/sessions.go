package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"
)

// Session is an authenticated remote session.
type Session struct {
	User    string    `json:"user"`
	Method  string    `json:"method"`
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
	Remote  string    `json:"remote"`
}

// Sessions stores sessions by token hash, persisted to disk.
type Sessions struct {
	path  string
	mu    sync.Mutex
	m     map[string]Session
	mtime time.Time
}

// LoadSessions loads the session store.
func LoadSessions(path string) (*Sessions, error) {
	s := &Sessions{path: path, m: map[string]Session{}}
	return s, s.reloadLocked()
}

// reloadLocked rereads the file when another process (the CLI revoking a
// user) changed it.
func (s *Sessions) reloadLocked() error {
	info, err := os.Stat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info.ModTime().Equal(s.mtime) {
		return err
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	m := map[string]Session{}
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	s.m, s.mtime = m, info.ModTime()
	return nil
}

// Create opens a session and returns its bearer token. Only the token hash
// is stored.
func (s *Sessions) Create(sess Session, ttl time.Duration) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := "mnt_" + base64.RawURLEncoding.EncodeToString(raw)
	sess.Created = time.Now()
	sess.Expires = sess.Created.Add(ttl)
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reloadLocked()
	s.m[hashToken(token)] = sess
	return token, s.saveLocked()
}

// Lookup returns the session of a token.
func (s *Sessions) Lookup(token string) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reloadLocked()
	sess, ok := s.m[hashToken(token)]
	if !ok || time.Now().After(sess.Expires) {
		return Session{}, false
	}
	return sess, true
}

// Revoke deletes a session.
func (s *Sessions) Revoke(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, hashToken(token))
	_ = s.saveLocked()
}

// RevokeUser deletes every session of a user.
func (s *Sessions) RevokeUser(user string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reloadLocked()
	n := 0
	for k, v := range s.m {
		if v.User == user {
			delete(s.m, k)
			n++
		}
	}
	_ = s.saveLocked()
	return n
}

func (s *Sessions) saveLocked() error {
	now := time.Now()
	for k, v := range s.m {
		if now.After(v.Expires) {
			delete(s.m, k)
		}
	}
	data, err := json.Marshal(s.m)
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.path, data, 0o600); err != nil {
		return err
	}
	if info, err := os.Stat(s.path); err == nil {
		s.mtime = info.ModTime()
	}
	return nil
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}
