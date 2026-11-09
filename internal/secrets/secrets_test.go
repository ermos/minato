package secrets

import (
	"bytes"
	"strings"
	"testing"

	"filippo.io/age"
)

func newIdentity(t *testing.T) *age.X25519Identity {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRoundTripMultipleRecipients(t *testing.T) {
	server, dev := newIdentity(t), newIdentity(t)
	s, err := New([]string{server.Recipient().String(), dev.Recipient().String()})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Set("DB_PASSWORD", "s3cr$t")
	_ = s.Set("CERT", "line1\nline2\n")
	data, err := s.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("s3cr$t")) {
		t.Fatal("clear value leaked in file")
	}
	for _, id := range []age.Identity{server, dev} {
		e, err := Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Open(e, []age.Identity{id})
		if err != nil {
			t.Fatal(err)
		}
		if v, _ := got.Get("CERT"); v != "line1\nline2\n" {
			t.Fatalf("bad value %q", v)
		}
	}
	e, _ := Parse(data)
	if _, err := Open(e, []age.Identity{newIdentity(t)}); err == nil {
		t.Fatal("foreign identity must not decrypt")
	}
}

func TestUnchangedValuesKeepCiphertext(t *testing.T) {
	id := newIdentity(t)
	s, _ := New([]string{id.Recipient().String()})
	_ = s.Set("A", "1")
	_ = s.Set("B", "2")
	first, _ := s.Marshal()

	e, _ := Parse(first)
	opened, err := Open(e, []age.Identity{id})
	if err != nil {
		t.Fatal(err)
	}
	_ = opened.Set("B", "3")
	second, _ := opened.Marshal()
	e2, _ := Parse(second)
	if e.Values["A"] != e2.Values["A"] {
		t.Fatal("unchanged value must keep its ciphertext")
	}
	if e.Values["B"] == e2.Values["B"] {
		t.Fatal("changed value must be re-encrypted")
	}
	if e.Meta.DataKey != e2.Meta.DataKey {
		t.Fatal("data key must be kept without rotation")
	}
}

func TestTamperingIsDetected(t *testing.T) {
	id := newIdentity(t)
	s, _ := New([]string{id.Recipient().String()})
	_ = s.Set("A", "1")
	_ = s.Set("B", "2")
	data, _ := s.Marshal()
	e, _ := Parse(data)

	swapped := &Encrypted{Meta: e.Meta, Values: map[string]string{"A": e.Values["B"], "B": e.Values["A"]}}
	if _, err := Open(swapped, []age.Identity{id}); err == nil {
		t.Fatal("swapped values must fail")
	}
	removed := &Encrypted{Meta: e.Meta, Values: map[string]string{"A": e.Values["A"]}}
	if _, err := Open(removed, []age.Identity{id}); err == nil || !strings.Contains(err.Error(), "MAC") {
		t.Fatalf("removed entry must fail MAC check, got %v", err)
	}
}

func TestRotateDropsOldRecipient(t *testing.T) {
	keep, drop := newIdentity(t), newIdentity(t)
	s, _ := New([]string{keep.Recipient().String(), drop.Recipient().String()})
	_ = s.Set("A", "1")
	if err := s.Rotate([]string{keep.Recipient().String()}); err != nil {
		t.Fatal(err)
	}
	data, _ := s.Marshal()
	e, _ := Parse(data)
	if _, err := Open(e, []age.Identity{drop}); err == nil {
		t.Fatal("removed recipient must not decrypt after rotation")
	}
	if _, err := Open(e, []age.Identity{keep}); err != nil {
		t.Fatal(err)
	}
}
