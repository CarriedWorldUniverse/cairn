package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/internal/worktree"
)

// `git checkout -b` forks from the branch you are on; the reflex table maps it
// to `cairn express`, which forked from the ROOT no matter where you stood.
// Standing in develop/ and expressing a line must now fork from develop.
func TestExpressInsideAFolderForksFromThatLine(t *testing.T) {
	root := t.TempDir()
	mustRun(t, "init", root)
	if err := os.WriteFile(filepath.Join(root, "main", "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "main")
	mustRun(t, "express", "--repo", root, "develop")

	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(filepath.Join(root, "develop")); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "express", "feat")
	// And from the root, the default is still the root.
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "express", "other")

	r, err := worktree.Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()
	parentOf := func(name string) string {
		t.Helper()
		st, err := r.Status(name)
		if err != nil {
			t.Fatalf("Status %s: %v", name, err)
		}
		if len(st.Lineage) < 2 {
			return ""
		}
		return st.Lineage[len(st.Lineage)-2]
	}
	if p := parentOf("feat"); p != "develop" {
		t.Fatalf("feat expressed from inside develop/ has parent %q, want develop", p)
	}
	if p := parentOf("other"); p != "main" {
		t.Fatalf("other expressed at the repo root has parent %q, want main", p)
	}
}
