package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression tests for the review of the refs stream.

// remoteWithOther returns a repo+bare with main pushed, and a second clone of the bare.
func refsWithOther(t *testing.T) (root, bare, other string) {
	t.Helper()
	refsNeedGit(t)
	root = refsRepo(t)
	bare = refsBare(t)
	mustRun(t, "remote", "add", "--repo", root, "origin", bare)
	mustRun(t, "push", "--repo", root, "origin", "main")
	other = filepath.Join(t.TempDir(), "other")
	refsGit(t, "", "clone", "-q", bare, other)
	return
}

func refsOtherCommit(t *testing.T, other, branch, file string) {
	t.Helper()
	_ = os.WriteFile(filepath.Join(other, file), []byte(file+"\n"), 0o644)
	refsGitIn(t, other, "add", ".")
	refsGitIn(t, other, "-c", "user.name=o", "-c", "user.email=o@x", "commit", "-q", "-m", "o "+file)
	refsGitIn(t, other, "push", "-q", "origin", "HEAD:refs/heads/"+branch)
}

// 1: a line with no tracking ref is expected ABSENT on the remote.
func TestReviewLeaseMissingTrackingRefIsStale(t *testing.T) {
	root, bare, other := refsWithOther(t)
	refsOtherCommit(t, other, "feat", "theirs.txt")
	theirs := strings.TrimSpace(refsGit(t, bare, "rev-parse", "feat"))
	mustRun(t, "branch", "--repo", root, "feat")
	refsCommit(t, root, "feat", "f.txt", "f\n", "ours")
	for _, args := range [][]string{
		{"push", "--repo", root, "--force-with-lease", "origin"},
		{"push", "--repo", root, "--force-with-lease", "origin", "feat"},
	} {
		if err := run(args); err == nil || !strings.Contains(err.Error(), "stale info") {
			t.Fatalf("%v: %v", args, err)
		}
		if got := strings.TrimSpace(refsGit(t, bare, "rev-parse", "feat")); got != theirs {
			t.Fatalf("%v overwrote the remote branch", args)
		}
	}
}

// 2: --force-with-lease also guards deletes.
func TestReviewLeaseGuardsDelete(t *testing.T) {
	root, bare, other := refsWithOther(t)
	mustRun(t, "branch", "--repo", root, "feat")
	refsCommit(t, root, "feat", "f.txt", "f\n", "ours")
	mustRun(t, "push", "--repo", root, "origin", "feat")
	refsGitIn(t, other, "fetch", "-q", "origin")
	refsGitIn(t, other, "checkout", "-q", "-B", "feat", "origin/feat")
	refsOtherCommit(t, other, "feat", "advanced.txt") // someone else moves it
	for _, args := range [][]string{
		{"push", "--repo", root, "--force-with-lease", "--delete", "origin", "feat"},
		{"push", "--repo", root, "--force-with-lease", "origin", ":feat"},
	} {
		if err := run(args); err == nil {
			t.Fatalf("%v deleted a branch someone else advanced", args)
		}
		if refsGit(t, bare, "branch", "--list", "feat") == "" {
			t.Fatalf("%v: branch is gone", args)
		}
	}
	mustRun(t, "fetch", "--repo", root, "origin")
	mustRun(t, "push", "--repo", root, "--force-with-lease", "--delete", "origin", "feat")
	if refsGit(t, bare, "branch", "--list", "feat") != "" {
		t.Fatal("delete with a fresh lease did not happen")
	}
}

// 3: the lease never moves remote tags, and src:dst sends only the named ref.
func TestReviewLeaseAndSrcDstLeaveTagsAlone(t *testing.T) {
	root, bare, _ := refsWithOther(t)
	mustRun(t, "tag", "--repo", root, "t1")
	mustRun(t, "push", "--repo", root, "origin", "main")
	t1 := strings.TrimSpace(refsGit(t, bare, "rev-parse", "refs/tags/t1"))
	refsCommit(t, root, "main", "b.txt", "b\n", "second")
	mustRun(t, "tag", "--repo", root, "-f", "t1", "main")
	_ = run([]string{"push", "--repo", root, "--force-with-lease", "origin", "main"}) // may refuse the tag move; must not force it
	if got := strings.TrimSpace(refsGit(t, bare, "rev-parse", "refs/tags/t1")); got != t1 {
		t.Fatalf("lease push moved remote tag t1: %s -> %s", t1, got)
	}
	mustRun(t, "tag", "--repo", root, "t2", "main")
	mustRun(t, "branch", "--repo", root, "feat")
	refsCommit(t, root, "feat", "f.txt", "f\n", "feat")
	mustRun(t, "push", "--repo", root, "origin", "feat:rel")
	if refsGit(t, bare, "tag", "-l", "t2") != "" {
		t.Fatal("src:dst push published tags")
	}
	mustRun(t, "push", "--repo", root, "--tags", "origin")
}

// 4: deleting a line that has no folder succeeds (and so does plain abandon).
func TestReviewBranchDeleteNonExpressed(t *testing.T) {
	root := refsRepo(t)
	for _, name := range []string{"a", "b"} {
		mustRun(t, "branch", "--repo", root, name)
		mustRun(t, "unexpress", "--repo", root, name)
	}
	mustRun(t, "branch", "--repo", root, "-D", "a")
	mustRun(t, "abandon", "--repo", root, "b")
	if got := refsOut(t, "branch", "--repo", root, "--list"); got != "* main\n" {
		t.Fatalf("lines left: %q", got)
	}
}

