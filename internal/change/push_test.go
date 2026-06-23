package change

import (
	"os"
	"path/filepath"
	"runtime"
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
	r, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("a\n")})
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
	if _, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("1\n")}); err != nil {
		t.Fatalf("commit1: %v", err)
	}
	if err := e.AddRemote("origin", bareDir, "git"); err != nil {
		t.Fatalf("AddRemote: %v", err)
	}
	if err := e.PushToRemote("origin", false); err != nil {
		t.Fatalf("push1: %v", err)
	}

	advanceBareMainIndependently(t, bareDir)

	if _, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("2\n")}); err != nil {
		t.Fatalf("commit2: %v", err)
	}
	if err := e.PushToRemote("origin", false); err == nil {
		t.Fatal("expected non-fast-forward rejection")
	}
	if err := e.PushToRemote("origin", true); err != nil {
		t.Fatalf("force push should succeed: %v", err)
	}
}
