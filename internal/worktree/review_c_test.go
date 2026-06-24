package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIgnoreDoesNotDropTrackedFile asserts git semantics: a .gitignore pattern
// only affects UNTRACKED paths. A path already committed (tracked in the tip)
// must survive a later .gitignore that matches it — it is neither dropped from
// the working diff nor silently removed from history on the next commit. A
// brand-new untracked file matching the same ignore is still excluded.
func TestIgnoreDoesNotDropTrackedFile(t *testing.T) {
	root := t.TempDir()
	r, err := Open(root, "t")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	if err := r.Express("exp", "main"); err != nil {
		t.Fatalf("Express: %v", err)
	}
	dir := filepath.Join(root, "exp")

	// Commit data.txt — it is now tracked.
	if err := os.WriteFile(filepath.Join(dir, "data.txt"), []byte("payload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("exp", ""); err != nil {
		t.Fatalf("commit data.txt: %v", err)
	}

	// Add a .gitignore that matches the already-tracked file, then commit.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("data.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("exp", ""); err != nil {
		t.Fatalf("commit .gitignore: %v", err)
	}

	// After the second commit the tracked file must be retained, not deleted.
	line, err := r.eng.LineByName("exp")
	if err != nil {
		t.Fatalf("LineByName: %v", err)
	}
	committed, err := r.eng.Files(line.TipCommit)
	if err != nil {
		t.Fatalf("Files(tip): %v", err)
	}
	if _, ok := committed["data.txt"]; !ok {
		t.Fatal("tracked data.txt silently removed from history by .gitignore")
	}

	// WorkingDiff must not report data.txt as a deletion.
	diffs, err := r.WorkingDiff("exp")
	if err != nil {
		t.Fatalf("WorkingDiff: %v", err)
	}
	for _, d := range diffs {
		if d.Path == "data.txt" {
			t.Fatalf("WorkingDiff reported a change for tracked-but-ignored data.txt: %+v", d)
		}
	}

	// isDirty must be false: nothing changed on disk vs the tip.
	dirty, err := r.isDirty("exp")
	if err != nil {
		t.Fatalf("isDirty: %v", err)
	}
	if dirty {
		t.Fatal("isDirty true after committing tracked-then-ignored file (should be clean)")
	}

	// Contrast: a brand-new UNTRACKED file matching the ignore is still excluded.
	if err := os.WriteFile(filepath.Join(dir, "fresh.log"), []byte("noise\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("data.txt\n*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tracked := trackedSet(committed)
	scanned, _, err := Scan(dir, tracked)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if _, ok := scanned["fresh.log"]; ok {
		t.Fatal("brand-new untracked *.log was not excluded by ignore")
	}
	if _, ok := scanned["data.txt"]; !ok {
		t.Fatal("tracked data.txt excluded by ignore (Scan must keep tracked paths)")
	}
}

// TestChmodOnlyIsDirty asserts a chmod-only change (no content change) shows as
// dirty, so status accuracy and pre-destructive dirty checks are mode-aware.
func TestChmodOnlyIsDirty(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	r, err := Open(root, "t")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	if err := r.Express("exp", "main"); err != nil {
		t.Fatalf("Express: %v", err)
	}
	dir := filepath.Join(root, "exp")
	script := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("exp", ""); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Clean immediately after commit + re-materialize.
	if dirty, err := r.isDirty("exp"); err != nil {
		t.Fatalf("isDirty (pre-chmod): %v", err)
	} else if dirty {
		t.Fatal("dirty right after commit (should be clean)")
	}

	// chmod +x with no content change.
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	dirty, err := r.isDirty("exp")
	if err != nil {
		t.Fatalf("isDirty (post-chmod): %v", err)
	}
	if !dirty {
		t.Fatal("chmod-only change not reported dirty")
	}
}

// TestMaterializeUniqueTempNoCollide asserts no *.cairn-tmp-* sibling dir is
// left behind in the parent after a successful Materialize.
func TestMaterializeUniqueTempNoCollide(t *testing.T) {
	root := t.TempDir()
	r, err := Open(root, "t")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	if err := r.Express("exp", "main"); err != nil {
		t.Fatalf("Express: %v", err)
	}
	dir := filepath.Join(root, "exp")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Commit re-materializes the head into dir (a Materialize round-trip).
	if _, err := r.Commit("exp", ""); err != nil {
		t.Fatalf("commit: %v", err)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir parent: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".cairn-tmp-") {
			t.Fatalf("leftover temp dir after Materialize: %q", e.Name())
		}
	}
}
