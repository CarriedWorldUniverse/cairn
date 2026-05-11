package cairn

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/CarriedWorldUniverse/cairn/services/cairn/hook"
)

// CommitSignHelper reads data from stdin, loads the agent's
// OpenSSH-format ed25519 private key from <HostDir>/<slug>.key (mode
// 0600), produces an SSH-format signature in the given namespace
// (typically "git"), and writes the PEM-armored result to stdout.
//
// This is the function git invokes when configured with
//
//	gpg.format       = ssh
//	gpg.ssh.program  = cairn commit-sign-helper --slug <slug>
//
// The agent generates and owns its keypair on disk; this helper only
// reads it. Keys whose underlying type is not ed25519 are rejected.
func CommitSignHelper(instanceURL, slug, namespace string, in io.Reader, out io.Writer) error {
	if namespace == "" {
		namespace = "git"
	}

	paths, err := ResolvePaths(instanceURL)
	if err != nil {
		return err
	}
	keyFile := paths.KeyFile(slug)

	info, err := os.Stat(keyFile)
	if err != nil {
		return fmt.Errorf("cairn commit-sign-helper: stat key %q: %w", keyFile, err)
	}
	if perm := info.Mode().Perm(); perm&0077 != 0 {
		return fmt.Errorf("cairn cli: private key %q has group/other bits set (mode %#o)", keyFile, perm)
	}
	keyBytes, err := os.ReadFile(keyFile)
	if err != nil {
		return fmt.Errorf("cairn commit-sign-helper: read key %q: %w", keyFile, err)
	}

	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		return fmt.Errorf("cairn commit-sign-helper: parse key %q: %w", keyFile, err)
	}
	if signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return fmt.Errorf("cairn commit-sign-helper: key %q has type %q, only ed25519 supported",
			keyFile, signer.PublicKey().Type())
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

