package change

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

// wcLine forks line "w" off a seeded main, seals one commit on it holding
// files, and returns its id — a non-root line with one sealed commit above its
// base and a fresh, empty working change.
func wcLine(t *testing.T, e *Engine, files map[string]string) string {
	t.Helper()
	lineID, _, _ := sealOne(t, e, "w", files, "first")
	return lineID
}

// snapshot amends the line's working change to hold files.
func snapshot(t *testing.T, e *Engine, lineID string, files map[string]string) {
	t.Helper()
	w, err := e.openWorkingChange(lineID)
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string]TreeEntry{}
	for p, c := range files {
		entries[p] = blobEntry(t, e, c)
	}
	if _, _, err := e.SnapshotWorking(w.ID, entries); err != nil {
		t.Fatalf("SnapshotWorking: %v", err)
	}
}

func treeFiles(t *testing.T, e *Engine, commit string) map[string]string {
	t.Helper()
	files, err := e.Files(commit)
	if err != nil {
		t.Fatalf("Files(%s): %v", commit, err)
	}
	out := map[string]string{}
	for p, b := range files {
		out[p] = string(b)
	}
	return out
}

func tipOf(t *testing.T, e *Engine, lineID string) string {
	t.Helper()
	l, err := e.lineByID(lineID)
	if err != nil {
		t.Fatal(err)
	}
	return l.TipCommit
}

func lastOpType(t *testing.T, e *Engine) string {
	t.Helper()
	op, err := e.LastOp()
	if err != nil {
		t.Fatal(err)
	}
	return op.OpType
}

// TestAppendSealedKeepsTheWorkingChange: the new commit goes UNDER the open
// working change, which keeps its change-id and its un-sealed delta — the
// property that makes undo of every sealing verb a one-tip restore.
func TestAppendSealedKeepsTheWorkingChange(t *testing.T) {
	e := newTestEngine(t)
	lineID := wcLine(t, e, map[string]string{"a.txt": "1\n"})
	snapshot(t, e, lineID, map[string]string{"a.txt": "1\n", "wip.txt": "wip\n"})
	wBefore, _ := e.openWorkingChange(lineID)
	tipBefore := tipOf(t, e, lineID)

	tree, err := e.WriteTree(map[string]TreeEntry{"a.txt": blobEntry(t, e, "1\n"), "new.txt": blobEntry(t, e, "n\n")})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.AppendSealed(lineID, OpAm, AppendSpec{Message: "add new", Tree: tree,
		Author: &Signature{Name: "Pat", Email: "pat@example.org"}})
	if err != nil {
		t.Fatalf("AppendSealed: %v", err)
	}
	if len(res.Conflicts) != 0 {
		t.Fatalf("conflicts = %v", res.Conflicts)
	}
	wAfter, _ := e.openWorkingChange(lineID)
	if wAfter.ID != wBefore.ID {
		t.Errorf("the open change id moved: %s -> %s", wBefore.ID, wAfter.ID)
	}
	if got, _ := e.firstParent(wAfter.HeadCommit); got != res.Commit {
		t.Errorf("working commit's parent = %s, want the new sealed commit %s", got, res.Commit)
	}
	if got := treeFiles(t, e, wAfter.HeadCommit); got["wip.txt"] != "wip\n" || got["new.txt"] != "n\n" {
		t.Errorf("working tree after append = %v, want the un-sealed file AND the new commit's", got)
	}
	if got := treeFiles(t, e, res.Commit); got["wip.txt"] != "" {
		t.Errorf("the sealed commit swallowed un-sealed work: %v", got)
	}
	info, _ := e.CommitInfoOf(res.Commit)
	if info.AuthorName != "Pat" || info.AuthorEmail != "pat@example.org" || info.Subject != "add new" {
		t.Errorf("sealed commit meta = %+v", info)
	}
	if got := lastOpType(t, e); got != OpAm {
		t.Errorf("op = %q, want %q", got, OpAm)
	}
	// Undo restores exactly the pre-op tip.
	if err := e.Undo(); err != nil {
		t.Fatal(err)
	}
	if got := tipOf(t, e, lineID); got != tipBefore {
		t.Errorf("tip after undo = %s, want %s", got, tipBefore)
	}
}

