package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUnexpressRefusesUnsealedWork: unexpress without --force must refuse when
// the branch folder contains un-sealed edits (openRepoSynced captures them into
// the working change so the dirty-guard fires). With --force it must succeed.
func TestUnexpressRefusesUnsealedWork(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	mustRun(t, "init", root)
	// init expresses the root branch; express a child branch off it.
	child := "feature"
	mustRun(t, "express", "--repo", root, "--from", soleExpressedDir(t, root), child)

	// Write a file into the child folder WITHOUT committing → un-sealed work.
	if err := os.WriteFile(
		filepath.Join(root, child, "unsaved.txt"),
		[]byte("work in progress\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	// unexpress without --force must error and mention "un-sealed".
	if err := run([]string{"unexpress", "--repo", root, child}); err == nil {
		t.Fatal("unexpress without --force: expected error for un-sealed work, got nil")
	} else if !strings.Contains(err.Error(), "un-sealed") {
		t.Fatalf("unexpress error should mention 'un-sealed', got: %v", err)
	}

	// The child folder must still exist (not silently removed).
	if _, err := os.Stat(filepath.Join(root, child)); err != nil {
		t.Fatalf("child folder removed despite failed unexpress: %v", err)
	}

	// unexpress --force must succeed, discarding the un-sealed work.
	mustRun(t, "unexpress", "--repo", root, "--force", child)

	// The child folder must now be gone.
	if _, err := os.Stat(filepath.Join(root, child)); !os.IsNotExist(err) {
		t.Fatalf("child folder still present after --force unexpress (err=%v)", err)
	}
}

// TestWCCEditsAutoCaptured: a working-copy edit with NO commit is captured by
// the command-start auto-snapshot, so status/diff/log all observe it.
func TestWCCEditsAutoCaptured(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	mustRun(t, "init", root)
	def := soleExpressedDir(t, root)

	if err := os.WriteFile(filepath.Join(root, def, "a.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// status shows the addition without any commit.
	st := mustRunOut(t, "status", "--repo", root, def)
	if !strings.Contains(st, "A a.txt") {
		t.Fatalf("status missing 'A a.txt':\n%s", st)
	}
	// diff shows the addition (Added files print a status line, no unified hunk).
	diff := mustRunOut(t, "diff", "--repo", root, def)
	if !strings.Contains(diff, "added: a.txt") {
		t.Fatalf("diff missing the addition:\n%s", diff)
	}
	// log labels the top (working) entry.
	lg := mustRunOut(t, "log", "--repo", root, def)
	if !strings.Contains(lg, "(working)") {
		t.Fatalf("log missing (working) label:\n%s", lg)
	}
}

// TestWCCCommitSealsAndAdvances: commit seals the working change; a later edit
// shows a fresh delta against the sealed commit.
func TestWCCCommitSealsAndAdvances(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	mustRun(t, "init", root)
	def := soleExpressedDir(t, root)

	if err := os.WriteFile(filepath.Join(root, def, "a.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "-m", "add a", def)

	// log shows the sealed entry with its message, NOT labeled working.
	lg := mustRunOut(t, "log", "--repo", root, def)
	if !strings.Contains(lg, "add a") {
		t.Fatalf("log missing sealed 'add a' entry:\n%s", lg)
	}

	// Edit again: status now shows the delta against the sealed commit.
	if err := os.WriteFile(filepath.Join(root, def, "a.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := mustRunOut(t, "status", "--repo", root, def)
	if !strings.Contains(st, "M a.txt") {
		t.Fatalf("status missing 'M a.txt' against sealed commit:\n%s", st)
	}
	diff := mustRunOut(t, "diff", "--repo", root, def)
	if !strings.Contains(diff, "-v1") || !strings.Contains(diff, "+v2") {
		t.Fatalf("diff missing -v1/+v2:\n%s", diff)
	}
}

// TestWCCUndoRecoversUnsealed: an auto-captured (unsealed) edit is undone, the
// file reverts on disk, and a subsequent status sees it gone (self-heal: the
// next SyncWorking re-amends consistently).
func TestWCCUndoRecoversUnsealed(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	mustRun(t, "init", root)
	def := soleExpressedDir(t, root)

	// Seed a sealed baseline so undo has a prior op to revert to.
	if err := os.WriteFile(filepath.Join(root, def, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "-m", "base", def)

	// Make an uncommitted edit, then run a command so it is auto-captured.
	if err := os.WriteFile(filepath.Join(root, def, "extra.txt"), []byte("extra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := mustRunOut(t, "status", "--repo", root, def) // auto-captures the edit
	if !strings.Contains(st, "A extra.txt") {
		t.Fatalf("precondition: edit not captured:\n%s", st)
	}

	// Undo reverts the auto-snapshot op: the file reverts on disk.
	mustRun(t, "undo", "--repo", root)
	if _, err := os.Stat(filepath.Join(root, def, "extra.txt")); !os.IsNotExist(err) {
		t.Fatalf("extra.txt still on disk after undo (err=%v)", err)
	}

	// status now shows no addition (self-heal: next SyncWorking re-amends cleanly).
	st2 := mustRunOut(t, "status", "--repo", root, def)
	if strings.Contains(st2, "extra.txt") {
		t.Fatalf("status still references extra.txt after undo:\n%s", st2)
	}
}
