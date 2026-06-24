package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

// sealCommit writes path=content in the branch folder and seals it, returning the
// sealed head sha.
func sealCommit(t *testing.T, r *Repo, branch, path, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.root, branch, path), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := r.Commit(branch, "seal "+path)
	if err != nil {
		t.Fatalf("Commit %s: %v", path, err)
	}
	if res.HeadCommit == "" {
		t.Fatalf("Commit %s: empty head", path)
	}
	return res.HeadCommit
}

// TestSyncWorkingSuspendedDuringBisect asserts that while a bisect session is
// active SyncWorking is a no-op: the open working change's head is NOT advanced to
// capture the materialized historical midpoint, even after the folder is edited.
func TestSyncWorkingSuspendedDuringBisect(t *testing.T) {
	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	if err := r.Express("feat", ""); err != nil {
		t.Fatalf("Express: %v", err)
	}
	var good, bad string
	for i := 0; i < 6; i++ {
		sha := sealCommit(t, r, "feat", "step.txt", string(rune('a'+i))+"\n")
		if i == 0 {
			good = sha
		}
		bad = sha
	}

	// Capture the open working change's head BEFORE bisecting.
	entry := r.st.Expressed["feat"]
	chBefore, err := r.eng.GetChange(entry.ChangeID)
	if err != nil {
		t.Fatalf("GetChange: %v", err)
	}
	headBefore := chBefore.HeadCommit

	step, err := r.BisectStart("feat", good, bad)
	if err != nil {
		t.Fatalf("BisectStart: %v", err)
	}
	if step.Done {
		t.Fatalf("bisect converged immediately, want a midpoint to test")
	}
	if active, _ := r.BisectActive(); !active {
		t.Fatal("BisectActive false after start")
	}

	// Edit the folder (as a command might), then SyncWorking — it must be a no-op.
	if err := os.WriteFile(filepath.Join(root, "feat", "intruder.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.SyncWorking(); err != nil {
		t.Fatalf("SyncWorking during bisect: %v", err)
	}
	chAfter, err := r.eng.GetChange(entry.ChangeID)
	if err != nil {
		t.Fatalf("GetChange after sync: %v", err)
	}
	if chAfter.HeadCommit != headBefore {
		t.Fatalf("working head advanced during bisect: before=%s after=%s", headBefore, chAfter.HeadCommit)
	}

	// After reset, SyncWorking captures edits again.
	if err := r.BisectReset(); err != nil {
		t.Fatalf("BisectReset: %v", err)
	}
	if active, _ := r.BisectActive(); active {
		t.Fatal("BisectActive true after reset")
	}
	if err := r.SyncWorking(); err != nil {
		t.Fatalf("SyncWorking after reset: %v", err)
	}
	chReset, err := r.eng.GetChange(entry.ChangeID)
	if err != nil {
		t.Fatalf("GetChange after reset: %v", err)
	}
	if chReset.HeadCommit == headBefore {
		t.Fatal("SyncWorking still a no-op after reset; working head unchanged")
	}
}
