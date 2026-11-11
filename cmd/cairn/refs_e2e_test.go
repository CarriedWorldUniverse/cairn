package main

import (
	"archive/tar"
	"bytes"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// e2e for the ref verbs: branch, tag, remote, push/fetch/pull/clone options,
// config + aliases, and the maintenance verbs. Where git's semantics match,
// the output is diffed against real git run on the same repository.

func refsNeedGit(t *testing.T) {
	t.Helper()
	skipOnWindows(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

func refsGit(t *testing.T, gitDir string, args ...string) string {
	t.Helper()
	full := args
	if gitDir != "" {
		full = append([]string{"--git-dir", gitDir}, args...)
	}
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", full, err, out)
	}
	return string(out)
}

// refsRepo makes a cairn repo with one sealed commit on main and returns its root.
func refsRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustRun(t, "init", root)
	refsCommit(t, root, "main", "a.txt", "a\n", "first")
	return root
}

func refsCommit(t *testing.T, root, line, file, content, msg string) {
	t.Helper()
	dir := filepath.Join(root, strings.ReplaceAll(line, "/", "-"))
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "-m", msg, line)
}

func refsBare(t *testing.T) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "origin.git")
	refsGit(t, "", "init", "-q", "--bare", "--initial-branch=main", bare)
	return bare
}

func refsOut(t *testing.T, args ...string) string {
	t.Helper()
	out, _, err := runOutErr(t, args...)
	if err != nil {
		t.Fatalf("run %v: %v", args, err)
	}
	return out
}

