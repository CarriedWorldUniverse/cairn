package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2E_StatusShowsChangesAndAhead commits a file, then in the expressed
// folder modifies it, adds a file, and deletes another, and asserts `cairn
// status` reports M/A/D lines and a real `ahead` count.
func TestE2E_StatusShowsChangesAndAhead(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	mustRun(t, "init", root)
	def := soleExpressedDir(t, root)

	write := func(branch, name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, branch, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Seed the root line so the forked branch has a base commit to be ahead of.
	write(def, "base.txt", "base\n")
	mustRun(t, "commit", "--repo", root, def)

	// Fork "exp" off the root, then commit twice so ahead == 2 (a real count,
	// not the old 0/1 flag).
	mustRun(t, "express", "--repo", root, "exp")
	write("exp", "keep.txt", "v1\n")
	write("exp", "gone.txt", "bye\n")
	mustRun(t, "commit", "--repo", root, "exp") // exp commit 1
	write("exp", "keep.txt", "v1b\n")
	mustRun(t, "commit", "--repo", root, "exp") // exp commit 2

	// Now make working-copy changes (uncommitted) on exp.
	write("exp", "keep.txt", "v2\n") // modify
	write("exp", "added.txt", "fresh\n")
	if err := os.Remove(filepath.Join(root, "exp", "gone.txt")); err != nil {
		t.Fatal(err) // delete
	}

	out := mustRunOut(t, "status", "--repo", root, "exp")
	if !strings.Contains(out, "M keep.txt") {
		t.Fatalf("status missing modified line:\n%s", out)
	}
	if !strings.Contains(out, "A added.txt") {
		t.Fatalf("status missing added line:\n%s", out)
	}
	if !strings.Contains(out, "D gone.txt") {
		t.Fatalf("status missing deleted line:\n%s", out)
	}
	// ahead: a real count — 2 commits since the branch point.
	if !strings.Contains(out, "ahead:     2") {
		t.Fatalf("status ahead not the real count:\n%s", out)
	}

	// cairn diff (working-vs-tip) should show the unified hunk for keep.txt.
	diff := mustRunOut(t, "diff", "--repo", root, "exp")
	if !strings.Contains(diff, "@@") {
		t.Fatalf("diff missing hunk header:\n%s", diff)
	}
	if !strings.Contains(diff, "-v1b") || !strings.Contains(diff, "+v2") {
		t.Fatalf("diff missing -v1b/+v2:\n%s", diff)
	}
}

// TestE2E_DiffTwoCommits commits, changes + commits again, and diffs the two
// commit shas (captured from `cairn commit` stdout, which prints the HeadCommit).
func TestE2E_DiffTwoCommits(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	mustRun(t, "init", root)
	def := soleExpressedDir(t, root)

	if err := os.WriteFile(filepath.Join(root, def, "f.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha1 := strings.TrimSpace(mustRunOut(t, "commit", "--repo", root, def))
	if err := os.WriteFile(filepath.Join(root, def, "f.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha2 := strings.TrimSpace(mustRunOut(t, "commit", "--repo", root, def))
	if sha1 == "" || sha2 == "" || sha1 == sha2 {
		t.Fatalf("bad shas: %q %q", sha1, sha2)
	}

	diff := mustRunOut(t, "diff", "--repo", root, sha1, sha2)
	if !strings.Contains(diff, "-one") || !strings.Contains(diff, "+two") {
		t.Fatalf("commit-vs-commit diff missing change:\n%s", diff)
	}
}

// TestE2E_DiffBinary asserts a NUL-containing file diffs as "Binary files differ".
func TestE2E_DiffBinary(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	mustRun(t, "init", root)
	def := soleExpressedDir(t, root)

	if err := os.WriteFile(filepath.Join(root, def, "b.bin"), []byte{0x00, 0x01}, 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, def)
	if err := os.WriteFile(filepath.Join(root, def, "b.bin"), []byte{0x00, 0x02}, 0o644); err != nil {
		t.Fatal(err)
	}
	diff := mustRunOut(t, "diff", "--repo", root, def)
	if !strings.Contains(diff, "Binary files differ: b.bin") {
		t.Fatalf("expected binary notice, got:\n%s", diff)
	}
}
