package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

// A commit that deletes a directory AND adds it to .gitignore — the usual way
// to move tools out of a repo — must delete the old files on pull, as git
// does. The deletion walk skips ignored paths, so before the fix every old
// file stayed on disk, invisible to status (now ignored) and never removed.
func TestPullRemovesFilesTheNewTreeIgnores(t *testing.T) {
	f := newRebaseFixture(t)
	f.mainMoves("add tools", map[string]string{
		"tools/t1.sh":      "t1\n",
		"tools/sub/t2.py":  "t2\n",
		"tools/edited.sh":  "e\n",
		"tools/sub/x.json": "x\n",
	})
	f.pull()
	dir := f.dir("main")
	for _, p := range []string{"tools/t1.sh", "tools/sub/t2.py"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Fatalf("precondition: %s not materialized: %v", p, err)
		}
	}
	// Local state the deletion must respect: a tracked file the operator
	// edited, and an ignored file cairn never tracked.
	writeIn(t, dir, "tools/edited.sh", "my change\n")
	writeIn(t, dir, "tools/sub/cache.pyc", "junk\n")
	if _, err := f.r.Status("main"); err != nil {
		t.Fatal(err)
	}

	gitIn(t, f.origin, "checkout", "-q", "main")
	gitIn(t, f.origin, "rm", "-rq", "tools")
	writeIn(t, f.origin, ".gitignore", "tools/\n")
	gitIn(t, f.origin, "add", ".gitignore")
	gitIn(t, f.origin, "commit", "-qm", "remove tools, ignore the dir")
	gitIn(t, f.origin, "checkout", "-q", "--detach")
	if _, err := f.r.Pull("origin"); err != nil {
		t.Fatalf("Pull: %v", err)
	}

	for _, p := range []string{"tools/t1.sh", "tools/sub/t2.py", "tools/sub/x.json"} {
		if _, err := os.Lstat(filepath.Join(dir, p)); !os.IsNotExist(err) {
			t.Errorf("%s survived the pull that deleted it (err %v)", p, err)
		}
	}
	// What was the operator's stays: the edit and the never-tracked file.
	if got, err := os.ReadFile(filepath.Join(dir, "tools/edited.sh")); err != nil || string(got) != "my change\n" {
		t.Errorf("an edited file was deleted or changed: %q (%v)", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tools/sub/cache.pyc")); err != nil {
		t.Errorf("an ignored file cairn never tracked was deleted: %v", err)
	}
}

// With nothing of the operator's left inside it, the directory goes too.
func TestPullPrunesTheDirectoryItEmptied(t *testing.T) {
	f := newRebaseFixture(t)
	f.mainMoves("add tools", map[string]string{"tools/a.sh": "a\n", "tools/deep/b.sh": "b\n"})
	f.pull()
	gitIn(t, f.origin, "checkout", "-q", "main")
	gitIn(t, f.origin, "rm", "-rq", "tools")
	writeIn(t, f.origin, ".gitignore", "tools/\n")
	gitIn(t, f.origin, "add", ".gitignore")
	gitIn(t, f.origin, "commit", "-qm", "remove tools")
	gitIn(t, f.origin, "checkout", "-q", "--detach")
	if _, err := f.r.Pull("origin"); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(f.dir("main"), "tools")); !os.IsNotExist(err) {
		t.Fatalf("tools/ left behind after its files were removed (err %v)", err)
	}
}
