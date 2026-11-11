//go:build !windows

package main

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// These pin the fold regressions found during the git-parity work:
//
//   - FoldLine advanced the parent LINE tip but left the parent's open working
//     commit parented on the old sealed tip, so the next sync amended it in
//     place and pointed the line back at it: the fold's commits fell out of the
//     parent's history and the folded files showed up as un-sealed adds.
//   - A fold used the child's raw tip — its "(working)" auto-snapshot — so that
//     placeholder was sealed into the parent's history.

// foldRepo: main has base + one commit made AFTER feat was expressed (so the
// fold has to adopt it), and feat has one sealed commit of its own.
func foldRepo(t *testing.T) *wc {
	t.Helper()
	w := newWCWithFeature(t)
	w.write("main", "m.txt", "m\n")
	w.ok("main", "commit", "-m", "main moves on")
	w.write("feat", "f.txt", "f\n")
	return w
}

// graph is real git's view of a line's sealed history in cairn's own store.
func (w *wc) graph(branch string) string {
	w.t.Helper()
	return w.store("log", "--graph", "--format=%s", w.head(branch))
}

// assertNoPlaceholder fails if a "(working)" commit is anywhere in the sealed
// history of branch.
func assertNoPlaceholder(t *testing.T, w *wc, branch string) {
	t.Helper()
	if g := w.graph(branch); strings.Contains(g, "(working)") {
		t.Errorf("a (working) placeholder is in %s's history:\n%s", branch, g)
	}
}

// pushAndGraph pushes branch to a fresh bare git remote and returns real git's
// `log --graph` of what arrived.
func pushAndGraph(t *testing.T, w *wc, branch string) string {
	t.Helper()
	needGit(t)
	bare := filepath.Join(t.TempDir(), "origin.git")
	gitIn(t, "", "init", "-q", "--bare", bare)
	mustRun(t, "remote", "add", "origin", bare, "--repo", w.root)
	mustRun(t, "push", "--repo", w.root, "origin", branch)
	return gitIn(t, "", "--git-dir", bare, "log", "--graph", "--format=%s", branch)
}

// TestFoldAfterParentAdvanced: the canonical local flow. After the fold, main's
// history holds feat's commit AND main's own, nothing is un-sealed, the next
// commit lands on top, and what is pushed to a plain git remote is that same
// history.
func TestFoldAfterParentAdvanced(t *testing.T) {
	w := foldRepo(t)
	w.ok("feat", "commit", "-m", "feat work") // adopts "main moves on"
	mustRun(t, "fold", "--repo", w.root, "feat")

	if got := w.porcelain("main"); got != "" {
		t.Fatalf("status on main after the fold = %q, want clean (the folded files must be SEALED history, not un-sealed adds)", got)
	}
	// A second command must not undo it either (the bug needed one sync to show).
	if got := w.porcelain("main"); got != "" {
		t.Fatalf("status on main, second look = %q", got)
	}
	for _, f := range []string{"base.txt", "m.txt", "f.txt"} {
		if !w.exists("main", f) {
			t.Errorf("%s missing from main after the fold", f)
		}
	}
	mustContain(t, strings.Join(w.subjects("main"), "\n"), "feat work")
	mustContain(t, w.graph("main"), "feat work", "main moves on", "base")
	assertNoPlaceholder(t, w, "main")

	// Life goes on: a commit on main sits on top of the fold.
	w.write("main", "after.txt", "a\n")
	w.ok("main", "commit", "-m", "after the fold")
	if got := w.subjects("main")[0]; got != "after the fold" {
		t.Errorf("top of main = %q", got)
	}
	remote := pushAndGraph(t, w, "main")
	mustContain(t, remote, "after the fold", "feat work", "main moves on", "base")
	if strings.Contains(remote, "(working)") {
		t.Errorf("a (working) placeholder was pushed:\n%s", remote)
	}
	if got, want := strings.Count(remote, "feat work"), strings.Count(w.graph("main"), "feat work"); got != want {
		t.Errorf("pushed history differs from local:\nremote:\n%s\nlocal:\n%s", remote, w.graph("main"))
	}
}

