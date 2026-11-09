// Package secrets implements minato encrypted secret files.
//
// The format is inspired by SOPS: keys stay in clear text so that diffs and
// reviews remain readable, values are encrypted with AES-256-GCM using a
// random data key, and the data key is wrapped with age for every recipient
// (server key, developer keys, CI key...). The key name is bound to its value
// as additional authenticated data, and a MAC over the whole document detects
// removed or swapped entries.
//
//	DB_PASSWORD: ENC[v1,base64(nonce|ciphertext)]
//	_minato:
//	  version: 1
//	  recipients: [age1...]
//	  data_key: base64(age(data key))
//	  mac: hex(hmac-sha256)
package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
	"gopkg.in/yaml.v3"
)

const (
	metaKey   = "_minato"
	encPrefix = "ENC[v1,"
	version   = 1
)

var keyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.\-]*$`)

// Meta is the clear text metadata block of an encrypted file.
type Meta struct {
	Version    int      `yaml:"version"`
	Recipients []string `yaml:"recipients"`
	DataKey    string   `yaml:"data_key"`
	MAC        string   `yaml:"mac"`
	Updated    string   `yaml:"updated,omitempty"`
}

// Encrypted is a parsed but not decrypted secret file.
type Encrypted struct {
	Values map[string]string
	Meta   Meta
}

// Keys returns the sorted secret names. It does not require any key.
func (e *Encrypted) Keys() []string {
	keys := make([]string, 0, len(e.Values))
	for k := range e.Values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// IsEncryptedFile reports whether data looks like a minato encrypted file.
func IsEncryptedFile(data []byte) bool {
	return bytes.Contains(data, []byte(metaKey+":"))
}

// Parse parses an encrypted file.
func Parse(data []byte) (*Encrypted, error) {
	var raw map[string]yaml.Node
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse secrets: %w", err)
	}
	e := &Encrypted{Values: map[string]string{}}
	metaNode, ok := raw[metaKey]
	if !ok {
		return nil, fmt.Errorf("not a minato secrets file (missing %s block)", metaKey)
	}
	if err := metaNode.Decode(&e.Meta); err != nil {
		return nil, fmt.Errorf("parse %s: %w", metaKey, err)
	}
	if e.Meta.Version != version {
		return nil, fmt.Errorf("unsupported secrets version %d", e.Meta.Version)
	}
	for k, n := range raw {
		if k == metaKey {
			continue
		}
		var v string
		if err := n.Decode(&v); err != nil || !strings.HasPrefix(v, encPrefix) {
			return nil, fmt.Errorf("secret %q is not encrypted", k)
		}
		e.Values[k] = v
	}
	return e, nil
}

// ReadFile reads and parses an encrypted file.
func ReadFile(path string) (*Encrypted, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	e, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return e, nil
}

// Secrets is a decrypted, editable secret file.
type Secrets struct {
	values     map[string]string
	dataKey    []byte
	recipients []string
	prev       *Encrypted
	prevPlain  map[string]string
}

// New creates an empty secret set encrypted for recipients.
func New(recipients []string) (*Secrets, error) {
	s := &Secrets{values: map[string]string{}}
	if err := s.Rotate(recipients); err != nil {
		return nil, err
	}
	return s, nil
}

// Open decrypts e with one of ids and verifies its integrity.
func Open(e *Encrypted, ids []age.Identity) (*Secrets, error) {
	wrapped, err := base64.StdEncoding.DecodeString(e.Meta.DataKey)
	if err != nil {
		return nil, fmt.Errorf("invalid data key encoding: %w", err)
	}
	r, err := age.Decrypt(bytes.NewReader(wrapped), ids...)
	if err != nil {
		return nil, fmt.Errorf("unwrap data key: %w", err)
	}
	dataKey, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("unwrap data key: %w", err)
	}
	if len(dataKey) != 32 {
		return nil, errors.New("invalid data key length")
	}
	plain := make(map[string]string, len(e.Values))
	for k, v := range e.Values {
		pv, err := decryptValue(dataKey, k, v)
		if err != nil {
			return nil, fmt.Errorf("decrypt %q: %w", k, err)
		}
		plain[k] = pv
	}
	if !hmac.Equal([]byte(computeMAC(dataKey, plain)), []byte(e.Meta.MAC)) {
		return nil, errors.New("MAC mismatch: the file was tampered with or entries were removed")
	}
	prevPlain := make(map[string]string, len(plain))
	for k, v := range plain {
		prevPlain[k] = v
	}
	return &Secrets{
		values:     plain,
		dataKey:    dataKey,
		recipients: append([]string(nil), e.Meta.Recipients...),
		prev:       e,
		prevPlain:  prevPlain,
	}, nil
}

// DecryptFile is a shortcut returning the clear values of an encrypted file.
func DecryptFile(path string, ids []age.Identity) (map[string]string, error) {
	e, err := ReadFile(path)
	if err != nil {
		return nil, err
	}
	s, err := Open(e, ids)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s.Values(), nil
}

// Values returns a copy of the clear values.
func (s *Secrets) Values() map[string]string {
	out := make(map[string]string, len(s.values))
	for k, v := range s.values {
		out[k] = v
	}
	return out
}

// Recipients returns the recipients the data key is wrapped for.
func (s *Secrets) Recipients() []string { return append([]string(nil), s.recipients...) }

// Get returns a value.
func (s *Secrets) Get(k string) (string, bool) {
	v, ok := s.values[k]
	return v, ok
}

// Set sets a value.
func (s *Secrets) Set(k, v string) error {
	if !keyPattern.MatchString(k) || k == metaKey {
		return fmt.Errorf("invalid secret name %q", k)
	}
	s.values[k] = v
	return nil
}

// Delete removes a value.
func (s *Secrets) Delete(k string) bool {
	_, ok := s.values[k]
	delete(s.values, k)
	return ok
}

// Replace replaces every value at once.
func (s *Secrets) Replace(values map[string]string) error {
	next := make(map[string]string, len(values))
	for k, v := range values {
		if !keyPattern.MatchString(k) || k == metaKey {
			return fmt.Errorf("invalid secret name %q", k)
		}
		next[k] = v
	}
	s.values = next
	return nil
}

// Rotate generates a new data key wrapped for recipients. Every value will be
// re-encrypted on the next Marshal. Use it when removing a recipient.
func (s *Secrets) Rotate(recipients []string) error {
	if _, err := ParseRecipients(recipients); err != nil {
		return err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	s.dataKey = key
	s.recipients = append([]string(nil), recipients...)
	s.prev = nil
	s.prevPlain = nil
	return nil
}

// Marshal encrypts the secrets. Unchanged values keep their previous
// ciphertext so that git diffs only show what really changed.
func (s *Secrets) Marshal() ([]byte, error) {
	keys := make([]string, 0, len(s.values))
	for k := range s.values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	root := &yaml.Node{Kind: yaml.MappingNode}
	for _, k := range keys {
		v := s.values[k]
		var ct string
		if s.prev != nil {
			if old, ok := s.prevPlain[k]; ok && old == v {
				ct = s.prev.Values[k]
			}
		}
		if ct == "" {
			var err error
			if ct, err = encryptValue(s.dataKey, k, v); err != nil {
				return nil, err
			}
		}
		root.Content = append(root.Content, str(k), str(ct))
	}

	meta := Meta{Version: version, Recipients: s.recipients, MAC: computeMAC(s.dataKey, s.values)}
	if s.prev != nil {
		meta.DataKey = s.prev.Meta.DataKey
		meta.Updated = s.prev.Meta.Updated
	} else {
		wrapped, err := wrapKey(s.dataKey, s.recipients)
		if err != nil {
			return nil, err
		}
		meta.DataKey = wrapped
	}
	if s.prev == nil || s.prev.Meta.MAC != meta.MAC {
		meta.Updated = time.Now().UTC().Format(time.RFC3339)
	}
	var metaNode yaml.Node
	if err := metaNode.Encode(meta); err != nil {
		return nil, err
	}
	root.Content = append(root.Content, str(metaKey), &metaNode)

	var buf bytes.Buffer
	buf.WriteString("# Encrypted by minato. Edit with: minato secrets edit <file>\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}); err != nil {
		return nil, err
	}
	return buf.Bytes(), enc.Close()
}

func str(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }

func wrapKey(key []byte, recipients []string) (string, error) {
	rs, err := ParseRecipients(recipients)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, rs...)
	if err != nil {
		return "", err
	}
	if _, err := w.Write(key); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

func gcm(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func encryptValue(key []byte, name, value string) (string, error) {
	aead, err := gcm(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := aead.Seal(nonce, nonce, []byte(value), []byte(name))
	return encPrefix + base64.StdEncoding.EncodeToString(out) + "]", nil
}

func decryptValue(key []byte, name, value string) (string, error) {
	if !strings.HasPrefix(value, encPrefix) || !strings.HasSuffix(value, "]") {
		return "", errors.New("malformed value")
	}
	raw, err := base64.StdEncoding.DecodeString(value[len(encPrefix) : len(value)-1])
	if err != nil {
		return "", err
	}
	aead, err := gcm(key)
	if err != nil {
		return "", err
	}
	if len(raw) < aead.NonceSize() {
		return "", errors.New("malformed value")
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], []byte(name))
	if err != nil {
		return "", errors.New("authentication failed")
	}
	return string(plain), nil
}

func computeMAC(key []byte, values map[string]string) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	m := hmac.New(sha256.New, key)
	for _, k := range keys {
		m.Write([]byte(k))
		m.Write([]byte{0})
		m.Write([]byte(values[k]))
		m.Write([]byte{0})
	}
	return hex.EncodeToString(m.Sum(nil))
}
