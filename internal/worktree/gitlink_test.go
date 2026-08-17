package worktree

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

// gitlinkSHA is a commit id from ANOTHER repository, so no object with this hash
// exists in the store under test — that is what makes it a gitlink and what #140
// tripped over. (The real entry from pingdotgg/t3code that surfaced the bug.)
const gitlinkSHA = "c9f5e549cf023632c3df948c207a58336192b3c7"

// gitlinkCommit builds a commit whose tree holds one regular file and one
// gitlink, and returns the engine and that commit's sha.
func gitlinkCommit(t *testing.T) (*change.Engine, string) {
	t.Helper()
	eng, err := change.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	main, err := eng.LineByName("main")
	if err != nil {
		t.Fatalf("LineByName: %v", err)
	}
	ch, err := eng.CreateChange(main.ID, "t")
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	blob, err := eng.WriteBlob([]byte("a\n"))
	if err != nil {
		t.Fatalf("WriteBlob: %v", err)
	}
	_, head, err := eng.SnapshotWorking(ch.ID, map[string]change.TreeEntry{
		"a.txt":         {SHA: blob, Mode: change.ModeRegular},
		"vendor/nested": {SHA: gitlinkSHA, Mode: change.ModeGitlink},
	})
	if err != nil {
		t.Fatalf("SnapshotWorking: %v", err)
	}
	return eng, head
}

// TestMaterializeGitlinkCreatesEmptyDir is the #140 repro at the unit level:
// materializing a tree with a gitlink used to abort on `change.readBlob: object
// not found`, leaving a truncated folder — and because materialize runs on every
// command, it wedged ls/tree/status too. The gitlink must become an empty
// directory (what git does for an uninitialized submodule) and the rest of the
// tree must land.
func TestMaterializeGitlinkCreatesEmptyDir(t *testing.T) {
	eng, head := gitlinkCommit(t)

	cacheDir := filepath.Join(t.TempDir(), "cache")
	dir := filepath.Join(t.TempDir(), "wc")
	if err := Materialize(eng, cacheDir, head, dir); err != nil {
		t.Fatalf("Materialize with a gitlink: %v", err)
	}

	// The rest of the tree must be complete — the original bug's signature was a
	// PARTIAL folder, so this is the assertion that matters most.
	if got, err := os.ReadFile(filepath.Join(dir, "a.txt")); err != nil || string(got) != "a\n" {
		t.Fatalf("a.txt = %q, err %v; want the whole tree materialized", got, err)
	}
	info, err := os.Stat(filepath.Join(dir, "vendor", "nested"))
	if err != nil {
		t.Fatalf("stat gitlink path: %v (want an empty directory)", err)
	}
	if !info.IsDir() {
		t.Fatalf("gitlink path is not a directory (mode %v)", info.Mode())
	}
	ents, err := os.ReadDir(filepath.Join(dir, "vendor", "nested"))
	if err != nil {
		t.Fatalf("ReadDir gitlink: %v", err)
	}
	if len(ents) != 0 {
		t.Errorf("gitlink dir has %d entries, want it left empty", len(ents))
	}

	// Materialize is run on every command: a second pass must be a clean no-op.
	if err := Materialize(eng, cacheDir, head, dir); err != nil {
		t.Fatalf("second Materialize: %v", err)
	}
}