// TestAppendSealedRefusesWithoutWriting: a delta that conflicts, or an empty
// one, changes nothing at all.
func TestAppendSealedRefusesWithoutWriting(t *testing.T) {
	e := newTestEngine(t)
	lineID := wcLine(t, e, map[string]string{"a.txt": "ours\n"})
	tipBefore := tipOf(t, e, lineID)
	opBefore, _ := e.LastOp()

	base, _ := e.WriteTree(map[string]TreeEntry{"a.txt": blobEntry(t, e, "base\n")})
	theirs, _ := e.WriteTree(map[string]TreeEntry{"a.txt": blobEntry(t, e, "theirs\n")})
	_, err := e.AppendSealed(lineID, OpRevert, AppendSpec{Message: "x", Base: base, Theirs: theirs})
	if !errors.Is(err, ErrWouldConflict) || !strings.Contains(err.Error(), "a.txt") {
		t.Fatalf("conflicting delta: err = %v, want ErrWouldConflict naming a.txt", err)
	}
	_, err = e.AppendSealed(lineID, OpRevert, AppendSpec{Message: "x", Base: base, Theirs: base})
	if !errors.Is(err, ErrNothingToCommit) {
		t.Fatalf("empty delta: err = %v, want ErrNothingToCommit", err)
	}
	if got := tipOf(t, e, lineID); got != tipBefore {
		t.Error("a refused AppendSealed moved the line tip")
	}
	if op, _ := e.LastOp(); op.ID != opBefore.ID {
		t.Error("a refused AppendSealed recorded an operation")
	}
	if _, err := e.AppendSealed(lineID, OpRevert, AppendSpec{Message: "empty", Base: base, Theirs: base, AllowEmpty: true}); err != nil {
		t.Errorf("AllowEmpty: %v", err)
	}
}

// TestMergeIntoWorkingRecordsConflictsOnTheWorkingChange: the conflict rows a
// stopped revert / stash pop leaves are on the open change, where status and
// resolve look.
func TestMergeIntoWorkingRecordsConflictsOnTheWorkingChange(t *testing.T) {
	e := newTestEngine(t)
	lineID := wcLine(t, e, map[string]string{"a.txt": "ours\n"})
	base, _ := e.WriteTree(map[string]TreeEntry{"a.txt": blobEntry(t, e, "base\n")})
	theirs, _ := e.WriteTree(map[string]TreeEntry{"a.txt": blobEntry(t, e, "theirs\n"), "b.txt": blobEntry(t, e, "b\n")})
	cf, changed, err := e.MergeIntoWorking(lineID, base, theirs, OpRevert)
	if err != nil || !changed || len(cf) != 1 || cf[0].Path != "a.txt" {
		t.Fatalf("MergeIntoWorking = %v, %v, %v", cf, changed, err)
	}
	w, _ := e.openWorkingChange(lineID)
	open, _ := e.Conflicts(w.ID)
	if len(open) != 1 || !w.HasConflict {
		t.Errorf("open conflicts on the working change = %d (has_conflict=%v), want 1", len(open), w.HasConflict)
	}
	if got := treeFiles(t, e, w.HeadCommit); got["b.txt"] != "b\n" || !strings.Contains(got["a.txt"], "<<<<<<<") {
		t.Errorf("working tree = %v", got)
	}
	// reset --hard drops them with the work.
	top, _ := e.SealedTip(lineID)
	if _, err := e.ResetLine(lineID, top, true); err != nil {
		t.Fatal(err)
	}
	w, _ = e.openWorkingChange(lineID)
	if open, _ := e.Conflicts(w.ID); len(open) != 0 || w.HasConflict {
		t.Errorf("after reset --hard: %d open conflicts, has_conflict=%v", len(open), w.HasConflict)
	}
}

