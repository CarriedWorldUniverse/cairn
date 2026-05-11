//
// Cairn-specific code; AGPLv3. See LICENSING.md.

package cairn

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
)

// AgentAttach posts an attachment request for an agent-generated keypair
// against a Cairn instance.
//
// The operator (or a bootstrap script) is responsible for having
// generated the keypair beforehand — typically with
// `ssh-keygen -t ed25519 -f ~/.config/cairn/<host>/<slug>.key`. This
// function reads the resulting `<slug>.key.pub` file (full OpenSSH text
// line) and POSTs its contents to
// /api/cairn/v1/agents/attachment-requests.
//
// If token is non-empty, an `Authorization: token <token>` header is
// set; this lets owners auto-approve their own attachment in one step
// (server-side wiring lands in Task 4). With no token the request goes
// out unauthed and lands in pending status; the proposed owner must
// approve it via the web UI or `cairn agents` command.
//
// The returned id, fingerprint, and status are printed to out. When the
// status is "pending", a next-step hint is also printed.
func AgentAttach(instanceURL, owner, slug, domain, pubkeyFile, token string, out io.Writer) error {
	if pubkeyFile == "" {
		return fmt.Errorf("cairn agent attach: --pubkey is required")
	}
	content, err := os.ReadFile(pubkeyFile)
	if err != nil {
		return fmt.Errorf("cairn agent attach: read pubkey file %q: %w", pubkeyFile, err)
	}
	pubkeyContent := strings.TrimRight(string(content), "\r\n")
	if pubkeyContent == "" {
		return fmt.Errorf("cairn agent attach: pubkey file %q is empty", pubkeyFile)
	}
	parsed, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pubkeyContent))
	if err != nil {
		return fmt.Errorf("cairn agent attach: parse pubkey %s: %w", pubkeyFile, err)
	}
	if parsed.Type() != ssh.KeyAlgoED25519 {
		return fmt.Errorf("cairn agent attach: pubkey at %s is not ed25519 (got %s)", pubkeyFile, parsed.Type())
	}

	c := NewClient(instanceURL, token)
	resp, err := c.PostAttachmentRequest(context.Background(), PostAttachmentRequestInput{
		OwnerUsername: owner,
		Slug:          slug,
		Domain:        domain,
		PubkeyContent: pubkeyContent,
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "id: %d\n", resp.ID)
	fmt.Fprintf(out, "fingerprint: %s\n", resp.Fingerprint)
	fmt.Fprintf(out, "status: %s\n", resp.Status)
	if resp.Status == "pending" {
		fmt.Fprintf(out, "note: awaiting %s's approval — owner can approve via the Cairn web UI or `cairn agents approve %s`\n",
			owner, resp.Fingerprint)
	}
	return nil
}
