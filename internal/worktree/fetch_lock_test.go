package worktree

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

// TestFetchDoesNotBlockOnWCLock is the fetch-overlap acceptance test for
// issue #98 Phase B: Fetch/FetchPruned moved off wc.lock onto their own
// per-remote remote.lock, since they write no wc.json/catalogue state. While
// handle A holds wc.lock, a Fetch on handle B must NOT block — the direct
// opposite of the (now-removed) Phase-A-era "Fetch blocks on wc.lock"
// coverage in lock_coverage_test.go.
func TestFetchDoesNotBlockOnWCLock(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("uses POSIX flock semantics; covered on unix")
	}
	bare, def := seedBareRemoteWT(t)
	root := t.TempDir()
	r0, err := Clone(bare, root, "t", nil)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	_ = def
	if err := r0.Close(); err != nil {
		t.Fatalf("close r0: %v", err)
	}

	rA, err := Open(root, "t")
	if err != nil {
		t.Fatalf("open rA: %v", err)
	}
	defer rA.Close()
	unlock, err := rA.lockState()
	if err != nil {
		t.Fatalf("lockState: %v", err)
	}
	defer unlock()

	rB, err := Open(root, "t")
	if err != nil {
		t.Fatalf("open rB: %v", err)
	}
	defer rB.Close()

	done := make(chan error, 1)
	go func() { done <- rB.Fetch("origin") }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Fetch errored: %v", err)
		}
		// Correct: it did NOT wait for A's wc.lock.
	case <-time.After(3 * time.Second):
		t.Fatal("Fetch blocked on wc.lock — #98 Phase B regression (Fetch should use remote.lock, not wc.lock)")
	}
}

// TestStatusDoesNotBlockOnSlowFetch is the complementary half: a slow Fetch
// (network phase artificially delayed) on handle A must not block a
// wc.lock-only Status call on handle B, proving Fetch's network phase holds
// only remote.lock.
func TestStatusDoesNotBlockOnSlowFetch(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("uses POSIX flock semantics; covered on unix")
	}
	bare, def := seedBareRemoteWT(t)
	root := t.TempDir()
	r0, err := Clone(bare, root, "t", nil)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, def, "x.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r0.Commit(def, "x"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := r0.Close(); err != nil {
		t.Fatalf("close r0: %v", err)
	}

	inNetwork := make(chan struct{})
	releaseNetwork := make(chan struct{})
	restore := change.SetFetchDelayHook(func() {
		close(inNetwork)
		<-releaseNetwork
	})
	defer restore()

	rA, err := Open(root, "t")
	if err != nil {
		t.Fatalf("open rA: %v", err)
	}
	defer rA.Close()

	fetchDone := make(chan error, 1)
	go func() { fetchDone <- rA.Fetch("origin") }()

	select {
	case <-inNetwork:
	case <-time.After(5 * time.Second):
		t.Fatal("fetch never entered its network phase")
	}

	rB, err := Open(root, "t")
	if err != nil {
		t.Fatalf("open rB: %v", err)
	}
	defer rB.Close()

	statusDone := make(chan error, 1)
	go func() { _, err := rB.Status(def); statusDone <- err }()

	select {
	case err := <-statusDone:
		if err != nil {
			t.Fatalf("Status errored: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Status blocked while A's fetch was in its network phase — #98 Phase B regression")
	}

	select {
	case err := <-fetchDone:
		t.Fatalf("A's fetch completed (err=%v) before B's status — no overlap was actually exercised", err)
	default:
	}

	close(releaseNetwork)
	select {
	case err := <-fetchDone:
		if err != nil {
			t.Fatalf("Fetch errored: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fetch did not complete after the network hook released")
	}
}

// TestFetchSerializesOnFetchLock proves remote.lock actually serializes two fetch
// concurrent fetches against the SAME remote from two handles — the
// counterpart to wclock_test.go's express-concurrency coverage, for the new
// per-remote lock. Without the lock, two concurrent fetchTracking calls could
// tear each other's tracking-ref writes.
func TestFetchSerializesOnFetchLock(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("uses POSIX flock semantics; covered on unix")
	}
	bare, def := seedBareRemoteWT(t)
	root := t.TempDir()
	r0, err := Clone(bare, root, "t", nil)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	_ = def
	if err := r0.Close(); err != nil {
		t.Fatalf("close r0: %v", err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	// remote.lock fully serializes the two fetchNetwork calls (the lock is
	// acquired BEFORE fetchTracking/the hook run), so at most one goroutine
	// is ever inside this hook at a time; the atomic counter is just to keep
	// the race detector satisfied about the cross-goroutine handoff.
	var calls atomic.Int32
	restore := change.SetFetchDelayHook(func() {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
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
	go func() { doneA <- rA.Fetch("origin") }()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first fetch never entered its network phase")
	}

	go func() { doneB <- rB.Fetch("origin") }()

	select {
	case err := <-doneB:
		t.Fatalf("B's fetch completed (err=%v) while A held remote.lock — not serialized", err)
	case <-time.After(300 * time.Millisecond):
		// Correct: B is blocked behind A's remote.lock.
	}

	close(release)
	select {
	case err := <-doneA:
		if err != nil {
			t.Fatalf("A's fetch errored: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("A's fetch did not complete")
	}
	select {
	case err := <-doneB:
		if err != nil {
			t.Fatalf("B's fetch errored: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("B's fetch did not complete after A released remote.lock")
	}
}