func TestRefsBranchCreateListRenameDelete(t *testing.T) {
	root := refsRepo(t)
	mustRun(t, "branch", "--repo", root, "feat")
	refsCommit(t, root, "feat", "f.txt", "f\n", "feat work")
	mustRun(t, "branch", "--repo", root, "other", "main") // <name> <start> = express --from

	// Listing: * on the line you stand in (the root outside a folder), + on
	// the other expressed lines.
	got := refsOut(t, "branch", "--repo", root)
	want := "+ feat\n* main\n+ other\n"
	if got != want {
		t.Fatalf("branch list:\n%s\nwant:\n%s", got, want)
	}
	// Standing in a folder moves the *.
	got = refsOut(t, "branch", "--repo", filepath.Join(root, "feat"))
	if got != "* feat\n+ main\n+ other\n" {
		t.Fatalf("branch list from feat:\n%s", got)
	}
	if got := refsOut(t, "branch", "--repo", filepath.Join(root, "feat"), "--show-current"); got != "feat\n" {
		t.Fatalf("show-current = %q", got)
	}
	if got := refsOut(t, "branch", "--repo", root, "--list", "f*"); got != "+ feat\n" {
		t.Fatalf("--list f* = %q", got)
	}
	v := refsOut(t, "branch", "--repo", root, "-v")
	if !strings.Contains(v, "feat work") || !strings.Contains(v, "first") {
		t.Fatalf("-v lacks subjects:\n%s", v)
	}

	// Rename moves the folder, the line and survives undo.
	mustRun(t, "branch", "--repo", root, "-m", "feat", "feature")
	if _, err := os.Stat(filepath.Join(root, "feature", "f.txt")); err != nil {
		t.Fatalf("renamed folder missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "feat")); err == nil {
		t.Fatal("old folder still exists")
	}
	if got := refsOut(t, "branch", "--repo", root, "--list"); !strings.Contains(got, "feature") || strings.Contains(got, " feat\n") {
		t.Fatalf("after rename:\n%s", got)
	}
	// The rename is one oplog entry: undo puts the line and the folder back.
	mustRun(t, "undo", "--repo", root)
	if _, err := os.Stat(filepath.Join(root, "feat", "f.txt")); err != nil {
		t.Fatalf("undo did not restore the folder: %v", err)
	}
	if got := refsOut(t, "branch", "--repo", root, "--list"); !strings.Contains(got, " feat\n") || strings.Contains(got, "feature") {
		t.Fatalf("after undo:\n%s", got)
	}
	// A renamed line keeps working: commit, and a child line follows its parent.
	mustRun(t, "branch", "--repo", root, "-m", "feat", "feature")
	mustRun(t, "branch", "--repo", filepath.Join(root, "feature"), "child")
	refsCommit(t, root, "feature", "g.txt", "g\n", "more")
	mustRun(t, "branch", "--repo", root, "-m", "feature", "feat")
	mustRun(t, "branch", "--repo", root, "-D", "child")

	// -d refuses unfolded, unpushed work; -D forces.
	err := run([]string{"branch", "--repo", root, "-d", "feat"})
	if err == nil || !strings.Contains(err.Error(), "not fully merged") {
		t.Fatalf("branch -d on unmerged line: %v", err)
	}
	// Standing inside a line's folder, you cannot delete it.
	if err := run([]string{"branch", "--repo", filepath.Join(root, "feat"), "-D", "feat"}); err == nil {
		t.Fatal("deleted the line we stand in")
	}
	out := refsOut(t, "branch", "--repo", root, "-D", "feat")
	if !strings.HasPrefix(out, "Deleted branch feat (was ") {
		t.Fatalf("-D output %q", out)
	}
	// `other` has nothing beyond main: -d is allowed.
	if err := run([]string{"branch", "--repo", root, "-d", "other"}); err != nil {
		t.Fatalf("-d on a merged line: %v", err)
	}
	if got := refsOut(t, "branch", "--repo", root); got != "* main\n" {
		t.Fatalf("final list %q", got)
	}
	// Renaming onto an existing name is refused without -M.
	mustRun(t, "branch", "--repo", root, "x")
	if err := run([]string{"branch", "--repo", root, "-m", "x", "main"}); err == nil {
		t.Fatal("renamed onto an existing line without -M")
	}
	if err := run([]string{"branch", "--repo", root, "-m", "x", "bad name"}); err == nil {
		t.Fatal("accepted an invalid name")
	}
}

func TestRefsBranchMergedContainsAndUpstream(t *testing.T) {
	refsNeedGit(t)
	root := refsRepo(t)
	bare := refsBare(t)
	mustRun(t, "remote", "add", "--repo", root, "origin", bare)
	mustRun(t, "push", "--repo", root, "origin", "main")
	mustRun(t, "branch", "--repo", root, "feat")
	refsCommit(t, root, "feat", "f.txt", "f\n", "feat work")
	mustRun(t, "push", "--repo", root, "origin", "feat")
	refsCommit(t, root, "feat", "f2.txt", "f2\n", "feat more")
	mustRun(t, "branch", "--repo", root, "idle")

	vv := refsOut(t, "branch", "--repo", root, "-vv")
	if !strings.Contains(vv, "[origin/feat: ahead 1]") || !strings.Contains(vv, "[origin/main]") {
		t.Fatalf("-vv:\n%s", vv)
	}
	for _, ln := range strings.Split(vv, "\n") {
		if strings.Contains(ln, " idle ") && strings.Contains(ln, "[origin") {
			t.Fatalf("an unpushed line must show no upstream:\n%s", vv)
		}
	}
	// -r lists tracking branches; -a both.
	if got := refsOut(t, "branch", "--repo", root, "-r"); got != "  origin/feat\n  origin/main\n" {
		t.Fatalf("-r = %q", got)
	}
	a := refsOut(t, "branch", "--repo", root, "-a")
	if !strings.Contains(a, "remotes/origin/main") || !strings.Contains(a, "* main") {
		t.Fatalf("-a:\n%s", a)
	}
	// main's tip is in feat's history; feat's tip is not in main's.
	// Everything is in feat's history: main, idle (forked from main), feat itself.
	if got := refsOut(t, "branch", "--repo", root, "--merged", "feat"); got != "+ feat\n+ idle\n* main\n" {
		t.Fatalf("--merged feat = %q", got)
	}
	if got := refsOut(t, "branch", "--repo", root, "--merged", "main"); got != "+ idle\n* main\n" {
		t.Fatalf("--merged main = %q", got)
	}
	if got := refsOut(t, "branch", "--repo", root, "--no-merged", "main"); got != "+ feat\n" {
		t.Fatalf("--no-merged main = %q", got)
	}
	if got := refsOut(t, "branch", "--repo", root, "--contains", "main"); !strings.Contains(got, "feat") || !strings.Contains(got, "main") {
		t.Fatalf("--contains main = %q", got)
	}
	if got := refsOut(t, "branch", "--repo", root, "--format=%(refname:short) %(upstream:short)", "--list", "feat"); got != "feat origin/feat\n" {
		t.Fatalf("--format = %q", got)
	}
	// -u is accepted and says why it is a no-op.
	_, serr, err := runOutErr(t, "branch", "--repo", root, "-u", "origin/feat")
	if err != nil || !strings.Contains(serr, "same-named") {
		t.Fatalf("-u: %v %q", err, serr)
	}
}

func TestRefsBranchDeletePushedLine(t *testing.T) {
	refsNeedGit(t)
	root := refsRepo(t)
	bare := refsBare(t)
	mustRun(t, "remote", "add", "--repo", root, "origin", bare)
	mustRun(t, "push", "--repo", root, "origin", "main")
	mustRun(t, "branch", "--repo", root, "feat")
	refsCommit(t, root, "feat", "f.txt", "f\n", "feat work")
	if err := run([]string{"branch", "--repo", root, "-d", "feat"}); err == nil {
		t.Fatal("-d accepted an unpushed, unfolded line")
	}
	mustRun(t, "push", "--repo", root, "origin", "feat")
	mustRun(t, "branch", "--repo", root, "-d", "feat") // pushed: allowed
}

func TestRefsTagListingMatchesGit(t *testing.T) {
	refsNeedGit(t)
	root := refsRepo(t)
	bare := refsBare(t)
	mustRun(t, "remote", "add", "--repo", root, "origin", bare)
	for _, n := range []string{"v1.10", "v1.2", "v1.9", "v2.0", "rc-1", "v1.2.1"} {
		mustRun(t, "tag", "--repo", root, n)
	}
	mustRun(t, "push", "--repo", root, "origin", "main")

	for _, c := range []struct{ cairn, git []string }{
		{[]string{"tag"}, []string{"tag"}},
		{[]string{"tag", "-l", "v1.*"}, []string{"tag", "-l", "v1.*"}},
		{[]string{"tag", "--sort=version:refname"}, []string{"tag", "--sort=version:refname"}},
		{[]string{"tag", "--sort=-version:refname"}, []string{"tag", "--sort=-version:refname"}},
		{[]string{"tag", "--sort=-refname"}, []string{"tag", "--sort=-refname"}},
	} {
		got := refsOut(t, append(append([]string{}, c.cairn[0]), append([]string{"--repo", root}, c.cairn[1:]...)...)...)
		want := refsGit(t, bare, c.git...)
		if got != want {
			t.Errorf("cairn %v:\n%s\ngit %v:\n%s", c.cairn, got, c.git, want)
		}
	}
}

func TestRefsTagAnnotatedAtRevPushDeleteUndo(t *testing.T) {
	refsNeedGit(t)
	root := refsRepo(t)
	bare := refsBare(t)
	mustRun(t, "remote", "add", "--repo", root, "origin", bare)
	mustRun(t, "push", "--repo", root, "origin", "main")
	first := strings.TrimSpace(refsGit(t, bare, "rev-parse", "refs/heads/main"))
	refsCommit(t, root, "main", "b.txt", "b\n", "second")

	// A tag at an arbitrary commit (sha), annotated, message from -m.
	mustRun(t, "tag", "--repo", root, "-m", "release one", "v1", first)
	// And one from a message file.
	msg := filepath.Join(t.TempDir(), "msg")
	_ = os.WriteFile(msg, []byte("from file\n\nbody\n"), 0o644)
	mustRun(t, "tag", "--repo", root, "-a", "-F", msg, "v2", "main")
	mustRun(t, "tag", "--repo", root, "light", "main")
	if err := run([]string{"tag", "--repo", root, "v1", "main"}); err == nil {
		t.Fatal("re-tagging without -f succeeded")
	}
	mustRun(t, "tag", "--repo", root, "-f", "light", first)
	if err := run([]string{"tag", "--repo", root, "-a", "noMsg"}); err == nil {
		t.Fatal("annotated tag without a message succeeded")
	}

	if got := refsOut(t, "tag", "--repo", root, "-n1"); !strings.Contains(got, "v1              release one") || !strings.Contains(got, "light") {
		t.Fatalf("-n1:\n%s", got)
	}
	if got := refsOut(t, "tag", "--repo", root, "--points-at", first); got != "light\nv1\n" {
		t.Fatalf("--points-at = %q", got)
	}
	if got := refsOut(t, "tag", "--repo", root, "--contains", first, "-l", "v*"); got != "v1\nv2\n" {
		t.Fatalf("--contains = %q", got)
	}
	if got := refsOut(t, "tag", "--repo", root, "--sort=-creatordate", "--format=%(refname:short)"); len(strings.Fields(got)) != 3 {
		t.Fatalf("--sort=-creatordate: %q", got)
	}

	// Push publishes annotated tag objects, not lightweight copies.
	mustRun(t, "push", "--repo", root, "origin", "main")
	if typ := strings.TrimSpace(refsGit(t, bare, "cat-file", "-t", "refs/tags/v1")); typ != "tag" {
		t.Fatalf("v1 on the remote is a %q, want an annotated tag object", typ)
	}
	if typ := strings.TrimSpace(refsGit(t, bare, "cat-file", "-t", "refs/tags/light")); typ != "commit" {
		t.Fatalf("light on the remote is a %q", typ)
	}
	if got := strings.TrimSpace(refsGit(t, bare, "rev-parse", "refs/tags/v1^{commit}")); got != first {
		t.Fatalf("v1 peels to %s, want %s", got, first)
	}
	if body := refsGit(t, bare, "tag", "-n9", "-l", "v2"); !strings.Contains(body, "from file") {
		t.Fatalf("v2 message: %q", body)
	}

	// Delete locally (undoable) and on the remote.
	mustRun(t, "tag", "--repo", root, "-d", "v2")
	if got := refsOut(t, "tag", "--repo", root, "-l", "v2"); got != "" {
		t.Fatalf("v2 survived -d: %q", got)
	}
	mustRun(t, "undo", "--repo", root)
	if got := refsOut(t, "tag", "--repo", root, "-l", "v2"); got != "v2\n" {
		t.Fatalf("undo did not restore v2: %q", got)
	}
	mustRun(t, "push", "--repo", root, "--delete", "origin", "v2")
	if got := refsGit(t, bare, "tag", "-l", "v2"); got != "" {
		t.Fatalf("remote still has v2: %q", got)
	}
	mustRun(t, "push", "--repo", root, "origin", ":refs/tags/light")
	if got := refsGit(t, bare, "tag", "-l", "light"); got != "" {
		t.Fatalf("remote still has light: %q", got)
	}
	// Undo of the tag creation: the ref goes with it.
	mustRun(t, "tag", "--repo", root, "temp", "main")
	mustRun(t, "undo", "--repo", root)
	if got := refsOut(t, "tag", "--repo", root, "-l", "temp"); got != "" {
		t.Fatalf("undo left the tag: %q", got)
	}
}

func TestRefsRemoteVerbsMatchGit(t *testing.T) {
	refsNeedGit(t)
	root := refsRepo(t)
	bare := refsBare(t)
	bare2 := refsBare(t)
	mustRun(t, "remote", "add", "--repo", root, "origin", bare)
	mustRun(t, "remote", "add", "--repo", root, "--cairn", "team", bare2)
	gitDir := filepath.Join(root, ".cairn", "objects.git")

	if got, want := refsOut(t, "remote", "--repo", root, "-v"), refsGit(t, gitDir, "remote", "-v"); got != want {
		t.Fatalf("remote -v:\n%s\ngit:\n%s", got, want)
	}
	if got, want := refsOut(t, "remote", "--repo", root), refsGit(t, gitDir, "remote"); got != want {
		t.Fatalf("remote:\n%s\ngit:\n%s", got, want)
	}
	if got := refsOut(t, "remote", "get-url", "--repo", root, "origin"); got != bare+"\n" {
		t.Fatalf("get-url = %q", got)
	}
	mustRun(t, "push", "--repo", root, "origin", "main")
	show := refsOut(t, "remote", "show", "--repo", root, "origin")
	for _, w := range []string{"* remote origin", "Fetch URL: " + bare, "Push  URL: " + bare, "HEAD branch:", "main"} {
		if !strings.Contains(show, w) {
			t.Fatalf("remote show lacks %q:\n%s", w, show)
		}
	}

	// rename moves the tracking refs, keeps the kind; undo restores.
	mustRun(t, "remote", "rename", "--repo", root, "team", "squad")
	if got := refsOut(t, "remote", "--repo", root); got != "origin\nsquad\n" {
		t.Fatalf("after rename: %q", got)
	}
	mustRun(t, "remote", "rename", "--repo", root, "origin", "up")
	if refsGit(t, gitDir, "rev-parse", "refs/remotes/up/main") == "" {
		t.Fatal("tracking ref did not move")
	}
	if out := refsGit(t, gitDir, "for-each-ref", "refs/remotes/origin"); out != "" {
		t.Fatalf("old tracking refs remain: %q", out)
	}
	mustRun(t, "undo", "--repo", root)
	if got := refsOut(t, "remote", "--repo", root); got != "origin\nsquad\n" {
		t.Fatalf("undo of rename: %q", got)
	}
	mustRun(t, "remote", "set-url", "--repo", root, "squad", bare)
	if got := refsOut(t, "remote", "get-url", "--repo", root, "squad"); got != bare+"\n" {
		t.Fatalf("set-url: %q", got)
	}
	mustRun(t, "undo", "--repo", root)
	if got := refsOut(t, "remote", "get-url", "--repo", root, "squad"); got != bare2+"\n" {
		t.Fatalf("undo of set-url: %q", got)
	}
	mustRun(t, "remote", "rm", "--repo", root, "squad")
	if got := refsOut(t, "remote", "--repo", root); got != "origin\n" {
		t.Fatalf("after rm: %q", got)
	}
	mustRun(t, "undo", "--repo", root)
	if got := refsOut(t, "remote", "--repo", root); got != "origin\nsquad\n" {
		t.Fatalf("undo of rm: %q", got)
	}
	if err := run([]string{"remote", "rm", "--repo", root, "nope"}); err == nil {
		t.Fatal("removed a missing remote")
	}
	// `remote add --cairn` kind survives a rename + undo (listing shows it in show).
	if show := refsOut(t, "remote", "show", "-n", "--repo", root, "squad"); !strings.Contains(show, "Kind: cairn") {
		t.Fatalf("kind lost:\n%s", show)
	}

	// prune: a branch deleted on the remote leaves a stale tracking ref.
	mustRun(t, "branch", "--repo", root, "gone")
	refsCommit(t, root, "gone", "g.txt", "g\n", "g")
	mustRun(t, "push", "--repo", root, "origin", "gone")
	refsGit(t, bare, "update-ref", "-d", "refs/heads/gone")
	pr := refsOut(t, "remote", "prune", "--repo", root, "-n", "origin")
	if !strings.Contains(pr, "[would prune] origin/gone") {
		t.Fatalf("prune -n:\n%s", pr)
	}
	pr = refsOut(t, "remote", "prune", "--repo", root, "origin")
	if !strings.Contains(pr, "[pruned] origin/gone") {
		t.Fatalf("prune:\n%s", pr)
	}
}

func TestRefsPushOptions(t *testing.T) {
	refsNeedGit(t)
	root := refsRepo(t)
	bare := refsBare(t)
	mustRun(t, "remote", "add", "--repo", root, "origin", bare)
	mustRun(t, "branch", "--repo", root, "feat")
	refsCommit(t, root, "feat", "f.txt", "f\n", "feat")

	// -n reports and sends nothing.
	out := refsOut(t, "push", "--repo", root, "-n", "origin", "feat")
	if !strings.Contains(out, "would push feat -> origin/feat (new branch)") {
		t.Fatalf("dry run:\n%s", out)
	}
	if refsGit(t, bare, "branch", "--list") != "" {
		t.Fatal("dry run published something")
	}

	// <src>:<dst>: a line under another remote name; -u / -q accepted.
	mustRun(t, "push", "--repo", root, "-u", "-q", "origin", "feat:release")
	if got := strings.TrimSpace(refsGit(t, bare, "branch", "--list")); got != "release" {
		t.Fatalf("remote branches = %q", got)
	}
	sha := strings.TrimSpace(refsGit(t, bare, "rev-parse", "release"))
	want := strings.TrimSpace(refsGit(t, filepath.Join(root, ".cairn", "objects.git"), "rev-parse", "refs/heads/feat"))
	if sha != want {
		t.Fatalf("release = %s, want %s", sha, want)
	}

	// --tags: only tags.
	mustRun(t, "tag", "--repo", root, "t1", "feat")
	mustRun(t, "push", "--repo", root, "--tags", "origin")
	if got := strings.TrimSpace(refsGit(t, bare, "tag", "-l")); got != "t1" {
		t.Fatalf("tags = %q", got)
	}
	if got := strings.TrimSpace(refsGit(t, bare, "branch", "--list")); got != "release" {
		t.Fatalf("--tags pushed branches: %q", got)
	}

	// --all, then --delete a branch and `:branch`.
	mustRun(t, "push", "--repo", root, "--all", "origin")
	if got := strings.Fields(refsGit(t, bare, "branch", "--list", "--format=%(refname:short)")); len(got) != 3 {
		t.Fatalf("after --all: %v", got)
	}
	mustRun(t, "push", "--repo", root, "--delete", "origin", "release")
	mustRun(t, "push", "--repo", root, "origin", ":feat")
	if got := strings.TrimSpace(refsGit(t, bare, "branch", "--list", "--format=%(refname:short)")); got != "main" {
		t.Fatalf("after deletes: %q", got)
	}
	if err := run([]string{"push", "--repo", root, "--delete", "origin", "nonexistent"}); err == nil {
		t.Fatal("deleting a missing remote branch succeeded")
	}
}

func TestRefsPushForceWithLease(t *testing.T) {
	refsNeedGit(t)
	root := refsRepo(t)
	bare := refsBare(t)
	mustRun(t, "remote", "add", "--repo", root, "origin", bare)
	mustRun(t, "push", "--repo", root, "origin", "main")

	// Someone else advances origin/main behind our back.
	other := filepath.Join(t.TempDir(), "other")
	refsGit(t, "", "clone", "-q", bare, other)
	_ = os.WriteFile(filepath.Join(other, "z.txt"), []byte("z\n"), 0o644)
	_ = refsGitIn(t, other, "add", "z.txt")
	_ = refsGitIn(t, other, "-c", "user.name=o", "-c", "user.email=o@x", "commit", "-q", "-m", "theirs")
	_ = refsGitIn(t, other, "push", "-q", "origin", "HEAD")

	// Our local work + a lease against a stale tracking ref must refuse.
	refsCommit(t, root, "main", "b.txt", "b\n", "ours")
	err := run([]string{"push", "--repo", root, "--force-with-lease", "origin", "main"})
	if err == nil || !strings.Contains(err.Error(), "stale info") {
		t.Fatalf("lease on a stale tracking ref: %v", err)
	}
	theirs := strings.TrimSpace(refsGit(t, bare, "rev-parse", "main"))
	// After fetching, the lease holds and the force goes through.
	mustRun(t, "fetch", "--repo", root, "origin")
	mustRun(t, "push", "--repo", root, "--force-with-lease", "origin", "main")
	if got := strings.TrimSpace(refsGit(t, bare, "rev-parse", "main")); got == theirs {
		t.Fatal("force-with-lease did not publish")
	}
}

func refsGitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestRefsFetchAndPullOptions(t *testing.T) {
	refsNeedGit(t)
	root := refsRepo(t)
	bare := refsBare(t)
	mustRun(t, "remote", "add", "--repo", root, "origin", bare)
	mustRun(t, "branch", "--repo", root, "feat")
	refsCommit(t, root, "feat", "f.txt", "f\n", "feat")
	mustRun(t, "push", "--repo", root, "--all", "origin")

	// A second clone advances both branches on the remote.
	other := filepath.Join(t.TempDir(), "other")
	refsGit(t, "", "clone", "-q", bare, other)
	for _, br := range []string{"main", "feat"} {
		refsGitIn(t, other, "checkout", "-q", br)
		_ = os.WriteFile(filepath.Join(other, br+".txt"), []byte(br+"\n"), 0o644)
		refsGitIn(t, other, "add", ".")
		refsGitIn(t, other, "-c", "user.name=o", "-c", "user.email=o@x", "commit", "-q", "-m", "adv "+br)
		refsGitIn(t, other, "push", "-q", "origin", br)
	}
	gitDir := filepath.Join(root, ".cairn", "objects.git")
	before := strings.TrimSpace(refsGit(t, gitDir, "rev-parse", "refs/remotes/origin/main"))

	// fetch <remote> <branch>: only that branch's tracking ref moves.
	mustRun(t, "fetch", "--repo", root, "-q", "origin", "feat")
	if got := strings.TrimSpace(refsGit(t, gitDir, "rev-parse", "refs/remotes/origin/main")); got != before {
		t.Fatalf("fetch origin feat moved origin/main")
	}
	if got, want := strings.TrimSpace(refsGit(t, gitDir, "rev-parse", "refs/remotes/origin/feat")), strings.TrimSpace(refsGit(t, bare, "rev-parse", "feat")); got != want {
		t.Fatalf("origin/feat = %s want %s", got, want)
	}
	mustRun(t, "fetch", "--repo", root, "--all", "--tags", "--prune", "-q")
	if got, want := strings.TrimSpace(refsGit(t, gitDir, "rev-parse", "refs/remotes/origin/main")), strings.TrimSpace(refsGit(t, bare, "rev-parse", "main")); got != want {
		t.Fatalf("fetch --all: origin/main = %s want %s", got, want)
	}

	// pull <remote> <branch> reconciles only that line.
	mustRun(t, "pull", "--repo", root, "--rebase", "--ff-only", "-q", "origin", "feat")
	if _, err := os.Stat(filepath.Join(root, "feat", "feat.txt")); err != nil {
		t.Fatalf("pull origin feat did not bring the commit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "main", "main.txt")); err == nil {
		t.Fatal("pull origin feat also advanced main")
	}
	mustRun(t, "pull", "--repo", root, "--no-rebase", "origin")
	if _, err := os.Stat(filepath.Join(root, "main", "main.txt")); err != nil {
		t.Fatalf("full pull did not advance main: %v", err)
	}

	// --ff-only refuses a diverged line.
	refsCommit(t, root, "main", "local.txt", "l\n", "local")
	refsGitIn(t, other, "checkout", "-q", "main")
	_ = os.WriteFile(filepath.Join(other, "again.txt"), []byte("x\n"), 0o644)
	refsGitIn(t, other, "add", ".")
	refsGitIn(t, other, "-c", "user.name=o", "-c", "user.email=o@x", "commit", "-q", "-m", "diverge")
	refsGitIn(t, other, "push", "-q", "origin", "main")
	err := run([]string{"pull", "--repo", root, "--ff-only", "origin"})
	if err == nil || !strings.Contains(err.Error(), "fast-forward") {
		t.Fatalf("--ff-only on a diverged line: %v", err)
	}
}

func TestRefsCloneFlags(t *testing.T) {
	refsNeedGit(t)
	skipOnWindows(t)
	bare := makeSeededBareRepo(t)
	// Add a second branch to the remote.
	work := filepath.Join(t.TempDir(), "w")
	refsGit(t, "", "clone", "-q", bare, work)
	refsGitIn(t, work, "checkout", "-q", "-b", "dev")
	_ = os.WriteFile(filepath.Join(work, "dev.txt"), []byte("d\n"), 0o644)
	refsGitIn(t, work, "add", ".")
	refsGitIn(t, work, "-c", "user.name=o", "-c", "user.email=o@x", "commit", "-q", "-m", "dev")
	refsGitIn(t, work, "push", "-q", "origin", "dev")

	for _, bad := range [][]string{{"--depth", "1"}, {"--single-branch"}, {"--bare"}} {
		args := append([]string{"clone"}, bad...)
		args = append(args, bare, filepath.Join(t.TempDir(), "c"))
		if err := run(args); err == nil {
			t.Fatalf("clone %v accepted", bad)
		}
	}
	dir := filepath.Join(t.TempDir(), "c")
	mustRun(t, "clone", "-q", "-b", "dev", "-o", "up", bare, dir)
	if _, err := os.Stat(filepath.Join(dir, "dev", "dev.txt")); err != nil {
		t.Fatalf("-b dev not expressed: %v", err)
	}
	if got := refsOut(t, "remote", "--repo", dir); got != "up\n" {
		t.Fatalf("-o: remotes %q", got)
	}
	dir2 := filepath.Join(t.TempDir(), "c2")
	if err := run([]string{"clone", "-q", "-b", "nope", bare, dir2}); err == nil {
		t.Fatal("clone -b of a missing line succeeded")
	}
	if _, err := os.Stat(dir2); err == nil {
		t.Fatal("failed clone left its directory behind")
	}
}

func TestRefsInitFlags(t *testing.T) {
	root := filepath.Join(t.TempDir(), "r")
	mustRun(t, "init", "-b", "trunk", "-q", root)
	if _, err := os.Stat(filepath.Join(root, "trunk")); err != nil {
		t.Fatalf("-b trunk: %v", err)
	}
	if got := refsOut(t, "branch", "--repo", root); got != "* trunk\n" {
		t.Fatalf("branch = %q", got)
	}
	if err := run([]string{"init", "--bare", filepath.Join(t.TempDir(), "b")}); err == nil || !strings.Contains(err.Error(), "bare") {
		t.Fatalf("--bare: %v", err)
	}
	// init.defaultBranch from the global config.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mustRun(t, "config", "--global", "init.defaultBranch", "develop")
	r2 := filepath.Join(t.TempDir(), "r2")
	mustRun(t, "init", r2)
	if _, err := os.Stat(filepath.Join(r2, "develop")); err != nil {
		t.Fatalf("init.defaultBranch ignored: %v", err)
	}
}

func TestRefsConfigListGetUnsetScopes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := refsRepo(t)
	mustRun(t, "config", "--repo", root, "core.editor", "vim")
	mustRun(t, "config", "--repo", root, "--local", "pull.rebase", "true")
	mustRun(t, "config", "--global", "user.name", "Glob")
	mustRun(t, "config", "--global", "alias.st", "status")
	mustRun(t, "config", "--repo", root, "Color.UI", "auto")

	list := refsOut(t, "config", "--repo", root, "--list")
	for _, w := range []string{"user.name=Glob", "alias.st=status", "core.editor=vim", "pull.rebase=true", "color.ui=auto"} {
		if !strings.Contains(list, w+"\n") {
			t.Fatalf("--list lacks %q:\n%s", w, list)
		}
	}
	if got := refsOut(t, "config", "--repo", root, "--local", "--list"); strings.Contains(got, "user.name") {
		t.Fatalf("--local --list leaked global:\n%s", got)
	}
	if got := refsOut(t, "config", "--repo", root, "--global", "-l"); strings.Contains(got, "core.editor") {
		t.Fatalf("--global -l leaked local:\n%s", got)
	}
	if got := refsOut(t, "config", "--repo", root, "--list", "--show-origin"); !strings.Contains(got, "file:") {
		t.Fatalf("--show-origin:\n%s", got)
	}
	if got := refsOut(t, "config", "--repo", root, "--get", "core.editor"); got != "vim\n" {
		t.Fatalf("--get = %q", got)
	}
	// Unset keys: --get exits 1 silently.
	err := run([]string{"config", "--repo", root, "--get", "nope.nothing"})
	if !errors.Is(err, errSilent) {
		t.Fatalf("--get on an unset key: %v", err)
	}
	mustRun(t, "config", "--repo", root, "--unset", "core.editor")
	if err := run([]string{"config", "--repo", root, "--get", "core.editor"}); !errors.Is(err, errSilent) {
		t.Fatalf("--unset did not remove: %v", err)
	}
	if err := run([]string{"config", "--repo", root, "--unset", "core.editor"}); !errors.Is(err, errSilent) {
		t.Fatalf("--unset of a missing key: %v", err)
	}
	mustRun(t, "config", "--global", "--unset", "alias.st")
	if got := refsOut(t, "config", "--global", "--list"); strings.Contains(got, "alias.st") {
		t.Fatalf("global unset:\n%s", got)
	}
	// Validation: shape, and the keys with a fixed vocabulary.
	for _, bad := range [][]string{{"nodots", "x"}, {"a..b", "x"}, {"push.default", "sideways"}, {"autosync", "maybe"}, {"init.defaultBranch", "bad name"}} {
		if err := run(append([]string{"config", "--repo", root}, bad...)); err == nil {
			t.Errorf("config %v accepted", bad)
		}
	}
	// The legacy form still works: bare key prints, unset prints an empty line.
	if got := refsOut(t, "config", "--repo", root, "user.name"); got != "Glob\n" {
		t.Fatalf("bare get falls through to global: %q", got)
	}
}

func TestRefsAliases(t *testing.T) {
	skipOnWindows(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := refsRepo(t)
	mustRun(t, "config", "--repo", root, "alias.br", "branch -v")
	mustRun(t, "config", "--repo", root, "alias.mkfeat", "branch feat main")
	mustRun(t, "config", "--repo", root, "alias.sh", "!echo shell-alias:$#:$1")
	mustRun(t, "config", "--repo", root, "alias.loop", "loop")
	mustRun(t, "config", "--repo", root, "alias.quoted", `tag -m "two words" qtag`)
	mustRun(t, "config", "--repo", root, "alias.status", "version") // must not shadow the built-in

	out := refsOut(t, "br", "--repo", root)
	if !strings.Contains(out, "first") {
		t.Fatalf("alias br:\n%s", out)
	}
	mustRun(t, "mkfeat", "--repo", root)
	if _, err := os.Stat(filepath.Join(root, "feat")); err != nil {
		t.Fatalf("alias with args: %v", err)
	}
	mustRun(t, "quoted", "--repo", root)
	if got := refsOut(t, "tag", "--repo", root, "-n1"); !strings.Contains(got, "two words") {
		t.Fatalf("quoted alias:\n%s", got)
	}
	// A shell alias runs in the repo root with extra args appended.
	cmd := exec.Command("sh", "-c", `echo`) // sanity: sh exists
	if err := cmd.Run(); err != nil {
		t.Skip("no sh")
	}
	t.Chdir(root)
	old := os.Stdout
	rp, wp, _ := os.Pipe()
	os.Stdout = wp
	err := run([]string{"sh", "arg1"})
	_ = wp.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, rp)
	if err != nil || strings.TrimSpace(buf.String()) != "shell-alias:1:arg1 arg1" {
		t.Fatalf("shell alias: %v %q", err, buf.String())
	}
	if err := run([]string{"loop"}); err == nil || !strings.Contains(err.Error(), "loop") {
		t.Fatalf("alias loop: %v", err)
	}
	// An alias never shadows a built-in: `status` still reports status.
	if _, _, err := runOutErr(t, "status", "--repo", root); err != nil {
		t.Fatalf("status shadowed: %v", err)
	}
}

func TestRefsReflog(t *testing.T) {
	root := refsRepo(t)
	refsCommit(t, root, "main", "b.txt", "b\n", "second")
	mustRun(t, "tag", "--repo", root, "v1")
	out := refsOut(t, "reflog", "--repo", root)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 || !strings.Contains(lines[0], "main@{0}: commit") || !strings.Contains(lines[1], "main@{1}: commit") {
		t.Fatalf("reflog:\n%s", out)
	}
	for _, l := range lines {
		if len(strings.Fields(l)[0]) != 7 {
			t.Fatalf("sha not abbreviated: %q", l)
		}
	}
	if got := refsOut(t, "reflog", "show", "--repo", root, "main", "-n", "1"); strings.Count(got, "\n") != 1 {
		t.Fatalf("-n 1: %q", got)
	}
	if err := run([]string{"reflog", "expire", "--repo", root}); err == nil {
		t.Fatal("reflog expire accepted")
	}
}

func TestRefsGcFsckCountObjects(t *testing.T) {
	root := refsRepo(t)
	for i := 0; i < 5; i++ {
		refsCommit(t, root, "main", "f.txt", strings.Repeat("line\n", 50+i), "edit")
	}
	mustRun(t, "branch", "--repo", root, "side")
	refsCommit(t, root, "side", "s.txt", "s\n", "side")
	mustRun(t, "tag", "--repo", root, "-m", "ann", "v1", "side")

	before := refsOut(t, "count-objects", "--repo", root, "-v")
	if !strings.Contains(before, "count: ") || !strings.Contains(before, "in-pack: 0") {
		t.Fatalf("count-objects -v before:\n%s", before)
	}
	if _, serr, err := runOutErr(t, "fsck", "--repo", root); err != nil || serr != "" {
		t.Fatalf("fsck on a healthy repo: %v %q", err, serr)
	}
	// undo history must survive gc: capture state, gc --prune=now, undo, verify.
	mustRun(t, "gc", "--repo", root, "--aggressive", "--prune=now")
	after := refsOut(t, "count-objects", "--repo", root, "-v")
	if !strings.Contains(after, "count: 0\n") || strings.Contains(after, "in-pack: 0\n") || !strings.Contains(after, "packs: 1\n") {
		t.Fatalf("count-objects -v after gc:\n%s", after)
	}
	if _, serr, err := runOutErr(t, "fsck", "--repo", root); err != nil || serr != "" {
		t.Fatalf("fsck after gc: %v %q", err, serr)
	}
	mustRun(t, "undo", "--repo", root) // reverts the last commit; its tip must still resolve
	if _, serr, err := runOutErr(t, "fsck", "--repo", root); err != nil || serr != "" {
		t.Fatalf("fsck after gc+undo: %v %q", err, serr)
	}
	if out := refsOut(t, "log", "--repo", root, "main"); !strings.Contains(out, "edit") {
		t.Fatalf("history lost after gc:\n%s", out)
	}
	// Real git can read what gc wrote.
	if _, err := exec.LookPath("git"); err == nil {
		gitDir := filepath.Join(root, ".cairn", "objects.git")
		mustRun(t, "gc", "--repo", root, "-q")
		_ = refsGit(t, gitDir, "fsck", "--no-dangling")
	}
}

func TestRefsFsckDetectsMissingObject(t *testing.T) {
	root := refsRepo(t)
	// Delete the blob of a.txt: reachable from the tip, unlike the working
	// snapshots a "first loose object" pick could land on.
	refsNeedGit(t)
	h := strings.TrimSpace(refsGitStdin(t, "a\n", "hash-object", "--stdin"))
	victim := filepath.Join(root, ".cairn", "objects.git", "objects", h[:2], h[2:])
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("expected loose blob %s: %v", h, err)
	}
	_ = os.Chmod(victim, 0o644)
	if err := os.Remove(victim); err != nil {
		t.Fatal(err)
	}
	_, serr, err := runOutErr(t, "fsck", "--repo", root)
	if err == nil || !strings.Contains(serr, "missing") {
		t.Fatalf("fsck missed a deleted object: %v %q", err, serr)
	}
}

