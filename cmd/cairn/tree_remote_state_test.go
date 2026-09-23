package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// `cairn tree` listed every local line alike — a branch deleted after its PR
// merged, a line never pushed, and a line in sync all looked the same. Each
// line now carries its state against the remote, and --gone lists the ones
// whose branch is gone so they can be abandoned.
func TestTreeShowsRemoteStatePerLine(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	bare := filepath.Join(t.TempDir(), "origin.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v %s", err, out)
	}
	mustRun(t, "init", root)
	if err := os.WriteFile(filepath.Join(root, "main", "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "main")
	mustRun(t, "remote", "add", "origin", bare, "--repo", root)
	mustRun(t, "push", "--repo", root, "origin", "main")

	// pushed: feat is pushed and in sync (the push itself records the
	// tracking ref — no fetch in between).
	mustRun(t, "express", "--repo", root, "feat")
	if err := os.WriteFile(filepath.Join(root, "feat", "f.txt"), []byte("f\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "feat")
	mustRun(t, "push", "--repo", root, "origin", "feat")
	// unpushed: local is expressed and committed but never pushed.
	mustRun(t, "express", "--repo", root, "local")
	if err := os.WriteFile(filepath.Join(root, "local", "l.txt"), []byte("l\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "local")

	out := mustRunOut(t, "tree", "--repo", root)
	for _, want := range []string{"main  ahead=0  [pushed]", "feat  ahead=1  [pushed]", "local  ahead=1  [unpushed]"} {
		if !strings.Contains(out, want) {
			t.Fatalf("tree lacks %q:\n%s", want, out)
		}
	}

	// ahead: one more sealed commit on feat that origin lacks.
	if err := os.WriteFile(filepath.Join(root, "feat", "g.txt"), []byte("g\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "feat")
	if out := mustRunOut(t, "tree", "--repo", root); !strings.Contains(out, "feat  ahead=2  [ahead 1]") {
		t.Fatalf("expected feat [ahead 1]:\n%s", out)
	}

	// gone: the branch is deleted on the remote (its PR merged, say). Without
	// a fetch the stale tracking ref still says pushed — that is why --fetch
	// prunes — and after it the line is marked gone.
	if out, err := exec.Command("git", "--git-dir", bare, "update-ref", "-d", "refs/heads/feat").CombinedOutput(); err != nil {
		t.Fatalf("delete remote branch: %v %s", err, out)
	}
	out = mustRunOut(t, "tree", "--fetch", "--repo", root)
	if !strings.Contains(out, "feat  ahead=2  [gone]") {
		t.Fatalf("after --fetch, feat should be gone:\n%s", out)
	}
	if !strings.Contains(out, "main  ahead=0  [pushed]") {
		t.Fatalf("main should still be pushed:\n%s", out)
	}
	gone := mustRunOut(t, "tree", "--gone", "--repo", root)
	if !strings.Contains(gone, "feat") || strings.Contains(gone, "local") || strings.Contains(gone, "main  ") {
		t.Fatalf("--gone should list only feat:\n%s", gone)
	}
	// --flat carries the state as a trailing field, after what scripts parse.
	flat := mustRunOut(t, "tree", "--flat", "--repo", root)
	if !strings.Contains(flat, "ahead=2 remote=gone") || !strings.Contains(flat, "feat (parent ") {
		t.Fatalf("--flat should keep the id form and append remote=:\n%s", flat)
	}
	// status shows the same state.
	st := mustRunOut(t, "status", "--repo", root, "local")
	if !strings.Contains(st, "remote:    origin: unpushed") {
		t.Fatalf("status should report the remote state:\n%s", st)
	}
}
