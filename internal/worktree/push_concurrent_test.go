package worktree

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// TestPushBranchSerializesOnWCLock is the deterministic guard for issue #84: it
// proves PushBranch participates in the cross-process working-copy lock (#81's
// .cairn/wc.lock). The disk-level push race is timing-dependent and does not
// reproduce reliably in-process (go's -race sees only memory, not the shared
// packfiles/refs two go-git handles write), so we assert the fix by construction
// instead: while one handle holds the lock, a PushBranch on a second handle must
// BLOCK until the lock is released. Pre-fix (PushBranch unlocked) it would not
// block and this test fails.
func TestPushBranchSerializesOnWCLock(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("uses POSIX flock semantics; covered on unix")
	}
	bare, def := seedBareRemoteWT(t)
	root := t.TempDir()
	r0, err := Clone(bare, root, "t", nil)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	defer r0.Close()
	if err := r0.Express("b1", def); err != nil {
		t.Fatalf("express: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "b1", "b1.txt"), []byte("b1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r0.Commit("b1", "add b1"); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Handle A holds the working-copy lock.
	rA, err := Open(root, "t")
	if err != nil {
		t.Fatalf("open rA: %v", err)
	}
	defer rA.Close()
	unlock, err := rA.lockState()
	if err != nil {
		t.Fatalf("lockState: %v", err)
	}

	// Handle B tries to push b1 — it must block on the lock A holds.
	rB, err := Open(root, "t")
	if err != nil {
		t.Fatalf("open rB: %v", err)
	}
	defer rB.Close()
	done := make(chan error, 1)
	go func() { done <- rB.PushBranch("origin", "b1", false) }()

	select {
	case err := <-done:
		unlock()
		t.Fatalf("PushBranch completed (err=%v) while the wc.lock was held — it is NOT serialized (#84 regression)", err)
	case <-time.After(300 * time.Millisecond):
		// Correct: blocked on the lock.
	}

	unlock() // release; B's push must now proceed and succeed
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("PushBranch after lock release errored: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("PushBranch did not complete after the lock was released")
	}
}

// TestConcurrentPushBranchBothLand is the regression test for issue #84 (sibling
// of #81): two cairn processes sharing one working copy each express a branch,
// commit, then `push origin <branch>` at the same time. Pre-fix, PushBranch was
// not covered by the cross-process wc.lock (#81 only locked Express/Commit/
// Unexpress/Pull), so the two pushes raced on shared local push/re-materialize
// state and a branch could silently fail to land even though push reported
// success. Two Repo handles on one root model the two processes; both branches
// must reach the remote.
func TestConcurrentPushBranchBothLand(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("uses POSIX flock semantics; covered on unix")
	}
	bare, def := seedBareRemoteWT(t)

	root := t.TempDir()
	r0, err := Clone(bare, root, "t", nil)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	defer r0.Close()

	// Express + commit two independent lines off the default, sequentially.
	for _, b := range []string{"b1", "b2"} {
		if err := r0.Express(b, def); err != nil {
			t.Fatalf("express %s: %v", b, err)
		}
		if err := os.WriteFile(filepath.Join(root, b, b+".txt"), []byte(b+"\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", b, err)
		}
		if _, err := r0.Commit(b, "add "+b); err != nil {
			t.Fatalf("commit %s: %v", b, err)
		}
	}

	// Two more handles (two processes) push their line concurrently.
	r1, err := Open(root, "t")
	if err != nil {
		t.Fatalf("open r1: %v", err)
	}
	defer r1.Close()
	r2, err := Open(root, "t")
	if err != nil {
		t.Fatalf("open r2: %v", err)
	}
	defer r2.Close()

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = r1.PushBranch("origin", "b1", false) }()
	go func() { defer wg.Done(); errs[1] = r2.PushBranch("origin", "b2", false) }()
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Fatalf("concurrent push %d errored: %v", i, e)
		}
	}

	// Both branches must exist on the bare remote (pre-fix, one could be dropped).
	bareRepo, err := git.PlainOpen(bare)
	if err != nil {
		t.Fatalf("PlainOpen bare: %v", err)
	}
	for _, b := range []string{"b1", "b2"} {
		if _, err := bareRepo.Reference(plumbing.ReferenceName("refs/heads/"+b), false); err != nil {
			t.Fatalf("branch %q did not land on remote after concurrent push — #84 regression: %v", b, err)
		}
	}
}

// seedBareRemoteWT builds a bare git repo with a default branch + one commit and
// returns (bare path, default branch name) for cairn Clone to import.
func seedBareRemoteWT(t *testing.T) (string, string) {
	t.Helper()
	bare := t.TempDir()
	if _, err := git.PlainInit(bare, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	work := t.TempDir()
	repo, err := git.PlainInit(work, false)
	if err != nil {
		t.Fatalf("PlainInit work: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, "readme.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("readme.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("init", &git.CommitOptions{Author: &object.Signature{Name: "o", Email: "o@x"}}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	def := head.Name().Short()
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{bare}}); err != nil {
		t.Fatalf("CreateRemote: %v", err)
	}
	if err := repo.Push(&git.PushOptions{RemoteName: "origin"}); err != nil {
		t.Fatalf("push seed: %v", err)
	}
	return bare, def
}
