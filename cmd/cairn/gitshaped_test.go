package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedRepo initialises a repo with one sealed commit on the root line and
// returns its root directory.
func seedRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustRun(t, "init", root)
	if err := os.WriteFile(filepath.Join(root, "main", "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "main", "-m", "base")
	return root
}

// TestGitMergeIsExactlyFold pins the Tier-1 guardrail from #139: `merge` is a
// SPELLING of `fold`, not a variant of it. The assertion is the one that
// matters — the alias produced the same integrated tree the canonical verb
// does — plus that fold's own flags reach it unchanged.
func TestGitMergeIsExactlyFold(t *testing.T) {
	root := seedRepo(t)
	mustRun(t, "express", "--repo", root, "feature")
	if err := os.WriteFile(filepath.Join(root, "feature", "onfeature.txt"), []byte("f\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "feature", "-m", "feature work")

	mustRun(t, "merge", "--repo", root, "feature")

	if _, err := os.Stat(filepath.Join(root, "main", "onfeature.txt")); err != nil {
		t.Fatalf(`"cairn merge" did not integrate the line the way "cairn fold" does: %v`, err)
	}

	// fold's flags must reach the alias untouched — an alias that quietly
	// dropped --force would be a second surface, which the issue forbids.
	mustRun(t, "express", "--repo", root, "scratch")
	if err := os.WriteFile(filepath.Join(root, "scratch", "unsealed.txt"), []byte("u\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"merge", "--repo", root, "scratch"}); err == nil {
		t.Fatal(`"cairn merge" on a line with unsealed work should refuse, exactly as fold does`)
	}
	mustRun(t, "merge", "--repo", root, "--force", "scratch")
}

// TestGitBranchSpellingsAllExpress covers the rest of Tier 1: the three git
// ways to say "make me a new branch" all reach express.
func TestGitBranchSpellingsAllExpress(t *testing.T) {
	root := seedRepo(t)
	for _, args := range [][]string{
		{"branch", "--repo", root, "viaBranch"},
		{"checkout", "--repo", root, "-b", "viaCheckout"},
		{"switch", "--repo", root, "-c", "viaSwitch"},
	} {
		if err := run(args); err != nil {
			t.Fatalf("run %v: %v", args, err)
		}
	}
	for _, name := range []string{"viaBranch", "viaCheckout", "viaSwitch"} {
		if fi, err := os.Stat(filepath.Join(root, name)); err != nil || !fi.IsDir() {
			t.Errorf("%s was not expressed as a folder (err=%v)", name, err)
		}
	}
}

// TestGitCheckoutMinusBKeepsTheStartPoint: `git checkout -b <name> <start>`
// forks off <start>. express spells that --from, and silently dropping it
// would fork off the WRONG line — so the start point must be translated, not
// ignored.
func TestGitCheckoutMinusBKeepsTheStartPoint(t *testing.T) {
	root := seedRepo(t)
	mustRun(t, "express", "--repo", root, "parentline")

	err := run([]string{"checkout", "--repo", root, "-b", "child", "parentline"})
	if err == nil {
		t.Fatal("checkout -b <name> <start-point> silently ignored the start point")
	}
	if !strings.Contains(err.Error(), "--from parentline") {
		t.Errorf("error %q does not show the express --from translation", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "child")); statErr == nil {
		t.Error("a line was created off the wrong parent instead of being corrected")
	}
}

// TestGitOnlyVerbsTeachTheTranslation is Tier 2: a git verb with no cairn
// semantics must not be faked, and must not answer with the bare "unknown
// subcommand" + usage wall that sent the reader hunting. Each correction has
// to name the cairn command to run instead.
func TestGitOnlyVerbsTeachTheTranslation(t *testing.T) {
	root := seedRepo(t)
	cases := []struct {
		args []string
		want []string // substrings the correction must carry
	}{
		{[]string{"add", "."}, []string{"no staging area", "cairn commit"}},
		{[]string{"rm", "base.txt"}, []string{"delete the file on disk", "cairn commit"}},
		{[]string{"mv", "a", "b"}, []string{"move the file on disk", "cairn commit"}},
		{[]string{"rebase", "main"}, []string{"never needs a rebase", "cairn commit"}},
		{[]string{"reset", "--hard"}, []string{"cairn oplog", "cairn undo"}},
		{[]string{"revert", "HEAD"}, []string{"cairn undo", "cairn drop"}},
		{[]string{"worktree", "add", "x"}, []string{"cairn express", "cairn ls"}},
		{[]string{"checkout", "release"}, []string{"cd <repo>/release/", "cairn express release"}},
		{[]string{"switch", "release"}, []string{"cd <repo>/release/", "cairn express release"}},
		{[]string{"branch"}, []string{"cairn tree", "cairn ls"}},
	}
	for _, tc := range cases {
		args := append([]string{tc.args[0], "--repo", root}, tc.args[1:]...)
		err := run(args)
		if err == nil {
			t.Errorf("cairn %v succeeded; a git-only verb must be corrected, not faked", tc.args)
			continue
		}
		for _, want := range tc.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("cairn %v: correction %q does not mention %q", tc.args, err, want)
			}
		}
	}
}

// TestNonGitUnknownSubcommandIsUnchanged: the corpus-aware corrections must not
// swallow the generic unknown-subcommand path (which still prints usage).
func TestNonGitUnknownSubcommandIsUnchanged(t *testing.T) {
	err := run([]string{"frobnicate"})
	if err == nil || !strings.Contains(err.Error(), `unknown subcommand "frobnicate"`) {
		t.Fatalf("got %v, want the generic unknown-subcommand error", err)
	}
}
