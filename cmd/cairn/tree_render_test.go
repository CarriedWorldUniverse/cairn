package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `cairn tree` used to print each line with its parent's ID — unreadable,
// and the reason a mis-parented branch was easy to miss. It now draws the
// tree by name; --flat keeps the id form for scripts.
func TestTreeRendersNamesAsAnIndentedTree(t *testing.T) {
	root := t.TempDir()
	mustRun(t, "init", root)
	if err := os.WriteFile(filepath.Join(root, "main", "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "main")
	mustRun(t, "express", "--repo", root, "develop")
	if err := os.WriteFile(filepath.Join(root, "develop", "d.txt"), []byte("d\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "develop")
	mustRun(t, "express", "--repo", root, "feature", "--from", "develop")
	mustRun(t, "express", "--repo", root, "hotfix")

	got := mustRunOut(t, "tree", "--repo", root)
	want := strings.Join([]string{
		"main  ahead=1",
		"├─ develop  ahead=1",
		"│  └─ feature  ahead=0",
		"└─ hotfix  ahead=0",
		"",
	}, "\n")
	if got != want {
		t.Fatalf("tree rendering differs\n--- got ---\n%s--- want ---\n%s", got, want)
	}

	flat := mustRunOut(t, "tree", "--flat", "--repo", root)
	if !strings.Contains(flat, "feature (parent ") || strings.Contains(flat, "├─") {
		t.Fatalf("--flat should keep the id form for scripts, got:\n%s", flat)
	}
}
