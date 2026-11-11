package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// An edit that preserves mtime and size (cp -p, touch -r, rsync -a) must still
// be seen by status, diff and commit: the stat fast path also checks ctime and
// inode, which no user-space tool can restore.
func TestE2E_EditPreservingMtimeAndSizeIsSeen(t *testing.T) {
	root := t.TempDir()
	mustRun(t, "init", root)
	a := filepath.Join(root, "main", "a.txt")
	if err := os.WriteFile(a, []byte("AAAA\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Age the file so it is not racy: the stat cache must be TRUSTED for the
	// stale-edit scenario to exist at all.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(a, old, old); err != nil {
		t.Fatal(err)
	}
	captureRun(t, "commit", "--repo", root, "--author", "tester", "-m", "first", "main")
	captureRun(t, "status", "--repo", root, "main") // warms the stat cache
	captureRun(t, "status", "--repo", root, "main")

	info, err := os.Stat(a)
	if err != nil {
		t.Fatal(err)
	}
	mt := info.ModTime()
	if err := os.WriteFile(a, []byte("BBBB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(a, mt, mt); err != nil {
		t.Fatal(err)
	}

	if out := captureRun(t, "status", "--repo", root, "main"); !strings.Contains(out, "a.txt") {
		t.Fatalf("status missed the mtime-preserving edit:\n%s", out)
	}
	if out := captureRun(t, "diff", "--repo", root, "main"); !strings.Contains(out, "BBBB") {
		t.Fatalf("diff missed the mtime-preserving edit:\n%s", out)
	}
	sha := strings.TrimSpace(captureRun(t, "commit", "--repo", root, "--author", "tester", "-m", "second", "main"))
	if out := captureRun(t, "show", "--repo", root, sha); !strings.Contains(out, "BBBB") {
		t.Fatalf("commit lost the mtime-preserving edit:\n%s", out)
	}
}
