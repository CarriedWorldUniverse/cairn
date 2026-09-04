package change

import (
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
)

// pushFixture builds an engine holding a live "feat" line with sealed history,
// plus a bare git remote named "origin" to push it at.
func pushFixture(t *testing.T) (*Engine, string) {
	t.Helper()
	skipOnWindowsPush(t)
	bareDir := t.TempDir()
	if _, err := git.PlainInit(bareDir, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	main, err := e.LineByName("main")
	if err != nil {
		t.Fatalf("LineByName main: %v", err)
	}
	mch, err := e.CreateChange(main.ID, "a")
	if err != nil {
		t.Fatalf("CreateChange main: %v", err)
	}
	if _, err := e.Commit(mch.ID, map[string][]byte{"a.txt": []byte("a\n")}, nil, "seed"); err != nil {
		t.Fatalf("Commit main: %v", err)
	}
	feat, err := e.CreateLine("feat", main.ID)
	if err != nil {
		t.Fatalf("CreateLine feat: %v", err)
	}
	fch, err := e.CreateChange(feat.ID, "a")
	if err != nil {
		t.Fatalf("CreateChange feat: %v", err)
	}
	if _, err := e.Commit(fch.ID, map[string][]byte{"b.txt": []byte("b\n")}, nil, "feat work"); err != nil {
		t.Fatalf("Commit feat: %v", err)
	}
	if err := e.AddRemote("origin", bareDir, "git"); err != nil {
		t.Fatalf("AddRemote: %v", err)
	}
	return e, "origin"
}

// abandonByName resolves a line name to its id and abandons it.
func abandonByName(t *testing.T, e *Engine, name string) {
	t.Helper()
	l, err := e.LineByName(name)
	if err != nil {
		t.Fatalf("LineByName %s: %v", name, err)
	}
	if err := e.AbandonLine(l.ID); err != nil {
		t.Fatalf("AbandonLine %s: %v", name, err)
	}
}

// A push that publishes nothing must never report that it published something.
//
// Export projects refs/heads/<name> only for lines with status='open' that have
// a sealed tip. A branch-scoped push builds the refspec
// "refs/heads/<b>:refs/heads/<b>" regardless, so when the projection has no such
// ref the refspec matches nothing, go-git finds no work to do, and every layer
// above reports success. `cairn push origin <branch>` printed
// "pushed <branch> -> origin" and exited 0 having created no ref on the remote
// (#159) — which makes the documented `cairn commit && cairn push` idiom unsafe,
// since the push is exactly the step that is supposed to prove the work landed.

// TestPushBranchRefusesWhenNothingWouldBePublished is the regression: an
// abandoned line is not projected, so pushing it must fail loudly rather than
// claim success.
func TestPushBranchRefusesWhenNothingWouldBePublished(t *testing.T) {
	e, remote := pushFixture(t)

	abandonByName(t, e, "feat")

	err := e.PushToRemoteBranch(remote, "feat", false)
	if err == nil {
		t.Fatal("push of an abandoned line reported success; it publishes nothing, so it must fail")
	}
	if !strings.Contains(err.Error(), "feat") {
		t.Fatalf("error does not name the branch: %v", err)
	}
	if !strings.Contains(err.Error(), "abandoned") {
		t.Fatalf("error does not explain that the line is abandoned, so the operator cannot act on it: %v", err)
	}
}

// The guard must not fire on the ordinary case: a live line with sealed history
// pushes exactly as before.
func TestPushBranchStillPublishesALiveLine(t *testing.T) {
	e, remote := pushFixture(t)
	if err := e.PushToRemoteBranch(remote, "feat", false); err != nil {
		t.Fatalf("push of a live line failed: %v", err)
	}
}

// A whole-repo push is unscoped and must stay unaffected by the branch guard,
// even when an abandoned line exists alongside live ones.
func TestWholeRepoPushUnaffectedByAnAbandonedLine(t *testing.T) {
	e, remote := pushFixture(t)
	abandonByName(t, e, "feat")
	if err := e.PushToRemote(remote, false); err != nil {
		t.Fatalf("whole-repo push failed because an abandoned line exists: %v", err)
	}
}
