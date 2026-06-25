package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2E_ReauthorRetagsAcrossLines drives `cairn reauthor` over a repo with
// placeholder identities on the root line and a feature line, and verifies every
// matching commit is retagged while the message and a non-matching identity are
// preserved.
func TestE2E_ReauthorRetagsAcrossLines(t *testing.T) {
	root := t.TempDir()
	mustRun(t, "init", root)

	// A commit on main authored by the cairn placeholder identity.
	if err := os.WriteFile(filepath.Join(root, "main", "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "--author", "cairn", "main", "-m", "first on main")

	// A feature line with a commit under a different placeholder name.
	mustRun(t, "express", "--repo", root, "--from", "main", "feat")
	if err := os.WriteFile(filepath.Join(root, "feat", "b.txt"), []byte("bee\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "--author", "agent-bob", "feat", "-m", "work on feat")

	// Dry run reports matches but changes nothing.
	dry := captureRun(t, "reauthor", "--repo", root, "--old-email", "*@users.noreply.cairn",
		"--name", "Jacinta", "--email", "jacinta@darksoft.co.nz", "--dry-run")
	if !strings.Contains(dry, "would rewrite") {
		t.Fatalf("dry-run output missing 'would rewrite': %q", dry)
	}

	// Real run.
	out := captureRun(t, "reauthor", "--repo", root, "--old-email", "*@users.noreply.cairn",
		"--name", "Jacinta", "--email", "jacinta@darksoft.co.nz")
	if !strings.Contains(out, "rewrote") {
		t.Fatalf("reauthor output missing 'rewrote': %q", out)
	}

	// Every commit on both lines now carries the new identity, not the placeholder.
	for _, branch := range []string{"main", "feat"} {
		logOut := captureRun(t, "log", "--repo", root, branch)
		if strings.Contains(logOut, "users.noreply.cairn") {
			t.Fatalf("%s log still shows a placeholder email:\n%s", branch, logOut)
		}
	}

	// The root commit keeps its message under the new author.
	mainLog := captureRun(t, "log", "--repo", root, "main")
	if !strings.Contains(mainLog, "first on main") {
		t.Fatalf("main log lost the original message:\n%s", mainLog)
	}
}

// TestE2E_ReauthorRequiresFilters guards against the footgun of matching every
// commit: with no --old-name/--old-email, the command must refuse.
func TestE2E_ReauthorRequiresFilters(t *testing.T) {
	root := t.TempDir()
	mustRun(t, "init", root)
	_, err := captureRunResult(t, "reauthor", "--repo", root, "--name", "X", "--email", "x@y.z")
	if err == nil {
		t.Fatal("reauthor with no match filter should error")
	}
	if !strings.Contains(err.Error(), "old-name") && !strings.Contains(err.Error(), "old-email") {
		t.Fatalf("error should mention the missing filter flags: %v", err)
	}
}
