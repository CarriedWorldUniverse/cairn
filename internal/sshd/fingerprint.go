package sshd

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"

	gossh "golang.org/x/crypto/ssh"
)

// Fingerprint computes herald's casket fingerprint for an SSH public key:
// base64url(sha256(rawEd25519Pub)[:16]). It must byte-for-byte match
// herald/internal/identity.Fingerprint so the directory lookup resolves.
// Only Ed25519 keys are accepted — casket identities are Ed25519.
func Fingerprint(pub gossh.PublicKey) (string, error) {
	ck, ok := pub.(gossh.CryptoPublicKey)
	if !ok {
		return "", errors.New("sshd.Fingerprint: key does not expose its crypto public key")
	}
	edPub, ok := ck.CryptoPublicKey().(ed25519.PublicKey)
	if !ok {
		return "", errors.New("sshd.Fingerprint: not an ed25519 key")
	}
	sum := sha256.Sum256(edPub)
	return base64.RawURLEncoding.EncodeToString(sum[:16]), nil
}
