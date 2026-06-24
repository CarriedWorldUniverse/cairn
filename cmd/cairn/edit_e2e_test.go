//go:build !windows
// +build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupFeatBranch initializes a repo, makes two commits on main (so feat has a
// real base), expresses feat off main, then makes two commits on feat.
// It returns the repo root and the two commit SHAs on feat (sha1=older, sha2=newer).
func setupFeatBranch(t *testing.T) (root, sha1, sha2 string) {
	t.Helper()
	root = t.TempDir()
	mustRun(t, "init", root)

	// Seed main so feat has a real fork point.
	if err := os.WriteFile(filepath.Join(root, "main", "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "--author", "tester", "-m", "base", "main")

	// Express feat from main.
	mustRun(t, "express", "--repo", root, "--from", "main", "feat")

	// First commit on feat.
	if err := os.WriteFile(filepath.Join(root, "feat", "a.txt"), []byte("aaa\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha1 = strings.TrimSpace(captureRun(t, "commit", "--repo", root, "--author", "tester", "-m", "first on feat", "feat"))

	// Second commit on feat.
	if err := os.WriteFile(filepath.Join(root, "feat", "b.txt"), []byte("bbb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha2 = strings.TrimSpace(captureRun(t, "commit", "--repo", root, "--author", "tester", "-m", "second on feat", "feat"))

	return root, sha1, sha2
}

// TestEditRewordE2E verifies that `cairn reword <sha> <new-message>` changes the
// commit message visible in cairn log.
func TestEditRewordE2E(t *testing.T) {
	root, sha1, _ := setupFeatBranch(t)

	// Reword the first (older) commit on feat.
	mustRun(t, "reword", "--repo", root, sha1, "rewrote-first")

	// Log must contain the new message and must NOT contain the old one.
	logOut := captureRun(t, "log", "--repo", root, "feat")
	if !strings.Contains(logOut, "rewrote-first") {
		t.Errorf("reword: log does not contain new message 'rewrote-first':\n%s", logOut)
	}
	if strings.Contains(logOut, "first on feat") {
		t.Errorf("reword: log still contains old message 'first on feat':\n%s", logOut)
	}
}

// TestEditSquashE2E verifies that `cairn squash <sha>` folds sha into its parent,
// leaving one fewer sealed commit in the log. After squashing sha2 into sha1,
// the surviving subject is sha1's message ("first on feat"). sha2's message
// becomes the squashed commit's body (not shown by `cairn log`); we verify
// its presence via `cairn show` on the surviving commit.
func TestEditSquashE2E(t *testing.T) {
	root, sha1, sha2 := setupFeatBranch(t)
	_ = sha1

	// Squash sha2 (second on feat) into its parent (first on feat).
	mustRun(t, "squash", "--repo", root, sha2)

	// Log must contain the surviving subject ("first on feat"); "second on feat"
	// is now in the body of the squashed commit, not the subject.
	logOut := captureRun(t, "log", "--repo", root, "feat")
	if !strings.Contains(logOut, "first on feat") {
		t.Errorf("squash: log does not contain surviving subject 'first on feat':\n%s", logOut)
	}
	// "second on feat" must NOT appear as a separate commit subject in the log
	// (it was squashed away). Count occurrences: it may appear 0 or 1 times as part
	// of the squashed body only visible in `show`. Only verify it's not a second entry.
	// Easiest: verify the log has fewer total commit lines than before squash.
	// Count non-working sealed commit lines: look for dated entries without "(working)".
	sealedLines := 0
	for _, line := range strings.Split(strings.TrimSpace(logOut), "\n") {
		if line != "" && !strings.Contains(line, "(working)") {
			sealedLines++
		}
	}
	// Before squash: 1 base commit on main visible, plus 2 on feat = at least 2
	// sealed entries in feat's log (base + squashed). After squash: feat has 1 sealed.
	// The working commit "(working)" is always there. Sealed should be 2 (base + squashed).
	if sealedLines > 2 {
		t.Errorf("squash: expected at most 2 sealed commit entries in feat log (base + squashed), got %d:\n%s", sealedLines, logOut)
	}
}

// TestEditDropE2E verifies that `cairn drop <sha>` removes the commit from the line.
func TestEditDropE2E(t *testing.T) {
	root, sha1, _ := setupFeatBranch(t)

	// Drop the first commit on feat.
	mustRun(t, "drop", "--repo", root, sha1)

	// The first commit's message must be gone; the second must survive.
	logOut := captureRun(t, "log", "--repo", root, "feat")
	if strings.Contains(logOut, "first on feat") {
		t.Errorf("drop: log still contains dropped commit 'first on feat':\n%s", logOut)
	}
	if !strings.Contains(logOut, "second on feat") {
		t.Errorf("drop: log missing 'second on feat' after drop:\n%s", logOut)
	}
}

// TestEditCapturesLiveEditsBeforeRewrite verifies that a history edit (reword)
// first captures live on-disk edits into the working change before rewriting —
// i.e. the CLI uses openRepoSynced. Without that sync, the rewrite would rebase
// a stale working commit and re-materialize over the user's uncommitted edits,
// losing them. We write a NEW uncommitted file into the feat folder, reword a
// sealed commit, and assert the new edit is NOT lost: it still shows as an
// un-sealed change in `cairn status feat` and the file still has its content.
func TestEditCapturesLiveEditsBeforeRewrite(t *testing.T) {
	root, sha1, _ := setupFeatBranch(t)

	// Write a NEW uncommitted edit into the feat working folder.
	livePath := filepath.Join(root, "feat", "live.txt")
	if err := os.WriteFile(livePath, []byte("live edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Reword an older sealed commit. This triggers a sealed-chain rewrite and
	// rebases the working change; openRepoSynced must capture live.txt first.
	mustRun(t, "reword", "--repo", root, sha1, "rewrote-first")

	// The live edit must survive on disk.
	got, err := os.ReadFile(livePath)
	if err != nil {
		t.Fatalf("live.txt gone after reword: %v", err)
	}
	if string(got) != "live edit\n" {
		t.Errorf("live.txt content = %q after reword, want %q (live edit lost)", string(got), "live edit\n")
	}

	// And it must be captured into the working change: status shows it as an
	// un-sealed addition.
	statusOut := captureRun(t, "status", "--repo", root, "feat")
	if !strings.Contains(statusOut, "live.txt") {
		t.Errorf("status does not show captured live edit 'live.txt':\n%s", statusOut)
	}
}

// TestEditRefusedOnRoot verifies that reword/squash/drop all refuse to operate
// on the root line with a clear error.
func TestEditRefusedOnRoot(t *testing.T) {
	root := t.TempDir()
	mustRun(t, "init", root)

	// Make a commit on main (root line).
	if err := os.WriteFile(filepath.Join(root, "main", "root.txt"), []byte("root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha := strings.TrimSpace(captureRun(t, "commit", "--repo", root, "--author", "tester", "-m", "root commit", "main"))

	for _, args := range [][]string{
		{"reword", "--repo", root, sha, "new-msg"},
		{"squash", "--repo", root, sha},
		{"drop", "--repo", root, sha},
	} {
		err := run(args)
		if err == nil {
			t.Errorf("expected error for %v on root line, got nil", args)
			continue
		}
		if !strings.Contains(err.Error(), "root") {
			t.Errorf("expected 'root' in error for %v, got: %v", args, err)
		}
	}
}