// 5: a rename that cannot move its folder leaves nothing half-applied.
func TestReviewRenameIsAtomic(t *testing.T) {
	root := refsRepo(t)
	mustRun(t, "branch", "--repo", root, "feat")
	if err := os.MkdirAll(filepath.Join(root, "feature"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"branch", "--repo", root, "-m", "feat", "feature"}); err == nil {
		t.Fatal("rename onto an existing directory succeeded")
	}
	got := refsOut(t, "branch", "--repo", root, "--list")
	if !strings.Contains(got, " feat\n") || strings.Contains(got, "feature") {
		t.Fatalf("half-applied rename:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(root, "feat")); err != nil {
		t.Fatalf("folder moved: %v", err)
	}
}

// 7: undo of an undo redoes tag / rename edits.
func TestReviewUndoOfUndoRedoes(t *testing.T) {
	root := refsRepo(t)
	mustRun(t, "tag", "--repo", root, "v1", "main")
	mustRun(t, "undo", "--repo", root)
	if refsOut(t, "tag", "--repo", root, "-l") != "" {
		t.Fatal("undo kept the tag")
	}
	mustRun(t, "undo", "--repo", root)
	if refsOut(t, "tag", "--repo", root, "-l") != "v1\n" {
		t.Fatal("undo of undo did not restore the tag")
	}
	mustRun(t, "branch", "--repo", root, "feat")
	mustRun(t, "branch", "--repo", root, "-m", "feat", "feature")
	mustRun(t, "undo", "--repo", root)
	mustRun(t, "undo", "--repo", root)
	if _, err := os.Stat(filepath.Join(root, "feature")); err != nil {
		t.Fatalf("redo of the rename did not move the folder: %v", err)
	}
	if got := refsOut(t, "branch", "--repo", root, "--list"); !strings.Contains(got, "feature") || strings.Contains(got, " feat\n") {
		t.Fatalf("lines after redo:\n%s", got)
	}
}

// 8: the LAST --sort key is primary, like git.
func TestReviewTagMultipleSortKeys(t *testing.T) {
	refsNeedGit(t)
	root := refsRepo(t)
	bare := refsBare(t)
	mustRun(t, "remote", "add", "--repo", root, "origin", bare)
	for _, n := range []string{"v1.10", "v1.2", "v1.9", "rc-1", "v2.0"} {
		mustRun(t, "tag", "--repo", root, n)
	}
	mustRun(t, "push", "--repo", root, "origin", "main")
	for _, keys := range [][]string{
		{"--sort=version:refname", "--sort=-refname"},
		{"--sort=-refname", "--sort=version:refname"},
	} {
		got := refsOut(t, append([]string{"tag", "--repo", root}, keys...)...)
		want := refsGit(t, bare, append([]string{"tag"}, keys...)...)
		if got != want {
			t.Errorf("%v:\ncairn %q\ngit   %q", keys, got, want)
		}
	}
}

// 10: a shell alias runs in the repo --repo named.
func TestReviewShellAliasRunsInRepoRoot(t *testing.T) {
	skipOnWindows(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := refsRepo(t)
	mustRun(t, "config", "--repo", root, "alias.where", "!pwd; true")
	t.Chdir(t.TempDir())
	out, _, err := runOutErr(t, "where", "--repo", root)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(root)
	got, _ := filepath.EvalSymlinks(strings.TrimSpace(strings.Split(out, "\n")[0]))
	if got != want {
		t.Fatalf("alias ran in %q, want %q", got, want)
	}
}

// 11: archive honours export-ignore like git archive.
func TestReviewArchiveExportIgnore(t *testing.T) {
	refsNeedGit(t)
	root := t.TempDir()
	mustRun(t, "init", root)
	m := filepath.Join(root, "main")
	files := map[string]string{
		".gitattributes":     "ignored.txt export-ignore\nhide export-ignore\n*.md export-ignore\n",
		"ignored.txt":        "x\n",
		"keep.txt":           "k\n",
		"README.md":          "r\n",
		"hide/inner.txt":     "i\n",
		"sub/.gitattributes": "local.txt export-ignore\n",
		"sub/local.txt":      "l\n",
		"sub/other.txt":      "o\n",
		"sub/doc.md":         "d\n",
	}
	for n, c := range files {
		p := filepath.Join(m, filepath.FromSlash(n))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(c), 0o644)
	}
	mustRun(t, "commit", "--repo", root, "-m", "attrs", "main")
	bare := refsBare(t)
	mustRun(t, "remote", "add", "--repo", root, "origin", bare)
	mustRun(t, "push", "--repo", root, "origin", "main")
	gitDir := filepath.Join(root, ".cairn", "objects.git")
	got := tarEntries(t, []byte(refsOut(t, "archive", "--repo", root, "main")))
	want := tarEntries(t, []byte(refsGit(t, gitDir, "archive", "--format=tar", "refs/heads/main")))
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("missing from cairn archive: %s", k)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("cairn archived %s, git did not", k)
		}
	}
}

// 12: you cannot delete the line whose folder the process stands in, even
// when --repo points at the repo from outside.
func TestReviewBranchDeleteStandingViaRepoFlag(t *testing.T) {
	root := refsRepo(t)
	mustRun(t, "branch", "--repo", root, "feat")
	t.Chdir(filepath.Join(root, "feat"))
	if err := run([]string{"branch", "--repo", root, "-D", "feat"}); err == nil {
		t.Fatal("deleted the line we stand in")
	}
}
