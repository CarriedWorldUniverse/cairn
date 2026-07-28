package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStashPopByID verifies that StashPop honours a specific stash id rather
// than always taking the top of the stack (issue #132: `cairn stash pop <id>`
// routed the id to the branch/express path and errored "branch is not
// expressed", while `stash drop <id>` accepted the same id).
//
// Three stashes are pushed; the MIDDLE one is popped by id. Its content must
// land on disk and its row must leave the stack, with the other two untouched.
func TestStashPopByID(t *testing.T) {
	skipOnWindows(t)

	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	branch, err := r.DefaultBranch()
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	path := filepath.Join(root, branch, "f.txt")

	// Seal a base so each later write is an un-sealed working change to shelve.
	if err := os.WriteFile(path, []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(branch, "base"); err != nil {
		t.Fatalf("Commit base: %v", err)
	}

	ids := make(map[string]int64)
	for _, s := range []string{"sx", "sy", "sz"} {
		if err := os.WriteFile(path, []byte(s+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := r.Stash(branch, s); err != nil {
			t.Fatalf("Stash %s: %v", s, err)
		}
		entries, lerr := r.StashList()
		if lerr != nil {
			t.Fatalf("StashList: %v", lerr)
		}
		if len(entries) == 0 {
			t.Fatalf("Stash %s: nothing on the stack", s)
		}
		ids[s] = entries[0].ID // newest-first
	}

	// Pop the MIDDLE entry by id — not the top.
	if err := r.StashPop(branch, ids["sy"]); err != nil {
		t.Fatalf("StashPop(%d): %v", ids["sy"], err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != "sy\n" {
		t.Errorf("popped stash %d: f.txt = %q, want %q", ids["sy"], got, "sy\n")
	}

	entries, err := r.StashList()
	if err != nil {
		t.Fatalf("StashList: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("after pop: %d stash entries, want 2", len(entries))
	}
	for _, e := range entries {
		if e.ID == ids["sy"] {
			t.Errorf("popped stash %d is still on the stack", e.ID)
		}
	}

	// An unknown id must report the stash, not be read as a branch name.
	err = r.StashPop(branch, 9999)
	if err == nil {
		t.Fatal("StashPop(9999): want an error, got nil")
	}
	if got := err.Error(); !strings.Contains(got, "stash 9999 not found") {
		t.Errorf("StashPop(9999) error = %q, want it to name the missing stash", got)
	}
}

// TestStashPopTopStillDefaults pins the pre-existing behaviour that id 0 pops
// the most recent entry, so the id-aware path did not change the bare form.
func TestStashPopTopStillDefaults(t *testing.T) {
	skipOnWindows(t)

	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	branch, err := r.DefaultBranch()
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	path := filepath.Join(root, branch, "f.txt")

	if err := os.WriteFile(path, []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(branch, "base"); err != nil {
		t.Fatalf("Commit base: %v", err)
	}
	for _, s := range []string{"first", "second"} {
		if err := os.WriteFile(path, []byte(s+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := r.Stash(branch, s); err != nil {
			t.Fatalf("Stash %s: %v", s, err)
		}
	}

	if err := r.StashPop(branch, 0); err != nil {
		t.Fatalf("StashPop(0): %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second\n" {
		t.Errorf("StashPop(0): f.txt = %q, want the most recent stash %q", got, "second\n")
	}
}
