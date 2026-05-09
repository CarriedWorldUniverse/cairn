package identity

import (
	"crypto/ed25519"
	"errors"
)

// ErrInvalidSignature is returned by VerifyCommitSignature when the
// signature does not verify against the public key.
var ErrInvalidSignature = errors.New("cairn identity: invalid signature")

// VerifyCommitSignature verifies an Ed25519 signature on a payload
// against a public key. Returns nil on valid, ErrInvalidSignature on
// invalid, or another error for malformed inputs.
//
// Note: this verifies *raw* Ed25519 signatures. The pre-receive hook
// is responsible for extracting the signature blob and the signed
// payload (commit object minus the signature trailer) from the git
// commit before calling this function. SSH-format wrapping (which
// git produces with gpg.format=ssh) is unwrapped at the call site.
func VerifyCommitSignature(payload, signature []byte, publicKey ed25519.PublicKey) error {
	if payload == nil {
		return errors.New("cairn identity: nil payload")
	}
	if len(signature) == 0 {
		return errors.New("cairn identity: empty signature")
	}
	if len(signature) != ed25519.SignatureSize {
		return errors.New("cairn identity: signature size mismatch")
	}
	if len(publicKey) == 0 {
		return errors.New("cairn identity: empty public key")
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("cairn identity: public key size mismatch")
	}

	if !ed25519.Verify(publicKey, payload, signature) {
		return ErrInvalidSignature
	}
	return nil
}
