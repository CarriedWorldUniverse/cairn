package identity

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
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
// not exist, generates a new key and writes it atomically with mode
// 0400 (returning the generated key). If the file exists, validates
// length (>= 32 bytes) and permission bits (must be exactly 0400) before
// returning the contents.
//
// Concurrent first-run callers that race on file creation will all
// converge on whichever key won the O_EXCL create — losers fall through
// to the read path and return the same bytes the winner persisted.
//
// Caller is responsible for ensuring the parent directory exists with
// appropriate ownership.
func LoadInstanceHMACKey(path string) ([]byte, error) {
	// Try the first-run create-write path. Use O_EXCL so concurrent
	// callers cannot both write — only one creates; others see EEXIST
	// and fall through to the read path below.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
	switch {
	case err == nil:
		// We won the create — generate, write, close.
		defer f.Close()
		key, gerr := GenerateInstanceHMACKey()
		if gerr != nil {
			return nil, gerr
		}
		if _, werr := f.Write(key); werr != nil {
			return nil, fmt.Errorf("cairn identity: write HMAC key %q: %w", path, werr)
		}
		return key, nil
	case errors.Is(err, fs.ErrExist):
		// File already exists — fall through to read.
	default:
		return nil, fmt.Errorf("cairn identity: open HMAC key %q: %w", path, err)
	}

	// Read path: validate mode, then length, then return.
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("cairn identity: stat HMAC key %q: %w", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0400 {
		return nil, fmt.Errorf("cairn identity: HMAC key %q has insecure mode %#o (want 0400)", path, perm)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cairn identity: read HMAC key %q: %w", path, err)
	}
	if len(b) < minHMACKeyBytes {
		return nil, errors.New("cairn identity: HMAC key file too short (min 32 bytes)")
	}
	return b, nil
}
