//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package summarizer

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
)

// TestInit_HMACKeyAccessorReturnsLoadedBytes is the sentinel that
// guarantees the encrypt path in routers/api/cairn/v1 sees the same
// bytes the resolver decrypts with. If this drifts, PUT-encrypted
// credentials become unreadable on next decrypt.
func TestInit_HMACKeyAccessorReturnsLoadedBytes(t *testing.T) {
	eng := cairntest.NewEngine(t)
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "hmac.key")
	want := []byte("test-hmac-key-32-bytes-long!!!ab")
	if err := os.WriteFile(keyPath, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Init(eng, keyPath); err != nil {
		t.Fatal(err)
	}
	defer SetGlobal(nil)

	got := HMACKey()
	if !bytes.Equal(got, want) {
		t.Errorf("HMACKey() = %q, want %q", got, want)
	}
}

// TestHMACKey_NilBeforeInit guards the "summarizer not initialized"
// branch in PutSummarizerConfig — if a caller hits PUT before Init has
// run, HMACKey must return nil so the handler can 503 instead of
// encrypting with garbage.
func TestHMACKey_NilBeforeInit(t *testing.T) {
	// Reset the cached pointer for this test; restore on exit.
	prev := hmacKeyForEncrypt.Load()
	hmacKeyForEncrypt.Store(nil)
	defer hmacKeyForEncrypt.Store(prev)

	if got := HMACKey(); got != nil {
		t.Errorf("HMACKey() = %v before Init, want nil", got)
	}
}

func TestInit_ResolverDecryptsCredential(t *testing.T) {
	eng := cairntest.NewEngine(t)
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "hmac.key")
	hmacKey := []byte("test-hmac-key-32-bytes-long!!!ab")
	if err := os.WriteFile(keyPath, hmacKey, 0o600); err != nil {
		t.Fatal(err)
	}

	cipher, err := EncryptCredential(hmacKey, []byte("sk-real-key"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &cairnmodels.SummarizerConfig{
		OwnerID:           42,
		Enabled:           true,
		Provider:          "openai-api",
		EndpointURL:       "https://api.example.com",
		ModelID:           "gpt-test",
		CredentialsCipher: cipher,
		LevelsEnabled:     cairnmodels.LevelPR,
	}
	if _, err := eng.Insert(cfg); err != nil {
		t.Fatal(err)
	}

	if err := Init(eng, keyPath); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer SetGlobal(nil)

	svc := Global()
	if svc == nil {
		t.Fatal("Global() returned nil after Init")
	}
	client, loaded, err := svc.resolver(42)
	if err != nil {
		t.Fatalf("resolver: %v", err)
	}
	if loaded == nil || !loaded.Enabled {
		t.Error("loaded config should be enabled")
	}
	if client == nil {
		t.Error("resolver returned nil client for enabled config")
	}
}

func TestInit_ResolverReturnsNilClientForDisabled(t *testing.T) {
	eng := cairntest.NewEngine(t)
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "hmac.key")
	if err := os.WriteFile(keyPath, []byte("test-hmac-key-32-bytes-long!!!ab"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Insert(&cairnmodels.SummarizerConfig{OwnerID: 99, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if err := Init(eng, keyPath); err != nil {
		t.Fatal(err)
	}
	defer SetGlobal(nil)

	client, cfg, err := Global().resolver(99)
	if err != nil {
		t.Fatalf("resolver: %v", err)
	}
	if client != nil {
		t.Error("disabled config should yield nil client")
	}
	if cfg == nil || cfg.Enabled {
		t.Error("loaded config should be present and disabled")
	}
}

func TestInit_ResolverReturnsNilForMissingRow(t *testing.T) {
	eng := cairntest.NewEngine(t)
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "hmac.key")
	if err := os.WriteFile(keyPath, []byte("test-hmac-key-32-bytes-long!!!ab"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Init(eng, keyPath); err != nil {
		t.Fatal(err)
	}
	defer SetGlobal(nil)

	client, cfg, err := Global().resolver(12345) // no row inserted
	if err != nil {
		t.Fatalf("resolver: %v", err)
	}
	if client != nil {
		t.Error("expected nil client for missing row")
	}
	if cfg != nil {
		t.Error("expected nil cfg for missing row")
	}
}

func TestInit_MissingHMACKeyReturnsError(t *testing.T) {
	eng := cairntest.NewEngine(t)
	if err := Init(eng, "/nonexistent/path/hmac.key"); err == nil {
		t.Error("expected error reading nonexistent key")
	}
}
