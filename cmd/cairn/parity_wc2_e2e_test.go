//go:build !windows

package main

import (
	"bufio"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// ── stash additions ──────────────────────────────────────────────────────────

// TestStashPushPaths: `stash push -- <paths>` shelves only those edits; the
// rest of the working change stays in the folder.
func TestStashPushPaths(t *testing.T) {
	w := newWC(t)
	w.write("main", "a.txt", "1\n")
	w.write("main", "b.txt", "1\n")
	w.write("main", "gone.txt", "1\n")
	w.ok("main", "commit", "-m", "base")
	w.write("main", "a.txt", "2\n")
	w.write("main", "b.txt", "2\n")
	w.write("main", "new.txt", "n\n")
	w.remove("main", "gone.txt")

	w.ok("main", "stash", "push", "-m", "some of it", "--", "a.txt", "new.txt", "gone.txt")
	if got := w.porcelain("main"); got != "M  b.txt\n" {
		t.Errorf("left in the working change = %q, want only b.txt", got)
	}
	if w.read("main", "a.txt") != "1\n" || w.exists("main", "new.txt") || !w.exists("main", "gone.txt") {
		t.Error("the stashed paths were not reset in the folder")
	}
	if got := w.ok("main", "stash", "show"); got != "M\ta.txt\nD\tgone.txt\nA\tnew.txt" {
		t.Errorf("stash show = %q", got)
	}
	mustContain(t, w.ok("main", "stash", "show", "-p", "stash@{0}"), "-1\n+2")

	// Pop merges back over the edits that stayed.
	w.ok("main", "stash", "pop")
	if got := w.porcelain("main"); got != "M  a.txt\nM  b.txt\nD  gone.txt\nA  new.txt\n" {
		t.Errorf("after pop = %q, want the whole working change back", got)
	}
	_, errOut, _ := w.run("main", "stash", "list")
	mustContain(t, errOut, "no stashes")
	mustContain(t, w.fails("main", "stash", "push", "--", "nope.txt"), "did not match")
}

// TestStashApplySaveClearAndGitIDs: apply keeps the entry, save is push -m,
// stash@{N} addresses the stack git's way, clear empties it.
func TestStashApplySaveClearAndGitIDs(t *testing.T) {
	w := newWC(t)
	w.write("main", "a.txt", "1\n")
	w.ok("main", "commit", "-m", "base")

	w.write("main", "first.txt", "1\n")
	w.ok("main", "stash", "save", "the", "first", "one")
	w.write("main", "second.txt", "2\n")
	w.ok("main", "stash", "-m", "the second one")
	list := w.ok("main", "stash", "list")
	mustContain(t, list, "the first one", "the second one")
	if !strings.Contains(strings.Split(list, "\n")[0], "the second one") {
		t.Errorf("stash list is not newest-first:\n%s", list)
	}

	// stash@{1} is the OLDER entry.
	w.ok("main", "stash", "apply", "stash@{1}")
	if !w.exists("main", "first.txt") || w.exists("main", "second.txt") {
		t.Error("stash apply stash@{1} did not restore the older entry")
	}
	if n := len(strings.Split(w.ok("main", "stash", "list"), "\n")); n != 2 {
		t.Errorf("stash apply dropped an entry: %d left", n)
	}
	// Applying over un-sealed work merges rather than refusing.
	w.ok("main", "stash", "apply")
	if got := w.porcelain("main"); got != "A  first.txt\nA  second.txt\n" {
		t.Errorf("after applying both = %q", got)
	}
	mustContain(t, w.fails("main", "stash", "pop", "stash@{7}"), "not a valid stash reference")
	w.ok("main", "stash", "drop", "stash@{0}")
	if list := w.ok("main", "stash", "list"); strings.Contains(list, "the second one") || !strings.Contains(list, "the first one") {
		t.Errorf("drop stash@{0} removed the wrong entry:\n%s", list)
	}
	w.ok("main", "stash", "-m", "another")
	_, errOut, err := w.run("main", "stash", "clear")
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, errOut, "discarded 2 stash entries")
	if out := w.ok("main", "stash", "list"); out != "" {
		t.Errorf("stash list after clear = %q", out)
	}
}

