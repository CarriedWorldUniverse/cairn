package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2E_BlameSealed verifies that `cairn blame` attributes each line to the
// correct author for a file committed in a single sealed change, and that the
// output contains the date and line text.
func TestE2E_BlameSealed(t *testing.T) {
	root := t.TempDir()
	mustRun(t, "init", root)

	// Write f.txt with two lines and commit it.
	content := "hello world\nbye world\n"
	if err := os.WriteFile(filepath.Join(root, "main", "f.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "--author", "tester", "-m", "add f", "main")

	// cairn blame should show per-line attribution.
	out := captureRun(t, "blame", "--repo", root, "f.txt", "main")

	if !strings.Contains(out, "tester") {
		t.Errorf("blame output missing author 'tester':\n%s", out)
	}
	// Date should appear in YYYY-MM-DD form
	if !strings.Contains(out, "20") { // at minimum a year prefix
		t.Errorf("blame output missing date:\n%s", out)
	}
	if !strings.Contains(out, "hello world") {
		t.Errorf("blame output missing 'hello world':\n%s", out)
	}
	if !strings.Contains(out, "bye world") {
		t.Errorf("blame output missing 'bye world':\n%s", out)
	}
}

// TestE2E_BlameWorking verifies that an edited-but-uncommitted line is labelled
// "(working)" when blame is run after a SyncWorking (via openRepoSynced).
func TestE2E_BlameWorking(t *testing.T) {
	root := t.TempDir()
	mustRun(t, "init", root)

	// Commit f.txt with two lines.
	if err := os.WriteFile(filepath.Join(root, "main", "f.txt"), []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "--author", "tester", "-m", "initial", "main")

	// Edit line2 on disk WITHOUT committing — this becomes the working commit
	// after SyncWorking runs inside openRepoSynced (which blame uses).
	if err := os.WriteFile(filepath.Join(root, "main", "f.txt"), []byte("line1\nline2-edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Run blame — openRepoSynced will snapshot the edit into the working commit.
	out := captureRun(t, "blame", "--repo", root, "f.txt", "main")

	// The edited line2 must show "(working)".
	if !strings.Contains(out, "(working)") {
		t.Errorf("blame output missing '(working)' for un-committed edit:\n%s", out)
	}
	// The original line1 should still be attributed normally (not working).
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected 2 blame lines, got %d:\n%s", len(lines), out)
	}
	// line1 row should NOT contain "(working)"
	if strings.Contains(lines[0], "(working)") {
		t.Errorf("line1 incorrectly labelled (working):\n%s", lines[0])
	}
	// line2-edited row should contain "(working)"
	if !strings.Contains(lines[1], "(working)") {
		t.Errorf("line2-edited row missing (working):\n%s", lines[1])
	}
}

// TestE2E_BlameDefaultBranch verifies that omitting the branch arg defaults to
// the repo's root branch.
func TestE2E_BlameDefaultBranch(t *testing.T) {
	root := t.TempDir()
	mustRun(t, "init", root)

	if err := os.WriteFile(filepath.Join(root, "main", "g.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "--author", "testuser", "-m", "add g", "main")

	// No branch arg — should default to main.
	out := captureRun(t, "blame", "--repo", root, "g.txt")
	if !strings.Contains(out, "testuser") {
		t.Errorf("blame (default branch) missing author:\n%s", out)
	}
	if !strings.Contains(out, "content") {
		t.Errorf("blame (default branch) missing line text:\n%s", out)
	}
}
