package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRepoTwoBranchConverge(t *testing.T) {
	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	if err := os.WriteFile(filepath.Join(root, "main", "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("main", ""); err != nil {
		t.Fatalf("commit main base: %v", err)
	}

	if err := r.Express("exp", "main"); err != nil {
		t.Fatalf("Express: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "main", "m.txt"), []byte("M\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "exp", "e.txt"), []byte("E\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Commit("main", ""); err != nil {
		t.Fatalf("commit main: %v", err)
	}
	res, err := r.Commit("exp", "")
	if err != nil {
		t.Fatalf("commit exp: %v", err)
	}
	if len(res.Conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %v", res.Conflicts)
	}

	if err := r.Fold("exp"); err != nil {
		t.Fatalf("Fold: %v", err)
	}

	got, err := Scan(filepath.Join(root, "main"))
	if err != nil {
		t.Fatalf("Scan main: %v", err)
	}
	if string(got["m.txt"]) != "M\n" || string(got["e.txt"]) != "E\n" || string(got["base.txt"]) != "base\n" {
		t.Fatalf("main did not converge: %v", got)
	}
}

func TestRepoConflictThenResolve(t *testing.T) {
	root := t.TempDir()
	r, _ := Open(root, "t")
	t.Cleanup(func() { _ = r.Close() })
	os.WriteFile(filepath.Join(root, "main", "f.txt"), []byte("base\n"), 0o644)
	if _, err := r.Commit("main", ""); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := r.Express("exp", "main"); err != nil {
		t.Fatalf("express: %v", err)
	}
	os.WriteFile(filepath.Join(root, "main", "f.txt"), []byte("X\n"), 0o644)
	if _, err := r.Commit("main", ""); err != nil {
		t.Fatalf("main adv: %v", err)
	}
	os.WriteFile(filepath.Join(root, "exp", "f.txt"), []byte("Y\n"), 0o644)
	res, err := r.Commit("exp", "")
	if err != nil {
		t.Fatalf("exp commit: %v", err)
	}
	if len(res.Conflicts) == 0 {
		t.Fatal("expected conflict")
	}
	os.WriteFile(filepath.Join(root, "exp", "f.txt"), []byte("resolved\n"), 0o644)
	if err := r.Resolve("exp", "f.txt"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := r.Fold("exp"); err != nil {
		t.Fatalf("Fold after resolve: %v", err)
	}
	got, _ := Scan(filepath.Join(root, "main"))
	if string(got["f.txt"]) != "resolved\n" {
		t.Fatalf("main f.txt = %q, want resolved", got["f.txt"])
	}
}