func tarEntries(t *testing.T, data []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		body, _ := io.ReadAll(tr)
		out[h.Name] = string(h.Typeflag) + "|" + string(body) + "|" + h.FileInfo().Mode().String() + "|" + h.ModTime.UTC().String()
		if h.Typeflag == tar.TypeSymlink {
			out[h.Name] = "2|" + h.Linkname + "|" + h.FileInfo().Mode().String() + "|" + h.ModTime.UTC().String()
		}
	}
}

func TestRefsArchiveMatchesGit(t *testing.T) {
	refsNeedGit(t)
	root := t.TempDir()
	mustRun(t, "init", root)
	m := filepath.Join(root, "main")
	_ = os.MkdirAll(filepath.Join(m, "sub", "deep"), 0o755)
	_ = os.WriteFile(filepath.Join(m, "a.txt"), []byte("a\n"), 0o644)
	_ = os.WriteFile(filepath.Join(m, "sub", "b.txt"), []byte("b\n"), 0o644)
	_ = os.WriteFile(filepath.Join(m, "sub", "deep", "c.sh"), []byte("#!/bin/sh\n"), 0o755)
	_ = os.Symlink("a.txt", filepath.Join(m, "link"))
	mustRun(t, "commit", "--repo", root, "-m", "tree", "main")
	bare := refsBare(t)
	mustRun(t, "remote", "add", "--repo", root, "origin", bare)
	mustRun(t, "push", "--repo", root, "origin", "main")
	gitDir := filepath.Join(root, ".cairn", "objects.git")

	for _, c := range []struct {
		name  string
		cairn []string
		git   []string
	}{
		{"whole tree", []string{"main"}, []string{"refs/heads/main"}},
		{"prefix", []string{"--prefix=proj-1/", "main"}, []string{"--prefix=proj-1/", "refs/heads/main"}},
		{"subpath", []string{"main", "sub"}, []string{"refs/heads/main", "sub"}},
		{"file", []string{"main", "a.txt"}, []string{"refs/heads/main", "a.txt"}},
	} {
		got := refsOut(t, append([]string{"archive", "--repo", root}, c.cairn...)...)
		want := refsGit(t, gitDir, append([]string{"archive", "--format=tar"}, c.git...)...)
		g, w := tarEntries(t, []byte(got)), tarEntries(t, []byte(want))
		var gk, wk []string
		for k := range g {
			gk = append(gk, k)
		}
		for k := range w {
			wk = append(wk, k)
		}
		sort.Strings(gk)
		sort.Strings(wk)
		if strings.Join(gk, ",") != strings.Join(wk, ",") {
			t.Errorf("%s: entries\n cairn %v\n git   %v", c.name, gk, wk)
			continue
		}
		for _, k := range wk {
			if g[k] != w[k] {
				t.Errorf("%s: %s differs:\n cairn %q\n git   %q", c.name, k, g[k], w[k])
			}
		}
	}

	// -o with the format inferred from the extension; zip and tar.gz are valid.
	dst := t.TempDir()
	mustRun(t, "archive", "--repo", root, "-o", filepath.Join(dst, "x.tar.gz"), "main")
	mustRun(t, "archive", "--repo", root, "--format=zip", "-o", filepath.Join(dst, "x.zip"), "main")
	if out, err := exec.Command("tar", "tzf", filepath.Join(dst, "x.tar.gz")).CombinedOutput(); err != nil || !strings.Contains(string(out), "sub/b.txt") {
		t.Fatalf("tar.gz: %v %s", err, out)
	}
	if _, err := exec.LookPath("unzip"); err == nil {
		if out, err := exec.Command("unzip", "-l", filepath.Join(dst, "x.zip")).CombinedOutput(); err != nil || !strings.Contains(string(out), "sub/b.txt") {
			t.Fatalf("zip: %v %s", err, out)
		}
	}
	if err := run([]string{"archive", "--repo", root, "main", "missing-path"}); err == nil {
		t.Fatal("archive of a non-matching path succeeded")
	}
	if err := run([]string{"archive", "--repo", root, "--format=rar", "main"}); err == nil {
		t.Fatal("unknown format accepted")
	}
}

