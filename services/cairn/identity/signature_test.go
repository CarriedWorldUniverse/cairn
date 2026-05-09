package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
)

func TestVerifyCommitSignature_Valid(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	payload := []byte("commit object payload to be signed")
	sig := ed25519.Sign(priv, payload)

	if err := VerifyCommitSignature(payload, sig, pub); err != nil {
		t.Errorf("valid signature rejected: %v", err)
	}
}

func TestVerifyCommitSignature_Invalid(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	payload := []byte("commit object payload")
	sig := ed25519.Sign(priv, payload)

	// Flip a byte in the signature.
	tampered := make([]byte, len(sig))
	copy(tampered, sig)
	tampered[0] ^= 0xff

	err := VerifyCommitSignature(payload, tampered, pub)
	if err == nil {
		t.Error("tampered signature accepted")
	}
	if !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("err = %v, want ErrInvalidSignature", err)
	}
}

func TestVerifyCommitSignature_WrongKey(t *testing.T) {
	pub1, priv1, _ := ed25519.GenerateKey(rand.Reader)
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	_ = pub1
	payload := []byte("commit payload")
	sig := ed25519.Sign(priv1, payload)

	err := VerifyCommitSignature(payload, sig, pub2)
	if err == nil {
		t.Error("signature accepted under wrong public key")
	}
}

func TestVerifyCommitSignature_TamperedPayload(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	payload := []byte("original commit payload")
	sig := ed25519.Sign(priv, payload)

	tamperedPayload := []byte("modified commit payload")

	err := VerifyCommitSignature(tamperedPayload, sig, pub)
	if err == nil {
		t.Error("signature accepted for tampered payload")
	}
}

func TestVerifyCommitSignature_MalformedInputs(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := VerifyCommitSignature(nil, []byte{1, 2, 3}, pub); err == nil {
		t.Error("nil payload accepted")
	}
	if err := VerifyCommitSignature([]byte("p"), nil, pub); err == nil {
		t.Error("nil signature accepted")
	}
	if err := VerifyCommitSignature([]byte("p"), []byte{1, 2}, pub); err == nil {
		t.Error("too-short signature accepted")
	}
	if err := VerifyCommitSignature([]byte("p"), make([]byte, 64), nil); err == nil {
		t.Error("nil pubkey accepted")
	}
}
