//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCleanNeverDeletesTrackedWorkingFiles: a file that is part of the
// un-sealed working change stays tracked work even when a later ignore rule (or
// a move into an ignored directory) matches it. clean must not delete it, and
// status --ignored must not list it twice.
func TestCleanNeverDeletesTrackedWorkingFiles(t *testing.T) {
	// Repro A: un-sealed file, then a .gitignore that matches it.
	w := newWC(t)
	w.write("main", "keep.txt", "keep\n")
	w.ok("main", "commit", "-m", "base")
	w.write("main", "notes.log", "precious\n")
	w.ok("main", "diff", "main") // snapshots it into the working change
	w.write("main", ".gitignore", "*.log\n")
	w.ok("main", "clean", "-fdx")
	if got := w.read("main", "notes.log"); got != "precious\n" {
		t.Fatalf("clean -fdx destroyed un-sealed tracked file: %q", got)
	}
	out := w.ok("main", "status", "--porcelain", "--ignored")
	if want := "A  .gitignore\nA  notes.log"; out != want {
		t.Errorf("status --ignored = %q, want %q", out, want)
	}
	if dry := w.ok("main", "clean", "-ndx"); dry != "" {
		t.Errorf("clean -ndx listed %q", dry)
	}

	// Repro B: mv into an ignored directory.
	w2 := newWC(t)
	w2.write("main", ".gitignore", "build/\n")
	w2.write("main", "a.c", "int a;\n")
	w2.ok("main", "commit", "-m", "base")
	w2.ok("main", "mv", "a.c", "build/a.c")
	w2.ok("main", "clean", "-fdx")
	if got := w2.read("main", "build/a.c"); got != "int a;\n" {
		t.Fatalf("clean -fdx deleted a moved tracked file: %q", got)
	}
	if got := w2.ok("main", "status", "--porcelain", "--ignored"); got == "" || strings.Contains(got, "!!") {
		t.Errorf("status --ignored = %q", got)
	}
}

// TestUndoOfConflictedOpLeavesNoOpenConflicts: undo of a revert / cherry-pick /
// stash pop that stopped on a conflict puts the open-conflict set back to its
// pre-op state (empty), and the stash a conflicted pop kept is unchanged.
func TestUndoOfConflictedOpLeavesNoOpenConflicts(t *testing.T) {
	w := newWC(t)
	w.write("main", "f.txt", "1\n2\n3\n")
	w.ok("main", "commit", "-m", "base")
	w.write("main", "f.txt", "1\nTWO\n3\n")
	target := w.ok("main", "commit", "-m", "two")
	w.write("main", "f.txt", "1\nTWO!\n3\n")
	w.ok("main", "commit", "-m", "two again")

	w.conflicts("main", "revert", target)
	mustRun(t, "undo", "--repo", w.root)
	if got := w.porcelain("main"); got != "" {
		t.Errorf("status after undo of a conflicted revert = %q, want clean", got)
	}
	if got := w.read("main", "f.txt"); got != "1\nTWO!\n3\n" {
		t.Errorf("f.txt after undo = %q", got)
	}
	w.write("main", "g.txt", "g\n")
	w.ok("main", "commit", "-m", "not blocked by a stale conflict")

	// stash pop that conflicts, then undo: stash stays, conflict goes.
	w.write("main", "f.txt", "stashed\n")
	w.ok("main", "stash", "-m", "wip")
	w.write("main", "f.txt", "sealed since\n")
	w.ok("main", "commit", "-m", "moved on")
	w.conflicts("main", "stash", "pop")
	mustRun(t, "undo", "--repo", w.root)
	if got := w.porcelain("main"); got != "" {
		t.Errorf("status after undo of a conflicted stash pop = %q, want clean", got)
	}
	mustContain(t, w.ok("main", "stash", "list"), "wip")
	w.ok("main", "commit", "--allow-empty", "-m", "commit works")
}

// TestUndoOfResolveReopensTheConflict: resolve is reversible too.
func TestUndoOfResolveReopensTheConflict(t *testing.T) {
	w := newWC(t)
	w.write("main", "f.txt", "1\n2\n3\n")
	w.ok("main", "commit", "-m", "base")
	w.write("main", "f.txt", "1\nTWO\n3\n")
	target := w.ok("main", "commit", "-m", "two")
	w.write("main", "f.txt", "1\nTWO!\n3\n")
	w.ok("main", "commit", "-m", "two again")
	w.conflicts("main", "revert", target)
	w.write("main", "f.txt", "1\n2\n3\n")
	mustRun(t, "resolve", "--repo", w.root, "main", "f.txt")
	if got := w.porcelain("main"); strings.Contains(got, "UU") {
		t.Fatalf("still conflicted after resolve: %q", got)
	}
	mustRun(t, "undo", "--repo", w.root)
	if got := w.porcelain("main"); !strings.Contains(got, "UU f.txt") {
		t.Errorf("status after undo of resolve = %q, want UU f.txt", got)
	}
}

