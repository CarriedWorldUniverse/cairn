package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// advanceSeededBareRepo clones the bare repo into a temp non-bare working copy,
// writes path=content on the default branch, commits, and pushes back — so the
// bare's default branch advances by one commit independently of any cairn clone.
func advanceSeededBareRepo(t *testing.T, bare, path, content string) {
	t.Helper()
	work := t.TempDir()
	repo, err := git.PlainClone(work, false, &git.CloneOptions{URL: bare})
	if err != nil {
		t.Fatalf("clone bare to advance: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, path), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add(path); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("advance", &git.CommitOptions{Author: &object.Signature{Name: "o", Email: "o@x"}}); err != nil {
		t.Fatalf("commit advance: %v", err)
	}
	if err := repo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{"refs/heads/*:refs/heads/*"}}); err != nil {
		t.Fatalf("push advance: %v", err)
	}
}

func TestE2E_CommitAutoSync(t *testing.T) {
	skipOnWindows(t)
	origin := makeSeededBareRepo(t)
	B := filepath.Join(t.TempDir(), "B")
	mustRun(t, "clone", origin, B)
	def := soleExpressedDir(t, B)
	mustRun(t, "config", "--repo", B, "autosync", "true")
	advanceSeededBareRepo(t, origin, "remote.txt", "R\n") // remote advances independently
	if err := os.WriteFile(filepath.Join(B, def, "local.txt"), []byte("L\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", B, def) // autosync pulls origin AFTER the commit
	if _, err := os.Stat(filepath.Join(B, def, "remote.txt")); err != nil {
		t.Fatalf("autosync didn't bring remote work: %v", err)
	}
	if _, err := os.Stat(filepath.Join(B, def, "local.txt")); err != nil {
		t.Fatalf("local work lost: %v", err)
	}
}

func TestE2E_CommitAutoSyncOfflineStillCommits(t *testing.T) {
	skipOnWindows(t)
	dir := filepath.Join(t.TempDir(), "local")
	mustRun(t, "init", dir) // no origin at all
	mustRun(t, "config", "--repo", dir, "autosync", "true")
	def := soleExpressedDir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, def, "x.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", dir, def) // must SUCCEED despite autosync on + no origin
}

func TestE2E_CommitAutoSyncBrokenOriginStillCommits(t *testing.T) {
	skipOnWindows(t)
	dir := filepath.Join(t.TempDir(), "local")
	mustRun(t, "init", dir)
	// origin is configured but points at a nonexistent repo, so the autosync
	// fetch itself errors (configured-but-broken path, not "no origin").
	badURL := filepath.Join(t.TempDir(), "does-not-exist.git")
	mustRun(t, "remote", "add", "--repo", dir, "origin", badURL)
	mustRun(t, "config", "--repo", dir, "autosync", "true")
	def := soleExpressedDir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, def, "x.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", dir, def) // must SUCCEED despite autosync on + broken origin
	if _, err := os.Stat(filepath.Join(dir, def, "x.txt")); err != nil {
		t.Fatalf("local work lost: %v", err)
	}
}

