package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCommitStatusSurfaceSkippedUnreadable covers #130's CLI-visible half: an
// unreadable UNTRACKED file must not just get a stderr warnf line (lost under
// redirection or a GUI wrapper) — `cairn commit`/`cairn status` print a
// structural STDOUT block naming it, and the commit still succeeds (exit 0).
func TestCommitStatusSurfaceSkippedUnreadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based unreadability is not meaningful on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("chmod ineffective as root")
	}

	root := t.TempDir()
	mustRun(t, "init", root)
	if err := os.WriteFile(filepath.Join(root, "main", "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "-m", "base", "main")

	locked := filepath.Join(root, "main", "locked.txt")
	if err := os.WriteFile(locked, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o644) })

	out := captureRun(t, "commit", "--repo", root, "-m", "second", "main")
	if !strings.Contains(out, "skipped 1 unreadable untracked path(s)") {
		t.Fatalf("commit stdout missing skipped-unreadable block, got: %q", out)
	}
	if !strings.Contains(out, "locked.txt") {
		t.Fatalf("commit stdout missing the skipped path name, got: %q", out)
	}

	out = captureRun(t, "status", "--repo", root, "main")
	if !strings.Contains(out, "skipped 1 unreadable untracked path(s)") {
		t.Fatalf("status stdout missing skipped-unreadable block, got: %q", out)
	}
	if !strings.Contains(out, "locked.txt") {
		t.Fatalf("status stdout missing the skipped path name, got: %q", out)
	}
}
