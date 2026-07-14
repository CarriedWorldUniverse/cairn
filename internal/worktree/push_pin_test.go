package worktree

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// TestPushNetworkPhaseDoesNotBlockOtherWCLockOps is the overlap acceptance
// test for issue #98 Phase B: while handle A's Push is in its (artificially
// slowed) network phase, a wc.lock-only operation on handle B (Commit on a
// sibling branch) must complete WITHOUT waiting for A's push to finish. Pre-
// fix, Push held wc.lock across the whole network round-trip, so B would
// block until A's push returned.
func TestPushNetworkPhaseDoesNotBlockOtherWCLockOps(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("uses POSIX flock semantics; covered on unix")
	}
	bare, def := seedBareRemoteWT(t)
	root := t.TempDir()
	r0, err := Clone(bare, root, "t", nil)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if err := r0.Express("b1", def); err != nil {
		t.Fatalf("express b1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "b1", "b1.txt"), []byte("b1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r0.Commit("b1", "add b1"); err != nil {
		t.Fatalf("commit b1: %v", err)
	}
	if err := r0.Express("b2", def); err != nil {
		t.Fatalf("express b2: %v", err)
	}
	if err := r0.Close(); err != nil {
		t.Fatalf("close r0: %v", err)
	}

	// Slow the network phase so there's a wide window to observe overlap.
	inNetwork := make(chan struct{})
	releaseNetwork := make(chan struct{})
	restore := change.SetNetworkDelayHook(func() {
		close(inNetwork)
		<-releaseNetwork
	})
	defer restore()

	rA, err := Open(root, "t")
	if err != nil {
		t.Fatalf("open rA: %v", err)
	}
	defer rA.Close()

	pushDone := make(chan error, 1)
	go func() { pushDone <- rA.PushBranch("origin", "b1", false) }()

	select {
	case <-inNetwork:
	case <-time.After(5 * time.Second):
		t.Fatal("push never entered its network phase")
	}

	// B commits on a DIFFERENT branch while A is mid-network-phase. This
	// needs wc.lock; if Push were still holding it (pre-fix), this would
	// block until the push finished.
	rB, err := Open(root, "t")
	if err != nil {
		t.Fatalf("open rB: %v", err)
	}
	defer rB.Close()
	if err := os.WriteFile(filepath.Join(root, "b2", "b2.txt"), []byte("b2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitDone := make(chan error, 1)
	go func() { _, err := rB.Commit("b2", "add b2"); commitDone <- err }()

	select {
	case err := <-commitDone:
		if err != nil {
			t.Fatalf("Commit on b2 errored: %v", err)
		}
		// Success: it completed WHILE A's push is still mid-network-phase.
	case <-time.After(3 * time.Second):
		t.Fatal("Commit on b2 blocked while A's push was in its network phase — #98 Phase B regression (network phase still holds wc.lock)")
	}

	// A's push must still be in flight at this point — proves genuine overlap,
	// not a lucky race where it happened to finish first.
	select {
	case err := <-pushDone:
		t.Fatalf("A's push completed (err=%v) before B's commit — no overlap was actually exercised", err)
	default:
	}

	close(releaseNetwork)
	select {
	case err := <-pushDone:
		if err != nil {
			t.Fatalf("PushBranch errored: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("push did not complete after the network hook released")
	}
}

// TestPushPinsFrozenSnapshotNotLaterCommit is the pin-snapshot acceptance
// test: a commit lands on the SAME branch during A's (slowed) network phase.
// The remote tip after the push must be the SHA frozen at prepare time, not
// the newer local tip B just created — proving NetworkPush publishes from
// the pinned refs, not from whatever refs/heads/* happens to hold when the
// network call actually runs.
func TestPushPinsFrozenSnapshotNotLaterCommit(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("uses POSIX flock semantics; covered on unix")
	}
	bare, def := seedBareRemoteWT(t)
	root := t.TempDir()
	r0, err := Clone(bare, root, "t", nil)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if err := r0.Express("b1", def); err != nil {
		t.Fatalf("express: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "b1", "b1.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	firstRes, err := r0.Commit("b1", "v1")
	if err != nil {
		t.Fatalf("commit v1: %v", err)
	}
	if err := r0.Close(); err != nil {
		t.Fatalf("close r0: %v", err)
	}

	inNetwork := make(chan struct{})
	releaseNetwork := make(chan struct{})
	restore := change.SetNetworkDelayHook(func() {
		close(inNetwork)
		<-releaseNetwork
	})
	defer restore()

	rA, err := Open(root, "t")
	if err != nil {
		t.Fatalf("open rA: %v", err)
	}
	defer rA.Close()

	pushDone := make(chan error, 1)
	go func() { pushDone <- rA.PushBranch("origin", "b1", false) }()

	select {
	case <-inNetwork:
	case <-time.After(5 * time.Second):
		t.Fatal("push never entered its network phase")
	}

	// A SECOND handle advances b1 further while A's network phase for the
	// FIRST commit is still in flight.
	rB, err := Open(root, "t")
	if err != nil {
		t.Fatalf("open rB: %v", err)
	}
	defer rB.Close()
	if err := os.WriteFile(filepath.Join(root, "b1", "b1.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := rB.Commit("b1", "v2"); err != nil {
		t.Fatalf("commit v2 during A's network phase: %v", err)
	}
	rB.Close()

	close(releaseNetwork)
	select {
	case err := <-pushDone:
		if err != nil {
			t.Fatalf("PushBranch errored: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("push did not complete after the network hook released")
	}

	bareRepo, err := git.PlainOpen(bare)
	if err != nil {
		t.Fatalf("PlainOpen bare: %v", err)
	}
	ref, err := bareRepo.Reference(plumbing.ReferenceName("refs/heads/b1"), false)
	if err != nil {
		t.Fatalf("remote b1 ref: %v", err)
	}
	if ref.Hash().String() != firstRes.HeadCommit {
		t.Fatalf("remote b1 tip = %s, want the SHA pinned at prepare time %s (the later v2 commit must NOT have leaked into this push)",
			ref.Hash().String(), firstRes.HeadCommit)
	}
}

// TestPushToDifferentRemotesOverlaps proves the per-remote remote.lock
// (issue #98 Phase B re-review) serializes network ops PER REMOTE, not
// globally: two PushBranch calls to two DIFFERENT remotes, each artificially
// slowed, must run their network phases concurrently rather than one
// blocking on the other's remote.lock. The sibling test,
// TestConcurrentPushBranchBothLand, is the same-remote counterpart proving
// the opposite — that two pushes TO ONE remote DO serialize (the fix for the
// real ref-write race two concurrent NetworkPush calls to one remote hit).
func TestPushToDifferentRemotesOverlaps(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("uses POSIX flock semantics; covered on unix")
	}
	bare1, def := seedBareRemoteWT(t)
	bare2, _ := seedBareRemoteWT(t)
	root := t.TempDir()
	r0, err := Clone(bare1, root, "t", nil)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if err := r0.AddRemote("origin2", bare2, "git"); err != nil {
		t.Fatalf("AddRemote origin2: %v", err)
	}
	if err := r0.Express("b1", def); err != nil {
		t.Fatalf("express b1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "b1", "b1.txt"), []byte("b1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r0.Commit("b1", "add b1"); err != nil {
		t.Fatalf("commit b1: %v", err)
	}
	if err := r0.Express("b2", def); err != nil {
		t.Fatalf("express b2: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "b2", "b2.txt"), []byte("b2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r0.Commit("b2", "add b2"); err != nil {
		t.Fatalf("commit b2: %v", err)
	}
	if err := r0.Close(); err != nil {
		t.Fatalf("close r0: %v", err)
	}

	// Both entering their network phase before either is released proves
	// they ran concurrently, not serialized behind one shared lock.
	var entered atomic.Int32
	bothEntered := make(chan struct{})
	release := make(chan struct{})
	restore := change.SetNetworkDelayHook(func() {
		if entered.Add(1) == 2 {
			close(bothEntered)
		}
		<-release
	})
	defer restore()

	rA, err := Open(root, "t")
	if err != nil {
		t.Fatalf("open rA: %v", err)
	}
	defer rA.Close()
	rB, err := Open(root, "t")
	if err != nil {
		t.Fatalf("open rB: %v", err)
	}
	defer rB.Close()

	doneA := make(chan error, 1)
	doneB := make(chan error, 1)
	go func() { doneA <- rA.PushBranch("origin", "b1", false) }()
	go func() { doneB <- rB.PushBranch("origin2", "b2", false) }()

	select {
	case <-bothEntered:
		// Correct: both network phases overlapped.
	case err := <-doneA:
		t.Fatalf("A's push to origin completed (err=%v) before B's push to origin2 even entered its network phase — not overlapping", err)
	case err := <-doneB:
		t.Fatalf("B's push to origin2 completed (err=%v) before A's push to origin even entered its network phase — not overlapping", err)
	case <-time.After(5 * time.Second):
		t.Fatal("both pushes never simultaneously entered their network phase — different-remote pushes are serializing when they should not be")
	}

	close(release)
	for i, done := range []chan error{doneA, doneB} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("push %d errored: %v", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("push %d did not complete after the network hook released", i)
		}
	}
}
