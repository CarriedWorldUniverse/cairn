package worktree

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLockCoverageBlocksConcurrentAccess is the deterministic guard for the
// #98 Phase A audit: Resolve, Fold, Abandon, Fetch, FetchPruned, Status, and
// WorkingDiff all mutate shared working-copy state (the catalogue, tracking
// refs, or the wc-cache) but previously ran unlocked. As with #84/#86, the
// underlying disk/db race is timing-dependent and does not reproduce
// reliably in-process, so each subtest asserts the fix by construction:
// while one handle holds the wc.lock, the operation on a second handle must
// BLOCK until the lock is released. Pre-fix (method unlocked) it would not
// block and the subtest fails.
//
// Stash and Tag are included as representative samples of the broader
// "flagged remainder" swept in the same Phase A pass (Stash/StashPop/
// StashDrop, the release-adapter methods, Reword/Squash/Drop/CherryPick/
// Reauthor/Undo, the Bisect* verbs, Tag/Reparent/MarkPrivate/UnmarkPrivate/
// MarkEmbargo/DiscloseCommit/AddRemote/SetConfig/SetPendingBump) — the
// locking mechanism is identical for all of them (see lockState's doc), so
// this table exercises two rather than all twenty-odd.
func TestLockCoverageBlocksConcurrentAccess(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("uses POSIX flock semantics; covered on unix")
	}

	t.Run("Resolve", func(t *testing.T) {
		root := t.TempDir()
		r0, err := Open(root, "t")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, "main", "f.txt"), []byte("base\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := r0.Commit("main", ""); err != nil {
			t.Fatalf("seed commit: %v", err)
		}
		if err := r0.Express("exp", "main"); err != nil {
			t.Fatalf("express: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, "main", "f.txt"), []byte("X\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := r0.Commit("main", ""); err != nil {
			t.Fatalf("main adv: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, "exp", "f.txt"), []byte("Y\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		res, err := r0.Commit("exp", "")
		if err != nil {
			t.Fatalf("exp commit: %v", err)
		}
		if len(res.Conflicts) == 0 {
			t.Fatal("expected conflict")
		}
		if err := os.WriteFile(filepath.Join(root, "exp", "f.txt"), []byte("resolved\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := r0.Close(); err != nil {
			t.Fatalf("close r0: %v", err)
		}

		assertBlocksOnWCLock(t, root, "Resolve", func(r *Repo) error {
			return r.Resolve("exp", "f.txt", false)
		})
	})

	t.Run("Fold", func(t *testing.T) {
		root := t.TempDir()
		r0, err := Open(root, "t")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, "main", "base.txt"), []byte("base\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := r0.Commit("main", ""); err != nil {
			t.Fatalf("seed commit: %v", err)
		}
		if err := r0.Express("exp", "main"); err != nil {
			t.Fatalf("express: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, "exp", "e.txt"), []byte("E\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := r0.Commit("exp", ""); err != nil {
			t.Fatalf("exp commit: %v", err)
		}
		if err := r0.Close(); err != nil {
			t.Fatalf("close r0: %v", err)
		}

		assertBlocksOnWCLock(t, root, "Fold", func(r *Repo) error {
			return r.Fold("exp", false)
		})
	})

	// Fetch's coverage moved to fetch_lock_test.go: issue #98 Phase B
	// deliberately took Fetch/FetchPruned OFF wc.lock (they write no
	// wc.json/catalogue state) and onto their own per-remote remote.lock, so a
	// slow fetch no longer blocks (or is blocked by) unrelated wc.lock
	// commands. TestFetchDoesNotBlockOnWCLock and
	// TestFetchSerializesOnFetchLock there are that coverage's replacement.

	t.Run("Status", func(t *testing.T) {
		root := t.TempDir()
		r0, err := Open(root, "t")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, "main", "base.txt"), []byte("base\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := r0.Commit("main", ""); err != nil {
			t.Fatalf("seed commit: %v", err)
		}
		if err := r0.Close(); err != nil {
			t.Fatalf("close r0: %v", err)
		}

		assertBlocksOnWCLock(t, root, "Status", func(r *Repo) error {
			_, err := r.Status("main")
			return err
		})
	})

	t.Run("Stash", func(t *testing.T) {
		root := t.TempDir()
		r0, err := Open(root, "t")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, "main", "base.txt"), []byte("base\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := r0.Commit("main", ""); err != nil {
			t.Fatalf("seed commit: %v", err)
		}
		// Un-sealed edit — Stash needs a non-empty working delta or it errors
		// "nothing to stash".
		if err := os.WriteFile(filepath.Join(root, "main", "base.txt"), []byte("edited\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := r0.Close(); err != nil {
			t.Fatalf("close r0: %v", err)
		}

		assertBlocksOnWCLock(t, root, "Stash", func(r *Repo) error {
			return r.Stash("main", "wip")
		})
	})

	t.Run("Tag", func(t *testing.T) {
		root := t.TempDir()
		r0, err := Open(root, "t")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, "main", "base.txt"), []byte("base\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := r0.Commit("main", ""); err != nil {
			t.Fatalf("seed commit: %v", err)
		}
		if err := r0.Close(); err != nil {
			t.Fatalf("close r0: %v", err)
		}

		assertBlocksOnWCLock(t, root, "Tag", func(r *Repo) error {
			return r.Tag("v1", "main")
		})
	})
}

// assertBlocksOnWCLock opens two Repo handles on root, has handle A take the
// wc.lock, then runs op on handle B in a goroutine — asserting it BLOCKS
// while A holds the lock, and completes successfully once A releases it.
func assertBlocksOnWCLock(t *testing.T, root, label string, op func(r *Repo) error) {
	t.Helper()
	rA, err := Open(root, "t")
	if err != nil {
		t.Fatalf("open rA: %v", err)
	}
	defer rA.Close()
	unlock, err := rA.lockState()
	if err != nil {
		t.Fatalf("lockState: %v", err)
	}

	rB, err := Open(root, "t")
	if err != nil {
		t.Fatalf("open rB: %v", err)
	}
	defer rB.Close()

	done := make(chan error, 1)
	go func() { done <- op(rB) }()

	select {
	case err := <-done:
		unlock()
		t.Fatalf("%s completed (err=%v) while the wc.lock was held — it is NOT serialized (#98 regression)", label, err)
	case <-time.After(300 * time.Millisecond):
		// Correct: blocked on the lock.
	}

	unlock() // release; B's op must now proceed and succeed
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s after lock release errored: %v", label, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not complete after the lock was released", label)
	}
}
