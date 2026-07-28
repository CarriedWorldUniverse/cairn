package worktree

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// expressedWithCommit expresses branch off the root with one sealed file and
// returns the repo root and the branch's folder path.
func expressedWithCommit(t *testing.T, branch string) (*Repo, string, string) {
	t.Helper()
	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	if err := r.Express(branch, ""); err != nil {
		t.Fatalf("Express %s: %v", branch, err)
	}
	dir := filepath.Join(root, FolderName(branch))
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("sealed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(branch, "base"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return r, root, dir
}

// TestSyncWorkingSkipsMissingFolder is the core of #133: a folder removed with
// `rm -rf` instead of `cairn unexpress` used to make SyncWorking — which runs
// ahead of nearly every command — fail hard, wedging even `ls`/`tree` and the
// `express`/`unexpress` needed to recover.
func TestSyncWorkingSkipsMissingFolder(t *testing.T) {
	skipOnWindows(t)

	r, _, dir := expressedWithCommit(t, "work")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	if err := r.SyncWorking(); err != nil {
		t.Fatalf("SyncWorking with a deleted expressed folder: %v (want it skipped)", err)
	}
	// Repo-wide reads must still work.
	if _, err := r.Tree(); err != nil {
		t.Errorf("Tree after folder deletion: %v", err)
	}
}

// TestMissingFolderNeverSnapshotsPhantomDeletion guards the dangerous failure
// mode the skip exists to prevent: scanning a vanished folder yields an empty
// tree, so a snapshot would record every tracked file as deleted. The sealed
// tip must be untouched after a sync over a missing folder.
func TestMissingFolderNeverSnapshotsPhantomDeletion(t *testing.T) {
	skipOnWindows(t)

	r, _, dir := expressedWithCommit(t, "work")
	tipBefore := lineTip(t, r, "work")

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := r.SyncWorking(); err != nil {
		t.Fatalf("SyncWorking: %v", err)
	}

	if got := lineTip(t, r, "work"); got != tipBefore {
		t.Errorf("tip moved after syncing a missing folder: %q → %q (a phantom deletion was recorded)", tipBefore, got)
	}
}

// TestOpsOnMissingFolderReportIt checks that commands TARGETING the branch get
// an actionable ErrFolderMissing rather than silently succeeding on a stale
// snapshot.
func TestOpsOnMissingFolderReportIt(t *testing.T) {
	skipOnWindows(t)

	r, _, dir := expressedWithCommit(t, "work")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Status("work"); !errors.Is(err, ErrFolderMissing) {
		t.Errorf("Status on a missing folder: err = %v, want ErrFolderMissing", err)
	}
	if _, err := r.Commit("work", "should not seal"); !errors.Is(err, ErrFolderMissing) {
		t.Errorf("Commit on a missing folder: err = %v, want ErrFolderMissing", err)
	}
}

// TestExpressRestoresMissingFolder covers the recovery path: re-expressing a
// branch whose folder was deleted rebuilds it (previously a silent no-op,
// because the wc.json entry still existed).
func TestExpressRestoresMissingFolder(t *testing.T) {
	skipOnWindows(t)

	r, _, dir := expressedWithCommit(t, "work")

	// An un-sealed edit, synced into the working change before the deletion.
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("unsealed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.SyncWorking(); err != nil {
		t.Fatalf("SyncWorking: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	if err := r.Express("work", ""); err != nil {
		t.Fatalf("Express to restore a deleted folder: %v", err)
	}
	for name, want := range map[string]string{"a.txt": "sealed\n", "b.txt": "unsealed\n"} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("after restore, %s: %v", name, err)
			continue
		}
		if string(got) != want {
			t.Errorf("after restore, %s = %q, want %q", name, got, want)
		}
	}
	if _, err := r.Status("work"); err != nil {
		t.Errorf("Status after restore: %v", err)
	}
}

// TestUnexpressDropsMissingFolder covers the other recovery path: dropping the
// record for a folder the user already deleted.
func TestUnexpressDropsMissingFolder(t *testing.T) {
	skipOnWindows(t)

	r, _, dir := expressedWithCommit(t, "work")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	if err := r.Unexpress("work", false); err != nil {
		t.Fatalf("Unexpress a deleted folder: %v", err)
	}
	if r.IsExpressed("work") {
		t.Error("branch is still expressed after Unexpress")
	}
}

// lineTip returns the named line's current tip commit.
func lineTip(t *testing.T, r *Repo, name string) string {
	t.Helper()
	line, err := r.eng.LineByName(name)
	if err != nil {
		t.Fatalf("LineByName(%q): %v", name, err)
	}
	return line.TipCommit
}
