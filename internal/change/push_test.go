package change

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func skipOnWindowsPush(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("go-git local-transport flakes under Windows file locking")
	}
}

func TestPushToRemoteGitRefs(t *testing.T) {
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
	main, _ := e.LineByName("main")
	ch, _ := e.CreateChange(main.ID, "a")
	r, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("a\n")}, nil, "")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := e.Tag("v1", r.HeadCommit, "rel"); err != nil {
		t.Fatalf("Tag: %v", err)
	}
	if err := e.AddRemote("origin", bareDir, "git"); err != nil {
		t.Fatalf("AddRemote: %v", err)
	}
	if err := e.PushToRemote("origin", false); err != nil {
		t.Fatalf("PushToRemote: %v", err)
	}

	bare, err := git.PlainOpen(bareDir)
	if err != nil {
		t.Fatalf("PlainOpen bare: %v", err)
	}
	mref, err := bare.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil || mref.Hash().String() != r.HeadCommit {
		t.Fatalf("bare refs/heads/main = %v (%v), want %s", mref, err, r.HeadCommit)
	}
	if _, err := bare.Reference(plumbing.NewTagReferenceName("v1"), true); err != nil {
		t.Fatalf("bare tag v1 missing: %v", err)
	}
	if _, err := bare.Reference(plumbing.ReferenceName("refs/cairn/change/"+ch.ID), true); err == nil {
		t.Fatal("refs/cairn/* must not be pushed to a git remote")
	}
}

// advanceBareMainIndependently makes the bare remote's main diverge from the
// local engine: it clones the bare into a temp working tree, commits an
// unrelated change on the default branch, and pushes it back. After this the
// engine's next plain push to origin is a non-fast-forward.
func advanceBareMainIndependently(t *testing.T, bareDir string) {
	t.Helper()
	work := t.TempDir()
	// The bare repo's default HEAD points at refs/heads/master (go-git's init
	// default), but the engine pushed refs/heads/main, so check out main
	// explicitly rather than relying on the bare's HEAD.
	repo, err := git.PlainClone(work, false, &git.CloneOptions{
		URL:           bareDir,
		ReferenceName: plumbing.NewBranchReferenceName("main"),
	})
	if err != nil {
		t.Fatalf("clone bare: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, "diverge.txt"), []byte("diverge\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := wt.Add("diverge.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit("diverge", &git.CommitOptions{
		Author: &object.Signature{Name: "o", Email: "o@x"},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := repo.Push(&git.PushOptions{}); err != nil {
		t.Fatalf("push diverge: %v", err)
	}
}

func TestPushNonFastForwardThenForce(t *testing.T) {
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
	main, _ := e.LineByName("main")
	ch, _ := e.CreateChange(main.ID, "a")
	if _, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("1\n")}, nil, ""); err != nil {
		t.Fatalf("commit1: %v", err)
	}
	if err := e.AddRemote("origin", bareDir, "git"); err != nil {
		t.Fatalf("AddRemote: %v", err)
	}
	if err := e.PushToRemote("origin", false); err != nil {
		t.Fatalf("push1: %v", err)
	}

	advanceBareMainIndependently(t, bareDir)

	if _, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("2\n")}, nil, ""); err != nil {
		t.Fatalf("commit2: %v", err)
	}
	if err := e.PushToRemote("origin", false); err == nil {
		t.Fatal("expected non-fast-forward rejection")
	}
	if err := e.PushToRemote("origin", true); err != nil {
		t.Fatalf("force push should succeed: %v", err)
	}
}