func TestE2E_PushAutoPullRetry(t *testing.T) {
	skipOnWindows(t)
	origin := makeSeededBareRepo(t)
	A := filepath.Join(t.TempDir(), "A")
	B := filepath.Join(t.TempDir(), "B")
	mustRun(t, "clone", origin, A)
	mustRun(t, "clone", origin, B)
	def := soleExpressedDir(t, A)
	if err := os.WriteFile(filepath.Join(A, def, "fromA.txt"), []byte("A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", A, def)
	mustRun(t, "push", "--repo", A)
	if err := os.WriteFile(filepath.Join(B, def, "fromB.txt"), []byte("B\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", B, def)
	mustRun(t, "push", "--repo", B) // no --force: must auto-pull+retry and SUCCEED
	if _, err := os.Stat(filepath.Join(B, def, "fromA.txt")); err != nil {
		t.Fatalf("auto-pull didn't bring A's work: %v", err)
	}
	C := filepath.Join(t.TempDir(), "C")
	mustRun(t, "clone", origin, C)
	if _, err := os.Stat(filepath.Join(C, def, "fromB.txt")); err != nil {
		t.Fatalf("B's push didn't land on remote: %v", err)
	}
}

func TestE2E_PushAutoPullConflictStops(t *testing.T) {
	skipOnWindows(t)
	origin := makeSeededBareRepo(t) // seeds readme.txt
	A := filepath.Join(t.TempDir(), "A")
	B := filepath.Join(t.TempDir(), "B")
	mustRun(t, "clone", origin, A)
	mustRun(t, "clone", origin, B)
	def := soleExpressedDir(t, A)

	// A edits readme.txt and publishes.
	if err := os.WriteFile(filepath.Join(A, def, "readme.txt"), []byte("A-edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", A, def)
	mustRun(t, "push", "--repo", A)

	// B edits the SAME readme.txt region and commits — pushing now diverges and
	// the auto-pull merge conflicts, so push must stop and ask to resolve.
	if err := os.WriteFile(filepath.Join(B, def, "readme.txt"), []byte("B-edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", B, def)
	err := run([]string{"push", "--repo", B})
	if err == nil {
		t.Fatalf("push over a conflicting divergence should error, got nil")
	}
	if !strings.Contains(err.Error(), "resolve, then push") {
		t.Fatalf("conflict-stop error %q should say 'resolve, then push'", err.Error())
	}

	// readme.txt now has conflict markers on disk; write the resolution, resolve,
	// then push must succeed (the merge is now a remote descendant).
	if err := os.WriteFile(filepath.Join(B, def, "readme.txt"), []byte("resolved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "resolve", "--repo", B, def, "readme.txt") // resolve <branch> <path>
	mustRun(t, "push", "--repo", B)

	C := filepath.Join(t.TempDir(), "C")
	mustRun(t, "clone", origin, C)
	got, err := os.ReadFile(filepath.Join(C, def, "readme.txt"))
	if err != nil {
		t.Fatalf("read C readme.txt: %v", err)
	}
	if string(got) != "resolved\n" {
		t.Fatalf("remote readme.txt = %q, want resolved", got)
	}
}

func TestE2E_PushForceBypassesRetry(t *testing.T) {
	skipOnWindows(t)
	origin := makeSeededBareRepo(t)
	A := filepath.Join(t.TempDir(), "A")
	B := filepath.Join(t.TempDir(), "B")
	mustRun(t, "clone", origin, A)
	mustRun(t, "clone", origin, B)
	def := soleExpressedDir(t, A)
	// A and B edit the SAME file differently
	os.WriteFile(filepath.Join(A, def, "readme.txt"), []byte("A-version\n"), 0o644)
	mustRun(t, "commit", "--repo", A, def)
	mustRun(t, "push", "--repo", A)
	os.WriteFile(filepath.Join(B, def, "readme.txt"), []byte("B-version\n"), 0o644)
	mustRun(t, "commit", "--repo", B, def)
	// force push goes straight through, no pull/conflict
	mustRun(t, "push", "--repo", B, "--force")
	// remote now has B's version (force won)
	C := filepath.Join(t.TempDir(), "C")
	mustRun(t, "clone", origin, C)
	got, _ := os.ReadFile(filepath.Join(C, def, "readme.txt"))
	if string(got) != "B-version\n" {
		t.Fatalf("force push didn't win: %q", got)
	}
}

// TestE2E_PushDivergentRebasesLinear: a CLEAN divergence (local commits vs an
// independently-advanced remote) must reconcile by REBASING — linear history,
// no 2-parent "merge remote-tracking" commit. Regression test for the
// double-commit report.
func TestE2E_PushDivergentRebasesLinear(t *testing.T) {
	skipOnWindows(t)
	origin := makeSeededBareRepo(t)
	B := filepath.Join(t.TempDir(), "B")
	mustRun(t, "clone", origin, B)
	def := soleExpressedDir(t, B)

	// Two local commits on distinct files.
	if err := os.WriteFile(filepath.Join(B, def, "b.txt"), []byte("B\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", B, def)
	if err := os.WriteFile(filepath.Join(B, def, "c.txt"), []byte("C\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", B, def)

	// Remote advances an independent file → clean divergence.
	advanceSeededBareRepo(t, origin, "remote.txt", "R\n")

	// Push reconciles by rebasing the two local commits onto the remote tip.
	mustRun(t, "push", "--repo", B)

	// Origin history must be linear: every commit has ≤1 parent (no merge).
	repo, err := git.PlainOpen(origin)
	if err != nil {
		t.Fatalf("open origin: %v", err)
	}
	ref, err := repo.Reference(plumbing.NewBranchReferenceName(def), true)
	if err != nil {
		t.Fatalf("ref %s: %v", def, err)
	}
	iter, err := repo.Log(&git.LogOptions{From: ref.Hash()})
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if err := iter.ForEach(func(c *object.Commit) error {
		if c.NumParents() > 1 {
			t.Fatalf("merge commit after clean divergent push: %q (%d parents)", strings.TrimSpace(c.Message), c.NumParents())
		}
		return nil
	}); err != nil {
		t.Fatalf("iterate history: %v", err)
	}
}
