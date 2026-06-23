package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUnexpressDirtyRefused verifies that Unexpress refuses with force=false when
// the branch folder has uncommitted changes, and succeeds with force=true.
func TestUnexpressDirtyRefused(t *testing.T) {
	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	// Seed main and express a child.
	if err := os.WriteFile(filepath.Join(root, "main", "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("main", "seed"); err != nil {
		t.Fatalf("commit main: %v", err)
	}
	if err := r.Express("child", "main"); err != nil {
		t.Fatalf("Express child: %v", err)
	}
	// Commit child clean.
	if err := os.WriteFile(filepath.Join(root, "child", "c.txt"), []byte("c\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("child", "child clean"); err != nil {
		t.Fatalf("commit child: %v", err)
	}

	// Write an uncommitted file into child.
	if err := os.WriteFile(filepath.Join(root, "child", "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// force=false must refuse.
	err = r.Unexpress("child", false)
	if err == nil {
		t.Fatal("Unexpress(child, false) must error on dirty branch")
	}
	if !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("error must mention 'uncommitted', got: %v", err)
	}
	// Folder must still exist.
	if _, statErr := os.Stat(filepath.Join(root, "child")); os.IsNotExist(statErr) {
		t.Fatal("child folder must still exist after refused Unexpress")
	}

	// force=true must succeed.
	if err := r.Unexpress("child", true); err != nil {
		t.Fatalf("Unexpress(child, true) must succeed: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "child")); !os.IsNotExist(statErr) {
		t.Fatal("child folder must be removed after force Unexpress")
	}
}

// TestAbandonDirtyRefused verifies that Abandon refuses with force=false when
// the branch folder has uncommitted changes, and succeeds with force=true.
func TestAbandonDirtyRefused(t *testing.T) {
	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	// Seed main and express a child.
	if err := os.WriteFile(filepath.Join(root, "main", "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("main", "seed"); err != nil {
		t.Fatalf("commit main: %v", err)
	}
	if err := r.Express("child", "main"); err != nil {
		t.Fatalf("Express child: %v", err)
	}
	// Commit child clean.
	if err := os.WriteFile(filepath.Join(root, "child", "c.txt"), []byte("c\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("child", "child clean"); err != nil {
		t.Fatalf("commit child: %v", err)
	}

	// Write an uncommitted file.
	if err := os.WriteFile(filepath.Join(root, "child", "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// force=false must refuse.
	err = r.Abandon("child", false)
	if err == nil {
		t.Fatal("Abandon(child, false) must error on dirty branch")
	}
	if !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("error must mention 'uncommitted', got: %v", err)
	}
	// Branch must still be expressed (line still exists).
	if _, ok := r.Ls()["child"]; !ok {
		t.Fatal("child must still be expressed after refused Abandon")
	}

	// force=true must succeed and remove the line.
	if err := r.Abandon("child", true); err != nil {
		t.Fatalf("Abandon(child, true) must succeed: %v", err)
	}
	if _, ok := r.Ls()["child"]; ok {
		t.Fatal("child must no longer be expressed after Abandon")
	}
	if _, statErr := os.Stat(filepath.Join(root, "child")); !os.IsNotExist(statErr) {
		t.Fatal("child folder must be removed after Abandon")
	}
}

// TestFoldDirtyRefused verifies that Fold refuses with force=false when dirty,
// and succeeds (no force needed) when the branch is clean.
func TestFoldDirtyRefused(t *testing.T) {
	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	// Seed main.
	if err := os.WriteFile(filepath.Join(root, "main", "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("main", "seed"); err != nil {
		t.Fatalf("commit main: %v", err)
	}

	// Express and commit child.
	if err := r.Express("child", "main"); err != nil {
		t.Fatalf("Express child: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "child", "c.txt"), []byte("c\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("child", "child clean"); err != nil {
		t.Fatalf("commit child: %v", err)
	}

	// Write an uncommitted file → dirty.
	if err := os.WriteFile(filepath.Join(root, "child", "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// force=false must refuse while dirty.
	err = r.Fold("child", false)
	if err == nil {
		t.Fatal("Fold(child, false) must error on dirty branch")
	}
	if !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("error must mention 'uncommitted', got: %v", err)
	}

	// Remove the dirty file to make it clean again, then fold succeeds.
	if err := os.Remove(filepath.Join(root, "child", "dirty.txt")); err != nil {
		t.Fatal(err)
	}
	if err := r.Fold("child", false); err != nil {
		t.Fatalf("Fold(child, false) on clean branch must succeed: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "child")); !os.IsNotExist(statErr) {
		t.Fatal("child folder must be removed after Fold")
	}
}

// TestIsDirtyErrNotFoundLineGone exercises the ErrNotFound early-return in
// isDirty: the line is removed directly via the engine while the worktree entry
// remains in st.Expressed — isDirty must return (false, nil) and not propagate
// the error.
func TestIsDirtyErrNotFoundLineGone(t *testing.T) {
	skipOnWindows(t)
	r, err := Open(t.TempDir(), "tester")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	root, err := r.DefaultBranch()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Express("feat", root); err != nil {
		t.Fatal(err)
	}
	// Remove the line directly via the engine, leaving the worktree entry behind.
	line, err := r.eng.LineByName("feat")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.eng.AbandonLine(line.ID); err != nil {
		t.Fatal(err)
	}
	dirty, err := r.isDirty("feat")
	if err != nil {
		t.Fatalf("isDirty errored on a gone line: %v", err)
	}
	if dirty {
		t.Fatal("isDirty should report a gone line as not-dirty")
	}
}

// TestIsDirtyMissingLineSafe verifies that isDirty returns (false, nil) after a
// successful Abandon (line no longer exists in the engine).
func TestIsDirtyMissingLineSafe(t *testing.T) {
	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	// Express and commit child.
	if err := r.Express("child", "main"); err != nil {
		t.Fatalf("Express child: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "child", "c.txt"), []byte("c\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("child", "clean"); err != nil {
		t.Fatalf("commit child: %v", err)
	}

	// Abandon with force=true (clean commit, but we use force to be direct).
	if err := r.Abandon("child", true); err != nil {
		t.Fatalf("Abandon(child, true): %v", err)
	}

	// Now child is no longer in st.Expressed; isDirty should return (false, nil).
	// We access isDirty directly since it's package-private and we're in the same package.
	dirty, derr := r.isDirty("child")
	if derr != nil {
		t.Fatalf("isDirty after Abandon returned error: %v", derr)
	}
	if dirty {
		t.Fatal("isDirty after Abandon must return false")
	}
}
