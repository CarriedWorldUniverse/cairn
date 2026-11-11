package worktree

import (
	"testing"
	"time"
)

// gc must hold every per-remote lock: a fetch writes loose objects under that
// lock alone, and gc must not prune beneath it.
func TestGcWaitsForRemoteLocks(t *testing.T) {
	dir := t.TempDir()
	r, err := Open(dir, "t")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.AddRemote("origin", "/nonexistent/x.git", "git"); err != nil {
		t.Fatal(err)
	}
	r2, err := Open(dir, "t")
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	held := make(chan struct{})
	done := make(chan struct{})
	const hold = 600 * time.Millisecond
	go func() {
		_ = r2.withRemoteLock("origin", func() error {
			close(held)
			time.Sleep(hold)
			return nil
		})
		close(done)
	}()
	<-held
	start := time.Now()
	if _, err := r.Gc(true); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el < hold/2 {
		t.Fatalf("gc finished in %s while a remote lock was held for %s", el, hold)
	}
	<-done
}
