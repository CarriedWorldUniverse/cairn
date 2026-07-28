package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDeletionSurvivesTreeNoOpMerge is the core regression: a deletion was
// resurrected as a FALSE modify/delete conflict on a file neither side had
// actually changed relative to their true common ancestor.
//
// The merge base never advanced past the fork point, because a merge-forward
// whose result equalled the change's own tree did not record the adopted
// parent-line tip as a second parent. So when the child later deleted a path
// that both lines had independently brought to the same content, the merge
// compared against the STALE fork-point content, decided the parent had
// "modified" it, and kept the parent's copy.
func TestDeletionSurvivesTreeNoOpMerge(t *testing.T) {
	skipOnWindows(t)

	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	main, err := r.DefaultBranch()
	if err != nil {
		t.Fatal(err)
	}
	mainX := filepath.Join(root, main, "x.txt")
	if err := os.WriteFile(mainX, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(main, "base v1"); err != nil {
		t.Fatalf("Commit base: %v", err)
	}

	// Fork while x.txt is still v1 — this is the stale base.
	if err := r.Express("work", ""); err != nil {
		t.Fatalf("Express: %v", err)
	}
	workX := filepath.Join(root, "work", "x.txt")

	// The parent advances x.txt to v2.
	if err := os.WriteFile(mainX, []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(main, "main: x -> v2"); err != nil {
		t.Fatalf("Commit main: %v", err)
	}

	// The child independently arrives at the SAME content, so the merge-forward
	// is a tree no-op — the case that used to skip the second parent.
	if err := os.WriteFile(workX, []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("work", "work: x -> v2"); err != nil {
		t.Fatalf("Commit work: %v", err)
	}

	// Now delete it. Both lines hold identical content, so this is a clean
	// deletion, NOT a modify/delete conflict.
	if err := os.Remove(workX); err != nil {
		t.Fatal(err)
	}
	res, err := r.Commit("work", "work: drop x")
	if err != nil {
		t.Fatalf("Commit the deletion: %v", err)
	}
	if len(res.Conflicts) != 0 {
		t.Errorf("false conflict on a deletion neither side contested: %v", res.Conflicts)
	}
	if _, err := os.Stat(workX); !os.IsNotExist(err) {
		t.Error("x.txt was resurrected by the merge-forward")
	}
}

// TestChildDoesNotAdoptParentsUnsealedWork pins the second half of the fix:
// merge-forward adopts the parent's SEALED tip, not its raw TipCommit. An
// expressed parent keeps a "(working)" auto-snapshot at its tip that every sync
// re-amends, so adopting the raw tip pulled the parent's UN-SEALED scratch files
// onto the child — and merged the child against a commit that changed underfoot.
func TestChildDoesNotAdoptParentsUnsealedWork(t *testing.T) {
	skipOnWindows(t)

	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	main, err := r.DefaultBranch()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, main, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(main, "base"); err != nil {
		t.Fatalf("Commit base: %v", err)
	}
	if err := r.Express("work", ""); err != nil {
		t.Fatalf("Express: %v", err)
	}

	// Leave the parent dirty: un-sealed scratch work, synced into its working
	// change so the parent's TipCommit is a "(working)" commit.
	if err := os.WriteFile(filepath.Join(root, main, "scratch.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.SyncWorking(); err != nil {
		t.Fatalf("SyncWorking: %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "work", "w.txt"), []byte("w\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := r.Commit("work", "work c1")
	if err != nil {
		t.Fatalf("Commit work: %v", err)
	}
	if len(res.Conflicts) != 0 {
		t.Errorf("child conflicted on the parent's un-sealed work: %v", res.Conflicts)
	}
	if _, err := os.Stat(filepath.Join(root, "work", "scratch.txt")); err == nil {
		t.Error("the parent's UN-SEALED scratch.txt was adopted onto the child line")
	}
}

// TestForkedLineCommitCountUnchanged guards the COST of advancing the merge
// base rather than a bug on main — it passes before this change and must keep
// passing after. The adopted parent may be recorded only when it is not already
// an ancestor: recording it unconditionally added a duplicate merge commit to
// every forked line's first commit and inflated `ahead` (caught in review by
// cmd/cairn's TestE2E_StatusShowsChangesAndAhead).
func TestForkedLineCommitCountUnchanged(t *testing.T) {
	skipOnWindows(t)

	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	main, err := r.DefaultBranch()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, main, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(main, "base"); err != nil {
		t.Fatal(err)
	}
	if err := r.Express("exp", ""); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"v1", "v1b"} {
		if err := os.WriteFile(filepath.Join(root, "exp", "keep.txt"), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Commit("exp", "exp "+v); err != nil {
			t.Fatalf("Commit exp %s: %v", v, err)
		}
	}

	st, err := r.Status("exp")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Ahead != 2 {
		t.Errorf("ahead = %d after two commits, want 2 (a redundant merge commit was recorded)", st.Ahead)
	}
}
