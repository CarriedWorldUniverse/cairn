package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2E_UndoOplog verifies that:
//  1. cairn undo reverts the tip (cairn log shows only the first commit after undo).
//  2. cairn oplog lists multiple operations (commits + the undo).
func TestE2E_UndoOplog(t *testing.T) {
	skipOnWindows(t)

	root := t.TempDir()
	mustRun(t, "init", root)

	// First commit
	if err := os.WriteFile(filepath.Join(root, "main", "a.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha1out := captureRun(t, "commit", "--repo", root, "--author", "tester", "-m", "first", "main")
	sha1 := strings.TrimSpace(sha1out)
	if sha1 == "" {
		t.Fatal("first commit returned empty sha")
	}

	// Second commit
	if err := os.WriteFile(filepath.Join(root, "main", "a.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha2out := captureRun(t, "commit", "--repo", root, "--author", "tester", "-m", "second", "main")
	sha2 := strings.TrimSpace(sha2out)
	if sha2 == "" || sha2 == sha1 {
		t.Fatalf("second commit sha = %q (sha1=%q)", sha2, sha1)
	}

	// Undo the second commit — the tip should revert to sha1.
	mustRun(t, "undo", "--repo", root)

	// cairn log should contain "first" and NOT "second" as a commit message
	// (the undo reverts the tip to sha1; the second commit is no longer reachable
	// via the tip walk).
	logOut := captureRun(t, "log", "--repo", root, "main")
	if !strings.Contains(logOut, sha1[:8]) {
		t.Errorf("log after undo must contain sha1 prefix %q; got:\n%s", sha1[:8], logOut)
	}
	if strings.Contains(logOut, sha2[:8]) {
		t.Errorf("log after undo must NOT contain sha2 prefix %q; got:\n%s", sha2[:8], logOut)
	}

	// Also verify cairn status shows ahead=1 (one commit on tip after undo).
	statusOut := captureRun(t, "status", "--repo", root, "main")
	if !strings.Contains(statusOut, "ahead:") {
		t.Errorf("status output should contain 'ahead:'; got:\n%s", statusOut)
	}

	// cairn oplog should list multiple operations (at least: express + commit1 + commit2 + undo).
	oplogOut := captureRun(t, "oplog", "--repo", root)
	lines := strings.Split(strings.TrimSpace(oplogOut), "\n")
	if len(lines) < 2 {
		t.Errorf("oplog should have multiple lines, got %d:\n%s", len(lines), oplogOut)
	}
	// The last entry should be the undo op.
	lastLine := lines[len(lines)-1]
	if !strings.Contains(lastLine, "undo") {
		t.Errorf("last oplog line should contain 'undo'; got: %q", lastLine)
	}
}
