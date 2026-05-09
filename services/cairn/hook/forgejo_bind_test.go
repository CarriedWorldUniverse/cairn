// Cairn-specific code; AGPLv3. See LICENSING.md.

package hook

import (
	"strings"
	"testing"
)

func TestParseAuthorAndMessage_VanillaCommit(t *testing.T) {
	raw := []byte("tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\n" +
		"parent 1234567890abcdef1234567890abcdef12345678\n" +
		"author Jane Doe <jane@example.com> 1700000000 +0000\n" +
		"committer Jane Doe <jane@example.com> 1700000000 +0000\n" +
		"\n" +
		"Add a thing\n\nLonger description.\n")

	email, msg := parseAuthorAndMessage(raw)
	if email != "jane@example.com" {
		t.Errorf("author email: got %q want jane@example.com", email)
	}
	if !strings.HasPrefix(msg, "Add a thing") {
		t.Errorf("message: got %q want it to start with 'Add a thing'", msg)
	}
}

func TestParseAuthorAndMessage_AgentCommitWithGPGSig(t *testing.T) {
	// Author header followed by a multi-line gpgsig header (continuation
	// lines start with one space) — parser must skip the continuations
	// and still find the author email.
	raw := []byte("tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\n" +
		"author nexus-plumb <nexus-plumb@darksoft.co.nz> 1700000000 +0000\n" +
		"committer nexus-plumb <nexus-plumb@darksoft.co.nz> 1700000000 +0000\n" +
		"gpgsig -----BEGIN SSH SIGNATURE-----\n" +
		" U1NIU0lHAAAAA...\n" +
		" ...continued...\n" +
		" -----END SSH SIGNATURE-----\n" +
		"\n" +
		"feat: do a thing\n\nCairn-Agent-Slug: plumb\nCairn-Agent-Domain: darksoft.co.nz\n")

	email, msg := parseAuthorAndMessage(raw)
	if email != "nexus-plumb@darksoft.co.nz" {
		t.Errorf("author email: got %q want nexus-plumb@darksoft.co.nz", email)
	}
	if !strings.Contains(msg, "Cairn-Agent-Slug: plumb") {
		t.Errorf("message: missing trailers, got %q", msg)
	}
}

func TestParseAuthorAndMessage_NoSeparator(t *testing.T) {
	// Malformed: no \n\n header/body separator. Should return empties,
	// which the caller (verifyOne via ParseAgentEmail) treats as a
	// non-agent commit and skips.
	raw := []byte("tree abc\nauthor x <x@y> 0 +0000\n")
	email, msg := parseAuthorAndMessage(raw)
	if email != "" || msg != "" {
		t.Errorf("malformed: got email=%q msg=%q want empties", email, msg)
	}
}

func TestParseAuthorAndMessage_NoAuthorLine(t *testing.T) {
	raw := []byte("tree abc\ncommitter x <x@y> 0 +0000\n\nbody\n")
	email, _ := parseAuthorAndMessage(raw)
	if email != "" {
		t.Errorf("missing author: got %q want empty", email)
	}
}

func TestExtractEmail(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Jane Doe <jane@example.com> 1700000000 +0000", "jane@example.com"},
		{"nexus-plumb <nexus-plumb@darksoft.co.nz> 0 +0000", "nexus-plumb@darksoft.co.nz"},
		{"no email here", ""},
		{"truncated <jane@example.com", ""},
		{"<just@email.com>", "just@email.com"},
	}
	for _, c := range cases {
		got := extractEmail([]byte(c.in))
		if got != c.want {
			t.Errorf("extractEmail(%q): got %q want %q", c.in, got, c.want)
		}
	}
}