// TestFoldAdoptsParentWithoutPlaceholder: feat sealed BEFORE main moved, so the
// fold itself must adopt main (a "fold: adopt main" merge). That merge must sit
// on feat's sealed commit, not on its "(working)" snapshot.
func TestFoldAdoptsParentWithoutPlaceholder(t *testing.T) {
	w := newWCWithFeature(t)
	w.write("feat", "f.txt", "f\n")
	w.ok("feat", "commit", "-m", "feat work")
	_ = w.porcelain("feat") // leave feat with a (clean) working snapshot at its tip
	w.write("main", "m.txt", "m\n")
	w.ok("main", "commit", "-m", "main moves on")
	mustRun(t, "fold", "--repo", w.root, "feat")

	if got := w.porcelain("main"); got != "" {
		t.Fatalf("status on main after the fold = %q, want clean", got)
	}
	g := w.graph("main")
	mustContain(t, g, "fold: adopt main", "feat work", "main moves on", "base")
	assertNoPlaceholder(t, w, "main")
	remote := pushAndGraph(t, w, "main")
	mustContain(t, remote, "fold: adopt main", "feat work", "main moves on")
	if strings.Contains(remote, "(working)") {
		t.Errorf("a (working) placeholder was pushed:\n%s", remote)
	}
	// It is a real merge: both sides are in the graph.
	if !strings.Contains(remote, "|\\") && !strings.Contains(remote, "|/") {
		t.Errorf("the adoption is not a merge in git's graph:\n%s", remote)
	}
}

// TestFoldFastForwardHasNoPlaceholder: the simplest fold — main did not move.
func TestFoldFastForwardHasNoPlaceholder(t *testing.T) {
	w := newWCWithFeature(t)
	w.write("feat", "f.txt", "f\n")
	w.ok("feat", "commit", "-m", "feat work")
	_ = w.porcelain("feat")
	_ = w.porcelain("main") // main has a working snapshot too
	mustRun(t, "fold", "--repo", w.root, "feat")
	if got := w.porcelain("main"); got != "" {
		t.Fatalf("status on main = %q, want clean", got)
	}
	if got, want := w.subjects("main"), []string{"feat work", "base"}; !reflect.DeepEqual(got, want) {
		t.Errorf("main's history = %v, want %v", got, want)
	}
	assertNoPlaceholder(t, w, "main")
}

// TestFoldKeepsUnsealedEditsOnParent: edits sitting un-sealed in main's folder
// ride across the fold — still there, still un-sealed, and ONLY they.
func TestFoldKeepsUnsealedEditsOnParent(t *testing.T) {
	w := foldRepo(t)
	w.ok("feat", "commit", "-m", "feat work")
	w.write("main", "wip.txt", "wip\n")
	w.write("main", "m.txt", "m, edited but not sealed\n")
	mustRun(t, "fold", "--repo", w.root, "feat")

	if got := w.porcelain("main"); got != "M  m.txt\nA  wip.txt\n" {
		t.Fatalf("status on main after the fold = %q, want exactly main's own un-sealed edits", got)
	}
	if got := w.read("main", "m.txt"); got != "m, edited but not sealed\n" {
		t.Errorf("main's un-sealed edit was overwritten by the fold: %q", got)
	}
	if !w.exists("main", "f.txt") {
		t.Error("the folded file is missing from main's folder")
	}
	mustContain(t, w.graph("main"), "feat work", "main moves on")
	assertNoPlaceholder(t, w, "main")
	w.ok("main", "commit", "-m", "main's own work")
	if got := w.subjects("main")[0]; got != "main's own work" {
		t.Errorf("top of main = %q", got)
	}
	if got := w.store("show", "--format=", "--name-only", w.head("main")); got != "m.txt\nwip.txt\n" {
		t.Errorf("main's next commit holds %q, want only its own edits (not the folded files)", got)
	}
	remote := pushAndGraph(t, w, "main")
	mustContain(t, remote, "main's own work", "feat work", "main moves on", "base")
}

