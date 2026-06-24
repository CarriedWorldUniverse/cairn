package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExecBitRoundTrips verifies that an executable file keeps its +x bit
// through a commit + re-materialize round-trip.
func TestExecBitRoundTrips(t *testing.T) {
	skipOnWindows(t)

	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	branch, err := r.DefaultBranch()
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}

	script := filepath.Join(root, branch, "build.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(branch, "add build.sh"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Commit re-materializes the folder. Stat the re-materialized file.
	info, err := os.Stat(script)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("build.sh lost its executable bit: mode=%v", info.Mode())
	}
}

// TestSymlinkRoundTrips verifies a symlink stays a symlink (not a copy) and
// keeps its target through a commit + re-materialize round-trip.
func TestSymlinkRoundTrips(t *testing.T) {
	skipOnWindows(t)

	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	branch, err := r.DefaultBranch()
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}

	folder := filepath.Join(root, branch)
	if err := os.WriteFile(filepath.Join(folder, "target.txt"), []byte("payload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.txt", filepath.Join(folder, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(branch, "add symlink"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	link := filepath.Join(folder, "link")
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link is not a symlink after round-trip: mode=%v", info.Mode())
	}
	tgt, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if tgt != "target.txt" {
		t.Errorf("link target = %q, want target.txt", tgt)
	}
}

// TestDanglingSymlinkCommits verifies a symlink to a nonexistent target commits
// without error and round-trips as a symlink to "nonexistent".
func TestDanglingSymlinkCommits(t *testing.T) {
	skipOnWindows(t)

	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	branch, err := r.DefaultBranch()
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}

	folder := filepath.Join(root, branch)
	if err := os.Symlink("nonexistent", filepath.Join(folder, "broken")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(branch, "dangling symlink"); err != nil {
		t.Fatalf("Commit dangling symlink: %v", err)
	}

	link := filepath.Join(folder, "broken")
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("broken is not a symlink: mode=%v", info.Mode())
	}
	tgt, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if tgt != "nonexistent" {
		t.Errorf("broken target = %q, want nonexistent", tgt)
	}
}

// TestRegularFileStaysRegular verifies a normal 0o644 file round-trips as a
// regular, non-executable, non-symlink file.
func TestRegularFileStaysRegular(t *testing.T) {
	skipOnWindows(t)

	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	branch, err := r.DefaultBranch()
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}

	f := filepath.Join(root, branch, "notes.txt")
	if err := os.WriteFile(f, []byte("plain\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(branch, "regular file"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	info, err := os.Lstat(f)
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if info.Mode()&0o111 != 0 {
		t.Errorf("notes.txt unexpectedly executable: mode=%v", info.Mode())
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Errorf("notes.txt unexpectedly a symlink: mode=%v", info.Mode())
	}
}
