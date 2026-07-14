package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// TestE2E_PullRestructureNoResurrection is the #103 regression: machine A
// pushes a restructure (renames/moves) and machine B — an expressed line with
// a CLEAN working change — pulls. The old paths must be REMOVED from B's disk
// and must NOT be swept into the working change as adds (which would have
// silently half-undone the restructure on B's next commit).
func TestE2E_PullRestructureNoResurrection(t *testing.T) {
	skipOnWindows(t)
	bare := makeSeededBareRepo(t) // seeds readme.txt on the default branch

	// Machine A: add the OLD layout on the default branch and push.
	workA := t.TempDir()
	repoA, err := git.PlainClone(workA, false, &git.CloneOptions{URL: bare})
	if err != nil {
		t.Fatalf("clone A: %v", err)
	}
	wtA, err := repoA.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	writeA := func(rel, content string) {
		t.Helper()
		full := filepath.Join(workA, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := wtA.Add(rel); err != nil {
			t.Fatal(err)
		}
	}
	commitPushA := func(msg string) {
		t.Helper()
		if _, err := wtA.Commit(msg, &git.CommitOptions{All: true, Author: &object.Signature{Name: "a", Email: "a@x"}}); err != nil {
			t.Fatalf("commit A: %v", err)
		}
		if err := repoA.Push(&git.PushOptions{}); err != nil {
			t.Fatalf("push A: %v", err)
		}
	}
	writeA("stream/one.gd", "a\n")
	writeA("cave/two.gd", "b\n")
	writeA("NOTES.md", "m\n")
	commitPushA("old layout")

	// Machine B: cairn clone with the old layout; working change stays CLEAN.
	dirB := t.TempDir()
	mustRun(t, "clone", bare, dirB)
	def := soleExpressedDir(t, dirB)
	for _, p := range []string{"stream/one.gd", "cave/two.gd", "NOTES.md"} {
		if _, err := os.Stat(filepath.Join(dirB, def, p)); err != nil {
			t.Fatalf("B missing pre-restructure file %s: %v", p, err)
		}
	}

	// Machine A: restructure — move everything, push.
	mv := func(from, to string) {
		t.Helper()
		full := filepath.Join(workA, to)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(workA, from), full); err != nil {
			t.Fatal(err)
		}
		if _, err := wtA.Remove(from); err != nil {
			t.Fatal(err)
		}
		if _, err := wtA.Add(to); err != nil {
			t.Fatal(err)
		}
	}
	mv("stream/one.gd", "client/one.gd")
	mv("cave/two.gd", "experimental/two.gd")
	mv("NOTES.md", "docs/design/NOTES.md")
	commitPushA("restructure")

	// Machine B pulls.
	mustRun(t, "pull", "--repo", dirB)

	// OLD paths gone from disk; NEW paths present.
	for _, p := range []string{"stream/one.gd", "cave/two.gd", "NOTES.md"} {
		if _, err := os.Stat(filepath.Join(dirB, def, p)); !os.IsNotExist(err) {
			t.Errorf("old path %s still on disk after pull (stat err=%v) — #103 regression", p, err)
		}
	}
	for _, p := range []string{"client/one.gd", "experimental/two.gd", "docs/design/NOTES.md"} {
		if _, err := os.Stat(filepath.Join(dirB, def, p)); err != nil {
			t.Errorf("new path %s missing after pull: %v", p, err)
		}
	}

	// The working change must be CLEAN: no resurrected adds.
	out := mustRunOut(t, "diff", "--repo", dirB, def)
	if strings.TrimSpace(out) != "" {
		t.Errorf("working diff not empty after pull — old files swept in as changes:\n%s", out)
	}
}
