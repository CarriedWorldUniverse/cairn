package worktree

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestSyncWorkingCapturesEditsNoCommit asserts SyncWorking snapshots folder
// edits into the expressed branch's OPEN working change with no explicit commit:
// the working change's head_commit tree must match the folder contents.
func TestSyncWorkingCapturesEditsNoCommit(t *testing.T) {
	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	if err := os.WriteFile(filepath.Join(root, "main", "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.SyncWorking(); err != nil {
		t.Fatalf("SyncWorking: %v", err)
	}

	entry := r.st.Expressed["main"]
	ch, err := r.eng.GetChange(entry.ChangeID)
	if err != nil {
		t.Fatalf("GetChange: %v", err)
	}
	if ch.HeadCommit == "" {
		t.Fatal("working change has no head after SyncWorking")
	}
	files, err := r.eng.Files(ch.HeadCommit)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if got, ok := files["a.txt"]; !ok || !bytes.Equal(got, []byte("hello\n")) {
		t.Fatalf("working tree a.txt = %q (ok=%v), want %q", got, ok, "hello\n")
	}
	if len(files) != 1 {
		t.Fatalf("working tree has %d files, want 1: %v", len(files), keys(files))
	}
}

// TestSyncWorkingNoEditsNoOp asserts a second sync with no edits leaves the
// working head unchanged (amend-in-place coalesces to the same commit).
func TestSyncWorkingNoEditsNoOp(t *testing.T) {
	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	if err := os.WriteFile(filepath.Join(root, "main", "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.SyncWorking(); err != nil {
		t.Fatalf("SyncWorking #1: %v", err)
	}
	entry := r.st.Expressed["main"]
	ch1, err := r.eng.GetChange(entry.ChangeID)
	if err != nil {
		t.Fatalf("GetChange #1: %v", err)
	}
	if err := r.SyncWorking(); err != nil {
		t.Fatalf("SyncWorking #2: %v", err)
	}
	ch2, err := r.eng.GetChange(entry.ChangeID)
	if err != nil {
		t.Fatalf("GetChange #2: %v", err)
	}
	if ch1.HeadCommit != ch2.HeadCommit {
		t.Fatalf("no-edit re-sync changed head: %s -> %s", ch1.HeadCommit, ch2.HeadCommit)
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
