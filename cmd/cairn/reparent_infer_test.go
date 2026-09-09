package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/internal/worktree"
)

// `reparent --infer --dry-run` reports without changing; `--infer` applies.
func TestReparentInferDryRunThenApply(t *testing.T) {
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
	// feature genuinely forks from develop, but is recorded under main.
	mustRun(t, "express", "--repo", root, "feature", "--from", "develop")
	mustRun(t, "reparent", "--repo", root, "feature", "main")

	lineage := func() string {
		r, err := worktree.Open(root, "tester")
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		st, err := r.Status("feature")
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(st.Lineage, " → ")
	}
	if got := lineage(); got != "main → feature" {
		t.Fatalf("precondition: lineage %q", got)
	}
	mustRun(t, "reparent", "--repo", root, "--infer", "--dry-run")
	if got := lineage(); got != "main → feature" {
		t.Fatalf("dry-run changed the tree: %q", got)
	}
	mustRun(t, "reparent", "--repo", root, "--infer")
	if got := lineage(); got != "main → develop → feature" {
		t.Fatalf("after --infer: lineage %q, want main → develop → feature", got)
	}
}
