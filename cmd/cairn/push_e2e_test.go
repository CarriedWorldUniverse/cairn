package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// TestE2E_PushSingleBranch covers `cairn push <remote> <branch>`: only that line
// is published, so you can feed a feature line to a remote for a PR without
// touching the (remote-tracked) default branch.
func TestE2E_PushSingleBranch(t *testing.T) {
	skipOnWindows(t)
	bare := makeSeededBareRepo(t)
	dir := t.TempDir()
	mustRun(t, "clone", bare, dir)
	def := soleExpressedDir(t, dir)

	mustRun(t, "express", "--repo", dir, "--from", def, "feat")
	if err := os.WriteFile(filepath.Join(dir, "feat", "x.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", dir, "feat", "-m", "feat work")

	mustRun(t, "push", "--repo", dir, "origin", "feat")

	bareRepo, err := git.PlainOpen(bare)
	if err != nil {
		t.Fatalf("PlainOpen bare: %v", err)
	}
	if _, err := bareRepo.Reference(plumbing.ReferenceName("refs/heads/feat"), false); err != nil {
		t.Fatalf("single-branch push did not publish refs/heads/feat: %v", err)
	}
}

// makeSeededBareRepo builds a bare git repo seeded with a default branch and a
// committed readme.txt, then returns the bare repo's path (usable as a clone
// URL). The seed goes in via a temporary non-bare working clone that commits and
// pushes back, so the bare ends up with a real default branch + one commit for
// cairn clone to import.
func makeSeededBareRepo(t *testing.T) string {
	t.Helper()
	bare := t.TempDir()
	if _, err := git.PlainInit(bare, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	work := t.TempDir()
	repo, err := git.PlainInit(work, false)
	if err != nil {
		t.Fatalf("PlainInit work: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, "readme.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("readme.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("init", &git.CommitOptions{Author: &object.Signature{Name: "o", Email: "o@x"}}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{bare}}); err != nil {
		t.Fatalf("CreateRemote: %v", err)
	}
	if err := repo.Push(&git.PushOptions{RemoteName: "origin"}); err != nil {
		t.Fatalf("push seed: %v", err)
	}
	return bare
}

// soleExpressedDir returns the single subdirectory of dir that is not ".cairn"
// (the one expressed branch folder created by clone).
func soleExpressedDir(t *testing.T, dir string) string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", dir, err)
	}
	found := ""
	for _, e := range ents {
		if !e.IsDir() || e.Name() == ".cairn" {
			continue
		}
		if found != "" {
			t.Fatalf("expected one expressed dir, found %q and %q", found, e.Name())
		}
		found = e.Name()
	}
	if found == "" {
		t.Fatalf("no expressed dir under %s", dir)
	}
	return found
}

// TestE2E_PushCairnKindStillSucceeds asserts that pushing to a remote registered
// with kind "cairn" still succeeds (the cairn->git fallback notice goes to stderr
// and is non-fatal).
func TestE2E_PushCairnKindStillSucceeds(t *testing.T) {
	skipOnWindows(t)
	bare := makeSeededBareRepo(t)
	dir := filepath.Join(t.TempDir(), "work")
	mustRun(t, "clone", bare, dir)
	if err := run([]string{"remote", "add", "--repo", dir, "--cairn", "peer", bare}); err != nil {
		t.Fatalf("remote add --cairn: %v", err)
	}
	if err := run([]string{"push", "--repo", dir, "peer"}); err != nil {
		t.Fatalf("push to cairn-kind remote should succeed: %v", err)
	}
}

func TestE2E_CloneWorkPushReclone(t *testing.T) {
	skipOnWindows(t)
	origin := makeSeededBareRepo(t)
	dirA := filepath.Join(t.TempDir(), "A")
	mustRun(t, "clone", origin, dirA)
	def := soleExpressedDir(t, dirA) // the one non-".cairn" subdir
	if err := os.WriteFile(filepath.Join(dirA, def, "new.txt"), []byte("NEW\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", dirA, def)
	mustRun(t, "push", "--repo", dirA)
	dirB := filepath.Join(t.TempDir(), "B")
	mustRun(t, "clone", origin, dirB)
	if _, err := os.Stat(filepath.Join(dirB, def, "new.txt")); err != nil {
		t.Fatalf("pushed new.txt not present after re-clone: %v", err)
	}
}

// TestE2E_PushSealedOnly (P2): push publishes the SEALED commit, never the
// working snapshot, and un-sealed working edits are not pushed.
func TestE2E_PushSealedOnly(t *testing.T) {
	skipOnWindows(t)
	bare := t.TempDir()
	if _, err := git.PlainInit(bare, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	root := t.TempDir()
	mustRun(t, "init", root)
	if err := os.WriteFile(filepath.Join(root, "main", "f.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "main", "-m", "real commit")
	mustRun(t, "remote", "add", "--repo", root, "origin", bare)
	// An un-sealed working edit that must NOT be published.
	if err := os.WriteFile(filepath.Join(root, "main", "uncommitted.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "push", "--repo", root, "origin", "main")

	br, err := git.PlainOpen(bare)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := br.Reference(plumbing.NewBranchReferenceName("main"), false)
	if err != nil {
		t.Fatalf("main not pushed: %v", err)
	}
	c, err := br.CommitObject(ref.Hash())
	if err != nil {
		t.Fatal(err)
	}
	if c.Message == "(working)\n" || c.Message[:len("(working)")] == "(working)" {
		t.Fatalf("pushed a working snapshot, not the sealed commit: %q", c.Message)
	}
	tree, _ := c.Tree()
	if _, err := tree.File("uncommitted.txt"); err == nil {
		t.Fatal("un-sealed working file was published")
	}
}

// TestE2E_PushDefaultsToCurrentLine (P1): `push` from inside a branch folder
// publishes only that line, never main.
func TestE2E_PushDefaultsToCurrentLine(t *testing.T) {
	skipOnWindows(t)
	bare := t.TempDir()
	if _, err := git.PlainInit(bare, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	root := t.TempDir()
	mustRun(t, "init", root)
	if err := os.WriteFile(filepath.Join(root, "main", "f.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "main", "-m", "base")
	mustRun(t, "remote", "add", "--repo", root, "origin", bare)
	mustRun(t, "express", "--repo", root, "--from", "main", "feat")
	if err := os.WriteFile(filepath.Join(root, "feat", "g.txt"), []byte("w\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "feat", "-m", "feat work")

	// push with no branch arg, from inside the feat folder.
	mustRun(t, "push", "--repo", filepath.Join(root, "feat"), "origin")

	br, err := git.PlainOpen(bare)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := br.Reference(plumbing.NewBranchReferenceName("feat"), false); err != nil {
		t.Fatalf("feat not pushed: %v", err)
	}
	if _, err := br.Reference(plumbing.NewBranchReferenceName("main"), false); err == nil {
		t.Fatal("push from inside feat must NOT publish main")
	}
}
