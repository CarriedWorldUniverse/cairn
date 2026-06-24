package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExecBitSurvivesMergeForward verifies that committing an executable file on
// a CHILD branch (whose parent line already has a tip, so the commit triggers a
// merge-forward through mergeTrees) preserves the +x bit through the merge +
// re-materialize. Before threading modes through mergeTrees, the merge wrote the
// tree with nil modes and silently stripped the exec bit.
func TestExecBitSurvivesMergeForward(t *testing.T) {
	skipOnWindows(t)

	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	base, err := r.DefaultBranch()
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	// Give the parent (root) line a tip so the child commit must merge forward.
	if err := os.WriteFile(filepath.Join(root, base, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(base, "seed"); err != nil {
		t.Fatalf("Commit base: %v", err)
	}

	if err := r.Express("child", base); err != nil {
		t.Fatalf("Express child: %v", err)
	}
	folder := filepath.Join(root, "child")
	script := filepath.Join(folder, "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho go\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "plain.txt"), []byte("plain\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := r.Commit("child", "add exec on child")
	if err != nil {
		t.Fatalf("Commit child: %v", err)
	}
	if len(res.Conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %v", res.Conflicts)
	}

	// Commit re-materializes the folder from the merged head. The exec bit must
	// have survived the merge-forward.
	info, err := os.Stat(script)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("run.sh lost its executable bit through merge-forward: mode=%v", info.Mode())
	}
}

// TestSymlinkSurvivesMergeForward verifies that committing a symlink on a CHILD
// branch preserves it as a symlink (with its target) through merge-forward +
// re-materialize. Before threading modes, mergeTrees wrote the symlink's target
// string as a REGULAR file's content, corrupting the symlink into a plain file.
func TestSymlinkSurvivesMergeForward(t *testing.T) {
	skipOnWindows(t)

	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	base, err := r.DefaultBranch()
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, base, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(base, "seed"); err != nil {
		t.Fatalf("Commit base: %v", err)
	}

	if err := r.Express("child", base); err != nil {
		t.Fatalf("Express child: %v", err)
	}
	folder := filepath.Join(root, "child")
	if err := os.WriteFile(filepath.Join(folder, "target.txt"), []byte("payload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.txt", filepath.Join(folder, "link")); err != nil {
		t.Fatal(err)
	}
	res, err := r.Commit("child", "add symlink on child")
	if err != nil {
		t.Fatalf("Commit child: %v", err)
	}
	if len(res.Conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %v", res.Conflicts)
	}

	link := filepath.Join(folder, "link")
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link is not a symlink after merge-forward: mode=%v", info.Mode())
	}
	tgt, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if tgt != "target.txt" {
		t.Errorf("link target = %q, want target.txt", tgt)
	}
}

// TestResolvePreservesOtherModes verifies that resolving a TEXT conflict does not
// strip the exec bit from an unrelated executable file in the same tree. Before
// threading modes through ResolveConflict, the resolve rebuilt the head tree with
// nil modes, stripping every non-regular mode in the tree.
func TestResolvePreservesOtherModes(t *testing.T) {
	skipOnWindows(t)

	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	base, err := r.DefaultBranch()
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	// Seed the base with the conflict file + an executable tool.
	if err := os.WriteFile(filepath.Join(root, base, "f.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, base, "tool.sh"), []byte("#!/bin/sh\necho tool\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(base, "seed"); err != nil {
		t.Fatalf("Commit base: %v", err)
	}

	if err := r.Express("child", base); err != nil {
		t.Fatalf("Express child: %v", err)
	}

	// Diverge: parent edits f.txt one way, child edits it another → conflict on commit.
	if err := os.WriteFile(filepath.Join(root, base, "f.txt"), []byte("X\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(base, "parent edit"); err != nil {
		t.Fatalf("Commit parent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "child", "f.txt"), []byte("Y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := r.Commit("child", "child edit")
	if err != nil {
		t.Fatalf("Commit child: %v", err)
	}
	if len(res.Conflicts) == 0 {
		t.Fatal("expected a conflict on f.txt")
	}

	// Resolve the text conflict.
	if err := os.WriteFile(filepath.Join(root, "child", "f.txt"), []byte("resolved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.Resolve("child", "f.txt"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// The unrelated executable tool must still be executable after the resolve
	// re-materialized the folder.
	info, err := os.Stat(filepath.Join(root, "child", "tool.sh"))
	if err != nil {
		t.Fatalf("Stat tool.sh: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("tool.sh lost its executable bit through resolve: mode=%v", info.Mode())
	}
}