// TestAmendTopAndResetLineGuards: both rewrite sealed history and so follow
// reword's rule — never on the root line, never under a child line — and reset
// only moves back along the line's own history, never below its base.
func TestAmendTopAndResetLineGuards(t *testing.T) {
	e := newTestEngine(t)
	lineID := wcLine(t, e, map[string]string{"a.txt": "1\n"})
	main, _ := e.LineByName("main")
	line, _ := e.lineByID(lineID)
	openChange(t, e, main.ID)

	msg := "x"
	// The root's top commit is line "w"'s base: amending it would rewrite w's
	// foundation — refused, naming w.
	if _, err := e.AmendTop(main.ID, &msg, nil, false); err == nil || !strings.Contains(err.Error(), `child line "w"`) {
		t.Errorf("AmendTop of a child's base = %v, want the child-line refusal naming w", err)
	}
	// Back to the line's own base is the furthest a reset may go...
	top, _ := e.SealedTip(lineID)
	res, err := e.ResetLine(lineID, line.BaseCommit, false)
	if err != nil || res.Dropped != 1 || res.OldTip != top {
		t.Fatalf("ResetLine to the line's base = %+v, %v", res, err)
	}
	if got, _ := e.SealedTip(lineID); got != line.BaseCommit {
		t.Errorf("sealed tip after reset = %s, want the base %s", got, line.BaseCommit)
	}
	// ...and --soft kept the dropped commit's content as the working change.
	w, _ := e.openWorkingChange(lineID)
	if got := treeFiles(t, e, w.HeadCommit); got["a.txt"] != "1\n" {
		t.Errorf("working tree after a soft reset = %v, want the un-sealed commit's files", got)
	}
	if err := e.Undo(); err != nil {
		t.Fatal(err)
	}
	// Engine.Undo restores line tips; re-pointing the open change's head is the
	// worktree layer's half (Repo.reconcileRewound). The restored tip is the
	// sealed commit itself, so the working change starts afresh on it.
	if err := e.SetWorkingHead(w.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.SealedTip(lineID); got != top {
		t.Fatalf("sealed tip after undo = %s, want %s", got, top)
	}

	// A second line, forked later: main's older history is below ITS base.
	laterBase := seedLineTip(t, e, main.ID, map[string][]byte{"base.txt": []byte("base\n"), "more.txt": []byte("m\n")})
	other, err := e.CreateLine("other", main.ID)
	if err != nil {
		t.Fatal(err)
	}
	oc := openChange(t, e, other.ID)
	if _, _, err := e.SnapshotWorking(oc, map[string]TreeEntry{"o.txt": blobEntry(t, e, "o\n")}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.Seal(oc, "other work"); err != nil {
		t.Fatal(err)
	}
	if laterBase == line.BaseCommit {
		t.Fatal("precondition: main should have moved")
	}
	if _, err := e.ResetLine(other.ID, line.BaseCommit, false); err == nil || !strings.Contains(err.Error(), "below") {
		t.Errorf("ResetLine below the base = %v, want a refusal", err)
	}
	// Onto a commit that is not in the line's history at all.
	if _, err := e.ResetLine(other.ID, top, true); err == nil || !strings.Contains(err.Error(), "not an ancestor") {
		t.Errorf("ResetLine onto an unrelated commit = %v, want a refusal", err)
	}
	// A child line pins the commit it is based on (and so everything a reset
	// below it would drop) — but not commits sealed after it forked.
	if _, err := e.CreateLine("child", lineID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.AmendTop(lineID, &msg, nil, false); err == nil || !strings.Contains(err.Error(), `child line "child"`) {
		t.Errorf("AmendTop of a child's base = %v", err)
	}
	if _, err := e.ResetLine(lineID, line.BaseCommit, true); err == nil || !strings.Contains(err.Error(), `child line "child"`) {
		t.Errorf("ResetLine dropping a child's base = %v", err)
	}
	snapshot(t, e, lineID, map[string]string{"a.txt": "1\n", "later.txt": "l\n"})
	wNow, _ := e.openWorkingChange(lineID)
	if _, _, err := e.Seal(wNow.ID, "after the fork"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.AmendTop(lineID, &msg, nil, false); err != nil {
		t.Errorf("AmendTop of a commit sealed after the child forked: %v", err)
	}
	if _, err := e.ResetLine(lineID, top, false); err != nil {
		t.Errorf("ResetLine back to (not below) the child's base: %v", err)
	}
}

// TestRootLineEditsStopAtTheRemote: on the root line, commit --amend and reset
// work on un-pushed commits and refuse — pointing at revert — once the commit
// is reachable from the line's remote-tracking ref.
func TestRootLineEditsStopAtTheRemote(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	w := openChange(t, e, main.ID)
	seal := func(files map[string]string, msg string) string {
		t.Helper()
		snapshot(t, e, main.ID, files)
		cur, _ := e.openWorkingChange(main.ID)
		if _, _, err := e.Seal(cur.ID, msg); err != nil {
			t.Fatal(err)
		}
		top, err := e.SealedTip(main.ID)
		if err != nil {
			t.Fatal(err)
		}
		return top
	}
	_ = w
	c1 := seal(map[string]string{"a.txt": "1\n"}, "one")
	c2 := seal(map[string]string{"a.txt": "2\n"}, "two")
	c3 := seal(map[string]string{"a.txt": "3\n"}, "three")

	// Nothing pushed: amend the top, then reset back two.
	snapshot(t, e, main.ID, map[string]string{"a.txt": "3 amended\n"})
	amended, err := e.AmendTop(main.ID, nil, nil, false)
	if err != nil {
		t.Fatalf("AmendTop on an un-pushed root commit: %v", err)
	}
	if got, _ := e.firstParent(amended); got != c2 {
		t.Errorf("amended commit's parent = %s, want %s (commits below the amend untouched)", got, c2)
	}
	if got := treeFiles(t, e, amended); got["a.txt"] != "3 amended\n" {
		t.Errorf("amended tree = %v", got)
	}
	_ = c3

	// Push up to c2.
	if err := e.AddRemote("origin", t.TempDir(), "git"); err != nil {
		t.Fatal(err)
	}
	if err := e.git.Storer.SetReference(plumbing.NewHashReference("refs/remotes/origin/main", plumbing.NewHash(c2))); err != nil {
		t.Fatal(err)
	}
	// The un-pushed top is still amendable and droppable...
	msg := "three, reworded"
	if _, err := e.AmendTop(main.ID, &msg, nil, false); err != nil {
		t.Errorf("AmendTop above the pushed tip: %v", err)
	}
	if _, err := e.ResetLine(main.ID, c2, false); err != nil {
		t.Errorf("ResetLine back to the pushed tip: %v", err)
	}
	// ...but c2 itself is published: neither amend nor a reset below it.
	if _, err := e.AmendTop(main.ID, &msg, nil, false); err == nil || !strings.Contains(err.Error(), "already on origin") || !strings.Contains(err.Error(), "cairn revert") {
		t.Errorf("AmendTop of a pushed root commit = %v, want the pushed-root refusal", err)
	}
	if _, err := e.ResetLine(main.ID, c1, true); err == nil || !strings.Contains(err.Error(), "already on origin") {
		t.Errorf("ResetLine below the pushed tip = %v, want the pushed-root refusal", err)
	}
}

// TestAmendTopKeepsChangeIDAndAuthor: the amended commit is the same change
// (so reword/drop/log still address it) by the same author.
func TestAmendTopKeepsChangeIDAndAuthor(t *testing.T) {
	e := newTestEngine(t)
	e.SetIdentity("First Author", "first@example.org")
	lineID := wcLine(t, e, map[string]string{"a.txt": "1\n"})
	top, _ := e.SealedTip(lineID)
	cid := e.changeIDOf(top)
	snapshot(t, e, lineID, map[string]string{"a.txt": "2\n"})

	e.SetIdentity("Second Person", "second@example.org")
	sealed, err := e.AmendTop(lineID, nil, nil, false)
	if err != nil {
		t.Fatalf("AmendTop: %v", err)
	}
	if got := e.changeIDOf(sealed); got != cid {
		t.Errorf("amended change-id = %s, want %s", got, cid)
	}
	info, _ := e.CommitInfoOf(sealed)
	if info.AuthorName != "First Author" || info.Subject != "first" {
		t.Errorf("amended commit = %+v, want the original author and message", info)
	}
	if got := treeFiles(t, e, sealed); got["a.txt"] != "2\n" {
		t.Errorf("amended tree = %v", got)
	}
	if ch, _ := e.GetChange(cid); ch.HeadCommit != sealed {
		t.Errorf("the sealed change row points at %s, want %s", ch.HeadCommit, sealed)
	}
	chain, _ := e.sealedChain(lineID)
	if len(chain) != 1 || chain[0].Commit != sealed {
		t.Errorf("sealed chain after amend = %+v", chain)
	}
	// --reset-author takes authorship.
	sealed2, err := e.AmendTop(lineID, nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := e.CommitInfoOf(sealed2); info.AuthorName != "Second Person" {
		t.Errorf("--reset-author: author = %q", info.AuthorName)
	}
}

// TestVerbClosesTheSnapshotBurst: a snapshot op that predates a working-copy
// verb must not be absorbed by a LATER seal — its view_before is from before
// the verb, so undoing that commit would silently rewind the verb too.
func TestVerbClosesTheSnapshotBurst(t *testing.T) {
	e := newTestEngine(t)
	lineID := wcLine(t, e, map[string]string{"a.txt": "1\n"})
	snapshot(t, e, lineID, map[string]string{"a.txt": "2\n", "b.txt": "b\n"}) // snapshot op X
	w, _ := e.openWorkingChange(lineID)

	// A partial commit of a.txt.
	tree, _ := e.WriteTree(map[string]TreeEntry{"a.txt": blobEntry(t, e, "2\n")})
	partial, err := e.AppendSealed(lineID, OpCommitPaths, AppendSpec{Message: "just a", Tree: tree})
	if err != nil {
		t.Fatal(err)
	}
	afterPartial := tipOf(t, e, lineID)

	// Then a full seal, with no new snapshot in between.
	if _, _, err := e.Seal(w.ID, "the rest"); err != nil {
		t.Fatal(err)
	}
	if err := e.Undo(); err != nil {
		t.Fatal(err)
	}
	if got := tipOf(t, e, lineID); got != afterPartial {
		t.Fatalf("undoing the seal put the tip at %s, want the post-partial-commit tip %s (the partial commit must survive)", got, afterPartial)
	}
	if got, _ := e.firstParent(tipOf(t, e, lineID)); got != partial.Commit {
		t.Errorf("the partial commit is no longer under the tip")
	}
	// A later burst still coalesces and is still absorbed, as before.
	ops, _ := e.OperationLog()
	closed := 0
	for _, op := range ops {
		if op.OpType == opSnapshot && strings.HasSuffix(op.Detail, "(closed)") {
			closed++
		}
	}
	if closed != 1 {
		t.Errorf("closed snapshot ops = %d, want exactly the one that predates the verb", closed)
	}
}

// TestAdoptWorkingTip: a stale "(working)" commit of a sealed change becomes
// the open change's working commit; real commits are left alone.
func TestAdoptWorkingTip(t *testing.T) {
	e := newTestEngine(t)
	lineID := wcLine(t, e, map[string]string{"a.txt": "1\n"})
	snapshot(t, e, lineID, map[string]string{"a.txt": "2\n"})
	old, _ := e.openWorkingChange(lineID)
	staleTip := old.HeadCommit
	newID, _, err := e.Seal(old.ID, "second")
	if err != nil {
		t.Fatal(err)
	}
	sealedTip := tipOf(t, e, lineID)

	// A sealed commit is not a working placeholder.
	if _, adopted, err := e.AdoptWorkingTip(newID, sealedTip); err != nil || adopted {
		t.Errorf("AdoptWorkingTip(sealed commit) = adopted %v, %v", adopted, err)
	}
	head, adopted, err := e.AdoptWorkingTip(newID, staleTip)
	if err != nil || !adopted {
		t.Fatalf("AdoptWorkingTip(stale working commit) = %v, %v", adopted, err)
	}
	if got := e.changeIDOf(head); got != newID {
		t.Errorf("adopted commit's change-id = %s, want the open change %s", got, newID)
	}
	staleParent, _ := e.firstParent(staleTip)
	if got, _ := e.firstParent(head); got != staleParent {
		t.Errorf("adopted commit's parent = %s, want %s", got, staleParent)
	}
	if got := treeFiles(t, e, head); got["a.txt"] != "2\n" {
		t.Errorf("adopted tree = %v", got)
	}
	if ch, _ := e.GetChange(newID); ch.HeadCommit != head || tipOf(t, e, lineID) != head {
		t.Error("the open change and line tip do not point at the adopted commit")
	}
	// Its own working commit needs no adopting.
	if _, adopted, _ := e.AdoptWorkingTip(newID, head); adopted {
		t.Error("AdoptWorkingTip re-adopted the change's own working commit")
	}
}

// TestCherryPickWithTransformsTheMessage backs `cherry-pick -x`.
func TestCherryPickWithTransformsTheMessage(t *testing.T) {
	e := newTestEngine(t)
	_, picked, _ := sealOne(t, e, "A", map[string]string{"f.txt": "F\n"}, "add f")
	main, _ := e.LineByName("main")
	openChange(t, e, main.ID)
	newID, cf, err := e.CherryPickWith(picked, main.ID, func(m string) string { return m + "\n\n(cherry picked from commit " + picked + ")" })
	if err != nil || len(cf) != 0 {
		t.Fatalf("CherryPickWith: %v %v", cf, err)
	}
	ch, _ := e.GetChange(newID)
	info, _ := e.CommitInfoOf(ch.HeadCommit)
	if info.Message != "add f\n\n(cherry picked from commit "+picked+")" {
		t.Errorf("picked message = %q", info.Message)
	}
}

// TestEditKeepsAdoptedParentAndFoldedHistory covers the two things a history
// edit must not do to a merge on the line's spine. (1) A sealed commit that
// adopted the parent line is a merge; rewriting it must keep the adopted tip
// in the line's ancestry (a single-parented rewrite would drop it — on the
// root line, after a fold, that is the root's own earlier commits). (2) A
// commit that came in with a fold belongs to the folded line and is never
// rewritten from the line it was folded into.
func TestEditKeepsAdoptedParentAndFoldedHistory(t *testing.T) {
	e := newTestEngine(t)
	lineID := wcLine(t, e, map[string]string{"a.txt": "1\n"})
	main, _ := e.LineByName("main")
	// main moves on; the child's next seal adopts it (a merge-forward commit).
	mainTip := seedLineTip(t, e, main.ID, map[string][]byte{"base.txt": []byte("base\n"), "m.txt": []byte("m\n")})
	snapshot(t, e, lineID, map[string]string{"a.txt": "1\n", "b.txt": "b\n"})
	w, _ := e.openWorkingChange(lineID)
	if _, cf, err := e.Seal(w.ID, "adopts main"); err != nil || len(cf) != 0 {
		t.Fatalf("Seal: %v %v", cf, err)
	}
	adopting, _ := e.SealedTip(lineID)
	if anc, _ := e.isAncestor(mainTip, adopting); !anc {
		t.Fatal("precondition: the seal should have adopted main's tip")
	}
	chain, _ := e.sealedChain(lineID)
	if n := len(chain); n != 2 || len(chain[1].Extra) != 1 || chain[1].Extra[0] != mainTip {
		t.Fatalf("chain = %+v, want the adopting step to carry main's tip as an extra parent", chain)
	}
	if chain[1].Parent != chain[0].Commit {
		t.Errorf("step.Parent = %s, want the previous step %s", chain[1].Parent, chain[0].Commit)
	}

	// Reword the FIRST commit: the adopting commit above it is rewritten.
	if _, err := e.Reword(chain[0].Commit, "first, reworded"); err != nil {
		t.Fatalf("Reword: %v", err)
	}
	top, _ := e.SealedTip(lineID)
	if top == adopting {
		t.Fatal("the reword did not rewrite the commit above the edit point")
	}
	if anc, _ := e.isAncestor(mainTip, top); !anc {
		t.Error("rewriting an adopting commit dropped the adopted parent tip from the line's ancestry")
	}
	if got := treeFiles(t, e, top); got["m.txt"] != "m\n" || got["b.txt"] != "b\n" {
		t.Errorf("tree after the reword = %v", got)
	}

	// Fold the line into main: its commits are now main's tip — and immutable.
	if err := e.FoldLine(lineID); err != nil {
		t.Fatalf("FoldLine: %v", err)
	}
	if cur, err := e.openWorkingChange(main.ID); err == nil {
		// Start main's working change afresh on the folded tip, as a newly
		// expressed folder would.
		if err := e.SetWorkingHead(cur.ID, ""); err != nil {
			t.Fatal(err)
		}
	} else {
		openChange(t, e, main.ID)
	}
	msg := "x"
	if _, err := e.AmendTop(main.ID, &msg, nil, false); err == nil || !strings.Contains(err.Error(), "fold") || !strings.Contains(err.Error(), "cairn revert") {
		t.Errorf("AmendTop of a folded-in commit = %v, want the folded-history refusal", err)
	}
	if _, err := e.ResetLine(main.ID, mainTip, true); err == nil || !strings.Contains(err.Error(), "fold") {
		t.Errorf("ResetLine dropping folded-in commits = %v, want the folded-history refusal", err)
	}
}
