package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

// modifyDeleteConflict builds the state behind #134: a child line deletes a file
// that its PARENT line has since modified. Commit records a modify/delete
// conflict and keeps the parent's copy, so the file is back on disk.
// It returns the repo, the child's folder, and the conflicted path.
func modifyDeleteConflict(t *testing.T) (*Repo, string, string) {
	t.Helper()

	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	main, err := r.DefaultBranch()
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	mainDir := filepath.Join(root, main)
	if err := os.MkdirAll(filepath.Join(mainDir, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mainDir, "pkg", "a.go"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mainDir, "keep.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(main, "base"); err != nil {
		t.Fatalf("Commit base: %v", err)
	}
	if err := r.Express("work", ""); err != nil {
		t.Fatalf("Express work: %v", err)
	}
	workDir := filepath.Join(root, "work")

	// The parent line MODIFIES the file the child is about to delete.
	if err := os.WriteFile(filepath.Join(mainDir, "pkg", "a.go"), []byte("a-on-main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(main, "main touches pkg"); err != nil {
		t.Fatalf("Commit on main: %v", err)
	}

	// The child deletes it.
	if err := os.RemoveAll(filepath.Join(workDir, "pkg")); err != nil {
		t.Fatal(err)
	}
	res, err := r.Commit("work", "drop pkg")
	if err != nil {
		t.Fatalf("Commit work: %v", err)
	}
	if len(res.Conflicts) == 0 {
		t.Fatal("expected a modify/delete conflict, got none")
	}
	return r, workDir, "pkg/a.go"
}

// TestResolveDeletionSettlesModifyDeleteConflict is the #134 regression: a
// modify/delete conflict used to be UNRESOLVABLE in favour of the deletion.
// Resolve read the resolution from the file on disk, so a deleted file errored
// "no such file or directory", while Commit refused to seal while the conflict
// stayed open — the removal could never land.
func TestResolveDeletionSettlesModifyDeleteConflict(t *testing.T) {
	skipOnWindows(t)

	r, workDir, path := modifyDeleteConflict(t)

	// Delete it again (the conflict put the parent's copy back) and resolve.
	if err := os.RemoveAll(filepath.Join(workDir, "pkg")); err != nil {
		t.Fatal(err)
	}
	if err := r.Resolve("work", path, false); err != nil {
		t.Fatalf("Resolve a deleted path: %v (the deletion must be a valid resolution)", err)
	}

	st, err := r.Status("work")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(st.Conflicts) != 0 {
		t.Errorf("conflicts still open after resolve: %v", st.Conflicts)
	}

	// The seal must now succeed and must NOT bring the file back.
	if _, err := r.Commit("work", "drop pkg for real"); err != nil {
		t.Fatalf("Commit after resolve: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "pkg", "a.go")); !os.IsNotExist(err) {
		t.Errorf("%s is back on disk after committing the resolved deletion", path)
	}
}

// TestResolvedDeletionSurvivesParentMoving pins the other half of the report —
// the oscillating history. Once resolved in favour of the deletion, the removal
// must stay put as the parent line keeps advancing.
func TestResolvedDeletionSurvivesParentMoving(t *testing.T) {
	skipOnWindows(t)

	r, workDir, path := modifyDeleteConflict(t)
	if err := os.RemoveAll(filepath.Join(workDir, "pkg")); err != nil {
		t.Fatal(err)
	}
	if err := r.Resolve("work", path, false); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, err := r.Commit("work", "drop pkg"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	main, err := r.DefaultBranch()
	if err != nil {
		t.Fatal(err)
	}
	mainDir := filepath.Join(r.Root(), main)
	for _, n := range []string{"m1", "m2", "m3"} {
		if err := os.WriteFile(filepath.Join(mainDir, n+".txt"), []byte(n+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Commit(main, "main moves "+n); err != nil {
			t.Fatalf("Commit main %s: %v", n, err)
		}
		if err := os.WriteFile(filepath.Join(workDir, n+"w.txt"), []byte(n+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Commit("work", "work "+n); err != nil {
			t.Fatalf("Commit work %s: %v", n, err)
		}
		if _, err := os.Stat(filepath.Join(workDir, "pkg", "a.go")); !os.IsNotExist(err) {
			t.Fatalf("%s resurrected after the parent line advanced (%s)", path, n)
		}
	}
}

// TestResolveStillTakesOnDiskContent guards the normal path: a file that IS
// present resolves to its bytes, unchanged by the deletion branch.
func TestResolveStillTakesOnDiskContent(t *testing.T) {
	skipOnWindows(t)

	r, workDir, path := modifyDeleteConflict(t)

	// Keep the file, with content of the operator's choosing.
	if err := os.MkdirAll(filepath.Join(workDir, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "pkg", "a.go"), []byte("chosen\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.Resolve("work", path, false); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, err := r.Commit("work", "keep pkg"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(workDir, "pkg", "a.go"))
	if err != nil {
		t.Fatalf("read after resolve: %v", err)
	}
	if string(got) != "chosen\n" {
		t.Errorf("resolved content = %q, want %q", got, "chosen\n")
	}
}
