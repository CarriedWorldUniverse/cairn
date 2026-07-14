package worktree

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

// TestContainedJoin (#126) is the sink-side guard: Materialize must refuse to
// resolve a tree path that escapes the branch folder, even if a traversal path
// somehow reached it past the change-layer read guard.
func TestContainedJoin(t *testing.T) {
	dir := t.TempDir()
	ok := []string{"a.txt", "src/mod/f.gd", "deep/a/b/c"}
	for _, p := range ok {
		got, err := containedJoin(dir, p)
		if err != nil {
			t.Errorf("containedJoin(%q) = %v, want ok", p, err)
			continue
		}
		if want := filepath.Join(dir, filepath.FromSlash(p)); got != want {
			t.Errorf("containedJoin(%q) = %q, want %q", p, got, want)
		}
	}
	bad := []string{"../escape", "a/../../escape", "../../etc/passwd", ".."}
	for _, p := range bad {
		if _, err := containedJoin(dir, p); err == nil {
			t.Errorf("containedJoin(%q) = nil, want escape error", p)
		}
	}
	if runtime.GOOS == "windows" {
		if _, err := containedJoin(dir, `..\escape`); err == nil {
			t.Errorf("containedJoin backslash-traversal accepted on windows")
		}
	}
}

// TestMaterializeRejectsCrossPullSymlinkEscape (#126, item A — the CRITICAL
// finding both reviewers reproduced) drives a REAL hostile two-pull sequence
// through change.Engine + Materialize, end to end: pull 1's tree has entry
// "linkdir" as a SYMLINK whose target escapes the branch folder; pull 2's tree
// replaces "linkdir" with a DIRECTORY containing "linkdir/pwned". Materialize
// must neutralize the on-disk symlink component before creating "linkdir" as a
// directory — otherwise os.MkdirAll's Stat follows the stale symlink and the
// second write lands outside the branch folder entirely. Asserts nothing
// escapes: a canary file placed just outside dir survives untouched, and no
// unexpected file appears at the symlink's target location.
func TestMaterializeRejectsCrossPullSymlinkEscape(t *testing.T) {
	eng, err := change.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	main, _ := eng.LineByName("main")
	ch, _ := eng.CreateChange(main.ID, "t")

	root := t.TempDir()
	dir := filepath.Join(root, "wc")
	cacheDir := filepath.Join(root, "cache")

	// "outside" is a real EXISTING directory one level above the branch folder
	// (where sibling expressed branches and .cairn live). The symlink target
	// below resolves to it — so on unfixed code os.MkdirAll's Stat follows the
	// stale symlink to a directory that already exists, returns nil, and the
	// pwned write lands SILENTLY at root/outside/pwned. Pointing the symlink at
	// a real dir (not a file) is what exercises the true silent-escape path
	// rather than merely provoking a "not a directory" hard error.
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("seed outside dir: %v", err)
	}

	// Pull 1: "linkdir" is a symlink escaping the branch folder into root/outside.
	r1, err := eng.Commit(ch.ID,
		map[string][]byte{"linkdir": []byte("../outside")},
		map[string]change.EntryMode{"linkdir": change.ModeSymlink}, "")
	if err != nil {
		t.Fatalf("commit r1: %v", err)
	}
	if err := Materialize(eng, cacheDir, r1.HeadCommit, dir); err != nil {
		t.Fatalf("mat1: %v", err)
	}
	linkPath := filepath.Join(dir, "linkdir")
	fi, err := os.Lstat(linkPath)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected linkdir to be a symlink after mat1, Lstat: %v, mode: %v", err, fi)
	}

	// Pull 2: "linkdir" is now a DIRECTORY containing "pwned" — the hostile
	// tree-shape replay. On unfixed code the write follows the stale symlink and
	// lands at root/outside/pwned (silent escape, no error). The fix removes the
	// symlink component before MkdirAll, so pwned stays inside dir.
	r2, err := eng.Commit(ch.ID,
		map[string][]byte{"linkdir/pwned": []byte("pwned\n")}, nil, "")
	if err != nil {
		t.Fatalf("commit r2: %v", err)
	}
	if err := Materialize(eng, cacheDir, r2.HeadCommit, dir); err != nil {
		t.Fatalf("mat2 (cross-pull symlink escape): %v", err)
	}

	// The escape target must be empty — nothing written through the symlink.
	if _, err := os.Stat(filepath.Join(outside, "pwned")); !os.IsNotExist(err) {
		t.Fatalf("pwned escaped through the symlink to root/outside (stat err=%v) — #126 symlink write-through", err)
	}
	// No file escaped to root itself either.
	if _, err := os.Stat(filepath.Join(root, "pwned")); !os.IsNotExist(err) {
		t.Fatal("pwned escaped to the parent of the branch folder")
	}
	// linkdir must now be a real directory containing pwned, INSIDE dir.
	fi2, err := os.Lstat(linkPath)
	if err != nil || fi2.Mode()&os.ModeSymlink != 0 || !fi2.IsDir() {
		t.Fatalf("linkdir should be a plain directory after mat2, got mode %v err %v", fi2, err)
	}
	got2, err := os.ReadFile(filepath.Join(dir, "linkdir", "pwned"))
	if err != nil || string(got2) != "pwned\n" {
		t.Fatalf("linkdir/pwned should exist INSIDE dir with correct content: got %q err %v", got2, err)
	}
}

// TestMaterializeRejectsDuplicateNameTree (#126, item B, sink side) is in
// internal/change/pathtraversal_test.go, NOT here: it needs to craft a raw
// hostile tree with two same-named entries, which requires access to
// change.Engine's unexported git storer (change.Commit/writeTree refuse to
// build such a tree, since a hostile remote's fetch does not go through
// writeTree). That test lives in package change (which worktree already
// depends on, so change importing worktree in its own _test.go — production
// code has no such dependency — creates no cycle) and drives
// worktree.Materialize directly against the raw tree.
