package hook

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestSSHSig_RoundTrip(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}

	data := []byte("hello sshsig")
	armored, err := SignSSHSig(signer, data, "git")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(armored), "BEGIN SSH SIGNATURE") {
		t.Fatalf("missing PEM header: %q", armored)
	}

	sig, err := ParseSSHSignature(armored)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifySSHSignedData(signer.PublicKey(), data, sig, "git"); err != nil {
		t.Errorf("verify failed: %v", err)
	}

	// Wrong namespace should not verify.
	if err := VerifySSHSignedData(signer.PublicKey(), data, sig, "other"); err == nil {
		t.Error("verify with wrong namespace unexpectedly succeeded")
	}

	// Tampered data should not verify.
	if err := VerifySSHSignedData(signer.PublicKey(), []byte("tampered"), sig, "git"); err == nil {
		t.Error("verify with tampered data unexpectedly succeeded")
	}
}

func TestParseSSHSignature_NotPEM(t *testing.T) {
	if _, err := ParseSSHSignature([]byte("not a pem block")); err == nil {
		t.Error("expected error parsing non-PEM input")
	}
}
