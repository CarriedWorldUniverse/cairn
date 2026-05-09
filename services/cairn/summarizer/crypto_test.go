//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package summarizer

import (
	"bytes"
	"errors"
	"testing"
)

func TestEncryptDecryptRoundtrip(t *testing.T) {
	hmacKey := bytes.Repeat([]byte{0xab}, 32)
	plaintext := []byte("sk-test-credential-1234567890")

	cipher, err := EncryptCredential(hmacKey, plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if bytes.Contains(cipher, plaintext) {
		t.Fatal("plaintext appears in ciphertext")
	}

	out, err := DecryptCredential(hmacKey, cipher)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(out, plaintext) {
		t.Fatalf("roundtrip mismatch: got %q want %q", out, plaintext)
	}
}

func TestDecryptRejectsTamperedCiphertext(t *testing.T) {
	hmacKey := bytes.Repeat([]byte{0xab}, 32)
	cipher, _ := EncryptCredential(hmacKey, []byte("secret"))
	cipher[len(cipher)-1] ^= 0x01
	_, err := DecryptCredential(hmacKey, cipher)
	if err == nil {
		t.Fatal("expected decryption to fail on tampered ciphertext")
	}
	if !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("expected ErrInvalidCiphertext, got %v", err)
	}
}

func TestDecryptRejectsShortCiphertext(t *testing.T) {
	hmacKey := bytes.Repeat([]byte{0xab}, 32)
	for _, tc := range []struct {
		label string
		input []byte
	}{
		{"empty", []byte{}},
		{"sub-nonce", []byte("tooshort")},
	} {
		_, err := DecryptCredential(hmacKey, tc.input)
		if err == nil {
			t.Errorf("%s: expected error on too-short ciphertext", tc.label)
		}
		if !errors.Is(err, ErrInvalidCiphertext) {
			t.Errorf("%s: expected ErrInvalidCiphertext, got %v", tc.label, err)
		}
	}
}

func TestEncryptDecryptEmptyPlaintext(t *testing.T) {
	hmacKey := bytes.Repeat([]byte{0xab}, 32)
	cipher, err := EncryptCredential(hmacKey, []byte{})
	if err != nil {
		t.Fatalf("encrypt empty: %v", err)
	}
	out, err := DecryptCredential(hmacKey, cipher)
	if err != nil {
		t.Fatalf("decrypt empty: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty plaintext, got %q", out)
	}
}

func TestEncryptRejectsShortKey(t *testing.T) {
	_, err := EncryptCredential(bytes.Repeat([]byte{0xab}, 31), []byte("x"))
	if err == nil {
		t.Error("expected error on 31-byte hmac key")
	}
}

func TestDecryptRejectsWrongKey(t *testing.T) {
	cipher, _ := EncryptCredential(bytes.Repeat([]byte{0xab}, 32), []byte("secret"))
	_, err := DecryptCredential(bytes.Repeat([]byte{0xcd}, 32), cipher)
	if err == nil {
		t.Fatal("expected decryption to fail under wrong key")
	}
	if !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("expected ErrInvalidCiphertext, got %v", err)
	}
}
