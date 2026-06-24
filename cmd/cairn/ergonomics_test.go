package main

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConflictReturnsErrConflicts verifies that cmdCommit returns errConflicts
// (not nil) when the commit produces merge conflicts. This is the signal that
// main() should use to exit with code 2 rather than 0.
func TestConflictReturnsErrConflicts(t *testing.T) {
	// Reproduce the exact conflict setup from TestE2E_ConflictResolveViaCLI:
	// two branches edit the same file differently; committing the child after
	// the parent has advanced triggers a 3-way merge conflict.
	root := t.TempDir()
	mustRun(t, "init", root)
	if err := os.WriteFile(filepath.Join(root, "main", "f.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "main")
	mustRun(t, "express", "--repo", root, "exp")
	// Advance main with a conflicting edit.
	if err := os.WriteFile(filepath.Join(root, "main", "f.txt"), []byte("X\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "main")
	// Edit the same file on exp with a different value.
	if err := os.WriteFile(filepath.Join(root, "exp", "f.txt"), []byte("Y\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Committing exp now must return errConflicts, not nil.
	err := run([]string{"commit", "--repo", root, "exp"})
	if !errors.Is(err, errConflicts) {
		t.Fatalf("commit with conflicts: got %v, want errConflicts", err)
	}
}

// TestHelpExitsClean verifies that -h on a subcommand returns flag.ErrHelp
// (which main() maps to exit 0), not a generic error that would produce exit 1
// with the "cairn: flag: help requested" prefix.
func TestHelpExitsClean(t *testing.T) {
	err := run([]string{"commit", "-h"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("commit -h: got %v, want flag.ErrHelp", err)
	}

	err = run([]string{"status", "-h"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("status -h: got %v, want flag.ErrHelp", err)
	}
}

// TestNotFoundKeepsContext verifies that expressing a branch from a
// non-existent parent surfaces the missing entity name in the error message,
// rather than the bare "not found" that mapErr previously returned.
func TestNotFoundKeepsContext(t *testing.T) {
	root := t.TempDir()
	mustRun(t, "init", root)

	// Attempt to express a new branch from a parent that does not exist.
	// Note: --from must come before the positional branch name; flag.Parse stops
	// at the first non-flag argument, so flags after positionals are silently ignored.
	err := run([]string{"express", "--repo", root, "--from", "doesnotexist", "newbranch"})
	if err == nil {
		t.Fatal("expected error expressing from non-existent parent, got nil")
	}
	if !strings.Contains(err.Error(), "doesnotexist") {
		t.Fatalf("error %q should contain the missing entity name %q", err.Error(), "doesnotexist")
	}
}
