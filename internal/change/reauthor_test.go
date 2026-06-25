package change

import (
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

// commitAt loads the commit object at sha (test helper).
func commitAt(t *testing.T, e *Engine, sha string) (name, email, message string, when int64) {
	t.Helper()
	c, err := e.git.CommitObject(plumbing.NewHash(sha))
	if err != nil {
		t.Fatalf("CommitObject(%s): %v", sha, err)
	}
	return c.Author.Name, c.Author.Email, c.Message, c.Author.When.Unix()
}

// allReachableEmails walks every commit reachable from the catalogue anchors and
// returns the set of author emails seen — used to assert no placeholder survives.
func allReachableEmails(t *testing.T, e *Engine) map[string]bool {
	t.Helper()
	anchors, err := e.commitAnchors()
	if err != nil {
		t.Fatalf("commitAnchors: %v", err)
	}
	order, err := e.topoCommits(anchors)
	if err != nil {
		t.Fatalf("topoCommits: %v", err)
	}
	emails := map[string]bool{}
	for _, sha := range order {
		_, email, _, _ := commitAt(t, e, sha)
		emails[email] = true
	}
	return emails
}

// seedMixedIdentities builds: r1 on main (placeholder cairn identity), a feature
// line forked off it with a commit under a different placeholder, and a second
// main commit under a real (non-matching) identity. Returns the three head SHAs
// and r1's original author timestamp.
func seedMixedIdentities(t *testing.T, e *Engine) (mainTip, featTip string, r1When int64) {
	t.Helper()
	root, _ := e.LineByName("main")

	ch1, err := e.CreateChange(root.ID, "cairn")
	if err != nil {
		t.Fatalf("CreateChange main: %v", err)
	}
	e.SetIdentity("cairn", "cairn@users.noreply.cairn")
	r1, err := e.Commit(ch1.ID, map[string][]byte{"a.txt": []byte("one\n")}, nil, "first on main")
	if err != nil {
		t.Fatalf("Commit main: %v", err)
	}
	_, _, _, r1When = commitAt(t, e, r1.HeadCommit)

	feat, err := e.CreateLine("feat", root.ID)
	if err != nil {
		t.Fatalf("CreateLine: %v", err)
	}
	chf, err := e.CreateChange(feat.ID, "agent-bob")
	if err != nil {
		t.Fatalf("CreateChange feat: %v", err)
	}
	e.SetIdentity("agent bob", "agent-bob@users.noreply.cairn")
	// Carry the parent's a.txt so merge-forward adopts an identical tree and emits
	// no extra merge commit — keeps the placeholder count deterministic at two.
	if _, err := e.Commit(chf.ID, map[string][]byte{"a.txt": []byte("one\n"), "b.txt": []byte("bee\n")}, nil, "work on feat"); err != nil {
		t.Fatalf("Commit feat: %v", err)
	}

	ch2, err := e.CreateChange(root.ID, "Real Person")
	if err != nil {
		t.Fatalf("CreateChange main 2: %v", err)
	}
	e.SetIdentity("Real Person", "real@corp.com")
	if _, err := e.Commit(ch2.ID, map[string][]byte{"c.txt": []byte("see\n")}, nil, "real commit"); err != nil {
		t.Fatalf("Commit main 2: %v", err)
	}

	root, _ = e.LineByName("main")
	feat, _ = e.LineByName("feat")
	return root.TipCommit, feat.TipCommit, r1When
}

func TestReauthorRetagsPlaceholdersAcrossAllLines(t *testing.T) {
	e := newTestEngine(t)
	_, _, r1When := seedMixedIdentities(t, e)

	res, err := e.Reauthor(ReauthorSpec{
		OldEmail: "*@users.noreply.cairn",
		NewName:  "Jacinta",
		NewEmail: "jacinta@darksoft.co.nz",
	})
	if err != nil {
		t.Fatalf("Reauthor: %v", err)
	}
	// r1 and the feat commit matched; r2 (real) did not but rebuilt onto the new r1.
	if res.Matched != 2 {
		t.Fatalf("Matched = %d, want 2", res.Matched)
	}
	if res.Rewritten != 3 {
		t.Fatalf("Rewritten = %d, want 3", res.Rewritten)
	}

	// No placeholder email survives anywhere reachable.
	emails := allReachableEmails(t, e)
	if emails["cairn@users.noreply.cairn"] || emails["agent-bob@users.noreply.cairn"] {
		t.Fatalf("a placeholder email survived: %v", emails)
	}
	if !emails["jacinta@darksoft.co.nz"] {
		t.Fatalf("new identity not present: %v", emails)
	}
	// The non-matching real identity is preserved.
	if !emails["real@corp.com"] {
		t.Fatalf("non-matching identity was clobbered: %v", emails)
	}

	// The retagged root commit keeps its message AND its original timestamp.
	root, _ := e.LineByName("main")
	var foundR1 bool
	anchors, _ := e.commitAnchors()
	order, _ := e.topoCommits(anchors)
	for _, sha := range order {
		name, email, msg, when := commitAt(t, e, sha)
		if msgHasSubject(msg, "first on main") {
			foundR1 = true
			if name != "Jacinta" || email != "jacinta@darksoft.co.nz" {
				t.Fatalf("r1 retagged author = %s <%s>, want Jacinta", name, email)
			}
			if when != r1When {
				t.Fatalf("r1 timestamp changed: got %d, want %d", when, r1When)
			}
		}
	}
	if !foundR1 {
		t.Fatal("could not find the retagged 'first on main' commit")
	}
	if root.TipCommit == "" {
		t.Fatal("main tip empty after reauthor")
	}
}

func TestReauthorIsIdempotent(t *testing.T) {
	e := newTestEngine(t)
	seedMixedIdentities(t, e)
	spec := ReauthorSpec{OldEmail: "*@users.noreply.cairn", NewName: "Jacinta", NewEmail: "jacinta@darksoft.co.nz"}
	if _, err := e.Reauthor(spec); err != nil {
		t.Fatalf("Reauthor 1: %v", err)
	}
	res, err := e.Reauthor(spec)
	if err != nil {
		t.Fatalf("Reauthor 2: %v", err)
	}
	if res.Rewritten != 0 || res.Matched != 0 {
		t.Fatalf("second pass changed something: %+v", res)
	}
}

func TestReauthorDryRunChangesNothing(t *testing.T) {
	e := newTestEngine(t)
	mainTipBefore, _, _ := seedMixedIdentities(t, e)

	res, err := e.Reauthor(ReauthorSpec{
		OldEmail: "*@users.noreply.cairn",
		NewName:  "Jacinta",
		NewEmail: "jacinta@darksoft.co.nz",
		DryRun:   true,
	})
	if err != nil {
		t.Fatalf("Reauthor dry-run: %v", err)
	}
	if res.Matched != 2 || res.Rewritten != 3 {
		t.Fatalf("dry-run counts = %+v, want Matched 2 Rewritten 3", res)
	}
	root, _ := e.LineByName("main")
	if root.TipCommit != mainTipBefore {
		t.Fatalf("dry-run moved the main tip: %s -> %s", mainTipBefore, root.TipCommit)
	}
	if !allReachableEmails(t, e)["cairn@users.noreply.cairn"] {
		t.Fatal("dry-run rewrote the placeholder (should be untouched)")
	}
}

// msgHasSubject reports whether the commit message's first line is subject.
func msgHasSubject(msg, subject string) bool {
	for i := 0; i < len(msg); i++ {
		if msg[i] == '\n' {
			return msg[:i] == subject
		}
	}
	return msg == subject
}