// TestFoldRefusedWhenParentEditsOverlap: un-sealed edits on main that touch
// what feat changed cannot be carried across; the fold is refused with nothing
// changed, and works once they are sealed.
func TestFoldRefusedWhenParentEditsOverlap(t *testing.T) {
	w := newWCWithFeature(t)
	w.write("feat", "base.txt", "feat's version\n")
	w.ok("feat", "commit", "-m", "feat edits base")
	w.write("main", "base.txt", "main's un-sealed version\n")

	err := run([]string{"fold", "--repo", w.root, "feat"})
	if err == nil {
		t.Fatal("fold over overlapping un-sealed edits on the parent succeeded")
	}
	mustContain(t, err.Error(), "un-sealed edits", "base.txt", "commit or stash")
	if got := w.read("main", "base.txt"); got != "main's un-sealed version\n" {
		t.Errorf("a refused fold changed main's folder: %q", got)
	}
	if !w.exists("feat", "base.txt") {
		t.Error("a refused fold removed feat's folder")
	}
	if got := w.subjects("main"); !reflect.DeepEqual(got, []string{"base"}) {
		t.Errorf("a refused fold changed main's history: %v", got)
	}
	// Shelve them and it folds; they come back on top.
	w.ok("main", "stash")
	mustRun(t, "fold", "--repo", w.root, "feat")
	if got := w.read("main", "base.txt"); got != "feat's version\n" {
		t.Errorf("base.txt after the fold = %q", got)
	}
	assertNoPlaceholder(t, w, "main")
}

// TestFoldWithConflictingAdoption: main and feat sealed different versions of
// one file. The fold refuses (nothing changed) and says how to proceed; sealing
// feat records the conflict, resolving and sealing again clears it, and then
// the fold lands a clean history.
func TestFoldWithConflictingAdoption(t *testing.T) {
	w := newWCWithFeature(t)
	w.write("feat", "base.txt", "feat's version\n")
	w.ok("feat", "commit", "-m", "feat edits base")
	w.write("main", "base.txt", "main's version\n")
	w.ok("main", "commit", "-m", "main edits base")

	err := run([]string{"fold", "--repo", w.root, "feat"})
	if err == nil {
		t.Fatal("fold with a conflicting adoption succeeded")
	}
	mustContain(t, err.Error(), "conflict", "cairn commit feat")
	if got := w.subjects("main"); !reflect.DeepEqual(got, []string{"main edits base", "base"}) {
		t.Fatalf("a refused fold changed main's history: %v", got)
	}
	if got := w.porcelain("main"); got != "" {
		t.Fatalf("a refused fold left un-sealed changes on main: %q", got)
	}

	w.conflicts("feat", "commit", "-m", "adopt main")
	w.write("feat", "base.txt", "both versions, reconciled\n")
	mustRun(t, "resolve", "--repo", w.root, "feat", "base.txt")
	w.ok("feat", "commit", "-m", "resolve base")
	mustRun(t, "fold", "--repo", w.root, "feat")

	if got := w.read("main", "base.txt"); got != "both versions, reconciled\n" {
		t.Errorf("base.txt on main after the fold = %q", got)
	}
	if got := w.porcelain("main"); got != "" {
		t.Errorf("status on main after the fold = %q, want clean", got)
	}
	g := w.graph("main")
	mustContain(t, g, "resolve base", "feat edits base", "main edits base", "base")
	assertNoPlaceholder(t, w, "main")
	remote := pushAndGraph(t, w, "main")
	mustContain(t, remote, "resolve base", "feat edits base", "main edits base")
}

// TestFoldForceCarriesUnsealedWorkAsARealCommit: `fold --force` on a line with
// un-sealed work has always carried that work into the parent; it must arrive
// as a commit with a message, not as a "(working)" placeholder.
func TestFoldForceCarriesUnsealedWorkAsARealCommit(t *testing.T) {
	w := newWCWithFeature(t)
	w.write("feat", "f.txt", "sealed\n")
	w.ok("feat", "commit", "-m", "feat work")
	w.write("feat", "loose.txt", "never sealed\n")
	if err := run([]string{"fold", "--repo", w.root, "feat"}); err == nil {
		t.Fatal("fold of a line with un-sealed work succeeded without --force")
	}
	mustRun(t, "fold", "--repo", w.root, "--force", "feat")
	if got := w.read("main", "loose.txt"); got != "never sealed\n" {
		t.Errorf("loose.txt on main = %q", got)
	}
	if got := w.porcelain("main"); got != "" {
		t.Errorf("status on main = %q, want clean", got)
	}
	mustContain(t, w.graph("main"), "fold feat: un-sealed work", "feat work")
	assertNoPlaceholder(t, w, "main")
}

