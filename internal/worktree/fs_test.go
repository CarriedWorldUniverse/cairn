package worktree

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

// TestMaterializeCreatesParentDirs reproduces the bug hit expressing a branch
// whose name contains "/" (e.g. "docs/readme-refresh"): the target dir is nested
// (<root>/docs/readme-refresh) but its parent (<root>/docs) does not exist —
// after a clone that expressed only the root, no "docs/" folder was ever made.
// Materialize must create the parent before building its temp dir.
func TestMaterializeCreatesParentDirs(t *testing.T) {
	eng, err := change.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	main, _ := eng.LineByName("main")
	ch, _ := eng.CreateChange(main.ID, "t")
	r, err := eng.Commit(ch.ID, map[string][]byte{"README.md": []byte("hi\n")}, nil, "")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	cacheDir := filepath.Join(t.TempDir(), "cache")
	root := t.TempDir()
	// Parent "docs" deliberately does NOT exist on disk.
	dir := filepath.Join(root, "docs", "readme-refresh")
	if err := Materialize(eng, cacheDir, r.HeadCommit, dir); err != nil {
		t.Fatalf("Materialize into nested dir: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "README.md")); err != nil || string(got) != "hi\n" {
		t.Fatalf("expressed file missing/wrong: got %q err %v", got, err)
	}
}

func TestMaterializeScanRoundTrip(t *testing.T) {
	eng, err := change.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	main, _ := eng.LineByName("main")
	ch, _ := eng.CreateChange(main.ID, "t")
	files := map[string][]byte{"a.txt": []byte("a\n"), "dir/b.txt": []byte("b\n")}
	r, err := eng.Commit(ch.ID, files, nil, "")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	cacheDir := filepath.Join(t.TempDir(), "cache")
	dir := filepath.Join(t.TempDir(), "wc")
	if err := Materialize(eng, cacheDir, r.HeadCommit, dir); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	got, _, err := Scan(dir, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if string(got["a.txt"]) != "a\n" || string(got["dir/b.txt"]) != "b\n" || len(got) != 2 {
		t.Fatalf("round-trip mismatch: %v", got)
	}
}

func TestMaterializeClearsStaleFiles(t *testing.T) {
	eng, _ := change.Open(t.TempDir())
	t.Cleanup(func() { _ = eng.Close() })
	main, _ := eng.LineByName("main")
	ch, _ := eng.CreateChange(main.ID, "t")
	r1, err := eng.Commit(ch.ID, map[string][]byte{"keep.txt": []byte("1\n"), "gone.txt": []byte("x\n")}, nil, "")
	if err != nil {
		t.Fatalf("commit r1: %v", err)
	}
	cacheDir := filepath.Join(t.TempDir(), "cache")
	dir := filepath.Join(t.TempDir(), "wc")
	if err := Materialize(eng, cacheDir, r1.HeadCommit, dir); err != nil {
		t.Fatalf("mat1: %v", err)
	}
	r2, err := eng.Commit(ch.ID, map[string][]byte{"keep.txt": []byte("2\n")}, nil, "")
	if err != nil {
		t.Fatalf("commit r2: %v", err)
	}
	if err := Materialize(eng, cacheDir, r2.HeadCommit, dir); err != nil {
		t.Fatalf("mat2: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "gone.txt")); !os.IsNotExist(err) {
		t.Fatal("stale gone.txt not removed after re-materialize")
	}
}
