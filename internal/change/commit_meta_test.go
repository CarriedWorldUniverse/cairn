package change

import (
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

// tipCommitObject reads the commit object at the change's current head.
func tipCommitObject(t *testing.T, e *Engine, head string) (name, email, message string) {
	t.Helper()
	c, err := e.git.CommitObject(plumbing.NewHash(head))
	if err != nil {
		t.Fatalf("CommitObject(%s): %v", head, err)
	}
	return c.Author.Name, c.Author.Email, c.Message
}

func TestCommitMessageStored(t *testing.T) {
	e := newTestEngine(t)
	root, _ := e.LineByName("main")
	ch, err := e.CreateChange(root.ID, "agent-a")
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	e.SetIdentity("Agent A", "agent@x.io")
	res, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("one\n")}, nil, "fix the parser")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	_, _, msg := tipCommitObject(t, e, res.HeadCommit)
	if !strings.HasPrefix(msg, "fix the parser") {
		t.Fatalf("message %q does not start with %q", msg, "fix the parser")
	}
	if !strings.Contains(msg, "Change-Id: ") {
		t.Fatalf("message %q missing Change-Id trailer", msg)
	}
}

func TestCommitDefaultMessage(t *testing.T) {
	e := newTestEngine(t)
	root, _ := e.LineByName("main")
	ch, err := e.CreateChange(root.ID, "agent-a")
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	res, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("one\n")}, nil, "")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	_, _, msg := tipCommitObject(t, e, res.HeadCommit)
	if !strings.HasPrefix(msg, "snapshot") {
		t.Fatalf("empty-message commit %q does not start with %q", msg, "snapshot")
	}
}

func TestCommitIdentity(t *testing.T) {
	e := newTestEngine(t)
	root, _ := e.LineByName("main")
	ch, err := e.CreateChange(root.ID, "agent-a")
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	e.SetIdentity("Jane Dev", "jane@x.io")
	res, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("one\n")}, nil, "msg")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	name, email, _ := tipCommitObject(t, e, res.HeadCommit)
	if name != "Jane Dev" {
		t.Fatalf("author name = %q, want %q", name, "Jane Dev")
	}
	if email != "jane@x.io" {
		t.Fatalf("author email = %q, want %q", email, "jane@x.io")
	}
}

func TestCommitDefaultIdentityEmail(t *testing.T) {
	e := newTestEngine(t)
	root, _ := e.LineByName("main")
	ch, err := e.CreateChange(root.ID, "agent-a")
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	// No SetIdentity: writeCommit must fall back to a non-routable placeholder,
	// NOT the broken "@cairn" email.
	res, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("one\n")}, nil, "")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	_, email, _ := tipCommitObject(t, e, res.HeadCommit)
	if !strings.HasSuffix(email, "@users.noreply.cairn") {
		t.Fatalf("default email = %q, want suffix @users.noreply.cairn", email)
	}
	if strings.HasSuffix(email, "@cairn") {
		t.Fatalf("default email %q must not use the broken @cairn suffix", email)
	}
}
