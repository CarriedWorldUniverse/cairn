package worktree

import "testing"

func scanSet(t *testing.T, dir string, tracked map[string]struct{}) map[string]bool {
	t.Helper()
	files, _, _, err := Scan(dir, tracked)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for p := range files {
		got[p] = true
	}
	return got
}

func assertPresent(t *testing.T, got map[string]bool, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if !got[p] {
			t.Errorf("expected present but missing: %q", p)
		}
	}
}

func assertAbsent(t *testing.T, got map[string]bool, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if got[p] {
			t.Errorf("expected ignored but present: %q", p)
		}
	}
}

// A nested .gitignore is scoped to its own subtree (the headline gap).
func TestNestedGitignoreScopedToSubtree(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "sub/.gitignore", "secret\n")
	mustWrite(t, dir, "secret", "root-secret\n") // root scope: NOT ignored
	mustWrite(t, dir, "sub/secret", "x\n")       // ignored by sub/.gitignore
	mustWrite(t, dir, "sub/ok", "x\n")
	got := scanSet(t, dir, nil)
	assertPresent(t, got, "secret", "sub/ok", "sub/.gitignore")
	assertAbsent(t, got, "sub/secret")
}

// A deeper .gitignore negation re-includes a file the root ignored.
func TestNestedGitignoreNegationReincludes(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, ".gitignore", "*.log\n")
	mustWrite(t, dir, "sub/.gitignore", "!keep.log\n")
	mustWrite(t, dir, "a.log", "x\n")
	mustWrite(t, dir, "sub/keep.log", "x\n")
	mustWrite(t, dir, "sub/other.log", "x\n")
	got := scanSet(t, dir, nil)
	assertPresent(t, got, "sub/keep.log")
	assertAbsent(t, got, "a.log", "sub/other.log")
}

// Anchored (leading /) matches only at the dir root; floating matches at any depth.
func TestAnchoredVsFloatingInNestedDir(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "sub/.gitignore", "/foo\nbar\n")
	mustWrite(t, dir, "sub/foo", "x\n")   // anchored → ignored
	mustWrite(t, dir, "sub/x/foo", "x\n") // anchored to sub root → kept
	mustWrite(t, dir, "sub/bar", "x\n")   // floating → ignored
	mustWrite(t, dir, "sub/x/bar", "x\n") // floating → ignored
	got := scanSet(t, dir, nil)
	assertPresent(t, got, "sub/x/foo")
	assertAbsent(t, got, "sub/foo", "sub/bar", "sub/x/bar")
}

// A nested .cairnignore is honored (not just the root one).
func TestNestedCairnignoreHonored(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "pkg/.cairnignore", "*.gen\n")
	mustWrite(t, dir, "pkg/x.gen", "x\n") // ignored
	mustWrite(t, dir, "x.gen", "x\n")     // root scope → kept
	got := scanSet(t, dir, nil)
	assertPresent(t, got, "x.gen")
	assertAbsent(t, got, "pkg/x.gen")
}

// Within a directory, .cairnignore is applied after .gitignore, so a cairn
// negation overrides a git ignore (last-match-wins).
func TestCairnignoreOverridesGitignoreSameDir(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "sub/.gitignore", "*.tmp\n")
	mustWrite(t, dir, "sub/.cairnignore", "!keep.tmp\n")
	mustWrite(t, dir, "sub/a.tmp", "x\n")
	mustWrite(t, dir, "sub/keep.tmp", "x\n")
	got := scanSet(t, dir, nil)
	assertPresent(t, got, "sub/keep.tmp")
	assertAbsent(t, got, "sub/a.tmp")
}

// A TRACKED file under a newly-nested ignore is never dropped (load-bearing
// git semantic, across the new per-directory code path).
func TestTrackedFileUnderNestedIgnoreNotDropped(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "sub/.gitignore", "data.bin\n")
	mustWrite(t, dir, "sub/data.bin", "x\n")
	// Untracked → dropped.
	if scanSet(t, dir, nil)["sub/data.bin"] {
		t.Error("untracked sub/data.bin should be ignored")
	}
	// Tracked → kept despite the nested ignore.
	got := scanSet(t, dir, map[string]struct{}{"sub/data.bin": {}})
	assertPresent(t, got, "sub/data.bin")
}

// A tracked file inside an ignored directory keeps the directory descended; its
// untracked siblings are still filtered.
func TestTrackedDescendantKeepsIgnoredDirDescended(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "sub/.gitignore", "vendor/\n")
	mustWrite(t, dir, "sub/vendor/lib.go", "package lib\n")
	mustWrite(t, dir, "sub/vendor/junk", "x\n")
	got := scanSet(t, dir, map[string]struct{}{"sub/vendor/lib.go": {}})
	assertPresent(t, got, "sub/vendor/lib.go")
	assertAbsent(t, got, "sub/vendor/junk")
}
