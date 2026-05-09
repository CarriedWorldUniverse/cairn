package identity

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadInstanceHMACKey_GeneratesIfMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "instance-hmac.key")

	key, err := LoadInstanceHMACKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != 32 {
		t.Errorf("key length = %d, want 32", len(key))
	}

	// File should now exist with mode 0400.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0400 {
		t.Errorf("file mode = %#o, want 0400", perm)
	}
}

func TestLoadInstanceHMACKey_ReadsExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "instance-hmac.key")

	first, err := LoadInstanceHMACKey(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadInstanceHMACKey(path)
	if err != nil {
		t.Fatal(err)
	}

	if string(first) != string(second) {
		t.Error("subsequent loads returned different keys")
	}
}

func TestLoadInstanceHMACKey_RejectsTooShort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "instance-hmac.key")
	// Write a 16-byte file (too short).
	if err := os.WriteFile(path, []byte("0123456789abcdef"), 0400); err != nil {
		t.Fatal(err)
	}

	_, err := LoadInstanceHMACKey(path)
	if err == nil {
		t.Error("expected error for too-short key")
	}
}

func TestGenerateInstanceHMACKey_Random(t *testing.T) {
	a, err := GenerateInstanceHMACKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateInstanceHMACKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 32 || len(b) != 32 {
		t.Errorf("generated key lengths: %d, %d", len(a), len(b))
	}
	if string(a) == string(b) {
		t.Error("two calls returned identical keys (vanishingly unlikely)")
	}
}