// TestResetHardDiscardsConflictedPullMerge: a pull/push auto-pull that stopped
// on a conflict leaves a merge as the working commit; plain `reset --hard`
// throws it (and its conflicts) away, as git does, leaving a single-parent
// working commit that can be sealed again.
func TestResetHardDiscardsConflictedPullMerge(t *testing.T) {
	skipOnWindows(t)
	origin := makeSeededBareRepo(t)
	A := filepath.Join(t.TempDir(), "A")
	B := filepath.Join(t.TempDir(), "B")
	mustRun(t, "clone", origin, A)
	mustRun(t, "clone", origin, B)
	def := soleExpressedDir(t, A)
	if err := os.WriteFile(filepath.Join(A, def, "readme.txt"), []byte("A-edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", A, def)
	mustRun(t, "push", "--repo", A)
	if err := os.WriteFile(filepath.Join(B, def, "readme.txt"), []byte("B-edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", B, def)
	if err := run([]string{"push", "--repo", B}); err == nil {
		t.Fatal("push over a conflicting divergence should stop")
	}
	dir := filepath.Join(B, def)
	if out, _, _ := runOutErr(t, "status", "--porcelain", "--repo", dir); !strings.Contains(out, "UU readme.txt") {
		t.Fatalf("expected a conflict, status = %q", out)
	}
	mustRun(t, "reset", "--hard", "--repo", dir)
	if out, _, _ := runOutErr(t, "status", "--porcelain", "--repo", dir); out != "" {
		t.Errorf("status after reset --hard = %q, want clean", out)
	}
	got, err := os.ReadFile(filepath.Join(dir, "readme.txt"))
	if err != nil || string(got) != "B-edit\n" {
		t.Errorf("readme.txt after reset --hard = %q, %v; want B's sealed version", got, err)
	}
	// The reset is one undo step: the conflict comes back.
	mustRun(t, "undo", "--repo", B)
	if out, _, _ := runOutErr(t, "status", "--porcelain", "--repo", dir); !strings.Contains(out, "UU readme.txt") {
		t.Errorf("status after undoing the reset = %q, want the conflict back", out)
	}
}

// TestResetHardOverMergeThenCommit: after the discard the working commit has a
// single parent, so a new edit seals normally.
func TestResetHardOverMergeThenCommit(t *testing.T) {
	skipOnWindows(t)
	origin := makeSeededBareRepo(t)
	A := filepath.Join(t.TempDir(), "A")
	B := filepath.Join(t.TempDir(), "B")
	mustRun(t, "clone", origin, A)
	mustRun(t, "clone", origin, B)
	def := soleExpressedDir(t, A)
	for _, x := range []struct{ repo, text string }{{A, "A-edit\n"}, {B, "B-edit\n"}} {
		if err := os.WriteFile(filepath.Join(x.repo, def, "readme.txt"), []byte(x.text), 0o644); err != nil {
			t.Fatal(err)
		}
		mustRun(t, "commit", "--repo", x.repo, def)
		if x.repo == A {
			mustRun(t, "push", "--repo", A)
		}
	}
	if err := run([]string{"push", "--repo", B}); err == nil {
		t.Fatal("push over a conflicting divergence should stop")
	}
	dir := filepath.Join(B, def)
	mustRun(t, "reset", "--hard", "--repo", dir)
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("B-again\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", dir, "-m", "after reset")
}

// TestRestoreOnUnsealedLineRefuses: with no sealed commit there is nothing to
// restore from; `restore .` must not delete every un-sealed file.
func TestRestoreOnUnsealedLineRefuses(t *testing.T) {
	w := newWC(t)
	w.write("main", "a.txt", "a\n")
	w.write("main", "b/c.txt", "c\n")
	_ = w.porcelain("main")
	mustContain(t, w.fails("main", "restore", "."), "did not match any file(s) known")
	mustContain(t, w.fails("main", "restore", "a.txt"), "did not match any file(s) known")
	if w.read("main", "a.txt") != "a\n" || w.read("main", "b/c.txt") != "c\n" {
		t.Fatal("restore on an unsealed line deleted files")
	}
}

// TestCheckoutForcePathsDelegatesToRestore: checkout-only flags are dropped or
// translated before the path form is handed to restore.
func TestCheckoutForcePathsDelegatesToRestore(t *testing.T) {
	w := newWC(t)
	w.write("main", "a.txt", "a\n")
	w.ok("main", "commit", "-m", "base")
	for _, flag := range []string{"-f", "-q", "--force", "--quiet"} {
		w.write("main", "a.txt", "edited\n")
		_ = w.porcelain("main")
		if _, errOut, err := w.run("main", "checkout", flag, "--", "a.txt"); err != nil {
			t.Fatalf("checkout %s -- a.txt: %v\n%s", flag, err, errOut)
		}
		if got := w.read("main", "a.txt"); got != "a\n" {
			t.Errorf("checkout %s -- a.txt left %q", flag, got)
		}
	}
	w.write("main", "a.txt", "edited\n")
	_ = w.porcelain("main")
	if _, _, err := w.run("main", "checkout", "-f", "HEAD", "--", "a.txt"); err != nil {
		t.Fatalf("checkout -f HEAD -- a.txt: %v", err)
	}
	if got := w.read("main", "a.txt"); got != "a\n" {
		t.Errorf("a.txt = %q", got)
	}
	mustContain(t, w.fails("main", "checkout", "--ours", "--", "a.txt"), "cairn resolve")
	mustContain(t, w.fails("main", "checkout", "--detach", "--", "a.txt"), "cairn restore")
}

// TestResolveKeepsUnsnapshottedEditsToOtherConflicts: resolving one conflicted
// file must not re-materialize the other conflicted files over hand edits that
// no command has snapshotted yet (released 0.1.48 lost them).
func TestResolveKeepsUnsnapshottedEditsToOtherConflicts(t *testing.T) {
	w := newWC(t)
	w.write("main", "x.txt", "a\n")
	w.write("main", "y.txt", "b\n")
	w.ok("main", "commit", "-m", "base")
	mustRun(t, "express", "--repo", w.root, "feat")
	w.write("main", "x.txt", "A1\n")
	w.write("main", "y.txt", "B1\n")
	w.ok("main", "commit", "-m", "main moves")
	w.write("feat", "x.txt", "A2\n")
	w.write("feat", "y.txt", "B2\n")
	w.conflicts("feat", "commit", "-m", "feat")
	w.write("feat", "x.txt", "AX\n")
	w.write("feat", "y.txt", "BX\n")
	mustRun(t, "resolve", "--repo", w.root, "feat", "x.txt")
	if got := w.read("feat", "y.txt"); got != "BX\n" {
		t.Fatalf("y.txt after resolving x.txt = %q, want the hand edit BX", got)
	}
	mustRun(t, "resolve", "--repo", w.root, "feat", "y.txt")
	w.ok("feat", "commit", "-m", "resolved")
	if got := w.read("feat", "x.txt") + w.read("feat", "y.txt"); got != "AX\nBX\n" {
		t.Fatalf("after commit = %q", got)
	}
}

// TestUndoOfUndoRedoesStashAndTagState: the undo op is recorded by the refs
// stream's recordRefOp; it must also own the stash rows Undo restored, so a
// second undo redoes a stash pop (and, independently, a tag create).
func TestUndoOfUndoRedoesStashAndTagState(t *testing.T) {
	w := newWC(t)
	w.write("main", "f.txt", "base\n")
	w.ok("main", "commit", "-m", "base")
	w.write("main", "f.txt", "wip\n")
	w.ok("main", "stash", "-m", "wip")
	w.ok("main", "stash", "pop")
	mustRun(t, "undo", "--repo", w.root) // undo the pop: stash is back
	mustContain(t, w.ok("main", "stash", "list"), "wip")
	mustRun(t, "undo", "--repo", w.root) // undo the undo: popped again
	if got := w.ok("main", "stash", "list"); strings.Contains(got, "wip") {
		t.Errorf("stash list after undo of undo = %q, want the pop redone", got)
	}
	if got := w.read("main", "f.txt"); got != "wip\n" {
		t.Errorf("f.txt after undo of undo = %q, want wip", got)
	}

	w.ok("main", "tag", "v1")
	mustRun(t, "undo", "--repo", w.root)
	mustRun(t, "undo", "--repo", w.root)
	mustContain(t, w.ok("main", "tag"), "v1")
}