// TestUndoOfFold: undo puts main back exactly as it was — history, folder and
// un-sealed edits — and re-opens the folded line, so it can be expressed and
// folded again.
func TestUndoOfFold(t *testing.T) {
	w := foldRepo(t)
	w.ok("feat", "commit", "-m", "feat work")
	w.write("main", "wip.txt", "wip\n")
	_ = w.porcelain("main")
	beforeTree := treeOf(t, w.dir("main"))
	beforeLog := w.subjects("main")

	mustRun(t, "fold", "--repo", w.root, "feat")
	mustRun(t, "undo", "--repo", w.root)

	if got := treeOf(t, w.dir("main")); got != beforeTree {
		t.Errorf("main's folder after undoing the fold:\ngot  %q\nwant %q", got, beforeTree)
	}
	if got := w.subjects("main"); !reflect.DeepEqual(got, beforeLog) {
		t.Errorf("main's history after undoing the fold = %v, want %v", got, beforeLog)
	}
	if got := w.porcelain("main"); got != "A  wip.txt\n" {
		t.Errorf("status on main after undoing the fold = %q", got)
	}
	// The line is open again: express it, and fold it again.
	mustRun(t, "express", "--repo", w.root, "feat")
	if got := w.read("feat", "f.txt"); got != "f\n" {
		t.Errorf("feat/f.txt after re-expressing the un-folded line = %q", got)
	}
	// foldRepo advanced main first, so the seal adopted it: the adoption merge
	// (own "adopt <parent> into <line>" message) sits on the stamped commit.
	if got := w.subjects("feat")[:2]; !reflect.DeepEqual(got, []string{"adopt main into feat", "feat work"}) {
		t.Errorf("top of the un-folded line = %q", got)
	}
	mustRun(t, "fold", "--repo", w.root, "feat")
	if got := w.porcelain("main"); got != "A  wip.txt\n" {
		t.Errorf("status on main after folding again = %q", got)
	}
	mustContain(t, w.graph("main"), "feat work", "main moves on")
	assertNoPlaceholder(t, w, "main")
}

// TestEditRootCommitAboveAFold: the history-edit rule across a fold, through
// the CLI. The commits a fold brought in are immutable from the line they were
// folded into; a root commit sealed AFTER the fold is editable, and editing it
// must not disturb the fold below — including main's own pre-fold commit, which
// is reachable only through the fold's merge.
func TestEditRootCommitAboveAFold(t *testing.T) {
	w := foldRepo(t)
	w.ok("feat", "commit", "-m", "feat work")
	mustRun(t, "fold", "--repo", w.root, "feat")
	foldedTop := w.head("main")

	// Straight after the fold main's tip is folded-in history.
	w.write("main", "x.txt", "x\n")
	mustContain(t, w.fails("main", "commit", "--amend", "--no-edit"), "fold", "cairn revert")
	mustContain(t, w.fails("main", "reset", "--hard", "HEAD~1"), "fold")
	err := run([]string{"reword", "--repo", w.root, foldedTop, "nope"})
	if err == nil {
		t.Fatal("reword of a folded-in commit succeeded")
	}

	late := w.ok("main", "commit", "-m", "after the fold")
	mustRun(t, "reword", "--repo", w.root, late, "after the fold, reworded")
	w.write("main", "y.txt", "y\n")
	w.ok("main", "commit", "--amend", "--no-edit")
	if got := w.subjects("main")[0]; got != "after the fold, reworded" {
		t.Errorf("top of main = %q", got)
	}
	for _, f := range []string{"base.txt", "m.txt", "f.txt", "x.txt", "y.txt"} {
		if !w.exists("main", f) {
			t.Errorf("%s is gone from main after editing a post-fold commit", f)
		}
	}
	g := w.graph("main")
	mustContain(t, g, "after the fold, reworded", "feat work", "main moves on", "base")
	assertNoPlaceholder(t, w, "main")
	// The fold below the edit kept its hashes.
	if anc := w.store("merge-base", "--is-ancestor", foldedTop, w.head("main")); anc != "" {
		t.Errorf("unexpected output: %q", anc)
	}
	// A reset back to (not below) the fold is fine; below it is not.
	w.ok("main", "reset", "--hard", "HEAD~1")
	if w.head("main")[:8] != foldedTop[:8] {
		t.Errorf("main after reset --hard HEAD~1 is at %s, want the fold tip %s", w.head("main"), foldedTop)
	}
	remote := pushAndGraph(t, w, "main")
	mustContain(t, remote, "feat work", "main moves on", "base")
}