// TestStashPopConflictKeepsTheStash: a stash that no longer applies cleanly
// leaves the conflict on the working change (exit 2) and stays on the stack.
func TestStashPopConflictKeepsTheStash(t *testing.T) {
	w := newWC(t)
	w.write("main", "f.txt", "1\n2\n3\n")
	w.ok("main", "commit", "-m", "base")
	w.write("main", "f.txt", "1\nstashed\n3\n")
	w.ok("main", "stash", "-m", "wip")
	w.write("main", "f.txt", "1\nsealed since\n3\n")
	w.ok("main", "commit", "-m", "moved on")

	errOut := w.conflicts("main", "stash", "pop")
	mustContain(t, errOut, "f.txt", "the stash is kept")
	if got := w.porcelain("main"); got != "UU f.txt\n" {
		t.Errorf("status = %q, want the conflict", got)
	}
	mustContain(t, w.ok("main", "stash", "list"), "wip")
}

// TestStashPopOntoMovedLine: popping onto a line that has sealed OTHER work
// since must not undo that work (the stash is merged, not written over).
func TestStashPopOntoMovedLine(t *testing.T) {
	w := newWC(t)
	w.write("main", "a.txt", "1\n")
	w.ok("main", "commit", "-m", "base")
	w.write("main", "wip.txt", "wip\n")
	w.ok("main", "stash")
	w.write("main", "a.txt", "2\n")
	w.ok("main", "commit", "-m", "sealed since")
	w.ok("main", "stash", "pop")
	if got := w.porcelain("main"); got != "A  wip.txt\n" {
		t.Errorf("after pop = %q: the pop reverted work sealed since the stash", got)
	}
	if got := w.read("main", "a.txt"); got != "2\n" {
		t.Errorf("a.txt = %q, want the sealed content kept", got)
	}
}

// TestStashBranch: a new line is expressed off the stash's line and the stash
// popped onto it.
func TestStashBranch(t *testing.T) {
	w := newWC(t)
	w.write("main", "a.txt", "1\n")
	w.ok("main", "commit", "-m", "base")
	w.write("main", "idea.txt", "idea\n")
	w.ok("main", "stash", "-m", "an idea")
	w.ok("main", "stash", "branch", "idea-line")
	if got := w.porcelain("idea-line"); got != "A  idea.txt\n" {
		t.Errorf("working change on the new line = %q", got)
	}
	if got := w.porcelain("main"); got != "" {
		t.Errorf("main after stash branch = %q, want clean", got)
	}
	if out := w.ok("main", "stash", "list"); out != "" {
		t.Errorf("the stash was not dropped: %q", out)
	}
	mustContain(t, w.fails("main", "stash", "branch", "main"), "already exists")
}

// ── apply / am ───────────────────────────────────────────────────────────────

// gitFixture builds the same base tree in a git repo and a cairn line, then a
// further set of edits in git only — the patch source.
func gitFixture(t *testing.T) (*wc, string) {
	t.Helper()
	w := newWC(t)
	g := newGit(t)
	b := both{t, w, g, "main"}
	b.write("code.go", "package x\n\nfunc A() {}\n\nfunc B() {}\n\nfunc C() {}\n")
	b.write("docs/readme.md", "# title\n\nbody\n")
	b.write("old.txt", "going away\n")
	b.write("name before.txt", "same content\nsecond line\nthird line\n")
	b.write("noeol.txt", "no newline at end")
	w.ok("main", "commit", "-m", "base")
	gitIn(t, g, "add", "-A")
	gitIn(t, g, "commit", "-qm", "base")
	return w, g
}

