//go:build !windows

package main

import (
	"bufio"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// ── restore ──────────────────────────────────────────────────────────────────

// TestRestoreDiscardsEditsPerPath: restore puts back exactly the named paths —
// a modified file gets its sealed content, a deleted one returns, and a file
// the working change added is removed (git semantics) — and leaves every other
// edit alone. `cairn undo` reverts the restore itself.
func TestRestoreDiscardsEditsPerPath(t *testing.T) {
	w := newWC(t)
	w.write("main", "mod.txt", "sealed\n")
	w.write("main", "del.txt", "sealed\n")
	w.write("main", "dir/keep.txt", "sealed\n")
	w.ok("main", "commit", "-m", "base")

	w.write("main", "mod.txt", "edited\n")
	w.remove("main", "del.txt")
	w.write("main", "added.txt", "new\n")
	w.write("main", "dir/keep.txt", "edited too\n")

	w.ok("main", "restore", "mod.txt", "del.txt", "added.txt")
	if got := w.read("main", "mod.txt"); got != "sealed\n" {
		t.Errorf("mod.txt = %q, want the sealed content", got)
	}
	if got := w.read("main", "del.txt"); got != "sealed\n" {
		t.Errorf("del.txt = %q, want it back", got)
	}
	if w.exists("main", "added.txt") {
		t.Error("added.txt survived a restore from a source that lacks it")
	}
	if got := w.porcelain("main"); got != "M  dir/keep.txt\n" {
		t.Errorf("status after restore = %q, want only the untouched edit", got)
	}

	mustRun(t, "undo", "--repo", w.root)
	if got := w.porcelain("main"); got != "A  added.txt\nD  del.txt\nM  dir/keep.txt\nM  mod.txt\n" {
		t.Errorf("status after undo = %q, want every edit back", got)
	}
	if got := w.read("main", "mod.txt"); got != "edited\n" {
		t.Errorf("mod.txt after undo = %q", got)
	}

	// A directory pathspec restores everything under it; "." the whole line.
	w.ok("main", "restore", "dir")
	if got := w.read("main", "dir/keep.txt"); got != "sealed\n" {
		t.Errorf("dir/keep.txt = %q after restoring its directory", got)
	}
	w.ok("main", "restore", ".")
	if got := w.porcelain("main"); got != "" {
		t.Errorf("status after `restore .` = %q, want clean", got)
	}

	// A pathspec that names nothing is an error, not a silent no-op.
	mustContain(t, w.fails("main", "restore", "nope.txt"), "did not match")
	// --staged alone has nothing to do (no index) and must not fail a script.
	w.write("main", "mod.txt", "edited\n")
	_, errOut, err := w.run("main", "restore", "--staged", "mod.txt")
	if err != nil {
		t.Fatalf("restore --staged: %v", err)
	}
	mustContain(t, errOut, "no staging area")
	if got := w.read("main", "mod.txt"); got != "edited\n" {
		t.Errorf("restore --staged touched the file: %q", got)
	}
}

// TestRestoreFromSourceAndGitCheckoutSpelling: --source restores from another
// commit, and git's `checkout [<rev>] -- <path>` is the same verb.
func TestRestoreFromSourceAndGitCheckoutSpelling(t *testing.T) {
	w := newWC(t)
	w.write("main", "f.txt", "v1\n")
	v1 := w.ok("main", "commit", "-m", "v1")
	w.write("main", "f.txt", "v2\n")
	w.write("main", "later.txt", "later\n")
	w.ok("main", "commit", "-m", "v2")

	w.ok("main", "restore", "--source="+v1, "f.txt", "later.txt")
	if got := w.read("main", "f.txt"); got != "v1\n" {
		t.Errorf("f.txt = %q, want v1 from --source", got)
	}
	if w.exists("main", "later.txt") {
		t.Error("later.txt should be removed: the source commit does not have it")
	}
	if got := w.porcelain("main"); got != "M  f.txt\nD  later.txt\n" {
		t.Errorf("status = %q", got)
	}

	// checkout -- <path>: back to the sealed tip.
	mustRun(t, w.args("main", "checkout", "--", "f.txt", "later.txt")...)
	if got := w.porcelain("main"); got != "" {
		t.Errorf("status after checkout -- = %q, want clean", got)
	}
	// checkout <rev> -- <path>: HEAD~1 is v1.
	mustRun(t, w.args("main", "checkout", "HEAD~1", "--", "f.txt")...)
	if got := w.read("main", "f.txt"); got != "v1\n" {
		t.Errorf("f.txt = %q after checkout HEAD~1 -- f.txt", got)
	}
	// And `checkout <branch>` is still the cd correction, not a restore.
	err := run(w.args("main", "checkout", "release"))
	if err == nil || !strings.Contains(err.Error(), "cairn express release") {
		t.Errorf("checkout <branch> = %v, want the lines-are-folders correction", err)
	}
}

// TestPathspecsAreRelativeToWhereYouStand: typed from a subfolder of the line,
// a pathspec is relative to that subfolder, as in git.
func TestPathspecsAreRelativeToWhereYouStand(t *testing.T) {
	w := newWC(t)
	w.write("main", "top.txt", "sealed\n")
	w.write("main", "sub/in.txt", "sealed\n")
	w.ok("main", "commit", "-m", "base")
	w.write("main", "top.txt", "edited\n")
	w.write("main", "sub/in.txt", "edited\n")

	t.Chdir(filepath.Join(w.dir("main"), "sub"))
	mustRun(t, "restore", ".")
	if got := w.read("main", "sub/in.txt"); got != "sealed\n" {
		t.Errorf("sub/in.txt = %q, want restored", got)
	}
	if got := w.read("main", "top.txt"); got != "edited\n" {
		t.Errorf("top.txt = %q: `restore .` in sub/ reached outside it", got)
	}
	mustRun(t, "restore", "../top.txt")
	if got := w.read("main", "top.txt"); got != "sealed\n" {
		t.Errorf("top.txt = %q after restore ../top.txt", got)
	}
	if err := run([]string{"restore", "../../elsewhere"}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Errorf("a pathspec outside the line's folder = %v, want an error", err)
	}
}

// ── reset ────────────────────────────────────────────────────────────────────

// TestResetHardDiscardsTheWorkingChange: plain `reset --hard` works on any
// line (the root included), is undoable, and the index-only forms are no-ops.
func TestResetHardDiscardsTheWorkingChange(t *testing.T) {
	w := newWC(t)
	w.write("main", "a.txt", "sealed\n")
	w.ok("main", "commit", "-m", "base")
	w.write("main", "a.txt", "edited\n")
	w.write("main", "new.txt", "new\n")

	for _, form := range [][]string{{}, {"a.txt"}, {"--", "a.txt"}, {"HEAD", "--", "a.txt"}, {"--mixed"}} {
		_, errOut, err := w.run("main", "reset", form...)
		if err != nil {
			t.Fatalf("reset %v: %v", form, err)
		}
		mustContain(t, errOut, "no staging area")
	}
	if got := w.porcelain("main"); got != "M  a.txt\nA  new.txt\n" {
		t.Fatalf("an index-only reset changed the working change: %q", got)
	}

	w.ok("main", "reset", "--hard")
	if got := w.porcelain("main"); got != "" {
		t.Errorf("status after reset --hard = %q, want clean", got)
	}
	if w.exists("main", "new.txt") || w.read("main", "a.txt") != "sealed\n" {
		t.Error("reset --hard left working-change content in the folder")
	}
	mustRun(t, "undo", "--repo", w.root)
	if got := w.porcelain("main"); got != "M  a.txt\nA  new.txt\n" {
		t.Errorf("status after undo = %q, want the discarded work back", got)
	}
}

// TestResetToRevMatchesGit runs the same history in git and in a cairn feature
// line and compares the result of --soft and --hard resets: git's status after
// a soft reset shows the un-sealed commits as staged changes, which is exactly
// cairn's working change in the X column.
func TestResetToRevMatchesGit(t *testing.T) {
	w := newWCWithFeature(t)
	g := newGit(t)
	writeAt(t, g, "base.txt", "base\n")
	gitIn(t, g, "add", "-A")
	gitIn(t, g, "commit", "-qm", "base")
	b := both{t, w, g, "feat"}
	commit := func(msg string) {
		w.ok("feat", "commit", "-m", msg)
		gitIn(t, g, "add", "-A")
		gitIn(t, g, "commit", "-qm", msg)
	}
	b.write("one.txt", "1\n")
	commit("one")
	b.write("two.txt", "2\n")
	b.write("one.txt", "1 changed\n")
	commit("two")
	b.remove("base.txt")
	b.write("three.txt", "3\n")
	commit("three")
	b.write("unsealed.txt", "u\n") // un-sealed work rides through a soft reset

	_, errOut, err := w.run("feat", "reset", "--soft", "HEAD~2")
	if err != nil {
		t.Fatalf("reset --soft: %v", err)
	}
	mustContain(t, errOut, "2 commit(s) un-sealed")
	gitIn(t, g, "reset", "-q", "--soft", "HEAD~2")
	gitIn(t, g, "add", "-A")
	if got, want := w.porcelain("feat"), gitIn(t, g, "status", "--porcelain"); got != want {
		t.Errorf("after reset --soft HEAD~2:\ncairn:\n%s\ngit:\n%s", got, want)
	}
	if got, want := treeOf(t, w.dir("feat")), treeOf(t, g); got != want {
		t.Errorf("a soft reset changed the folder:\ncairn %q\ngit   %q", got, want)
	}
	if got, want := w.subjects("feat"), []string{"one", "base"}; !reflect.DeepEqual(got, want) {
		t.Errorf("sealed history after soft reset = %v, want %v", got, want)
	}

	// Undo the reset: the three commits are sealed again.
	mustRun(t, "undo", "--repo", w.root)
	gitIn(t, g, "reset", "-q", "--soft", "ORIG_HEAD")
	if got, want := w.subjects("feat"), []string{"three", "two", "one", "base"}; !reflect.DeepEqual(got, want) {
		t.Errorf("sealed history after undo = %v, want %v", got, want)
	}
	if got := w.porcelain("feat"); got != "A  unsealed.txt\n" {
		t.Errorf("working change after undo = %q", got)
	}

	// --hard rewrites the folder and drops the un-sealed work too.
	w.ok("feat", "reset", "--hard", "HEAD~1")
	gitIn(t, g, "reset", "-q", "--hard", "HEAD~1")
	gitIn(t, g, "clean", "-fdq") // git leaves the untracked file; cairn tracked it
	if got, want := treeOf(t, w.dir("feat")), treeOf(t, g); got != want {
		t.Errorf("after reset --hard HEAD~1:\ncairn %q\ngit   %q", got, want)
	}
	if got := w.porcelain("feat"); got != "" {
		t.Errorf("status after reset --hard = %q, want clean", got)
	}
}

// TestResetSafetyRules: moving a tip is a history edit, held to the rule
// reword/drop use (a child line based on a dropped commit blocks it) — and only
// ever moves a line back along its own history.
func TestResetSafetyRules(t *testing.T) {
	w := newWCWithFeature(t)
	w.write("feat", "f.txt", "f\n")
	w.ok("feat", "commit", "-m", "feat work")
	mustRun(t, "express", "--repo", w.root, "--from", "main", "other")
	w.write("other", "o.txt", "o\n")
	otherTip := w.ok("other", "commit", "-m", "work on a sibling line")

	// Not an ancestor of the line: refused.
	mustContain(t, w.fails("feat", "reset", "--hard", otherTip), "not an ancestor", "cherry-pick")
	// A child line based on a commit the reset would drop blocks it, by name;
	// one forked from an older commit does not.
	mustRun(t, "express", "--repo", w.root, "--from", "feat", "grandchild")
	mustContain(t, w.fails("feat", "reset", "--hard", "HEAD~1"), `child line "grandchild"`)
	w.write("feat", "f2.txt", "f2\n")
	w.ok("feat", "commit", "-m", "feat, after the grandchild forked")
	w.ok("feat", "reset", "--hard", "HEAD~1")
	if w.exists("feat", "f2.txt") || !w.exists("feat", "f.txt") {
		t.Error("reset above the grandchild's base did not drop exactly the newer commit")
	}

	// The root line: its un-pushed tip may go (no line is based on it)...
	w.write("main", "m.txt", "m\n")
	w.ok("main", "commit", "-m", "main moves on")
	w.ok("main", "reset", "--hard", "HEAD~1")
	if w.exists("main", "m.txt") {
		t.Error("reset --hard HEAD~1 on the un-pushed root line did not drop the commit")
	}
	// ...and resetting to the current tip rewrites nothing, so it always works.
	w.write("main", "junk.txt", "junk\n")
	w.ok("main", "reset", "--hard", "HEAD")
	if w.exists("main", "junk.txt") {
		t.Error("reset --hard HEAD did not discard the working change")
	}
	mustContain(t, w.fails("feat", "reset", "--soft", "--hard", "HEAD~1"), "mutually exclusive")
	mustContain(t, w.fails("feat", "reset", "--hard", "no-such-rev"), "unknown revision")
}

// TestResetOfPushedLineWarnsForce: dropping commits the remote already has
// means the next push cannot fast-forward — say so, like git does by refusing
// the push.
func TestResetOfPushedLineWarnsForce(t *testing.T) {
	needGit(t)
	w := newWCWithFeature(t)
	bare := filepath.Join(t.TempDir(), "origin.git")
	gitIn(t, "", "init", "-q", "--bare", bare)
	mustRun(t, "remote", "add", "origin", bare, "--repo", w.root)
	w.write("feat", "f.txt", "1\n")
	w.ok("feat", "commit", "-m", "one")
	w.write("feat", "f.txt", "2\n")
	w.ok("feat", "commit", "-m", "two")
	mustRun(t, "push", "--repo", w.root, "origin", "feat")

	_, errOut, err := w.run("feat", "reset", "--hard", "HEAD~1")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	mustContain(t, errOut, "origin still has", "--force")

	// Amending a pushed commit needs the same warning.
	mustRun(t, "undo", "--repo", w.root)
	w.write("feat", "f.txt", "2 amended\n")
	_, errOut, err = w.run("feat", "commit", "--amend", "--no-edit")
	if err != nil {
		t.Fatalf("commit --amend: %v", err)
	}
	mustContain(t, errOut, "already on origin", "--force")
}

// ── revert ───────────────────────────────────────────────────────────────────

var shaRe = regexp.MustCompile(`[0-9a-f]{40}`)

// TestRevertMatchesGit: the revert commit has git's message (shas aside) and
// git's tree, and undo takes it back out.
func TestRevertMatchesGit(t *testing.T) {
	w := newWC(t)
	g := newGit(t)
	b := both{t, w, g, "main"}
	commit := func(msg string) string {
		gitIn(t, g, "add", "-A")
		gitIn(t, g, "commit", "-qm", msg)
		return w.ok("main", "commit", "-m", msg)
	}
	b.write("f.txt", "1\n2\n3\n4\n5\n6\n7\n8\n9\n")
	b.write("gone.txt", "x\n")
	commit("base")
	b.write("f.txt", "1\nTWO\n3\n4\n5\n6\n7\n8\n9\n")
	b.remove("gone.txt")
	b.write("born.txt", "y\n")
	target := commit(`rework "two", drop gone`)
	b.write("f.txt", "1\nTWO\n3\n4\n5\n6\n7\n8\nNINE\n")
	commit("later, elsewhere in the file")

	sealed := w.ok("main", "revert", target)
	gitIn(t, g, "revert", "--no-edit", "HEAD~1")

	if got, want := treeOf(t, w.dir("main")), treeOf(t, g); got != want {
		t.Errorf("tree after revert:\ncairn %q\ngit   %q", got, want)
	}
	gotMsg := shaRe.ReplaceAllString(strings.TrimSpace(strings.Split(w.store("log", "-1", "--format=%B", sealed), "\n\nChange-Id:")[0]), "<sha>")
	wantMsg := shaRe.ReplaceAllString(strings.TrimSpace(gitIn(t, g, "log", "-1", "--format=%B")), "<sha>")
	if gotMsg != wantMsg {
		t.Errorf("revert message:\ncairn %q\ngit   %q", gotMsg, wantMsg)
	}
	// The sha in the message is the reverted commit's.
	mustContain(t, w.store("log", "-1", "--format=%B", sealed), "This reverts commit "+target+".")
	if got := w.porcelain("main"); got != "" {
		t.Errorf("status after a clean revert = %q, want clean", got)
	}

	mustRun(t, "undo", "--repo", w.root)
	if got := w.subjects("main")[0]; got != "later, elsewhere in the file" {
		t.Errorf("top commit after undo = %q", got)
	}
	if !w.exists("main", "born.txt") || w.exists("main", "gone.txt") {
		t.Error("undo did not put the folder back to before the revert")
	}
}

// TestRevertKeepsUnsealedWork: a revert seals a commit under the operator's
// feet; their un-sealed edits must still be there, still un-sealed.
func TestRevertKeepsUnsealedWork(t *testing.T) {
	w := newWC(t)
	w.write("main", "a.txt", "a\n")
	w.ok("main", "commit", "-m", "base")
	w.write("main", "b.txt", "b\n")
	bad := w.ok("main", "commit", "-m", "add b")
	w.write("main", "wip.txt", "wip\n")
	w.write("main", "a.txt", "a edited\n")

	w.ok("main", "revert", bad)
	if w.exists("main", "b.txt") {
		t.Error("b.txt is still there after reverting the commit that added it")
	}
	if got := w.porcelain("main"); got != "M  a.txt\nA  wip.txt\n" {
		t.Errorf("working change after revert = %q, want the un-sealed edits intact", got)
	}
}

// TestRevertConflictResolveCommit: a revert that does not apply cleanly exits
// 2 with the conflict in the working change; the existing resolve flow settles
// it and a bare `cairn commit` seals it with the revert message. --abort
// rewinds instead.
func TestRevertConflictResolveCommit(t *testing.T) {
	w := newWC(t)
	w.write("main", "f.txt", "1\n2\n3\n")
	w.ok("main", "commit", "-m", "base")
	w.write("main", "f.txt", "1\nTWO\n3\n")
	target := w.ok("main", "commit", "-m", "two")
	w.write("main", "f.txt", "1\nTWO!\n3\n")
	w.ok("main", "commit", "-m", "two again")

	errOut := w.conflicts("main", "revert", target)
	mustContain(t, errOut, "f.txt", "cairn resolve main", "cairn commit")
	if !strings.Contains(w.read("main", "f.txt"), "<<<<<<<") {
		t.Fatalf("f.txt has no conflict markers:\n%s", w.read("main", "f.txt"))
	}
	if got := w.porcelain("main"); got != "UU f.txt\n" {
		t.Errorf("status during the conflict = %q, want UU f.txt", got)
	}
	if got := w.subjects("main")[0]; got != "two again" {
		t.Errorf("a conflicting revert sealed something: top = %q", got)
	}
	// Sealing with the conflict open is refused, as for any commit.
	mustContain(t, w.fails("main", "revert", "--continue"), "still open")

	// --abort: back to before the revert, conflict gone.
	_, _, err := w.run("main", "revert", "--abort")
	if err != nil {
		t.Fatalf("revert --abort: %v", err)
	}
	if got := w.read("main", "f.txt"); got != "1\nTWO!\n3\n" {
		t.Errorf("f.txt after --abort = %q", got)
	}
	if got := w.porcelain("main"); got != "" {
		t.Errorf("status after --abort = %q, want clean", got)
	}

	// Again, and this time resolve and commit — with no -m.
	w.conflicts("main", "revert", target)
	w.write("main", "f.txt", "1\n2\n3\n")
	mustRun(t, "resolve", "--repo", w.root, "main", "f.txt")
	sealed := w.ok("main", "commit")
	msg := w.store("log", "-1", "--format=%B", sealed)
	mustContain(t, msg, `Revert "two"`, "This reverts commit "+target+".")
	if got := w.porcelain("main"); got != "" {
		t.Errorf("status after sealing the resolved revert = %q", got)
	}
	// The kept message is used once.
	w.write("main", "f.txt", "1\n2\n3\n4\n")
	next := w.ok("main", "commit")
	if msg := w.store("log", "-1", "--format=%s", next); strings.Contains(msg, "Revert") {
		t.Errorf("the revert message leaked into the next commit: %q", msg)
	}
}

// TestRevertNoCommitAndMultiple: -n leaves the inverse un-sealed; several
// commits are reverted in the order given.
func TestRevertNoCommitAndMultiple(t *testing.T) {
	w := newWC(t)
	w.write("main", "a.txt", "a\n")
	w.ok("main", "commit", "-m", "base")
	w.write("main", "b.txt", "b\n")
	cb := w.ok("main", "commit", "-m", "add b")
	w.write("main", "c.txt", "c\n")
	cc := w.ok("main", "commit", "-m", "add c")

	w.ok("main", "revert", "-n", cc)
	if got := w.porcelain("main"); got != "D  c.txt\n" {
		t.Errorf("status after revert -n = %q", got)
	}
	if got := w.subjects("main")[0]; got != "add c" {
		t.Errorf("revert -n sealed a commit: top = %q", got)
	}
	w.ok("main", "reset", "--hard")

	out := w.ok("main", "revert", cc, cb)
	if n := len(strings.Fields(out)); n != 2 {
		t.Errorf("revert of two commits printed %d shas: %q", n, out)
	}
	if got, want := w.subjects("main")[:2], []string{`Revert "add b"`, `Revert "add c"`}; !reflect.DeepEqual(got, want) {
		t.Errorf("history = %v, want %v", got, want)
	}
	if w.exists("main", "b.txt") || w.exists("main", "c.txt") {
		t.Error("the reverted files are still in the folder")
	}
	mustContain(t, w.fails("main", "revert", "-m", "1", cb), "not a merge")
}

// TestRevertOfAdoptingCommitKeepsTheParentsWork: a seal on a child line that
// adopted new parent work is a merge under the hood; reverting it must take
// out only the operator's own change, not what the adoption brought in.
func TestRevertOfAdoptingCommitKeepsTheParentsWork(t *testing.T) {
	w := newWCWithFeature(t)
	w.write("main", "from-main.txt", "m\n")
	w.ok("main", "commit", "-m", "main work")
	w.write("feat", "mine.txt", "f\n")
	sealed := w.ok("feat", "commit", "-m", "feat work") // adopts main's commit
	if !w.exists("feat", "from-main.txt") {
		t.Fatal("precondition: the seal should have adopted main's work")
	}
	w.ok("feat", "revert", sealed)
	if w.exists("feat", "mine.txt") {
		t.Error("mine.txt survived the revert of the commit that added it")
	}
	if !w.exists("feat", "from-main.txt") {
		t.Error("the revert also took out the parent line's adopted work")
	}
}

// ── commit additions ─────────────────────────────────────────────────────────

// TestPartialCommitMatchesGit: `commit -m msg -- <paths>` seals only those
// paths — a modified file, a new one, a deleted one and a whole directory —
// and leaves the rest as un-sealed work, exactly as `git commit -- <paths>`
// leaves it un-committed.
func TestPartialCommitMatchesGit(t *testing.T) {
	w := newWC(t)
	g := newGit(t)
	b := both{t, w, g, "main"}
	b.write("mod.txt", "1\n")
	b.write("del.txt", "1\n")
	b.write("other.txt", "1\n")
	b.write("pkg/a.go", "a\n")
	b.write("pkg/gone.go", "g\n")
	w.ok("main", "commit", "-m", "base")
	gitIn(t, g, "add", "-A")
	gitIn(t, g, "commit", "-qm", "base")

	b.write("mod.txt", "2\n")
	b.remove("del.txt")
	b.write("new.txt", "n\n")
	b.write("pkg/a.go", "a2\n")
	b.write("pkg/sub/b.go", "b\n")
	b.remove("pkg/gone.go")
	b.write("other.txt", "2\n")     // not selected
	b.write("unrelated.txt", "u\n") // not selected

	sealed := w.ok("main", "commit", "-m", "the selected paths", "--", "mod.txt", "del.txt", "new.txt", "pkg")
	gitIn(t, g, "add", "-A")
	gitIn(t, g, "commit", "-qm", "the selected paths", "--", "mod.txt", "del.txt", "new.txt", "pkg")

	// What the commit holds.
	got := w.store("ls-tree", "-r", "--name-only", sealed)
	want := gitIn(t, g, "ls-tree", "-r", "--name-only", "HEAD")
	if got != want {
		t.Errorf("files in the partial commit:\ncairn:\n%s\ngit:\n%s", got, want)
	}
	if gotT, wantT := w.store("rev-parse", sealed+"^{tree}"), gitIn(t, g, "rev-parse", "HEAD^{tree}"); gotT != wantT {
		t.Errorf("partial commit tree = %s, git's = %s", gotT, wantT)
	}
	// What is left un-sealed.
	if got, want := w.porcelain("main"), gitIn(t, g, "status", "--porcelain"); got != want {
		t.Errorf("left un-sealed:\ncairn:\n%s\ngit:\n%s", got, want)
	}
	if got := w.read("main", "other.txt"); got != "2\n" {
		t.Errorf("an unselected edit was disturbed on disk: other.txt = %q", got)
	}

	// The same without "--": a positional that is not a line is a path.
	w.ok("main", "commit", "-m", "other", "other.txt")
	if got := w.porcelain("main"); got != "A  unrelated.txt\n" {
		t.Errorf("after the second partial commit: %q", got)
	}
	mustContain(t, w.fails("main", "commit", "-m", "x", "--", "mod.txt"), "nothing to commit")
	mustContain(t, w.fails("main", "commit", "-m", "x", "--", "no-such-file"), "did not match")
}

// TestPartialCommitUndo: undoing a partial commit puts back exactly the state
// before it — everything un-sealed again, nothing lost — and a later full
// commit is its own undo step, not a rewind of the partial one as well.
func TestPartialCommitUndo(t *testing.T) {
	w := newWC(t)
	w.write("main", "a.txt", "1\n")
	w.ok("main", "commit", "-m", "base")
	w.write("main", "a.txt", "2\n")
	w.write("main", "b.txt", "b\n")

	w.ok("main", "commit", "-m", "just a", "--", "a.txt")
	mustRun(t, "undo", "--repo", w.root)
	if got := w.porcelain("main"); got != "M  a.txt\nA  b.txt\n" {
		t.Fatalf("after undoing the partial commit: %q, want both edits un-sealed", got)
	}
	if got := w.subjects("main"); !reflect.DeepEqual(got, []string{"base"}) {
		t.Fatalf("history after undo = %v", got)
	}

	w.ok("main", "commit", "-m", "just a", "--", "a.txt")
	w.ok("main", "commit", "-m", "the rest")
	mustRun(t, "undo", "--repo", w.root)
	if got := w.subjects("main"); !reflect.DeepEqual(got, []string{"just a", "base"}) {
		t.Errorf("undoing the full commit also rewound the partial one: %v", got)
	}
	if got := w.porcelain("main"); got != "A  b.txt\n" {
		t.Errorf("after undoing the full commit: %q, want b.txt un-sealed again", got)
	}
	// And the line carries on cleanly: no "(working)" placeholder in history.
	w.ok("main", "commit", "-m", "the rest, again")
	if got := w.subjects("main"); !reflect.DeepEqual(got, []string{"the rest, again", "just a", "base"}) {
		t.Errorf("history = %v", got)
	}
}

// TestCommitAmend: the working change is folded into the previous sealed
// commit; the message is kept unless -m replaces it; the author is kept; the
// root line refuses, like reword.
func TestCommitAmend(t *testing.T) {
	w := newWCWithFeature(t)
	w.write("feat", "f.txt", "1\n")
	first := w.ok("feat", "commit", "-m", "first try", "--author=Orig Inal <orig@example.org>", "--date=2020-01-02T03:04:05+00:00")
	w.write("feat", "f.txt", "2\n")
	w.write("feat", "g.txt", "g\n")

	amended := w.ok("feat", "commit", "--amend", "--no-edit")
	if amended == first {
		t.Fatal("--amend returned the same commit")
	}
	if got := w.subjects("feat"); !reflect.DeepEqual(got, []string{"first try", "base"}) {
		t.Errorf("history after --amend = %v, want the one commit replaced", got)
	}
	if got := w.store("show", "--format=", "--name-only", amended); got != "f.txt\ng.txt\n" {
		t.Errorf("amended commit touches %q, want f.txt and g.txt", got)
	}
	if got := w.porcelain("feat"); got != "" {
		t.Errorf("working change after --amend = %q, want empty", got)
	}
	meta := strings.TrimSpace(w.store("log", "-1", "--format=%an|%ae|%aI|%cn", amended))
	if meta != "Orig Inal|orig@example.org|2020-01-02T03:04:05+00:00|Tess Ter" {
		t.Errorf("amended commit author/committer = %q, want the original author kept", meta)
	}

	// -m replaces the message (and needs no changes).
	reworded := w.ok("feat", "commit", "--amend", "-m", "second try")
	if got := w.subjects("feat")[0]; got != "second try" {
		t.Errorf("subject after --amend -m = %q", got)
	}
	if a, b := w.store("rev-parse", amended+"^{tree}"), w.store("rev-parse", reworded+"^{tree}"); a != b {
		t.Error("a message-only amend changed the tree")
	}

	mustRun(t, "undo", "--repo", w.root)
	if got := w.subjects("feat")[0]; got != "first try" {
		t.Errorf("subject after undo = %q", got)
	}

	// main's only commit is feat's base: amending it would rewrite feat's
	// foundation, so it is refused, naming feat.
	mustContain(t, w.fails("main", "commit", "--amend", "-m", "x"), `child line "feat"`)
	mustContain(t, w.fails("feat", "commit", "--amend", "-m", "x", "--", "f.txt"), "cannot be combined with paths")
}

// TestAmendAndResetOnTheRootLine: the top-5 git workflow — `commit --amend` on
// main right after a commit — works, as does `reset` back over un-pushed
// commits. Once a commit is on the remote it is published history: the root
// line is never force-pushed, so both are refused and point at revert.
func TestAmendAndResetOnTheRootLine(t *testing.T) {
	needGit(t)
	w := newWC(t)
	w.write("main", "a.txt", "1\n")
	w.ok("main", "commit", "-m", "one")
	w.write("main", "a.txt", "2\n")
	second := w.ok("main", "commit", "-m", "two, with a typo")

	w.write("main", "forgot.txt", "f\n")
	amended := w.ok("main", "commit", "--amend", "-m", "two")
	if got, want := w.subjects("main"), []string{"two", "one"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("history after --amend on main = %v, want %v", got, want)
	}
	if got := w.store("show", "--format=", "--name-only", amended); got != "a.txt\nforgot.txt\n" {
		t.Errorf("amended commit touches %q", got)
	}
	if amended == second {
		t.Error("--amend did not replace the commit")
	}
	mustRun(t, "undo", "--repo", w.root)
	if got := w.subjects("main")[0]; got != "two, with a typo" {
		t.Errorf("subject after undoing the amend = %q", got)
	}
	w.ok("main", "reset", "--hard")

	// Un-pushed commits can be reset over.
	w.ok("main", "reset", "--soft", "HEAD~1")
	if got := w.porcelain("main"); got != "M  a.txt\n" {
		t.Errorf("after reset --soft HEAD~1 on main: %q", got)
	}
	mustRun(t, "undo", "--repo", w.root)

	// Push; now "two" is published.
	bare := filepath.Join(t.TempDir(), "origin.git")
	gitIn(t, "", "init", "-q", "--bare", bare)
	mustRun(t, "remote", "add", "origin", bare, "--repo", w.root)
	mustRun(t, "push", "--repo", w.root, "origin", "main")
	w.write("main", "a.txt", "3\n")
	mustContain(t, w.fails("main", "commit", "--amend", "--no-edit"), "root line", "already on origin", "cairn revert")
	mustContain(t, w.fails("main", "reset", "--hard", "HEAD~1"), "root line", "already on origin", "cairn revert")
	if got := w.porcelain("main"); got != "M  a.txt\n" {
		t.Errorf("a refused amend/reset changed the working change: %q", got)
	}
	// A new commit on top is un-pushed again, and amendable.
	w.ok("main", "commit", "-m", "three")
	w.write("main", "a.txt", "3!\n")
	w.ok("main", "commit", "--amend", "--no-edit")
	if got, want := w.subjects("main"), []string{"three", "two, with a typo", "one"}; !reflect.DeepEqual(got, want) {
		t.Errorf("history = %v, want %v", got, want)
	}
	// And the push still fast-forwards: nothing published was rewritten.
	mustRun(t, "push", "--repo", w.root, "origin", "main")
}

// TestCommitMessageSources: repeated -m are paragraphs, -F reads a file or
// stdin, and a script that gives no message still gets the historical default.
func TestCommitMessageSources(t *testing.T) {
	w := newWC(t)
	i := 0
	edit := func() {
		i++
		w.write("main", "n.txt", strings.Repeat("x", i))
	}
	body := func(sha string) string {
		return strings.Split(w.store("log", "-1", "--format=%B", sha), "\n\nChange-Id:")[0]
	}

	edit()
	if got := body(w.ok("main", "commit", "-m", "subject", "-m", "second paragraph", "-m", "third")); got != "subject\n\nsecond paragraph\n\nthird" {
		t.Errorf("multiple -m = %q", got)
	}
	edit()
	msgFile := filepath.Join(t.TempDir(), "msg")
	if err := os.WriteFile(msgFile, []byte("from a file\n\nwith a body\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := body(w.ok("main", "commit", "-F", msgFile)); got != "from a file\n\nwith a body" {
		t.Errorf("-F file = %q", got)
	}
	edit()
	oldStdin := stdinReader
	stdinReader = bufio.NewReader(strings.NewReader("from stdin\n"))
	got := body(w.ok("main", "commit", "--file", "-"))
	stdinReader = oldStdin
	if got != "from stdin" {
		t.Errorf("-F - = %q", got)
	}
	mustContain(t, w.fails("main", "commit", "-m", "a", "-F", msgFile), "cannot be used together")

	// -a / --all and --allow-empty are accepted; bundled with -m as in `-am`.
	edit()
	if got := body(w.ok("main", "commit", "-am", "bundled")); got != "bundled" {
		t.Errorf("-am msg = %q", got)
	}
	if got := body(w.ok("main", "commit", "--allow-empty", "-m", "empty")); got != "empty" {
		t.Errorf("--allow-empty = %q", got)
	}
	// No message, not a terminal: unchanged behaviour for scripts.
	edit()
	if got := body(w.ok("main", "commit")); got != "snapshot" {
		t.Errorf("commit with no message from a script = %q, want the historical default", got)
	}
}

// TestCommitAuthorAndDate: git's --author="Name <email>" sets the AUTHOR of
// that one commit and leaves the committer alone; the bare-name form keeps
// cairn's original meaning (the identity this invocation acts as).
func TestCommitAuthorAndDate(t *testing.T) {
	w := newWC(t)
	w.write("main", "a.txt", "1\n")
	sha := w.ok("main", "commit", "-m", "git form", "--author=Ann Other <ann@other.org>", "--date=2019-05-06 07:08:09 +0200")
	if got := strings.TrimSpace(w.store("log", "-1", "--format=%an|%ae|%aI|%cn|%ce", sha)); got != "Ann Other|ann@other.org|2019-05-06T07:08:09+02:00|Tess Ter|tess@example.org" {
		t.Errorf("--author=\"Name <email>\" --date: %q", got)
	}
	w.write("main", "a.txt", "2\n")
	sha = w.ok("main", "commit", "-m", "date only", "--date", "@1700000000")
	if got := strings.TrimSpace(w.store("log", "-1", "--format=%an|%at", sha)); got != "Tess Ter|1700000000" {
		t.Errorf("--date alone: %q", got)
	}
	w.write("main", "a.txt", "3\n")
	sha = w.ok("main", "commit", "-m", "cairn form", "--author", "bob")
	if got := strings.TrimSpace(w.store("log", "-1", "--format=%an|%cn", sha)); got != "bob|bob" {
		t.Errorf("--author <name> (cairn's identity flag): %q, want bob as both", got)
	}
	mustContain(t, w.fails("main", "commit", "-m", "x", "--date", "not a date"), "unrecognized date")
}

// TestCommitEditorFallback: on a terminal with no -m the editor is launched
// ($CAIRN_EDITOR first), '#' lines are stripped, and an empty message aborts.
func TestCommitEditorFallback(t *testing.T) {
	w := newWC(t)
	oldTTY := stdinIsTerminal
	stdinIsTerminal = func() bool { return true }
	defer func() { stdinIsTerminal = oldTTY }()

	script := func(body string) string {
		p := filepath.Join(t.TempDir(), "editor.sh")
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	seen := filepath.Join(t.TempDir(), "template")
	t.Setenv("GIT_EDITOR", script(`echo wrong-editor > "$1"`))
	t.Setenv("CAIRN_EDITOR", script(`cp "$1" `+seen+`; printf 'typed subject\n# a comment\n\ntyped body\n# trailing comment\n' > "$1"`))

	w.write("main", "a.txt", "1\n")
	sha := w.ok("main", "commit")
	if got := strings.Split(w.store("log", "-1", "--format=%B", sha), "\n\nChange-Id:")[0]; got != "typed subject\n\ntyped body" {
		t.Errorf("message from the editor = %q", got)
	}
	tmpl, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, string(tmpl), "#\tnew file: a.txt")

	// An empty message aborts, sealing nothing.
	t.Setenv("CAIRN_EDITOR", script(`printf '# only comments\n' > "$1"`))
	w.write("main", "a.txt", "2\n")
	mustContain(t, w.fails("main", "commit"), "empty commit message")
	if got := w.porcelain("main"); got != "M  a.txt\n" {
		t.Errorf("an aborted commit changed the working change: %q", got)
	}
	// --no-edit and -m never open the editor.
	t.Setenv("CAIRN_EDITOR", script(`exit 1`))
	w.ok("main", "commit", "-m", "given")
	w.write("main", "a.txt", "3\n")
	w.ok("main", "commit", "--no-edit")
}

// ── status ───────────────────────────────────────────────────────────────────

// TestStatusPorcelainMatchesGit: for the same edits, cairn's porcelain status
// is byte-for-byte what git prints once everything is staged (`git add -A`) —
// which is cairn's model: no index, every edit is already in the working
// change. -z and path quoting included.
func TestStatusPorcelainMatchesGit(t *testing.T) {
	w := newWC(t)
	g := newGit(t)
	b := both{t, w, g, "main"}
	b.write("mod.txt", "1\n")
	b.write("del.txt", "1\n")
	b.write("dir/deep/mod.txt", "1\n")
	b.write("has space.txt", "1\n")
	b.write("same.txt", "1\n")
	w.ok("main", "commit", "-m", "base")
	gitIn(t, g, "add", "-A")
	gitIn(t, g, "commit", "-qm", "base")

	if got, want := w.porcelain("main"), gitIn(t, g, "status", "--porcelain"); got != want {
		t.Errorf("clean status: cairn %q, git %q", got, want)
	}

	b.write("mod.txt", "2\n")
	b.remove("del.txt")
	b.write("dir/deep/mod.txt", "2\n")
	b.write("has space.txt", "2\n")
	b.write("new file.txt", "n\n")
	b.write("dir/new.txt", "n\n")
	b.write("ünï.txt", "n\n")
	b.write(`quo"te.txt`, "n\n")
	gitIn(t, g, "add", "-A")

	for _, flags := range [][]string{{"--porcelain"}, {"--porcelain=v1"}, {"--short"}, {"-s"}, {"--porcelain", "-z"}, {"-z"}, {"-s", "--untracked-files=all"}, {"-s", "-uno"}} {
		got, _, err := w.run("main", "status", flags...)
		if err != nil {
			t.Fatalf("status %v: %v", flags, err)
		}
		gitFlags := flags
		if flags[len(flags)-1] == "-uno" || strings.HasPrefix(flags[len(flags)-1], "--untracked") {
			gitFlags = flags[:1] // nothing is untracked after add -A
		}
		want := gitIn(t, g, append([]string{"-c", "color.status=false", "status"}, gitFlags...)...)
		if got != want {
			t.Errorf("status %v:\ncairn:\n%q\ngit:\n%q", flags, got, want)
		}
	}

	// A pathspec filters, as in git.
	got, _, _ := w.run("main", "status", "-s", "--", "dir")
	if want := gitIn(t, g, "status", "-s", "--", "dir"); got != want {
		t.Errorf("status -s -- dir: cairn %q, git %q", got, want)
	}
	mustContain(t, w.fails("main", "status", "--porcelain=v3"), "porcelain v1")

	// The default long report is unchanged.
	long, _, _ := w.run("main", "status")
	mustContain(t, long, "branch:    main", "changes:", "  M mod.txt", "  D del.txt")
}

// TestStatusBranchHeader: -b prints git's `## line...remote/line [ahead N]`.
func TestStatusBranchHeader(t *testing.T) {
	needGit(t)
	w := newWC(t)
	w.write("main", "a.txt", "1\n")
	w.ok("main", "commit", "-m", "base")
	if got, _, _ := w.run("main", "status", "-sb"); got != "## main\n" {
		t.Errorf("status -sb with no remote = %q", got)
	}
	bare := filepath.Join(t.TempDir(), "origin.git")
	gitIn(t, "", "init", "-q", "--bare", bare)
	mustRun(t, "remote", "add", "origin", bare, "--repo", w.root)
	mustRun(t, "push", "--repo", w.root, "origin", "main")
	if got, _, _ := w.run("main", "status", "-sb"); got != "## main...origin/main\n" {
		t.Errorf("status -sb when pushed = %q", got)
	}
	w.write("main", "a.txt", "2\n")
	w.ok("main", "commit", "-m", "two")
	w.write("main", "b.txt", "b\n")
	if got, _, _ := w.run("main", "status", "--porcelain", "--branch"); got != "## main...origin/main [ahead 1]\nA  b.txt\n" {
		t.Errorf("status --porcelain --branch = %q", got)
	}
}

// TestStatusShowsConflictsAndIgnored: UU for an open conflict; !! only with
// --ignored.
func TestStatusShowsConflictsAndIgnored(t *testing.T) {
	w := newWC(t)
	w.write("main", ".gitignore", "*.log\nbuild/\n")
	w.write("main", "a.txt", "1\n")
	w.ok("main", "commit", "-m", "base")
	w.write("main", "x.log", "l\n")
	w.write("main", "build/out.o", "o\n")
	if got := w.porcelain("main"); got != "" {
		t.Errorf("ignored files showed up without --ignored: %q", got)
	}
	if got, _, _ := w.run("main", "status", "--porcelain", "--ignored"); got != "!! build/\n!! x.log\n" {
		t.Errorf("status --ignored = %q", got)
	}
}

// ── rm / mv / add / clean ────────────────────────────────────────────────────

// TestRm: files leave the folder and the working change, like `git rm`; a
// directory needs -r, un-sealed content needs -f, and undo brings them back.
func TestRm(t *testing.T) {
	w := newWC(t)
	g := newGit(t)
	b := both{t, w, g, "main"}
	b.write("a.txt", "a\n")
	b.write("dir/one.txt", "1\n")
	b.write("dir/sub/two.txt", "2\n")
	b.write("keep.txt", "k\n")
	w.ok("main", "commit", "-m", "base")
	gitIn(t, g, "add", "-A")
	gitIn(t, g, "commit", "-qm", "base")

	mustContain(t, w.fails("main", "rm", "dir"), "recursively without -r")
	mustContain(t, w.fails("main", "rm", "nope.txt"), "did not match")
	w.ok("main", "rm", "--ignore-unmatch", "nope.txt")

	if got, want := w.ok("main", "rm", "-n", "-r", "dir"), strings.TrimSpace(gitIn(t, g, "rm", "-n", "-r", "dir")); got != want {
		t.Errorf("rm -n -r dir:\ncairn %q\ngit   %q", got, want)
	}
	if !w.exists("main", "dir/one.txt") {
		t.Fatal("rm -n removed files")
	}
	got := w.ok("main", "rm", "a.txt", "-r", "dir")
	want := strings.TrimSpace(gitIn(t, g, "rm", "a.txt", "-r", "dir"))
	if got != want {
		t.Errorf("rm output:\ncairn %q\ngit   %q", got, want)
	}
	if got, want := w.porcelain("main"), gitIn(t, g, "status", "--porcelain"); got != want {
		t.Errorf("status after rm:\ncairn %q\ngit   %q", got, want)
	}
	if got, want := treeOf(t, w.dir("main")), treeOf(t, g); got != want {
		t.Errorf("folder after rm:\ncairn %q\ngit   %q", got, want)
	}
	mustRun(t, "undo", "--repo", w.root)
	if !w.exists("main", "a.txt") || !w.exists("main", "dir/sub/two.txt") {
		t.Error("undo did not bring the removed files back")
	}

	// Un-sealed content is not dropped without -f.
	w.write("main", "keep.txt", "edited\n")
	w.write("main", "fresh.txt", "f\n")
	mustContain(t, w.fails("main", "rm", "keep.txt"), "un-sealed changes", "-f")
	mustContain(t, w.fails("main", "rm", "fresh.txt"), "un-sealed changes")
	w.ok("main", "rm", "-f", "keep.txt", "fresh.txt")
	if w.exists("main", "keep.txt") || w.exists("main", "fresh.txt") {
		t.Error("rm -f left the files")
	}
	mustContain(t, w.fails("main", "rm", "--cached", "a.txt"), "no index")
}

// TestMv: a file renames, a directory moves with everything in it, several
// sources move into a directory; content and the exec bit ride along.
func TestMv(t *testing.T) {
	w := newWC(t)
	w.write("main", "a.txt", "a\n")
	w.write("main", "b.txt", "b\n")
	w.write("main", "src/x.go", "x\n")
	w.write("main", "src/deep/y.go", "y\n")
	w.write("main", "tool.sh", "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(w.dir("main"), "tool.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	w.ok("main", "commit", "-m", "base")

	if got := w.ok("main", "mv", "-n", "a.txt", "renamed.txt"); got != "Renaming a.txt to renamed.txt" {
		t.Errorf("mv -n = %q", got)
	}
	if !w.exists("main", "a.txt") {
		t.Fatal("mv -n moved the file")
	}
	w.ok("main", "mv", "a.txt", "renamed.txt")
	w.ok("main", "mv", "src", "lib")
	w.ok("main", "mv", "b.txt", "tool.sh", "lib")
	// Moved files are renames, as git's status pairs them (--no-renames: a delete and an add).
	if got := w.porcelain("main"); got != "R  b.txt -> lib/b.txt\nR  src/deep/y.go -> lib/deep/y.go\nR  tool.sh -> lib/tool.sh\nR  src/x.go -> lib/x.go\nR  a.txt -> renamed.txt\n" {
		t.Errorf("status after mv = %q", got)
	}
	if got, _, _ := w.run("main", "status", "--porcelain", "--no-renames"); got != "D  a.txt\nD  b.txt\nA  lib/b.txt\nA  lib/deep/y.go\nA  lib/tool.sh\nA  lib/x.go\nA  renamed.txt\nD  src/deep/y.go\nD  src/x.go\nD  tool.sh\n" {
		t.Errorf("status after mv = %q", got)
	}
	if w.exists("main", "src") || w.read("main", "lib/deep/y.go") != "y\n" || w.read("main", "renamed.txt") != "a\n" {
		t.Error("the folder does not reflect the moves")
	}
	if fi, err := os.Stat(filepath.Join(w.dir("main"), "lib", "tool.sh")); err != nil || fi.Mode()&0o111 == 0 {
		t.Errorf("tool.sh lost its exec bit in the move (%v)", err)
	}
	mustRun(t, "undo", "--repo", w.root)
	if !w.exists("main", "b.txt") || w.exists("main", "lib/b.txt") {
		t.Error("undo did not revert the last mv")
	}

	mustContain(t, w.fails("main", "mv", "nope.txt", "x.txt"), "bad source")
	mustContain(t, w.fails("main", "mv", "b.txt", "renamed.txt"), "destination exists")
	w.ok("main", "mv", "-f", "b.txt", "renamed.txt")
	if got := w.read("main", "renamed.txt"); got != "b\n" {
		t.Errorf("mv -f: renamed.txt = %q", got)
	}
	mustContain(t, w.fails("main", "mv", "renamed.txt", "tool.sh", "not-a-dir.txt"), "not a directory")
}

// TestAddIsASuccessfulNoOp: `git add -A && git commit -m …` must work.
func TestAddIsASuccessfulNoOp(t *testing.T) {
	w := newWC(t)
	w.write("main", "a.txt", "1\n")
	for _, form := range [][]string{{"-A"}, {"."}, {"--all"}, {"-u"}, {"a.txt", "b.txt"}, {"-f", "--", "-p"}} {
		_, errOut, err := w.run("main", "add", form...)
		if err != nil {
			t.Fatalf("add %v: %v", form, err)
		}
		mustContain(t, errOut, "no staging area", "cairn commit")
		if strings.Count(strings.TrimSpace(errOut), "\n") != 0 {
			t.Errorf("add %v printed more than one line: %q", form, errOut)
		}
	}
	w.ok("main", "commit", "-m", "after add")
	for _, form := range [][]string{{"-p"}, {"--patch", "a.txt"}, {"-i"}} {
		mustContain(t, w.fails("main", "add", form...), "cairn commit -m <msg> -- <path>")
	}
}

// TestCleanRemovesOnlyIgnoredFiles pins clean's semantics: it removes what
// `git clean -X` removes (ignored files), and NEVER a new source file — which
// in cairn is tracked work, not junk.
func TestCleanRemovesOnlyIgnoredFiles(t *testing.T) {
	w := newWC(t)
	g := newGit(t)
	b := both{t, w, g, "main"}
	b.write(".gitignore", "*.o\nbuild/\nnode_modules/\n")
	b.write("src/main.c", "int main;\n")
	w.ok("main", "commit", "-m", "base")
	gitIn(t, g, "add", "-A")
	gitIn(t, g, "commit", "-qm", "base")

	b.write("src/main.o", "obj\n")
	b.write("build/out/app", "bin\n")
	b.write("node_modules/dep/index.js", "js\n")
	b.write("top.o", "obj\n")
	w.write("main", "src/brand-new.c", "new source\n") // un-sealed, not ignored

	// Without -x there is nothing clean may remove, and it says why.
	_, errOut, err := w.run("main", "clean", "-fd")
	if err != nil {
		t.Fatalf("clean -fd: %v", err)
	}
	mustContain(t, errOut, "no untracked files", "clean -fdx")
	if !w.exists("main", "src/brand-new.c") || !w.exists("main", "top.o") {
		t.Fatal("clean without -x removed something")
	}
	// -x without -f refuses, as git does.
	mustContain(t, w.fails("main", "clean", "-dx"), "without -f")

	// The dry run lists exactly what git would remove.
	gotDry := w.ok("main", "clean", "-ndX")
	wantDry := strings.TrimSpace(gitIn(t, g, "clean", "-ndX"))
	if sortLines(gotDry) != sortLines(wantDry) {
		t.Errorf("clean -ndX:\ncairn:\n%s\ngit:\n%s", gotDry, wantDry)
	}
	if !w.exists("main", "top.o") {
		t.Fatal("clean -n removed files")
	}
	// Without -d, ignored directories stay.
	w.ok("main", "clean", "-fx", "--", "src")
	if w.exists("main", "src/main.o") || !w.exists("main", "top.o") {
		t.Error("clean -fx -- src should remove only the ignored file under src")
	}

	gotRun := w.ok("main", "clean", "-fdx")
	gitIn(t, g, "clean", "-fdX")
	mustContain(t, gotRun, "Removing build/", "Removing node_modules/", "Removing top.o")
	// The new source file is still there, and still un-sealed work.
	if got := w.read("main", "src/brand-new.c"); got != "new source\n" {
		t.Errorf("clean -fdx destroyed un-sealed new source: %q", got)
	}
	if got := w.porcelain("main"); got != "A  src/brand-new.c\n" {
		t.Errorf("status after clean = %q", got)
	}
	w.remove("main", "src/brand-new.c")
	if got, want := treeOf(t, w.dir("main")), treeOf(t, g); got != want {
		t.Errorf("folder after clean -fdx vs git clean -fdX:\ncairn %q\ngit   %q", got, want)
	}
}

// TestCleanSparesNestedRepositories: an ignored directory that is itself a
// checkout is never swept away.
func TestCleanSparesNestedRepositories(t *testing.T) {
	w := newWC(t)
	w.write("main", ".gitignore", "vendor/\n")
	w.ok("main", "commit", "-m", "base")
	w.write("main", "vendor/lib/.git/HEAD", "ref: refs/heads/main\n")
	w.write("main", "vendor/lib/code.c", "c\n")
	w.ok("main", "clean", "-fdx")
	if !w.exists("main", "vendor/lib/code.c") {
		t.Error("clean -fdx removed a directory holding a nested repository")
	}
}

func sortLines(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	for i := 0; i < len(lines); i++ {
		for j := i + 1; j < len(lines); j++ {
			if lines[j] < lines[i] {
				lines[i], lines[j] = lines[j], lines[i]
			}
		}
	}
	return strings.Join(lines, "\n")
}

// ── argument handling ────────────────────────────────────────────────────────

func TestExpandShortFlags(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	for _, b := range []string{"a", "f", "d", "x", "force"} {
		fs.Bool(b, false, "")
	}
	fs.String("m", "", "")
	fs.Int("n", 0, "")
	cases := []struct{ in, want []string }{
		{[]string{"-fdx"}, []string{"-f", "-d", "-x"}},
		{[]string{"-am", "msg"}, []string{"-a", "-m", "msg"}},
		{[]string{"-amfix it"}, []string{"-a", "-m", "fix it"}},
		{[]string{"-force", "-n5", "-mmsg"}, []string{"-force", "-n5", "-mmsg"}}, // not bundles
		{[]string{"-fz"}, []string{"-fz"}},                                       // unknown letter: left for the parser to reject
		{[]string{"-f", "--", "-fd"}, []string{"-f", "--", "-fd"}},               // after "--": paths
		{[]string{"--fd=1", "path"}, []string{"--fd=1", "path"}},
	}
	for _, tc := range cases {
		if got := expandShortFlags(fs, tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("expandShortFlags(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