func TestRefsCheckIgnoreMatchesGit(t *testing.T) {
	refsNeedGit(t)
	root := refsRepo(t)
	m := filepath.Join(root, "main")
	files := map[string]string{
		".gitignore":        "*.log\nbuild/\n!keep.log\n/rootonly\n# comment\n",
		"sub/.gitignore":    "*.tmp\n!important.tmp\n",
		"sub/.cairnignore":  "secret.txt\n",
		"a.log":             "",
		"keep.log":          "",
		"build/out.o":       "",
		"rootonly":          "",
		"sub/rootonly":      "",
		"sub/x.tmp":         "",
		"sub/important.tmp": "",
		"sub/secret.txt":    "",
		"sub/code.go":       "",
	}
	ref := t.TempDir()
	for name, content := range files {
		for _, base := range []string{m, ref} {
			p := filepath.Join(base, filepath.FromSlash(name))
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			_ = os.WriteFile(p, []byte(content), 0o644)
		}
	}
	// git sees .cairnignore as an ordinary file, so mirror it as a .gitignore
	// in the reference repo to compare the matching rules themselves.
	_ = os.WriteFile(filepath.Join(ref, "sub", ".gitignore"), []byte("*.tmp\n!important.tmp\nsecret.txt\n"), 0o644)
	refsGit(t, "", "init", "-q", ref)

	paths := []string{"a.log", "keep.log", "build/out.o", "rootonly", "sub/rootonly", "sub/x.tmp", "sub/important.tmp", "sub/secret.txt", "sub/code.go", "build"}
	for _, p := range paths {
		var wantOut string
		cmd := exec.Command("git", "check-ignore", p)
		cmd.Dir = ref
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		b, _ := cmd.Output()
		wantOut = string(b)
		t.Chdir(m)
		got, _, err := runOutErr(t, "check-ignore", "--repo", root, p)
		if wantOut == "" {
			if !errors.Is(err, errSilent) || got != "" {
				t.Errorf("%s: git says not ignored; cairn: %q %v", p, got, err)
			}
			continue
		}
		if err != nil || got != wantOut {
			t.Errorf("%s: git %q, cairn %q (%v)", p, wantOut, got, err)
		}
	}
	// -v: <source>:<line>:<pattern><TAB><path> — same as git for a .gitignore hit.
	cmd := exec.Command("git", "check-ignore", "-v", "a.log", "sub/x.tmp")
	cmd.Dir = ref
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	want, _ := cmd.Output()
	t.Chdir(m)
	got, _, err := runOutErr(t, "check-ignore", "--repo", root, "-v", "a.log", "sub/x.tmp")
	if err != nil || got != string(want) {
		t.Errorf("-v: git %q, cairn %q (%v)", want, got, err)
	}
	// .cairnignore is honoured with its own source named.
	got, _, _ = runOutErr(t, "check-ignore", "--repo", root, "-v", "sub/secret.txt")
	if !strings.HasPrefix(got, "sub/.cairnignore:1:secret.txt\t") {
		t.Errorf("cairnignore source: %q", got)
	}
	// And it agrees with what a commit actually does: ignored files never land.
	refsCommitAll(t, root)
	arch := tarEntries(t, []byte(refsOut(t, "archive", "--repo", root, "main")))
	for name := range arch {
		if strings.HasSuffix(name, "a.log") || strings.HasSuffix(name, "x.tmp") || strings.HasSuffix(name, "secret.txt") {
			t.Errorf("an ignored file was committed: %s", name)
		}
	}
	if _, ok := arch["sub/code.go"]; !ok {
		t.Errorf("a non-ignored file is missing from the commit: %v", arch)
	}
}

