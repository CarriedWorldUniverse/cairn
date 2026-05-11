package cairn

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/CarriedWorldUniverse/cairn/services/cairn/hook"
)

func generateTestRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// writeTestAgentKey generates an ed25519 keypair, writes the OpenSSH-
// format private key to <HostDir>/<slug>.key with mode 0600, and
// returns the public key for verification.
func writeTestAgentKey(t *testing.T, paths *Paths, slug string) ed25519.PublicKey {
	t.Helper()
	if err := paths.EnsureHostDir(); err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "test-"+slug)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.KeyFile(slug), pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	return pub
}

func TestCommitSignHelper_ProducesValidSSHSignature(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	paths, _ := ResolvePaths("https://cairn.example.com")
	pub := writeTestAgentKey(t, paths, "plumb")

	stdin := bytes.NewReader([]byte("commit blob to sign"))
	stdout := &bytes.Buffer{}

	if err := CommitSignHelper("https://cairn.example.com", "plumb", "git", stdin, stdout); err != nil {
		t.Fatal(err)
	}

	out := stdout.String()
	if !strings.Contains(out, "BEGIN SSH SIGNATURE") {
		t.Errorf("output missing SSH SIGNATURE armor:\n%s", out)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := hook.ParseSSHSignature(stdout.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if err := hook.VerifySSHSignedData(sshPub, []byte("commit blob to sign"), sig, "git"); err != nil {
		t.Errorf("signature did not verify: %v", err)
	}
}

func TestCommitSignHelper_RequiresKeyFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	stdin := bytes.NewReader([]byte("data"))
	stdout := &bytes.Buffer{}

	err := CommitSignHelper("https://cairn.example.com", "plumb", "git", stdin, stdout)
	if err == nil {
		t.Error("expected error when private key file is missing")
	}
}

func TestCommitSignHelper_RejectsInsecurePerms(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	paths, _ := ResolvePaths("https://cairn.example.com")
	writeTestAgentKey(t, paths, "plumb")
	// Loosen perms — should be rejected.
	if err := os.Chmod(paths.KeyFile("plumb"), 0644); err != nil {
		t.Fatal(err)
	}

	stdin := bytes.NewReader([]byte("data"))
	stdout := &bytes.Buffer{}
	err := CommitSignHelper("https://cairn.example.com", "plumb", "git", stdin, stdout)
	if err == nil || !strings.Contains(err.Error(), "insecure mode") {
		t.Errorf("expected insecure-mode rejection, got %v", err)
	}
}

func TestCommitSignHelper_RejectsNonEd25519(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	paths, _ := ResolvePaths("https://cairn.example.com")
	if err := paths.EnsureHostDir(); err != nil {
		t.Fatal(err)
	}

	// Generate a small RSA key and marshal it OpenSSH-style. We use the
	// stdlib's rsa.GenerateKey via the ssh helper.
	rsaKey := generateTestRSAKey(t)
	block, err := ssh.MarshalPrivateKey(rsaKey, "test-rsa")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.KeyFile("plumb"), pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}

	stdin := bytes.NewReader([]byte("data"))
	stdout := &bytes.Buffer{}
	err = CommitSignHelper("https://cairn.example.com", "plumb", "git", stdin, stdout)
	if err == nil || !strings.Contains(err.Error(), "only ed25519") {
		t.Errorf("expected non-ed25519 rejection, got %v", err)
	}
}

func TestInferSlugFromKeyfile(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/home/u/.config/cairn/host/plumb.key.pub", "plumb"},
		{"/home/u/.config/cairn/host/anvil.pub", "anvil"},
		{"/home/u/.config/cairn/host/forge.key", "forge"},
		{"plumb", "plumb"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := inferSlugFromKeyfile(tc.path); got != tc.want {
			t.Errorf("inferSlugFromKeyfile(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}
