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

// TestSyncWorkingSuspendedAfterConvergence is the data-corruption regression test:
// after a bisect CONVERGES (Done) but before reset, the session must stay alive so
// SyncWorking remains suspended — otherwise the next command would snapshot the
// historical first-bad commit (left in the folder) into the open working change.
func TestSyncWorkingSuspendedAfterConvergence(t *testing.T) {
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
	entry := r.st.Expressed["feat"]
	chBefore, _ := r.eng.GetChange(entry.ChangeID)
	headBefore := chBefore.HeadCommit

	// Drive to convergence by always marking "bad".
	step, err := r.BisectStart("feat", good, bad)
	if err != nil {
		t.Fatalf("BisectStart: %v", err)
	}
	for !step.Done {
		if step, err = r.BisectMark("bad"); err != nil {
			t.Fatalf("BisectMark: %v", err)
		}
	}
	if step.FirstBad == "" {
		t.Fatal("converged with empty FirstBad")
	}

	// CONVERGED, NOT reset: the session must still be active (suspend still in force).
	if active, _ := r.BisectActive(); !active {
		t.Fatal("BisectActive() false after convergence; the post-convergence window would corrupt the working change")
	}
	// A command edits/reads the folder + SyncWorking → must be a no-op.
	if err := os.WriteFile(filepath.Join(root, "feat", "step.txt"), []byte("HISTORICAL\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.SyncWorking(); err != nil {
		t.Fatalf("SyncWorking after convergence: %v", err)
	}
	chAfter, _ := r.eng.GetChange(entry.ChangeID)
	if chAfter.HeadCommit != headBefore {
		t.Fatalf("working head corrupted post-convergence: before=%s after=%s", headBefore, chAfter.HeadCommit)
	}

	// reset restores the folder to the working tip and clears the session.
	if err := r.BisectReset(); err != nil {
		t.Fatalf("BisectReset: %v", err)
	}
	if active, _ := r.BisectActive(); active {
		t.Fatal("BisectActive() true after reset")
	}
}

// TestBisectResetNoSession: reset with no session in progress errors cleanly.
func TestBisectResetNoSession(t *testing.T) {
	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if err := r.BisectReset(); err == nil {
		t.Fatal("BisectReset with no session: want error, got nil")
	}
}
