package identity

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
)

const minHMACKeyBytes = 32

// GenerateInstanceHMACKey returns 32 fresh random bytes suitable for
// use as an HMAC-SHA256 key.
func GenerateInstanceHMACKey() ([]byte, error) {
	b := make([]byte, minHMACKeyBytes)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("cairn identity: generate HMAC key: %w", err)
	}
	return b, nil
}

// LoadInstanceHMACKey reads the HMAC key from path. If the file does
// not exist, generates a new key, writes it to path with mode 0400,
// and returns it. Returns an error if the file exists but is shorter
// than 32 bytes.
//
// Caller is responsible for ensuring the parent directory exists with
// appropriate ownership.
func LoadInstanceHMACKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil && os.IsNotExist(err) {
		// First run: generate and persist.
		key, gerr := GenerateInstanceHMACKey()
		if gerr != nil {
			return nil, gerr
		}
		if werr := os.WriteFile(path, key, 0400); werr != nil {
			return nil, fmt.Errorf("cairn identity: write HMAC key %q: %w", path, werr)
		}
		return key, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cairn identity: read HMAC key %q: %w", path, err)
	}
	if len(b) < minHMACKeyBytes {
		return nil, errors.New("cairn identity: HMAC key file too short (min 32 bytes)")
	}
	return b, nil
}
