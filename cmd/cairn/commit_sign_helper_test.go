package cairn

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/CarriedWorldUniverse/cairn/services/cairn/hook"
	casket "github.com/CarriedWorldUniverse/casket-go"
)

func TestCommitSignHelper_ProducesValidSSHSignature(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	paths, _ := ResolvePaths("https://cairn.example.com")
	writeTestSeed(t, paths)

	stdin := bytes.NewReader([]byte("commit blob to sign"))
	stdout := &bytes.Buffer{}

	if err := CommitSignHelper("https://cairn.example.com", "plumb", "git", stdin, stdout); err != nil {
		t.Fatal(err)
	}

	out := stdout.String()
	if !strings.Contains(out, "BEGIN SSH SIGNATURE") {
		t.Errorf("output missing SSH SIGNATURE armor:\n%s", out)
	}

	// Parse the signature and verify against the agent's pubkey.
	seed, _ := paths.ReadSeed()
	_, pub, _ := casket.DeriveAgentKey(seed, "plumb")
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

func TestCommitSignHelper_RequiresSeed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	stdin := bytes.NewReader([]byte("data"))
	stdout := &bytes.Buffer{}

	err := CommitSignHelper("https://cairn.example.com", "plumb", "git", stdin, stdout)
	if err == nil {
		t.Error("expected error when seed file is missing")
	}
}
