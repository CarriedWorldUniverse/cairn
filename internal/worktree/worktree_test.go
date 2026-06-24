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

	if err := r.Fold("exp", false); err != nil {
		t.Fatalf("Fold: %v", err)
	}

	got, _, err := Scan(filepath.Join(root, "main"), nil)
	if err != nil {
		t.Fatalf("Scan main: %v", err)
	}
	if string(got["m.txt"]) != "M\n" || string(got["e.txt"]) != "E\n" || string(got["base.txt"]) != "base\n" {
		t.Fatalf("main did not converge: %v", got)
	}
}

func TestRepoConflictThenResolve(t *testing.T) {
	root := t.TempDir()
	r, err := Open(root, "t")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if err := os.WriteFile(filepath.Join(root, "main", "f.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("main", ""); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := r.Express("exp", "main"); err != nil {
		t.Fatalf("express: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "main", "f.txt"), []byte("X\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("main", ""); err != nil {
		t.Fatalf("main adv: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "exp", "f.txt"), []byte("Y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := r.Commit("exp", "")
	if err != nil {
		t.Fatalf("exp commit: %v", err)
	}
	if len(res.Conflicts) == 0 {
		t.Fatal("expected conflict")
	}
	if err := os.WriteFile(filepath.Join(root, "exp", "f.txt"), []byte("resolved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.Resolve("exp", "f.txt"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := r.Fold("exp", false); err != nil {
		t.Fatalf("Fold after resolve: %v", err)
	}
	got, _, _ := Scan(filepath.Join(root, "main"), nil)
	if string(got["f.txt"]) != "resolved\n" {
		t.Fatalf("main f.txt = %q, want resolved", got["f.txt"])
	}
}

func TestRepoReopenLoadsState(t *testing.T) {
	root := t.TempDir()
	r, err := Open(root, "t")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := r.Express("exp", "main"); err != nil {
		t.Fatalf("Express: %v", err)
	}
	_ = r.Close()
	r2, err := Open(root, "t")
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	t.Cleanup(func() { _ = r2.Close() })
	if _, ok := r2.Ls()["exp"]; !ok {
		t.Fatalf("re-Open did not load expressed 'exp': %v", r2.Ls())
	}
}

func TestRepoAbandonRemovesFolderParentUntouched(t *testing.T) {
	root := t.TempDir()
	r, err := Open(root, "t")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if err := os.WriteFile(filepath.Join(root, "main", "m.txt"), []byte("M\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("main", ""); err != nil {
		t.Fatalf("commit main: %v", err)
	}
	if err := r.Express("exp", "main"); err != nil {
		t.Fatalf("express: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "exp", "wild.txt"), []byte("W\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.Abandon("exp", true); err != nil {
		t.Fatalf("Abandon: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "exp")); !os.IsNotExist(err) {
		t.Fatal("exp folder must be removed after abandon")
	}
	if _, ok := r.Ls()["main"]; !ok {
		t.Fatal("main must still be expressed")
	}
	got, _, _ := Scan(filepath.Join(root, "main"), nil)
	if string(got["m.txt"]) != "M\n" || got["wild.txt"] != nil {
		t.Fatalf("main perturbed by abandon: %v", got)
	}
}

func TestRepoCannotAbandonOrUnexpressRoot(t *testing.T) {
	root := t.TempDir()
	r, err := Open(root, "t")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if err := r.Abandon("main", false); err == nil {
		t.Fatal("Abandon(main) must error")
	}
	if err := r.Unexpress("main", false); err == nil {
		t.Fatal("Unexpress(main) must error")
	}
	// main still expressed + intact
	if _, ok := r.Ls()["main"]; !ok {
		t.Fatal("main must still be expressed")
	}
}
