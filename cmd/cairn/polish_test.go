package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNotACairnRepo asserts that running a command in a non-repo directory
// returns a "not a cairn repo" error and does NOT silently create a .cairn dir.
func TestNotACairnRepo(t *testing.T) {
	dir := t.TempDir()
	err := run([]string{"status", "--repo", dir})
	if err == nil {
		t.Fatal("expected error for status in non-repo dir, got nil")
	}
	if !strings.Contains(err.Error(), "not a cairn repo") {
		t.Fatalf("error %q should contain %q", err.Error(), "not a cairn repo")
	}
	// The gate must NOT have bootstrapped .cairn as a side-effect.
	if _, statErr := os.Stat(filepath.Join(dir, ".cairn")); statErr == nil {
		t.Fatalf(".cairn was created in non-repo dir — gate failed to prevent bootstrap")
	}
}

// TestInitReInit asserts that re-running init on an already-initialised repo
// succeeds (exit 0, no error) without crashing or erroring.
func TestInitReInit(t *testing.T) {
	dir := t.TempDir()
	// First init — must succeed.
	mustRun(t, "init", dir)
	// Second init on the same dir — must also succeed (no-op).
	if err := run([]string{"init", dir}); err != nil {
		t.Fatalf("re-init should be a no-op, got error: %v", err)
	}
}

// TestCloneNonEmptyDestRefused asserts that cloning into a non-empty destination
// directory fails with a clear error BEFORE attempting the actual clone (so the
// bogus URL is never contacted).
func TestCloneNonEmptyDestRefused(t *testing.T) {
	dest := t.TempDir()
	// Put a file in the dest so it is non-empty.
	if err := os.WriteFile(filepath.Join(dest, "existing.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"clone", "file:///nonexistent-bogus-url.git", dest})
	if err == nil {
		t.Fatal("expected error cloning into non-empty dest, got nil")
	}
	if !strings.Contains(err.Error(), "already exists and is not empty") {
		t.Fatalf("error %q should contain %q", err.Error(), "already exists and is not empty")
	}
}

// TestUsageListsAllCommands asserts that the usage string mentions every
// subcommand name that the dispatch switch handles.
func TestUsageListsAllCommands(t *testing.T) {
	required := []string{
		"init", "clone", "express", "unexpress", "commit",
		"fold", "abandon", "status", "tree", "ls", "resolve",
		"remote", "push", "fetch", "pull", "pr", "config",
		"tag", "version", "release",
		"diff", "log", "show", "undo", "oplog",
	}
	for _, name := range required {
		if !strings.Contains(usage, name) {
			t.Errorf("usage string missing subcommand %q", name)
		}
	}
}
