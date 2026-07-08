package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

func TestIndexOfDashDash(t *testing.T) {
	cases := []struct {
		args []string
		want int
	}{
		{[]string{"a", "b"}, -1},
		{[]string{"--"}, 0},
		{[]string{"main", "--", "x.go"}, 1},
		{[]string{"--", "x.go", "y.go"}, 0},
	}
	for _, c := range cases {
		if got := indexOfDashDash(c.args); got != c.want {
			t.Errorf("indexOfDashDash(%v) = %d, want %d", c.args, got, c.want)
		}
	}
}

func TestFilterDiffsByPaths(t *testing.T) {
	diffs := []change.FileDiff{
		{Path: "tests/UnitTests/CaaS/Controllers/VaultV2ControllerTests.cs"},
		{Path: "tests/UnitTests/Other.cs"},
		{Path: "other.txt"},
	}
	// exact file
	got := filterDiffsByPaths(diffs, []string{"other.txt"})
	if len(got) != 1 || got[0].Path != "other.txt" {
		t.Fatalf("exact-file filter = %+v", got)
	}
	// directory prefix
	got = filterDiffsByPaths(diffs, []string{"tests/UnitTests"})
	if len(got) != 2 {
		t.Fatalf("dir-prefix filter want 2, got %d", len(got))
	}
	// a directory named "tests" catches both under it, not other.txt
	got = filterDiffsByPaths(diffs, []string{"tests"})
	for _, d := range got {
		if !strings.HasPrefix(d.Path, "tests/") {
			t.Fatalf("dir filter leaked %q", d.Path)
		}
	}
	// backslash pathspec (Windows) matches forward-slash tree paths
	got = filterDiffsByPaths(diffs, []string{`tests\UnitTests\Other.cs`})
	if len(got) != 1 || got[0].Path != "tests/UnitTests/Other.cs" {
		t.Fatalf("backslash filter = %+v", got)
	}
	// no pathspecs → unchanged
	if got := filterDiffsByPaths(diffs, nil); len(got) != 3 {
		t.Fatalf("nil filter should pass through, got %d", len(got))
	}
	// prefix must be a path boundary: "other" does NOT match "other.txt"
	if got := filterDiffsByPaths(diffs, []string{"other"}); len(got) != 0 {
		t.Fatalf("partial-name should not match, got %+v", got)
	}
}

// TestE2E_DiffSingleFile is the #89 regression: `cairn diff <path>` (and the
// explicit `cairn diff -- <path>`) must diff just that file of the current line,
// not error "branch not expressed".
func TestE2E_DiffSingleFile(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	mustRun(t, "init", root)
	def := soleExpressedDir(t, root)

	dir := filepath.Join(root, def, "tests", "UnitTests")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join("tests", "UnitTests", "VaultTests.cs")
	if err := os.WriteFile(filepath.Join(root, def, target), []byte("orig\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, def, "other.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, def)

	// Working-copy edits to both files.
	if err := os.WriteFile(filepath.Join(root, def, target), []byte("orig\nCHANGED\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, def, "other.txt"), []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	slashTarget := filepath.ToSlash(target)
	// Bare path arg (the exact Visa repro) — must NOT error, must show the file.
	out := mustRunOut(t, "diff", "--repo", root, slashTarget)
	if !strings.Contains(out, "CHANGED") || !strings.Contains(out, "VaultTests.cs") {
		t.Fatalf("bare-path diff missing target change:\n%s", out)
	}
	if strings.Contains(out, "other.txt") {
		t.Fatalf("bare-path diff leaked other.txt:\n%s", out)
	}
	// Explicit -- form, same result.
	out = mustRunOut(t, "diff", "--repo", root, "--", slashTarget)
	if !strings.Contains(out, "CHANGED") || strings.Contains(out, "other.txt") {
		t.Fatalf("-- path diff wrong:\n%s", out)
	}
	// Whole-tree diff still shows both.
	out = mustRunOut(t, "diff", "--repo", root)
	if !strings.Contains(out, "VaultTests.cs") || !strings.Contains(out, "other.txt") {
		t.Fatalf("whole-tree diff should show both:\n%s", out)
	}

	// A mistyped branch must stay a LOUD error, not silently print an empty
	// diff (it matches no path on disk and nothing in the diff).
	if err := run([]string{"diff", "--repo", root, "mian"}); err == nil {
		t.Fatalf("mistyped branch should error, not print an empty diff")
	}

	// An existing-but-unchanged file legitimately prints nothing (like git).
	if err := os.WriteFile(filepath.Join(root, def, "untouched.txt"), []byte("same\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, def)
	out = mustRunOut(t, "diff", "--repo", root, "untouched.txt")
	if strings.TrimSpace(out) != "" {
		t.Fatalf("unchanged existing file should print empty diff, got:\n%s", out)
	}

	// A deleted (previously committed) file still shows in its filtered diff —
	// the on-disk existence guard must not block it.
	if err := os.Remove(filepath.Join(root, def, "other.txt")); err != nil {
		t.Fatal(err)
	}
	out = mustRunOut(t, "diff", "--repo", root, "other.txt")
	if !strings.Contains(out, "other.txt") {
		t.Fatalf("deleted file should appear in its filtered diff, got:\n%s", out)
	}
}

// TestE2E_DiffPathspecForms covers the pathspec canonicalization the review
// pass demanded: `./`-prefixed, absolute, and cwd-relative-from-inside-the-
// branch-folder specs must all match (git rewrites pathspecs the same way),
// and a bogus explicit `--` pathspec must error loudly, not print nothing.
func TestE2E_DiffPathspecForms(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	mustRun(t, "init", root)
	def := soleExpressedDir(t, root)

	if err := os.MkdirAll(filepath.Join(root, def, "tests", "UnitTests"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join("tests", "UnitTests", "Vault.cs")
	if err := os.WriteFile(filepath.Join(root, def, target), []byte("orig\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, def)
	if err := os.WriteFile(filepath.Join(root, def, target), []byte("orig\nCHANGED\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// From INSIDE the branch folder (branch-hint territory), all these forms
	// must show the change:
	t.Chdir(filepath.Join(root, def))
	for _, spec := range []string{
		"tests/UnitTests/Vault.cs",       // canonical tree-relative
		"./tests/UnitTests/Vault.cs",     // ./-prefixed
		filepath.Join(root, def, target), // absolute
	} {
		out := mustRunOut(t, "diff", "--repo", root, "--", spec)
		if !strings.Contains(out, "CHANGED") {
			t.Errorf("spec %q: diff missing change:\n%s", spec, out)
		}
	}
	// Bare form from inside the file's own directory.
	t.Chdir(filepath.Join(root, def, "tests", "UnitTests"))
	out := mustRunOut(t, "diff", "--repo", root, "Vault.cs")
	if !strings.Contains(out, "CHANGED") {
		t.Errorf("cwd-relative bare spec: diff missing change:\n%s", out)
	}
	out = mustRunOut(t, "diff", "--repo", root, "--", "Vault.cs")
	if !strings.Contains(out, "CHANGED") {
		t.Errorf("cwd-relative -- spec: diff missing change:\n%s", out)
	}

	// A bogus explicit `--` pathspec errors loudly instead of printing nothing.
	if err := run([]string{"diff", "--repo", root, "--", "no/such/file.cs"}); err == nil {
		t.Errorf("bogus -- pathspec should error, not print an empty diff")
	}
}
