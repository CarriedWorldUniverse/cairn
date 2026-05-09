package cairn

import (
	"io"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"

	casket "github.com/CarriedWorldUniverse/casket-go"
	"github.com/CarriedWorldUniverse/cairn/services/cairn/hook"
)

// CommitSignHelper reads data from stdin, derives the agent's keypair
// from the owner's seed file via casket.DeriveAgentKey(seed, slug),
// produces an SSH-format signature in the given namespace (typically
// "git"), and writes the PEM-armored result to stdout.
//
// This is the function git invokes when configured with
//
//	gpg.format       = ssh
//	gpg.ssh.program  = cairn commit-sign-helper --slug <slug>
//
// The private key is never persisted to disk — it is derived on each
// signing call and discarded when the function returns.
func CommitSignHelper(instanceURL, slug, namespace string, in io.Reader, out io.Writer) error {
	if namespace == "" {
		namespace = "git"
	}

	paths, err := ResolvePaths(instanceURL)
	if err != nil {
		return err
	}
	seed, err := paths.ReadSeed()
	if err != nil {
		return err
	}

	priv, _, err := casket.DeriveAgentKey(seed, slug)
	if err != nil {
		return err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return err
	}

	data, err := io.ReadAll(in)
	if err != nil {
		return err
	}

	armored, err := hook.SignSSHSig(signer, data, namespace)
	if err != nil {
		return err
	}
	_, err = out.Write(armored)
	return err
}

// inferSlugFromKeyfile takes a keyfile path like
// "/home/user/.config/cairn/host/plumb.key.pub" and returns "plumb".
// Strips ".key.pub", ".pub", or ".key" suffix from the basename. If
// none match, returns the basename unchanged.
func inferSlugFromKeyfile(path string) string {
	if path == "" {
		return ""
	}
	base := filepath.Base(path)
	for _, suffix := range []string{".key.pub", ".pub", ".key"} {
		if strings.HasSuffix(base, suffix) {
			return strings.TrimSuffix(base, suffix)
		}
	}
	return base
}
