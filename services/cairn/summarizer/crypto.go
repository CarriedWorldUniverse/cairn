//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package summarizer

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// derivedAESKey returns a 32-byte AES key derived from the instance HMAC key
// via HKDF-SHA-256 (RFC 5869) with empty salt and a feature-specific info string.
func derivedAESKey(hmacKey []byte) ([]byte, error) {
	r := hkdf.New(sha256.New, hmacKey, nil, []byte("cairn-summarizer-v1"))
	key := make([]byte, 32)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("summarizer: hkdf: %w", err)
	}
	return key, nil
}

// EncryptCredential AES-256-GCM-encrypts plaintext with a key derived from hmacKey.
// Output format: nonce(12) || ciphertext.
func EncryptCredential(hmacKey, plaintext []byte) ([]byte, error) {
	if len(hmacKey) < 32 {
		return nil, errors.New("hmac key too short")
	}
	derived, err := derivedAESKey(hmacKey)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(derived)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	out := gcm.Seal(nonce, nonce, plaintext, nil)
	return out, nil
}

// ErrInvalidCiphertext is returned when the ciphertext is malformed,
// tampered, or encrypted under a different key.
var ErrInvalidCiphertext = errors.New("summarizer: invalid ciphertext")

// DecryptCredential reverses EncryptCredential.
func DecryptCredential(hmacKey, ciphertext []byte) ([]byte, error) {
	if len(hmacKey) < 32 {
		return nil, errors.New("hmac key too short")
	}
	derived, err := derivedAESKey(hmacKey)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(derived)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, ErrInvalidCiphertext
	}
	nonce, body := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return nil, ErrInvalidCiphertext
	}
	return plaintext, nil
}