func refsCommitAll(t *testing.T, root string) {
	t.Helper()
	mustRun(t, "commit", "--repo", root, "-m", "all", "main")
}

func TestRefsHelp(t *testing.T) {
	out := refsOut(t, "help", "branch")
	for _, w := range []string{"branch -d|-D", "branch -m|-M", "flags:", "-show-current"} {
		if !strings.Contains(out, w) {
			t.Fatalf("help branch lacks %q:\n%s", w, out)
		}
	}
	// <verb> -h prints the same section and exits cleanly.
	old := os.Stdout
	rp, wp, _ := os.Pipe()
	os.Stdout = wp
	err := run([]string{"tag", "--help"})
	_ = wp.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, rp)
	if !errors.Is(err, flag.ErrHelp) || !strings.Contains(buf.String(), "tag -d <name>") {
		t.Fatalf("tag --help: %v\n%s", err, buf.String())
	}
	if err := run([]string{"help", "no-such-verb"}); err == nil {
		t.Fatal("help for an unknown verb succeeded")
	}
	if out := refsOut(t, "help"); !strings.Contains(out, "subcommands:") {
		t.Fatalf("bare help:\n%s", out)
	}
	if out := refsOut(t, "help", "pr"); !strings.Contains(out, "cairn pr") {
		t.Fatalf("help pr:\n%s", out)
	}
}

func refsGitStdin(t *testing.T, in string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Stdin = strings.NewReader(in)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}