// TestMaterializeLeavesSubmoduleContents covers the hazard that making the repo
// expressible at all would otherwise introduce: once the operator populates the
// submodule (git submodule update, or their own clone), those files are untracked
// here, and the deletion pass would sweep them. cairn does not own that subtree.
func TestMaterializeLeavesSubmoduleContents(t *testing.T) {
	eng, head := gitlinkCommit(t)

	cacheDir := filepath.Join(t.TempDir(), "cache")
	dir := filepath.Join(t.TempDir(), "wc")
	if err := Materialize(eng, cacheDir, head, dir); err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	nested := filepath.Join(dir, "vendor", "nested")
	inner := filepath.Join(nested, "inner.txt")
	if err := os.WriteFile(inner, []byte("belongs to the other repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(nested, "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(nested, "deep", "more.txt")
	if err := os.WriteFile(deep, []byte("also theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Materialize(eng, cacheDir, head, dir); err != nil {
		t.Fatalf("re-Materialize over a populated submodule: %v", err)
	}
	for _, p := range []string{inner, deep} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("submodule content %s was swept: %v", p, err)
		}
	}

	// The same subtree must stay out of the scan: pulling it in would collide
	// with the gitlink entry itself when the tree is written ("exists as both a
	// file and a directory").
	meta, err := eng.FilesMeta(head)
	if err != nil {
		t.Fatalf("FilesMeta: %v", err)
	}
	scanned, _, _, err := Scan(dir, trackedSetMeta(meta), gitlinkSet(meta))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for p := range scanned {
		if p != "a.txt" {
			t.Errorf("scan picked up %q; want only a.txt (the submodule is not ours to track)", p)
		}
	}
}

// TestSealPreservesGitlink is the data-loss half of #140. A gitlink is a
// directory with no content of its own, so the scan yields nothing for it; if the
// snapshot simply took the scan's word, the next seal would record the submodule
// as deleted. The entry must survive a commit unchanged.
func TestSealPreservesGitlink(t *testing.T) {
	skipOnWindows(t)

	r, _, dir := expressedWithCommit(t, "work")

	// Put a gitlink on the line tip, the way a pull or a clone of a repo that
	// contains one would.
	line, err := r.eng.LineByName("work")
	if err != nil {
		t.Fatalf("LineByName: %v", err)
	}
	open, err := r.eng.OpenChangeForLine(line.ID)
	if err != nil {
		t.Fatalf("OpenChangeForLine: %v", err)
	}
	blob, err := r.eng.WriteBlob([]byte("sealed\n"))
	if err != nil {
		t.Fatalf("WriteBlob: %v", err)
	}
	if _, _, err := r.eng.SnapshotWorking(open.ID, map[string]change.TreeEntry{
		"a.txt":         {SHA: blob, Mode: change.ModeRegular},
		"vendor/nested": {SHA: gitlinkSHA, Mode: change.ModeGitlink},
	}); err != nil {
		t.Fatalf("SnapshotWorking: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "vendor", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}

	// An ordinary commit of an unrelated edit must not disturb the gitlink.
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := r.Commit("work", "add b.txt")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	meta, err := r.eng.FilesMeta(res.HeadCommit)
	if err != nil {
		t.Fatalf("FilesMeta: %v", err)
	}
	ent, ok := meta["vendor/nested"]
	if !ok {
		t.Fatal("the gitlink was DELETED by the seal — this is #140's data-loss half")
	}
	if ent.Mode != change.ModeGitlink {
		t.Errorf("gitlink mode = %v, want ModeGitlink", ent.Mode)
	}
	if ent.SHA != gitlinkSHA {
		t.Errorf("gitlink SHA = %s, want %s (the pointer must not move)", ent.SHA, gitlinkSHA)
	}
	if _, ok := meta["b.txt"]; !ok {
		t.Error("b.txt missing: the ordinary part of the commit did not land")
	}
}

// TestMaterializeFileToGitlinkTransition covers a path that is a regular FILE in
// one tree and a gitlink in the next — upstream replacing a vendored file with a
// nested checkout. MkdirAll fails with "not a directory" over an existing file,
// which would have reproduced #140's wedge by another route.
func TestMaterializeFileToGitlinkTransition(t *testing.T) {
	eng, err := change.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	main, _ := eng.LineByName("main")
	ch, _ := eng.CreateChange(main.ID, "t")

	blob, err := eng.WriteBlob([]byte("i am a file\n"))
	if err != nil {
		t.Fatal(err)
	}
	_, asFile, err := eng.SnapshotWorking(ch.ID, map[string]change.TreeEntry{
		"vendor/nested": {SHA: blob, Mode: change.ModeRegular},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, asGitlink, err := eng.SnapshotWorking(ch.ID, map[string]change.TreeEntry{
		"vendor/nested": {SHA: gitlinkSHA, Mode: change.ModeGitlink},
	})
	if err != nil {
		t.Fatal(err)
	}

	cacheDir := filepath.Join(t.TempDir(), "cache")
	dir := filepath.Join(t.TempDir(), "wc")
	if err := Materialize(eng, cacheDir, asFile, dir); err != nil {
		t.Fatalf("Materialize the file tree: %v", err)
	}
	if err := Materialize(eng, cacheDir, asGitlink, dir); err != nil {
		t.Fatalf("Materialize file→gitlink: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dir, "vendor", "nested"))
	if err != nil || !fi.IsDir() {
		t.Fatalf("path is not a directory after the transition: %v", err)
	}
}

// TestMaterializeSymlinkToGitlinkTransition is the more dangerous mirror: MkdirAll
// FOLLOWS an existing symlink, so it succeeds silently and leaves disk disagreeing
// with the tree — with the "submodule" resolving outside the branch folder, the
// #126 write-through shape. The stale link must be replaced by a real directory.
func TestMaterializeSymlinkToGitlinkTransition(t *testing.T) {
	skipOnWindows(t)

	eng, err := change.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	main, _ := eng.LineByName("main")
	ch, _ := eng.CreateChange(main.ID, "t")

	outside := t.TempDir() // deliberately outside the branch folder
	link, err := eng.WriteBlob([]byte(outside))
	if err != nil {
		t.Fatal(err)
	}
	_, asSymlink, err := eng.SnapshotWorking(ch.ID, map[string]change.TreeEntry{
		"vendor/nested": {SHA: link, Mode: change.ModeSymlink},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, asGitlink, err := eng.SnapshotWorking(ch.ID, map[string]change.TreeEntry{
		"vendor/nested": {SHA: gitlinkSHA, Mode: change.ModeGitlink},
	})
	if err != nil {
		t.Fatal(err)
	}

	cacheDir := filepath.Join(t.TempDir(), "cache")
	dir := filepath.Join(t.TempDir(), "wc")
	if err := Materialize(eng, cacheDir, asSymlink, dir); err != nil {
		t.Fatalf("Materialize the symlink tree: %v", err)
	}
	if err := Materialize(eng, cacheDir, asGitlink, dir); err != nil {
		t.Fatalf("Materialize symlink→gitlink: %v", err)
	}
	fi, err := os.Lstat(filepath.Join(dir, "vendor", "nested"))
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("the path is still a symlink to %s; the tree says gitlink", outside)
	}
	if !fi.IsDir() {
		t.Fatalf("the path is not a directory: mode %v", fi.Mode())
	}
}

// TestResolveConflictedGitlink covers the dead-end an adversarial review of the
// #140 fix found. Recording a conflict for a divergent gitlink pointer promised a
// resolution path that did not exist: `resolve` sources the settled bytes from the
// file on disk, but the path is a DIRECTORY, so ReadFile failed with "is a
// directory" — while `commit` refused to seal while the conflict stayed open. The
// line wedged exactly as #134's modify/delete conflict did.
func TestResolveConflictedGitlink(t *testing.T) {
	skipOnWindows(t)

	const pointerA = "1111111111111111111111111111111111111111"
	const pointerB = "2222222222222222222222222222222222222222"

	r, _, dir := expressedWithCommit(t, "work")

	snapshot := func(branch, sha string) {
		t.Helper()
		line, err := r.eng.LineByName(branch)
		if err != nil {
			t.Fatalf("LineByName(%s): %v", branch, err)
		}
		open, err := r.eng.OpenChangeForLine(line.ID)
		if err != nil {
			t.Fatalf("OpenChangeForLine(%s): %v", branch, err)
		}
		meta := map[string]change.TreeEntry{}
		if line.TipCommit != "" {
			meta, err = r.eng.FilesMeta(line.TipCommit)
			if err != nil {
				t.Fatalf("FilesMeta(%s): %v", branch, err)
			}
		}
		meta["vendor/nested"] = change.TreeEntry{SHA: sha, Mode: change.ModeGitlink}
		if _, _, err := r.eng.SnapshotWorking(open.ID, meta); err != nil {
			t.Fatalf("SnapshotWorking(%s): %v", branch, err)
		}
	}

	sealMain := func(msg string) {
		t.Helper()
		line, err := r.eng.LineByName("main")
		if err != nil {
			t.Fatalf("LineByName(main): %v", err)
		}
		open, err := r.eng.OpenChangeForLine(line.ID)
		if err != nil {
			t.Fatalf("OpenChangeForLine(main): %v", err)
		}
		if _, _, err := r.eng.Seal(open.ID, msg); err != nil {
			t.Fatalf("Seal(main): %v", err)
		}
	}

	// A shared base pointer on both sides, then each side moves it differently.
	snapshot("main", gitlinkSHA)
	sealMain("base pointer")
	snapshot("work", gitlinkSHA)
	if _, err := r.Commit("work", "adopt the submodule"); err != nil {
		t.Fatalf("Commit (adopt): %v", err)
	}
	// main's move must be SEALED to become "ours" in the reconcile: mergeForward
	// adopts the parent's sealedTip, not its churning (working) commit.
	snapshot("main", pointerA)
	sealMain("bump on main")
	snapshot("work", pointerB)
	if err := os.MkdirAll(filepath.Join(dir, "vendor", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := r.Commit("work", "bump the submodule")
	if err != nil {
		t.Fatalf("Commit (bump): %v", err)
	}
	if len(res.Conflicts) == 0 {
		t.Fatal("no conflict recorded for a pointer both sides moved differently")
	}

	// The wedge: this used to fail with "is a directory".
	if err := r.Resolve("work", "vendor/nested", false); err != nil {
		t.Fatalf("Resolve a conflicted gitlink: %v", err)
	}
	line, _ := r.eng.LineByName("work")
	open, _ := r.eng.OpenChangeForLine(line.ID)
	still, err := r.eng.Conflicts(open.ID)
	if err != nil {
		t.Fatalf("Conflicts: %v", err)
	}
	for _, c := range still {
		if c.Path == "vendor/nested" && c.Status == "open" {
			t.Fatal("the gitlink conflict is still open after resolve")
		}
	}
	// The entry must survive the resolution as a gitlink.
	ch, _ := r.eng.GetChange(open.ID)
	meta, err := r.eng.FilesMeta(ch.HeadCommit)
	if err != nil {
		t.Fatalf("FilesMeta: %v", err)
	}
	ent, ok := meta["vendor/nested"]
	if !ok {
		t.Fatal("resolve dropped the gitlink from the tree")
	}
	if ent.Mode != change.ModeGitlink {
		t.Errorf("resolve turned the gitlink into mode %v", ent.Mode)
	}
	// And the folder must still hold a directory there, not a file.
	fi, err := os.Lstat(filepath.Join(dir, "vendor", "nested"))
	if err != nil || !fi.IsDir() {
		t.Fatalf("gitlink path is not a directory after resolve: %v", err)
	}
}
