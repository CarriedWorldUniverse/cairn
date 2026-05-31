package sshd

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

func TestFingerprintMatchesHeraldConvention(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	// herald's convention, computed independently here.
	sum := sha256.Sum256(pub)
	want := base64.RawURLEncoding.EncodeToString(sum[:16])

	sshPub, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Fingerprint(sshPub)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if got != want {
		t.Fatalf("fingerprint = %s, want %s", got, want)
	}
}

func TestFingerprintRejectsNonEd25519(t *testing.T) {
	// An RSA-typed key must be rejected (casket identities are Ed25519).
	if _, err := Fingerprint(stubNonEd25519{}); err == nil {
		t.Fatal("want error for non-ed25519 key")
	}
}

type stubNonEd25519 struct{}

func (stubNonEd25519) Type() string                              { return "ssh-rsa" }
func (stubNonEd25519) Marshal() []byte                           { return nil }
func (stubNonEd25519) Verify(_ []byte, _ *gossh.Signature) error { return nil }