// conflictedLine builds a bare remote plus an engine with an "exp" line whose
// tip is a 2-parent merge commit carrying open diff3 conflict markers (issue
// #93's repro): main and exp both edit f.txt, exp's commit conflicts against
// main's, and the conflict is left open (never resolved).
func conflictedLine(t *testing.T) (e *Engine, bareDir string) {
	t.Helper()
	bareDir = t.TempDir()
	if _, err := git.PlainInit(bareDir, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	main, _ := e.LineByName("main")
	seedLineTip(t, e, main.ID, map[string][]byte{"f.txt": []byte("base\n")})
	exp, _ := e.CreateLine("exp", main.ID)
	mc, _ := e.CreateChange(main.ID, "m")
	if _, err := e.Commit(mc.ID, map[string][]byte{"f.txt": []byte("X\n")}, nil, ""); err != nil {
		t.Fatalf("commit main: %v", err)
	}
	ec, _ := e.CreateChange(exp.ID, "e")
	if _, err := e.Commit(ec.ID, map[string][]byte{"f.txt": []byte("Y\n")}, nil, ""); err != nil {
		t.Fatalf("commit exp (conflict): %v", err)
	}
	if err := e.AddRemote("origin", bareDir, "git"); err != nil {
		t.Fatalf("AddRemote: %v", err)
	}
	return e, bareDir
}

// bareBranchRef reports whether bareDir has refs/heads/<branch> and, if so,
// its hash.
func bareBranchRef(t *testing.T, bareDir, branch string) (string, bool) {
	t.Helper()
	bare, err := git.PlainOpen(bareDir)
	if err != nil {
		t.Fatalf("PlainOpen bare: %v", err)
	}
	ref, err := bare.Reference(plumbing.NewBranchReferenceName(branch), true)
	if err != nil {
		return "", false
	}
	return ref.Hash().String(), true
}

func TestPushToRemoteRefusedWithOpenConflict(t *testing.T) {
	skipOnWindowsPush(t)
	e, bareDir := conflictedLine(t)
	err := e.PushToRemote("origin", false)
	if !errors.Is(err, ErrPushHasConflict) {
		t.Fatalf("PushToRemote with open conflict: want ErrPushHasConflict, got %v", err)
	}
	if !strings.Contains(err.Error(), "exp") {
		t.Fatalf("error should name the conflicted branch %q: %v", "exp", err)
	}
	if _, ok := bareBranchRef(t, bareDir, "exp"); ok {
		t.Fatal("remote refs/heads/exp must not have been created by a refused push")
	}
}

func TestPushToRemoteBranchRefusedWithOpenConflict(t *testing.T) {
	skipOnWindowsPush(t)
	e, bareDir := conflictedLine(t)
	err := e.PushToRemoteBranch("origin", "exp", false)
	if !errors.Is(err, ErrPushHasConflict) {
		t.Fatalf("PushToRemoteBranch with open conflict: want ErrPushHasConflict, got %v", err)
	}
	if !strings.Contains(err.Error(), "exp") {
		t.Fatalf("error should name the conflicted branch %q: %v", "exp", err)
	}
	if _, ok := bareBranchRef(t, bareDir, "exp"); ok {
		t.Fatal("remote refs/heads/exp must not have been created by a refused push")
	}
}

func TestPushToRemoteForceOverridesConflictGate(t *testing.T) {
	skipOnWindowsPush(t)
	e, bareDir := conflictedLine(t)
	if err := e.PushToRemoteBranch("origin", "exp", true); err != nil {
		t.Fatalf("force push should bypass the conflict gate: %v", err)
	}
	if _, ok := bareBranchRef(t, bareDir, "exp"); !ok {
		t.Fatal("force push should have published refs/heads/exp despite the open conflict")
	}
}

func TestPushToRemoteCairnKindBypassesConflictGate(t *testing.T) {
	skipOnWindowsPush(t)
	e, bareDir := conflictedLine(t)
	if err := e.AddRemote("origin", bareDir, "cairn"); err != nil {
		t.Fatalf("AddRemote as cairn: %v", err)
	}
	if err := e.PushToRemoteBranch("origin", "exp", false); err != nil {
		t.Fatalf("cairn remote should not be gated by open conflicts: %v", err)
	}
	if _, ok := bareBranchRef(t, bareDir, "exp"); !ok {
		t.Fatal("cairn push should have published refs/heads/exp despite the open conflict")
	}
}

// TestPushSucceedsAfterAbandoningConflictedLine is the review-flagged
// over-blocking regression: AbandonLine (fold.go) flips the line's and its
// changes' status to 'abandoned' but never touches the conflict table, so an
// abandoned line can leave an orphaned OPEN conflict row behind. Before the
// l.status='open' / ch.status='open' filter, that orphan permanently blocked
// every whole-repo push even though the conflicted line no longer exists in
// any live sense. Abandoning the conflicted line must let an otherwise-clean
// push through.
func TestPushSucceedsAfterAbandoningConflictedLine(t *testing.T) {
	skipOnWindowsPush(t)
	e, bareDir := conflictedLine(t)
	exp, err := e.LineByName("exp")
	if err != nil {
		t.Fatalf("LineByName exp: %v", err)
	}
	if err := e.AbandonLine(exp.ID); err != nil {
		t.Fatalf("AbandonLine: %v", err)
	}
	if err := e.PushToRemote("origin", false); err != nil {
		t.Fatalf("PushToRemote after abandoning the conflicted line should succeed, got: %v", err)
	}
	if _, ok := bareBranchRef(t, bareDir, "main"); !ok {
		t.Fatal("expected refs/heads/main to have been published")
	}
}

// TestPushToRemoteBranchRefusedByConflictedTag is the security-flagged
// bypass regression: branchRefSpecs always appends refs/tags/* alongside the
// single named branch, but the branch-scoped gate used to only inspect that
// one line. Tagging a DIFFERENT conflicted line's tip ("cairn tag leak exp")
// then pushing a clean branch ("main") used to ship the marker-laden commit
// via the tag with no --force. Deleting the leaking tag must let the same
// push through; force must bypass it too.
func TestPushToRemoteBranchRefusedByConflictedTag(t *testing.T) {
	skipOnWindowsPush(t)
	e, bareDir := conflictedLine(t)
	exp, err := e.LineByName("exp")
	if err != nil {
		t.Fatalf("LineByName exp: %v", err)
	}
	if err := e.Tag("leak", exp.TipCommit, "t"); err != nil {
		t.Fatalf("Tag: %v", err)
	}

	err = e.PushToRemoteBranch("origin", "main", false)
	if !errors.Is(err, ErrPushHasConflict) {
		t.Fatalf("push of clean branch with a conflicted tag: want ErrPushHasConflict, got %v", err)
	}
	if !strings.Contains(err.Error(), "leak") {
		t.Fatalf("error should name the leaking tag %q: %v", "leak", err)
	}
	if _, ok := bareBranchRef(t, bareDir, "main"); ok {
		t.Fatal("remote refs/heads/main must not have been created while a conflicted tag would leak")
	}

	if err := e.DeleteTag("leak"); err != nil {
		t.Fatalf("DeleteTag: %v", err)
	}
	if err := e.PushToRemoteBranch("origin", "main", false); err != nil {
		t.Fatalf("push should succeed after deleting the leaking tag: %v", err)
	}
	if _, ok := bareBranchRef(t, bareDir, "main"); !ok {
		t.Fatal("expected refs/heads/main to be published after deleting the leaking tag")
	}
}

func TestPushToRemoteBranchForceOverridesTagConflictGate(t *testing.T) {
	skipOnWindowsPush(t)
	e, bareDir := conflictedLine(t)
	exp, err := e.LineByName("exp")
	if err != nil {
		t.Fatalf("LineByName exp: %v", err)
	}
	if err := e.Tag("leak", exp.TipCommit, "t"); err != nil {
		t.Fatalf("Tag: %v", err)
	}
	if err := e.PushToRemoteBranch("origin", "main", true); err != nil {
		t.Fatalf("force push should bypass the tag conflict gate: %v", err)
	}
	if _, ok := bareBranchRef(t, bareDir, "main"); !ok {
		t.Fatal("force push should have published refs/heads/main despite the leaking tag")
	}
}

func TestPushUnknownRemote(t *testing.T) {
	e := newTestEngine(t)
	if err := e.PushToRemote("nonexistent", false); err == nil || !strings.Contains(err.Error(), `no remote "nonexistent"`) {
		t.Fatalf("want clear no-remote error, got %v", err)
	}
}

func TestAddRemoteIdempotentAndListed(t *testing.T) {
	skipOnWindowsPush(t)
	e := newTestEngine(t)
	bare := t.TempDir()
	if _, err := git.PlainInit(bare, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	if err := e.AddRemote("origin", bare, ""); err != nil { // kind default "git"
		t.Fatalf("AddRemote: %v", err)
	}
	if err := e.AddRemote("origin", bare, "git"); err != nil { // idempotent
		t.Fatalf("re-add: %v", err)
	}
	rs, err := e.ListRemotes()
	if err != nil {
		t.Fatalf("ListRemotes: %v", err)
	}
	if len(rs) != 1 || rs[0].Name != "origin" || rs[0].Kind != "git" {
		t.Fatalf("remotes = %+v", rs)
	}
}
