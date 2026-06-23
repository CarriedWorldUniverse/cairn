package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
)

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
