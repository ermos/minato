package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func newSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func login(v *SSHVerifier, u *User, as string, s ssh.Signer) error {
	nonce := v.Challenges.Issue(as)
	sig, err := s.Sign(rand.Reader, SignedData(as, nonce))
	if err != nil {
		return err
	}
	return v.Verify(u, nonce, s.PublicKey().Marshal(), ssh.Marshal(sig))
}

func TestSSHKeyLogin(t *testing.T) {
	alice, mallory := newSigner(t), newSigner(t)
	u := &User{Name: "alice", Role: RoleAdmin}
	if err := u.AddSSHKey(string(ssh.MarshalAuthorizedKey(alice.PublicKey())) + " laptop"); err != nil {
		t.Fatal(err)
	}
	v := &SSHVerifier{Challenges: NewChallenges()}
	if err := login(v, u, "alice", alice); err != nil {
		t.Fatalf("authorized key rejected: %v", err)
	}
	if err := login(v, u, "alice", mallory); err == nil {
		t.Fatal("unknown key accepted")
	}

	// A nonce is single use and bound to the user it was issued for.
	nonce := v.Challenges.Issue("bob")
	sig, _ := alice.Sign(rand.Reader, SignedData("alice", nonce))
	if err := v.Verify(u, nonce, alice.PublicKey().Marshal(), ssh.Marshal(sig)); err == nil {
		t.Fatal("nonce issued for another user accepted")
	}
	nonce = v.Challenges.Issue("alice")
	sig, _ = alice.Sign(rand.Reader, SignedData("alice", nonce))
	_ = v.Verify(u, nonce, alice.PublicKey().Marshal(), ssh.Marshal(sig))
	if err := v.Verify(u, nonce, alice.PublicKey().Marshal(), ssh.Marshal(sig)); err == nil {
		t.Fatal("replayed nonce accepted")
	}
}

func TestSSHCertificateLogin(t *testing.T) {
	ca, otherCA, key := newSigner(t), newSigner(t), newSigner(t)
	issue := func(signer ssh.Signer, principals ...string) ssh.Signer {
		cert := &ssh.Certificate{
			Key: key.PublicKey(), CertType: ssh.UserCert, ValidPrincipals: principals,
			ValidAfter: uint64(time.Now().Add(-time.Minute).Unix()), ValidBefore: uint64(time.Now().Add(time.Hour).Unix()),
		}
		if err := cert.SignCert(rand.Reader, signer); err != nil {
			t.Fatal(err)
		}
		cs, err := ssh.NewCertSigner(cert, key)
		if err != nil {
			t.Fatal(err)
		}
		return cs
	}
	u := &User{Name: "alice", Role: RoleOperator}
	v := &SSHVerifier{Challenges: NewChallenges(), CAs: []ssh.PublicKey{ca.PublicKey()}}

	if err := login(v, u, "alice", issue(ca, "alice")); err != nil {
		t.Fatalf("valid certificate rejected: %v", err)
	}
	if err := login(v, u, "alice", issue(ca, "bob")); err == nil {
		t.Fatal("certificate for another principal accepted")
	}
	if err := login(v, u, "alice", issue(otherCA, "alice")); err == nil {
		t.Fatal("certificate from an untrusted CA accepted")
	}
}

func TestRolesAndPasswords(t *testing.T) {
	if !RoleAdmin.Allows(RoleOperator) || RoleViewer.Allows(RoleOperator) {
		t.Fatal("role ordering")
	}
	u := &User{Name: "bob"}
	if err := u.SetPassword("short"); err == nil {
		t.Fatal("short password accepted")
	}
	_ = u.SetPassword("correct horse")
	if !u.CheckPassword("correct horse") || u.CheckPassword("wrong horse") {
		t.Fatal("password check")
	}
}