func gitEdits(t *testing.T, g string) {
	t.Helper()
	writeAt(t, g, "code.go", "package x\n\nfunc A() { changed() }\n\nfunc B() {}\n\nfunc C() {}\n\nfunc D() {}\n")
	writeAt(t, g, "docs/new/deep.md", "new file\n")
	writeAt(t, g, "noeol.txt", "no newline at end\nnow two lines")
	writeAt(t, g, "run.sh", "#!/bin/sh\necho hi\n")
	if err := os.Chmod(filepath.Join(g, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, g, "rm", "-q", "old.txt")
	gitIn(t, g, "mv", "name before.txt", "name after.txt")
	gitIn(t, g, "add", "-A")
}

// TestApplyMatchesGit: a real `git diff` — modified, added (nested, and an
// executable), deleted and renamed files, a file with no trailing newline —
// applied by `cairn apply` leaves the line's folder identical to git's
// worktree; -R takes it back; --check and a failing patch change nothing; and
// undo reverts the apply.
func TestApplyMatchesGit(t *testing.T) {
	w, g := gitFixture(t)
	baseTree := treeOf(t, g)
	gitEdits(t, g)
	patch := filepath.Join(t.TempDir(), "change.diff")
	if err := os.WriteFile(patch, []byte(gitIn(t, g, "diff", "--cached", "-M")), 0o644); err != nil {
		t.Fatal(err)
	}
	want := treeOf(t, g)

	// --stat describes it without applying; --check verifies without applying.
	stat := w.ok("main", "apply", "--stat", patch)
	mustContain(t, stat, "code.go", "6 files changed")
	w.ok("main", "apply", "--check", patch)
	if got := treeOf(t, w.dir("main")); got != baseTree {
		t.Fatal("apply --stat/--check changed the folder")
	}

	w.ok("main", "apply", patch)
	if got := treeOf(t, w.dir("main")); got != want {
		t.Errorf("after apply:\ncairn %q\ngit   %q", got, want)
	}
	// It landed in the working change, un-sealed.
	if got, wantSt := w.porcelain("main"), gitIn(t, g, "-c", "diff.renames=false", "status", "--porcelain", "--no-renames"); got != wantSt {
		t.Errorf("status after apply:\ncairn:\n%s\ngit:\n%s", got, wantSt)
	}

	// Applying it again cannot work, and must leave everything as it is.
	mustContain(t, w.fails("main", "apply", patch), "apply:")
	mustContain(t, w.fails("main", "apply", "--check", patch), "apply:")
	if got := treeOf(t, w.dir("main")); got != want {
		t.Error("a failed apply changed the folder")
	}

	// -R reverses it.
	w.ok("main", "apply", "-R", patch)
	if got := treeOf(t, w.dir("main")); got != baseTree {
		t.Errorf("after apply -R:\ncairn %q\nbase  %q", got, baseTree)
	}
	if got := w.porcelain("main"); got != "" {
		t.Errorf("status after apply -R = %q, want clean", got)
	}

	// From stdin; then undo.
	data, _ := os.ReadFile(patch)
	oldStdin := stdinReader
	stdinReader = bufio.NewReader(strings.NewReader(string(data)))
	w.ok("main", "apply", "-")
	stdinReader = oldStdin
	if got := treeOf(t, w.dir("main")); got != want {
		t.Error("apply from stdin did not apply")
	}
	mustRun(t, "undo", "--repo", w.root)
	if got := treeOf(t, w.dir("main")); got != baseTree {
		t.Error("undo did not revert the apply")
	}
}

// TestApplyStripLevel: -p0 and -p2 strip path components as git does.
func TestApplyStripLevel(t *testing.T) {
	w := newWC(t)
	w.write("main", "f.txt", "a\n")
	w.ok("main", "commit", "-m", "base")
	dir := t.TempDir()
	p0 := filepath.Join(dir, "p0.diff")
	p2 := filepath.Join(dir, "p2.diff")
	os.WriteFile(p0, []byte("--- f.txt\n+++ f.txt\n@@ -1 +1 @@\n-a\n+b\n"), 0o644)
	os.WriteFile(p2, []byte("--- old/tree/f.txt\n+++ new/tree/f.txt\n@@ -1 +1 @@\n-b\n+c\n"), 0o644)
	w.ok("main", "apply", "-p0", p0)
	w.ok("main", "apply", "-p", "2", p2)
	if got := w.read("main", "f.txt"); got != "c\n" {
		t.Errorf("f.txt = %q after -p0 then -p2 patches", got)
	}
}

// TestAmMatchesGit: `git format-patch` output applied with `cairn am` yields
// commits with the same author, author date, message and tree as git's.
func TestAmMatchesGit(t *testing.T) {
	w, g := gitFixture(t)
	gitEdits(t, g)
	if out, err := gitTry(g, "-c", "user.name=x", "commit", "-q", "--author=Pat Cher <pat@example.org>", "--date=2022-02-03T04:05:06+05:00",
		"-m", "rework the code\n\nA body paragraph.\n\nAnother, with a trailing detail."); err != nil {
		t.Fatalf("git commit: %v %s", err, out)
	}
	writeAt(t, g, "docs/readme.md", "# title\n\nbody, extended\n")
	gitIn(t, g, "commit", "-qam", "docs: extend the body")
	mbox := filepath.Join(t.TempDir(), "series.mbox")
	if err := os.WriteFile(mbox, []byte(gitIn(t, g, "format-patch", "--stdout", "-M", "-2")), 0o644); err != nil {
		t.Fatal(err)
	}

	w.write("main", "wip.txt", "un-sealed work rides along\n")
	out := w.ok("main", "am", mbox)
	if out != "Applying: rework the code\nApplying: docs: extend the body" {
		t.Errorf("am output = %q", out)
	}
	if got := w.porcelain("main"); got != "A  wip.txt\n" {
		t.Errorf("working change after am = %q, want the un-sealed file only", got)
	}
	w.remove("main", "wip.txt")
	if got, want := treeOf(t, w.dir("main")), treeOf(t, g); got != want {
		t.Errorf("tree after am:\ncairn %q\ngit   %q", got, want)
	}
	const format = "--format=%an|%ae|%aI|%s|%b"
	changeID := regexp.MustCompile(`Change-Id: \S+`)
	strip := func(s string) string {
		return strings.Join(strings.Fields(changeID.ReplaceAllString(s, "")), " ")
	}
	head := w.head("main")
	for i, rev := range []string{"HEAD", "HEAD~1"} {
		cairnRev := head + strings.Repeat("~1", i)
		got := strip(w.store("log", "-1", format, cairnRev))
		want := strip(gitIn(t, g, "log", "-1", format, rev))
		if got != want {
			t.Errorf("commit %s:\ncairn %q\ngit   %q", rev, got, want)
		}
		if gt, wt := w.store("rev-parse", cairnRev+"^{tree}"), gitIn(t, g, "rev-parse", rev+"^{tree}"); gt != wt {
			t.Errorf("commit %s tree: cairn %s, git %s", rev, gt, wt)
		}
	}

	// A series that no longer applies stops, with a clear message.
	mustContain(t, w.fails("main", "am", mbox), "patch 1/2", "rework the code", "failed")
}

// ── cherry-pick additions ────────────────────────────────────────────────────

func pickFixture(t *testing.T) (w *wc, c1, c2, c3 string) {
	t.Helper()
	w = newWCWithFeature(t)
	w.write("feat", "one.txt", "1\n")
	c1 = w.ok("feat", "commit", "-m", "one")
	w.write("feat", "two.txt", "2\n")
	c2 = w.ok("feat", "commit", "-m", "two")
	w.write("feat", "three.txt", "3\n")
	c3 = w.ok("feat", "commit", "-m", "three")
	return w, c1, c2, c3
}

// TestCherryPickSeveralAndX: several commits are picked in order; -x records
// where each came from, in git's words.
func TestCherryPickSeveralAndX(t *testing.T) {
	w, c1, _, c3 := pickFixture(t)
	w.ok("main", "cherry-pick", "-x", c1, c3)
	if got, want := w.subjects("main"), []string{"three", "one", "base"}; !reflect.DeepEqual(got, want) {
		t.Errorf("history = %v, want %v", got, want)
	}
	if !w.exists("main", "one.txt") || w.exists("main", "two.txt") || !w.exists("main", "three.txt") {
		t.Error("the picked files are not what was picked")
	}
	msg := w.store("log", "-1", "--format=%B", w.head("main"))
	mustContain(t, msg, "three\n\n(cherry picked from commit "+c3+")")
}

// TestCherryPickRange: A..B picks what is after A up to B, oldest first; the
// trailing-branch positional of the original syntax still selects the line.
func TestCherryPickRange(t *testing.T) {
	w, c1, _, c3 := pickFixture(t)
	mustRun(t, "cherry-pick", "--repo", w.root, c1+".."+c3, "main")
	if got, want := w.subjects("main"), []string{"three", "two", "base"}; !reflect.DeepEqual(got, want) {
		t.Errorf("history = %v, want %v", got, want)
	}
	if w.exists("main", "one.txt") {
		t.Error("A..B picked A itself")
	}
	err := run(w.args("main", "cherry-pick", c3+".."+c1))
	if err == nil || !strings.Contains(err.Error(), "not an ancestor") {
		t.Errorf("a backwards range = %v, want an error", err)
	}
}

// TestCherryPickNoCommit: -n leaves the pick in the working change, and the
// commit that seals it takes the picked message.
func TestCherryPickNoCommit(t *testing.T) {
	w, _, c2, _ := pickFixture(t)
	w.ok("main", "cherry-pick", "-n", c2)
	if got := w.porcelain("main"); got != "A  two.txt\n" {
		t.Errorf("status after cherry-pick -n = %q", got)
	}
	if got := w.subjects("main")[0]; got != "base" {
		t.Errorf("cherry-pick -n sealed a commit: %q", got)
	}
	sha := w.ok("main", "cherry-pick", "--continue")
	if got := strings.TrimSpace(w.store("log", "-1", "--format=%s", sha)); got != "two" {
		t.Errorf("subject of the sealed pick = %q, want the picked message", got)
	}
}

// TestCherryPickAbort: --abort rewinds a conflicted pick, even after the
// folder was edited (and snapshotted) trying to resolve it — and refuses when
// there is no pick to abort.
func TestCherryPickAbort(t *testing.T) {
	w := newWCWithFeature(t)
	w.write("feat", "base.txt", "feat's version\n")
	pick := w.ok("feat", "commit", "-m", "feat edits base")
	w.write("main", "base.txt", "main's version\n")
	w.ok("main", "commit", "-m", "main edits base")
	mustContain(t, w.fails("main", "cherry-pick", "--abort"), "not a cherry-pick or revert")

	w.conflicts("main", "cherry-pick", pick)
	w.write("main", "base.txt", "half resolved\n")
	_ = w.porcelain("main") // a command in between snapshots the attempt
	mustContain(t, w.fails("main", "cherry-pick", "--skip"), "no sequencer state")

	_, _, err := w.run("main", "cherry-pick", "--abort")
	if err != nil {
		t.Fatalf("cherry-pick --abort: %v", err)
	}
	if got := w.read("main", "base.txt"); got != "main's version\n" {
		t.Errorf("base.txt after --abort = %q", got)
	}
	if got := w.porcelain("main"); got != "" {
		t.Errorf("status after --abort = %q, want clean and conflict-free", got)
	}
	if got := w.subjects("main")[0]; got != "main edits base" {
		t.Errorf("top commit after --abort = %q", got)
	}
	w.write("main", "next.txt", "n\n")
	w.ok("main", "commit", "-m", "life goes on")
}

// ── undo ─────────────────────────────────────────────────────────────────────

// TestUndoNeverBuriesAWorkingCommit: undoing a commit that followed another
// working-copy verb lands the line on a former working commit (the state as of
// that verb). That commit must become the open working change again — its
// edits back, un-sealed — not a "(working)" entry in the line's sealed history
// with a fresh working commit stacked on top.
func TestUndoNeverBuriesAWorkingCommit(t *testing.T) {
	w := newWC(t)
	w.write("main", "a.txt", "1\n")
	w.ok("main", "commit", "-m", "base")
	w.write("main", "a.txt", "2\n")
	w.write("main", "scrap.txt", "s\n")
	w.ok("main", "restore", "scrap.txt") // a verb between the edits and the commit
	w.ok("main", "commit", "-m", "second")

	mustRun(t, "undo", "--repo", w.root)
	if got := w.porcelain("main"); got != "M  a.txt\n" {
		t.Errorf("after undoing the commit: %q, want its content back as un-sealed work", got)
	}
	if got := w.read("main", "a.txt"); got != "2\n" {
		t.Errorf("a.txt = %q after undo", got)
	}
	w.write("main", "b.txt", "b\n")
	w.ok("main", "commit", "-m", "second, again")
	if got, want := w.subjects("main"), []string{"second, again", "base"}; !reflect.DeepEqual(got, want) {
		t.Errorf("history = %v, want %v", got, want)
	}
	if log := w.ok("main", "log", "-n", "50"); strings.Contains(strings.Join(strings.Split(log, "\n")[1:], "\n"), "(working)") {
		t.Errorf("a working placeholder was sealed into history:\n%s", log)
	}
	// The same after a stash, which is how this was first reachable.
	w.write("main", "c.txt", "c\n")
	w.ok("main", "stash")
	w.write("main", "d.txt", "d\n")
	w.ok("main", "commit", "-m", "third")
	mustRun(t, "undo", "--repo", w.root)
	w.write("main", "e.txt", "e\n")
	w.ok("main", "commit", "-m", "third, again")
	if log := w.ok("main", "log", "-n", "50"); strings.Contains(strings.Join(strings.Split(log, "\n")[1:], "\n"), "(working)") {
		t.Errorf("a working placeholder was sealed into history after stash+commit+undo:\n%s", log)
	}
}

// TestEveryVerbIsOneUndoStep walks the mutating verbs and checks each is
// recorded in the oplog under its own name and reverted by a single undo.
func TestEveryVerbIsOneUndoStep(t *testing.T) {
	w := newWCWithFeature(t)
	w.write("feat", "f.txt", "1\n2\n3\n")
	w.write("feat", "g.txt", "g\n")
	w.ok("feat", "commit", "-m", "first")
	w.write("feat", "f.txt", "1\n2\n3\n4\n")
	w.ok("feat", "commit", "-m", "second")
	patch := filepath.Join(t.TempDir(), "p.diff")
	os.WriteFile(patch, []byte("--- a/g.txt\n+++ b/g.txt\n@@ -1 +1 @@\n-g\n+patched\n"), 0o644)

	lastOp := func() string {
		lines := strings.Split(strings.TrimSpace(mustRunOut(t, "oplog", "--repo", w.root)), "\n")
		return strings.Fields(lines[len(lines)-1])[1]
	}
	steps := []struct {
		op   string
		prep func()
		args []string
	}{
		{"restore", func() { w.write("feat", "g.txt", "edited\n") }, []string{"restore", "g.txt"}},
		{"reset", func() { w.write("feat", "g.txt", "edited\n") }, []string{"reset", "--hard"}},
		{"reset", nil, []string{"reset", "--soft", "HEAD~1"}},
		{"revert", nil, []string{"revert", "HEAD"}},
		{"commit", func() { w.write("feat", "g.txt", "edited\n"); w.write("feat", "h.txt", "h\n") }, []string{"commit", "-m", "partial", "--", "g.txt"}},
		{"amend", func() { w.write("feat", "g.txt", "edited\n") }, []string{"commit", "--amend", "--no-edit"}},
		{"rm", nil, []string{"rm", "g.txt"}},
		{"mv", nil, []string{"mv", "g.txt", "moved.txt"}},
		{"apply", nil, []string{"apply", patch}},
		{"stash", func() { w.write("feat", "g.txt", "edited\n"); w.write("feat", "h.txt", "h\n") }, []string{"stash", "push", "--", "g.txt"}},
		{"stash", func() { w.write("feat", "g.txt", "edited2\n") }, []string{"stash", "push", "-m", "whole"}},
		{"stash-pop", func() { w.write("feat", "g.txt", "s1\n"); w.ok("feat", "stash", "push", "-m", "s1") }, []string{"stash", "pop"}},
		{"stash-drop", func() { w.write("feat", "g.txt", "s2\n"); w.ok("feat", "stash", "push", "-m", "s2") }, []string{"stash", "drop"}},
		{"stash-clear", func() { w.write("feat", "g.txt", "edited3\n"); w.ok("feat", "stash", "push", "-m", "third") }, []string{"stash", "clear"}},
	}
	for _, st := range steps {
		if st.prep != nil {
			st.prep()
		}
		_ = w.porcelain("feat") // settle the prep edits into the working change
		before := treeOf(t, w.dir("feat"))
		beforeSt := w.porcelain("feat")
		beforeLog := w.subjects("feat")
		beforeStash := w.ok("feat", "stash", "list")
		if _, errOut, err := w.run("feat", st.args[0], st.args[1:]...); err != nil {
			t.Fatalf("%v: %v\n%s", st.args, err, errOut)
		}
		if got := lastOp(); got != st.op {
			t.Errorf("%v recorded op %q, want %q", st.args, got, st.op)
		}
		mustRun(t, "undo", "--repo", w.root)
		if got := treeOf(t, w.dir("feat")); got != before {
			t.Errorf("undo of %v: folder differs\ngot  %q\nwant %q", st.args, got, before)
		}
		if got := w.porcelain("feat"); got != beforeSt {
			t.Errorf("undo of %v: status = %q, want %q", st.args, got, beforeSt)
		}
		if got := w.subjects("feat"); !reflect.DeepEqual(got, beforeLog) {
			t.Errorf("undo of %v: history = %v, want %v", st.args, got, beforeLog)
		}
		if got := w.ok("feat", "stash", "list"); got != beforeStash {
			t.Errorf("undo of %v: stash stack = %q, want %q", st.args, got, beforeStash)
		}
		w.ok("feat", "reset", "--hard") // start the next step clean
	}
}
