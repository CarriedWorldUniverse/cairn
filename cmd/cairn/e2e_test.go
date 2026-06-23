package main

import (
	"os"
	"path/filepath"
	"testing"
)

func mustRun(t *testing.T, args ...string) {
	t.Helper()
	if err := run(args); err != nil {
		t.Fatalf("run %v: %v", args, err)
	}
}

func TestE2E_TwoBranchConvergeViaCLI(t *testing.T) {
	root := t.TempDir()
	mustRun(t, "init", root)
	if err := os.WriteFile(filepath.Join(root, "main", "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "main")
	mustRun(t, "express", "--repo", root, "exp")
	if err := os.WriteFile(filepath.Join(root, "main", "m.txt"), []byte("M\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "exp", "e.txt"), []byte("E\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "main")
	mustRun(t, "commit", "--repo", root, "exp")
	mustRun(t, "fold", "--repo", root, "exp")
	for _, f := range []string{"base.txt", "m.txt", "e.txt"} {
		if _, err := os.Stat(filepath.Join(root, "main", f)); err != nil {
			t.Fatalf("main missing %s after converge: %v", f, err)
		}
	}
}

func TestE2E_ConflictResolveViaCLI(t *testing.T) {
	root := t.TempDir()
	mustRun(t, "init", root)
	if err := os.WriteFile(filepath.Join(root, "main", "f.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "main")
	mustRun(t, "express", "--repo", root, "exp") // forks at base
	if err := os.WriteFile(filepath.Join(root, "main", "f.txt"), []byte("X\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "main") // main advances
	if err := os.WriteFile(filepath.Join(root, "exp", "f.txt"), []byte("Y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// commit exp -> conflict; commit is non-fatal (exit 0), so run must return nil
	mustRun(t, "commit", "--repo", root, "exp")
	// exp/f.txt now has diff3 markers on disk; write the resolution and resolve
	if err := os.WriteFile(filepath.Join(root, "exp", "f.txt"), []byte("resolved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "resolve", "--repo", root, "exp", "f.txt") // resolve <branch> <path>
	mustRun(t, "fold", "--repo", root, "exp")
	got, err := os.ReadFile(filepath.Join(root, "main", "f.txt"))
	if err != nil {
		t.Fatalf("read main/f.txt: %v", err)
	}
	if string(got) != "resolved\n" {
		t.Fatalf("main/f.txt = %q, want resolved", got)
	}
}
