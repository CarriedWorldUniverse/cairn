package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStashRoundTrip verifies the full stash → list → pop cycle:
//  1. Write a.txt into the expressed folder WITHOUT committing.
//  2. `cairn stash -m wip`  → a.txt disappears; status shows no changes.
//  3. `cairn stash list`    → "wip" appears in stdout.
//  4. `cairn stash pop`     → a.txt is back with original content; status shows A a.txt.
//  5. `cairn stash list`    → empty (nothing printed to stdout).
func TestStashRoundTrip(t *testing.T) {
	root := t.TempDir()
	mustRun(t, "init", root)
	branch := soleExpressedDir(t, root)

	// Write a.txt into the expressed folder (un-committed).
	aPath := filepath.Join(root, branch, "a.txt")
	if err := os.WriteFile(aPath, []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Stash the working change.
	mustRun(t, "stash", "--repo", root, "-m", "wip")

	// a.txt must be gone from disk.
	if _, err := os.Stat(aPath); err == nil {
		t.Fatal("a.txt still exists after stash; expected folder to be reset")
	}

	// status should show no changes (no A/M/D lines).
	statusOut := mustRunOut(t, "status", "--repo", root, branch)
	if strings.Contains(statusOut, "A a.txt") || strings.Contains(statusOut, "changes:") {
		t.Fatalf("status shows changes after stash; expected clean:\n%s", statusOut)
	}

	// stash list must contain the "wip" message.
	listOut := mustRunOut(t, "stash", "list", "--repo", root)
	if !strings.Contains(listOut, "wip") {
		t.Fatalf("stash list output missing 'wip':\n%s", listOut)
	}

	// Pop the stash.
	mustRun(t, "stash", "pop", "--repo", root)

	// a.txt must be back with original content.
	got, err := os.ReadFile(aPath)
	if err != nil {
		t.Fatalf("a.txt missing after pop: %v", err)
	}
	if string(got) != "wip\n" {
		t.Fatalf("a.txt content = %q, want %q", string(got), "wip\n")
	}

	// status should show A a.txt again.
	statusOut2 := mustRunOut(t, "status", "--repo", root, branch)
	if !strings.Contains(statusOut2, "A a.txt") {
		t.Fatalf("status after pop missing 'A a.txt':\n%s", statusOut2)
	}

	// stash list must now be empty (no output).
	listOut2 := mustRunOut(t, "stash", "list", "--repo", root)
	if strings.TrimSpace(listOut2) != "" {
		t.Fatalf("stash list should be empty after pop, got:\n%s", listOut2)
	}
}

// TestStashNothingToStash verifies that stashing with no working changes returns
// an error containing "nothing to stash".
func TestStashNothingToStash(t *testing.T) {
	root := t.TempDir()
	mustRun(t, "init", root)
	// No edits — stash should fail with "nothing to stash".
	err := run([]string{"stash", "--repo", root})
	if err == nil {
		t.Fatal("expected error when stashing clean working copy, got nil")
	}
	if !strings.Contains(err.Error(), "nothing to stash") {
		t.Fatalf("error %q should contain 'nothing to stash'", err.Error())
	}
}

// TestStashDrop stashes twice, drops the top, and asserts one entry remains.
func TestStashDrop(t *testing.T) {
	root := t.TempDir()
	mustRun(t, "init", root)
	branch := soleExpressedDir(t, root)

	// First stash: write + stash a.txt.
	aPath := filepath.Join(root, branch, "a.txt")
	if err := os.WriteFile(aPath, []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "stash", "--repo", root, "-m", "first")

	// Second stash: write + stash b.txt (folder was reset to clean after first stash).
	bPath := filepath.Join(root, branch, "b.txt")
	if err := os.WriteFile(bPath, []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "stash", "--repo", root, "-m", "second")

	// Drop the top entry (most recent = "second").
	mustRun(t, "stash", "drop", "--repo", root)

	// One entry remains — must still contain "first".
	listOut := mustRunOut(t, "stash", "list", "--repo", root)
	if !strings.Contains(listOut, "first") {
		t.Fatalf("stash list after drop should still contain 'first':\n%s", listOut)
	}
	if strings.Contains(listOut, "second") {
		t.Fatalf("stash list should not contain 'second' after drop:\n%s", listOut)
	}
}
