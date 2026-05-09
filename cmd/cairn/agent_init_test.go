package cairn

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"strings"
	"testing"
)

// writeTestSeed places a 32-byte seed at the seed path with mode 0600.
func writeTestSeed(t *testing.T, paths *Paths) {
	t.Helper()
	if err := os.MkdirAll(paths.ConfigRoot, 0700); err != nil {
		t.Fatal(err)
	}
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.SeedFile, seed, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAgentInit_WritesPubKeyFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	paths, _ := ResolvePaths("https://cairn.example.com")
	writeTestSeed(t, paths)

	out := &bytes.Buffer{}
	if err := AgentInit("https://cairn.example.com", "plumb", "darksoft.co.nz", out); err != nil {
		t.Fatal(err)
	}

	pubFile := paths.KeyFile("plumb") + ".pub"
	pubBytes, err := os.ReadFile(pubFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pubBytes, []byte("ssh-ed25519 ")) {
		t.Errorf("pubkey file does not start with ssh-ed25519: %q", pubBytes[:32])
	}

	info, _ := os.Stat(pubFile)
	if perm := info.Mode().Perm(); perm != 0644 {
		t.Errorf("pubkey perm = %#o, want 0644", perm)
	}

	s := out.String()
	for _, want := range []string{"slug:", "plumb", "email:", "nexus-plumb@darksoft.co.nz"} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q\noutput=%s", want, s)
		}
	}
}

func TestAgentInit_RejectsMissingSeed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	out := &bytes.Buffer{}
	err := AgentInit("https://cairn.example.com", "plumb", "darksoft.co.nz", out)
	if err == nil {
		t.Error("expected error for missing seed file")
	}
}

func TestAgentInit_DeterministicKeypair(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	paths, _ := ResolvePaths("https://cairn.example.com")
	writeTestSeed(t, paths)

	out1 := &bytes.Buffer{}
	if err := AgentInit("https://cairn.example.com", "plumb", "darksoft.co.nz", out1); err != nil {
		t.Fatal(err)
	}
	pub1, _ := os.ReadFile(paths.KeyFile("plumb") + ".pub")

	out2 := &bytes.Buffer{}
	if err := AgentInit("https://cairn.example.com", "plumb", "darksoft.co.nz", out2); err != nil {
		t.Fatal(err)
	}
	pub2, _ := os.ReadFile(paths.KeyFile("plumb") + ".pub")

	if !bytes.Equal(pub1, pub2) {
		t.Error("re-run produced different pubkey; HKDF derivation is non-deterministic?")
	}
	_ = ed25519.PublicKeySize // keep import
}
