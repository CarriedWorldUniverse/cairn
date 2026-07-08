package worktree

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSyncWorkingSerializesOnWCLock is the deterministic guard for issue #86:
// SyncWorking scans EVERY expressed branch, reading each line's tip from the
// shared cairn.db and its commit object from the shared git store. Pre-fix it ran
// unlocked, so a concurrent builder committing a sibling branch mid-scan advanced
// that tip and wrote a new commit object this process's go-git cache had not
// seen, aborting the snapshot with "commitTree: object not found". The disk/cache
// race is timing-dependent, so — as with #84 — we assert the fix by construction:
// while one handle holds the wc.lock, a second handle's SyncWorking must BLOCK
// until release. Pre-fix (SyncWorking unlocked) it would not block; this fails.
func TestSyncWorkingSerializesOnWCLock(t *testing.T) {
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
	// One expressed+committed branch so SyncWorking has a tip/object to read.
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

	// Handle B's SyncWorking must block on the lock A holds.
	rB, err := Open(root, "t")
	if err != nil {
		t.Fatalf("open rB: %v", err)
	}
	defer rB.Close()
	done := make(chan error, 1)
	go func() { done <- rB.SyncWorking() }()

	select {
	case err := <-done:
		unlock()
		t.Fatalf("SyncWorking completed (err=%v) while the wc.lock was held — it is NOT serialized (#86 regression)", err)
	case <-time.After(300 * time.Millisecond):
		// Correct: blocked on the lock.
	}

	unlock() // release; B's SyncWorking must now proceed and succeed
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SyncWorking after lock release errored: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("SyncWorking did not complete after the lock was released")
	}
}
