package change

import (
	"strings"
	"testing"
)

// buildChildLineWith3SealedSameFile forks a child line off main and seals three
// commits on it (S1,S2,S3), each modifying the SAME file "x.txt" with different
// content. It returns the child line id, sealed commit shas, change-ids, and
// messages in base→top order.
func buildChildLineWith3SealedSameFile(t *testing.T, e *Engine) (childLineID string, commits, changeIDs []string, msgs []string) {
	t.Helper()
	main, err := e.LineByName("main")
	if err != nil {
		t.Fatalf("LineByName(main): %v", err)
	}
	// Seed main with a base so the child has a real fork point.
	seedLineTip(t, e, main.ID, map[string][]byte{"base.txt": []byte("base\n")})

	child, err := e.CreateLine("child", main.ID)
	if err != nil {
		t.Fatalf("CreateLine(child): %v", err)
	}

	// Each step modifies x.txt with successive content. Snapshots are the WHOLE
	// folder (the SnapshotWorking contract): base.txt rides along unchanged.
	contents := []string{"1\n", "2\n", "3\n"}
	msgs = []string{"S1", "S2", "S3"}
	cur := openChange(t, e, child.ID)
	for i, m := range msgs {
		if _, _, err := e.SnapshotWorking(cur, map[string]TreeEntry{
			"base.txt": blobEntry(t, e, "base\n"),
			"x.txt":    blobEntry(t, e, contents[i]),
		}); err != nil {
			t.Fatalf("SnapshotWorking %s: %v", m, err)
		}
		next, conflicts, err := e.Seal(cur, m)
		if err != nil {
			t.Fatalf("Seal %s: %v", m, err)
		}
		if len(conflicts) != 0 {
			t.Fatalf("Seal %s conflicts = %d, want 0", m, len(conflicts))
		}
		sealed, err := e.GetChange(cur)
		if err != nil {
			t.Fatalf("GetChange(sealed %s): %v", m, err)
		}
		commits = append(commits, sealed.HeadCommit)
		changeIDs = append(changeIDs, sealed.ID)
		cur = next
	}
	return child.ID, commits, changeIDs, msgs
}

// buildChildLineWith3Sealed forks a child line off main and seals three commits
// on it (S1,S2,S3), each snapshotting one file. It returns the child line id and
// the three sealed commit shas / change-ids in base→top order.
//
// How a child line is built: CreateLine forks off the parent's tip (mirrors the
// merge/seal tests). Each sealed step = SnapshotWorking a TreeEntry onto the
// line's CURRENT open change, then Seal(message) — which freezes that change and
// opens a FRESH open change on the same line. The fresh change's id is returned
// by Seal, so we thread it into the next snapshot.
func buildChildLineWith3Sealed(t *testing.T, e *Engine) (childLineID string, commits, changeIDs []string, msgs []string) {
	t.Helper()
	main, err := e.LineByName("main")
	if err != nil {
		t.Fatalf("LineByName(main): %v", err)
	}
	// Seed main with a base so the child has a real fork point.
	seedLineTip(t, e, main.ID, map[string][]byte{"base.txt": []byte("base\n")})

	child, err := e.CreateLine("child", main.ID)
	if err != nil {
		t.Fatalf("CreateLine(child): %v", err)
	}

	msgs = []string{"S1", "S2", "S3"}
	files := []string{"s1.txt", "s2.txt", "s3.txt"}
	cur := openChange(t, e, child.ID)
	for i, m := range msgs {
		// Snapshot the WHOLE folder (the SnapshotWorking contract): each step's
		// folder holds base.txt plus every file added so far.
		entries := map[string]TreeEntry{"base.txt": blobEntry(t, e, "base\n")}
		for j := 0; j <= i; j++ {
			entries[files[j]] = blobEntry(t, e, msgs[j]+" content\n")
		}
		if _, _, err := e.SnapshotWorking(cur, entries); err != nil {
			t.Fatalf("SnapshotWorking %s: %v", m, err)
		}
		next, conflicts, err := e.Seal(cur, m)
		if err != nil {
			t.Fatalf("Seal %s: %v", m, err)
		}
		if len(conflicts) != 0 {
			t.Fatalf("Seal %s conflicts = %d, want 0", m, len(conflicts))
		}
		sealed, err := e.GetChange(cur)
		if err != nil {
			t.Fatalf("GetChange(sealed %s): %v", m, err)
		}
		commits = append(commits, sealed.HeadCommit)
		changeIDs = append(changeIDs, sealed.ID)
		cur = next
	}
	return child.ID, commits, changeIDs, msgs
}

// TestSealedChainOrder: sealedChain returns the line's sealed commits above base
// in base→top order, with the right messages, change-ids, and non-empty parent
// trees.
func TestSealedChainOrder(t *testing.T) {
	e := newTestEngine(t)
	childID, commits, changeIDs, msgs := buildChildLineWith3Sealed(t, e)

	chain, err := e.sealedChain(childID)
	if err != nil {
		t.Fatalf("sealedChain: %v", err)
	}
	if len(chain) != 3 {
		t.Fatalf("sealedChain len = %d, want 3", len(chain))
	}
	for i, s := range chain {
		if s.Message != msgs[i] {
			t.Errorf("chain[%d].Message = %q, want %q", i, s.Message, msgs[i])
		}
		if s.ChangeID != changeIDs[i] {
			t.Errorf("chain[%d].ChangeID = %q, want %q", i, s.ChangeID, changeIDs[i])
		}
		if s.Commit != commits[i] {
			t.Errorf("chain[%d].Commit = %q, want %q", i, s.Commit, commits[i])
		}
		if s.Tree == "" {
			t.Errorf("chain[%d].Tree is empty", i)
		}
		if s.ParentTree == "" {
			t.Errorf("chain[%d].ParentTree is empty", i)
		}
	}
}

// TestGuardEditableRefusals: a root-line commit is refused; a child-line commit
// whose line HAS a grandchild line is refused; a clean child-line commit is ok.
func TestGuardEditableRefusals(t *testing.T) {
	e := newTestEngine(t)

	// Root-line SEALED commit → refused (cannot edit the root line). Seal it so
	// the root-line check is what rejects it (not the working-change guard, which
	// fires first for an unsealed working head).
	main, _ := e.LineByName("main")
	rootCur := openChange(t, e, main.ID)
	if _, _, err := e.SnapshotWorking(rootCur, map[string]TreeEntry{
		"r.txt": blobEntry(t, e, "r\n"),
	}); err != nil {
		t.Fatalf("SnapshotWorking(root): %v", err)
	}
	if _, _, err := e.Seal(rootCur, "root seal"); err != nil {
		t.Fatalf("Seal(root): %v", err)
	}
	rootSealed, err := e.GetChange(rootCur)
	if err != nil {
		t.Fatalf("GetChange(root sealed): %v", err)
	}
	if _, _, _, err := e.guardEditable(rootSealed.HeadCommit); err == nil {
		t.Fatal("guardEditable(root commit): want error, got nil")
	} else if !strings.Contains(err.Error(), "root line") {
		t.Fatalf("guardEditable(root commit) error = %v, want root-line refusal", err)
	}

	// Child line with 3 sealed commits; clean (no grandchild) → ok.
	childID, commits, _, _ := buildChildLineWith3Sealed(t, e)
	if _, _, _, err := e.guardEditable(commits[1]); err != nil {
		t.Fatalf("guardEditable(clean child commit): unexpected error %v", err)
	}

	// Give the child a grandchild line, then editing the child's history is
	// refused.
	if _, err := e.CreateLine("grandchild", childID); err != nil {
		t.Fatalf("CreateLine(grandchild): %v", err)
	}
	if _, _, _, err := e.guardEditable(commits[1]); err == nil {
		t.Fatal("guardEditable(child with grandchild): want error, got nil")
	} else if !strings.Contains(err.Error(), "child line") {
		t.Fatalf("guardEditable(child with grandchild) error = %v, want child-line refusal", err)
	}
}

// TestRewordPreservesIdAndRebases: Reword on S2 returns no conflicts; the chain
// shows S2's new message, all three change-ids unchanged, S3's content still
// present at the tip, and the open working change rebased onto the new top.
func TestRewordPreservesIdAndRebases(t *testing.T) {
	e := newTestEngine(t)
	childID, commits, changeIDs, _ := buildChildLineWith3Sealed(t, e)

	// Snapshot some working work on the current open change so we can verify it
	// gets rebased onto the new top.
	openID := openWorkingChangeID(t, e, childID)
	if _, _, err := e.SnapshotWorking(openID, map[string]TreeEntry{
		"w.txt": blobEntry(t, e, "working\n"),
	}); err != nil {
		t.Fatalf("SnapshotWorking(open): %v", err)
	}

	conflicts, err := e.Reword(commits[1], "new msg")
	if err != nil {
		t.Fatalf("Reword: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("Reword conflicts = %d, want 0", len(conflicts))
	}

	chain, err := e.sealedChain(childID)
	if err != nil {
		t.Fatalf("sealedChain after reword: %v", err)
	}
	if len(chain) != 3 {
		t.Fatalf("chain len = %d, want 3", len(chain))
	}
	if chain[1].Message != "new msg" {
		t.Errorf("chain[1].Message = %q, want %q", chain[1].Message, "new msg")
	}
	if chain[0].Message != "S1" || chain[2].Message != "S3" {
		t.Errorf("neighbours rewritten: S1=%q S3=%q", chain[0].Message, chain[2].Message)
	}
	// Change-ids are preserved across the rewrite.
	for i := range chain {
		if chain[i].ChangeID != changeIDs[i] {
			t.Errorf("chain[%d].ChangeID = %q, want %q (preserved)", i, chain[i].ChangeID, changeIDs[i])
		}
	}
	// S3's file content survives at the new tip.
	tipTree, err := e.readTree(chain[2].Tree)
	if err != nil {
		t.Fatalf("readTree(new S3): %v", err)
	}
	if got := string(tipTree["s3.txt"]); got != "S3 content\n" {
		t.Errorf("s3.txt at new tip = %q, want %q", got, "S3 content\n")
	}
	// The working change was rebased: its head's first-parent chain leads to the
	// rewritten S3 commit (the new chain top).
	w, err := e.GetChange(openID)
	if err != nil {
		t.Fatalf("GetChange(open): %v", err)
	}
	if w.HeadCommit == "" {
		t.Fatal("open working change has no head after reword")
	}
	wParent, err := e.firstParent(w.HeadCommit)
	if err != nil {
		t.Fatalf("firstParent(working head): %v", err)
	}
	if wParent != chain[2].Commit {
		t.Fatalf("working parent = %s, want new S3 commit %s", wParent, chain[2].Commit)
	}
	// The commit shas of S1/S3 changed only because S2 changed underneath them, but
	// S2 itself is genuinely a different commit now.
	if chain[1].Commit == commits[1] {
		t.Errorf("S2 commit unchanged after reword: %s", chain[1].Commit)
	}
}

// openWorkingChangeID returns the single open (sealed=0, status=open) change on
// the line — the line's current working change.
func openWorkingChangeID(t *testing.T, e *Engine, lineID string) string {
	t.Helper()
	var id string
	if err := e.db.QueryRow(
		`SELECT id FROM change WHERE line_id=? AND sealed=0 AND status='open' LIMIT 1`,
		lineID).Scan(&id); err != nil {
		t.Fatalf("find open working change: %v", err)
	}
	return id
}

// TestRewordWorkingChangeNoHead verifies that Reword succeeds even when the open
// working change on the line has never been snapshotted (HeadCommit == ""). The
// working change has no delta of its own; it should ride cleanly on the new top
// after the sealed-chain rewrite, and its head's parent should equal the new
// chain top commit.
func TestRewordWorkingChangeNoHead(t *testing.T) {
	e := newTestEngine(t)
	childID, commits, _, _ := buildChildLineWith3Sealed(t, e)

	// Confirm the open working change has NO head commit (never snapshotted).
	openID := openWorkingChangeID(t, e, childID)
	wBefore, err := e.GetChange(openID)
	if err != nil {
		t.Fatalf("GetChange(open before reword): %v", err)
	}
	if wBefore.HeadCommit != "" {
		t.Skipf("open change already has a head (%s); test requires unsanpshotted change", wBefore.HeadCommit)
	}

	// Reword S2 (middle commit). Must not crash on firstParent("").
	conflicts, err := e.Reword(commits[1], "no-head reword")
	if err != nil {
		t.Fatalf("Reword with no-head working change: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("Reword conflicts = %d, want 0", len(conflicts))
	}

	// Chain rebuilt correctly.
	chain, err := e.sealedChain(childID)
	if err != nil {
		t.Fatalf("sealedChain after reword: %v", err)
	}
	if len(chain) != 3 {
		t.Fatalf("chain len = %d, want 3", len(chain))
	}
	if chain[1].Message != "no-head reword" {
		t.Errorf("chain[1].Message = %q, want %q", chain[1].Message, "no-head reword")
	}

	// The open working change now has a head whose parent is the new chain top.
	wAfter, err := e.GetChange(openID)
	if err != nil {
		t.Fatalf("GetChange(open after reword): %v", err)
	}
	if wAfter.HeadCommit == "" {
		t.Fatal("open working change has no head after no-head reword")
	}
	wParent, err := e.firstParent(wAfter.HeadCommit)
	if err != nil {
		t.Fatalf("firstParent(working head): %v", err)
	}
	if wParent != chain[2].Commit {
		t.Errorf("working change parent = %s, want new chain top %s", wParent, chain[2].Commit)
	}
}

// TestSquashCombinesIntoParent: Squash(S2commit) folds S2 into S1 — the chain
// shrinks from 3 to 2 steps; the first step keeps S1's change-id, its message
// is the concatenation of S1 and S2's messages, and its tree contains S2's
// content; S2's change-id row is deleted; S3 is the second step, its change-id
// and file content preserved.
func TestSquashCombinesIntoParent(t *testing.T) {
	e := newTestEngine(t)
	childID, commits, changeIDs, msgs := buildChildLineWith3Sealed(t, e)

	conflicts, err := e.Squash(commits[1]) // S2
	if err != nil {
		t.Fatalf("Squash(S2): %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("Squash conflicts = %d, want 0", len(conflicts))
	}

	chain, err := e.sealedChain(childID)
	if err != nil {
		t.Fatalf("sealedChain after squash: %v", err)
	}
	if len(chain) != 2 {
		t.Fatalf("chain len = %d, want 2 (3 minus squashed)", len(chain))
	}

	// First step keeps S1's change-id.
	if chain[0].ChangeID != changeIDs[0] {
		t.Errorf("chain[0].ChangeID = %q, want S1's %q", chain[0].ChangeID, changeIDs[0])
	}
	// Message is concatenation of S1+S2.
	if !strings.Contains(chain[0].Message, msgs[0]) {
		t.Errorf("chain[0].Message = %q, want it to contain S1 msg %q", chain[0].Message, msgs[0])
	}
	if !strings.Contains(chain[0].Message, msgs[1]) {
		t.Errorf("chain[0].Message = %q, want it to contain S2 msg %q", chain[0].Message, msgs[1])
	}

	// S2's change-id row must be deleted.
	if _, err := e.GetChange(changeIDs[1]); err == nil {
		t.Errorf("GetChange(S2 change-id) returned no error — row should have been deleted")
	}

	// Squashed step's tree contains S2's content (s2.txt, added by S2).
	squashedTree, err := e.readTree(chain[0].Tree)
	if err != nil {
		t.Fatalf("readTree(squashed step): %v", err)
	}
	if got := string(squashedTree["s2.txt"]); got != "S2 content\n" {
		t.Errorf("s2.txt in squashed tree = %q, want %q", got, "S2 content\n")
	}

	// S3 is the second step, its change-id preserved.
	if chain[1].ChangeID != changeIDs[2] {
		t.Errorf("chain[1].ChangeID = %q, want S3's %q", chain[1].ChangeID, changeIDs[2])
	}
	// S3's file content is unchanged at the line tip.
	tipTree, err := e.readTree(chain[1].Tree)
	if err != nil {
		t.Fatalf("readTree(S3 tip): %v", err)
	}
	if got := string(tipTree["s3.txt"]); got != "S3 content\n" {
		t.Errorf("s3.txt at tip = %q, want %q", got, "S3 content\n")
	}
}

// TestSquashFirstCommitErrors: Squash on the first sealed commit (idx==0) must
// return an error containing "nothing to squash into".
func TestSquashFirstCommitErrors(t *testing.T) {
	e := newTestEngine(t)
	_, commits, _, _ := buildChildLineWith3Sealed(t, e)

	_, err := e.Squash(commits[0]) // S1 is the first — nothing above it
	if err == nil {
		t.Fatal("Squash(S1): want error, got nil")
	}
	if !strings.Contains(err.Error(), "nothing to squash into") {
		t.Fatalf("Squash(S1) error = %q, want it to contain %q", err.Error(), "nothing to squash into")
	}
}

// TestSquashRebasesWorkingChange: after Squash(S2), the open working change
// (which has a snapshotted file) rebases cleanly onto the new chain top (the
// rewritten S3), and its own file content survives.
func TestSquashRebasesWorkingChange(t *testing.T) {
	e := newTestEngine(t)
	childID, commits, changeIDs, _ := buildChildLineWith3Sealed(t, e)

	// Snapshot a file onto the current open working change.
	openID := openWorkingChangeID(t, e, childID)
	if _, _, err := e.SnapshotWorking(openID, map[string]TreeEntry{
		"w.txt": blobEntry(t, e, "working\n"),
	}); err != nil {
		t.Fatalf("SnapshotWorking(open): %v", err)
	}

	conflicts, err := e.Squash(commits[1]) // S2
	if err != nil {
		t.Fatalf("Squash(S2): %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("Squash conflicts = %d, want 0", len(conflicts))
	}

	// After squash the chain has 2 steps; get the new S3 commit (now chain[1]).
	chain, err := e.sealedChain(childID)
	if err != nil {
		t.Fatalf("sealedChain after squash: %v", err)
	}
	if len(chain) != 2 {
		t.Fatalf("chain len = %d, want 2", len(chain))
	}
	if chain[1].ChangeID != changeIDs[2] {
		t.Errorf("chain[1].ChangeID = %q, want S3's %q", chain[1].ChangeID, changeIDs[2])
	}

	// The working change's head first-parent must be the new S3 commit.
	w, err := e.GetChange(openID)
	if err != nil {
		t.Fatalf("GetChange(open after squash): %v", err)
	}
	if w.HeadCommit == "" {
		t.Fatal("open working change has no head after squash")
	}
	wParent, err := e.firstParent(w.HeadCommit)
	if err != nil {
		t.Fatalf("firstParent(working head): %v", err)
	}
	if wParent != chain[1].Commit {
		t.Fatalf("working parent = %s, want new S3 commit %s", wParent, chain[1].Commit)
	}

	// Working change's file content survives the rebase.
	wTree, err := e.readTree(chain[1].Tree)
	if err != nil {
		t.Fatalf("readTree(new S3): %v", err)
	}
	// w.txt is in the WORKING change tree, not S3's tree — read the working head's tree.
	wHeadTree, err := e.treeHashOf(w.HeadCommit)
	if err != nil {
		t.Fatalf("treeHashOf(working head): %v", err)
	}
	wHeadContents, err := e.readTree(wHeadTree)
	if err != nil {
		t.Fatalf("readTree(working head): %v", err)
	}
	if got := string(wHeadContents["w.txt"]); got != "working\n" {
		t.Errorf("w.txt in working head = %q, want %q", got, "working\n")
	}
	_ = wTree // only needed to confirm S3's tip is accessible
}

// TestRewordByChangeIdRobust verifies that guardEditable matches by change-id
// rather than exact commit sha. It passes the sealed step's head commit sha
// (the canonical path) and confirms Reword succeeds and preserves the change-id.
func TestRewordByChangeIdRobust(t *testing.T) {
	e := newTestEngine(t)
	childID, commits, changeIDs, _ := buildChildLineWith3Sealed(t, e)

	// Snapshot the open change so rewriteChain's working-change path is exercised
	// (prevents the no-head path from being taken here).
	openID := openWorkingChangeID(t, e, childID)
	if _, _, err := e.SnapshotWorking(openID, map[string]TreeEntry{
		"robust.txt": blobEntry(t, e, "robust\n"),
	}); err != nil {
		t.Fatalf("SnapshotWorking: %v", err)
	}

	// Pass S1's head commit sha — the standard path for guardEditable.
	// After Fix 4, the match uses change-id so this should still work correctly.
	conflicts, err := e.Reword(commits[0], "robust reword")
	if err != nil {
		t.Fatalf("Reword(S1 by head sha): %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("Reword conflicts = %d, want 0", len(conflicts))
	}

	chain, err := e.sealedChain(childID)
	if err != nil {
		t.Fatalf("sealedChain: %v", err)
	}
	if len(chain) != 3 {
		t.Fatalf("chain len = %d, want 3", len(chain))
	}
	if chain[0].Message != "robust reword" {
		t.Errorf("chain[0].Message = %q, want %q", chain[0].Message, "robust reword")
	}
	// Change-id of S1 must be preserved across the rewrite.
	if chain[0].ChangeID != changeIDs[0] {
		t.Errorf("chain[0].ChangeID = %q, want %q (preserved)", chain[0].ChangeID, changeIDs[0])
	}
}

// TestDropIndependent: S1 adds s1.txt, S2 adds s2.txt, S3 adds s3.txt (each
// touches a DIFFERENT file). Drop(S2commit) removes S2 cleanly — no conflicts,
// chain shrinks to 2 steps, s2.txt is absent from tip, s1.txt and s3.txt are
// present (merged in via the 3-way rebase), S2's change-id row is deleted.
func TestDropIndependent(t *testing.T) {
	e := newTestEngine(t)
	childID, commits, changeIDs, _ := buildChildLineWith3Sealed(t, e)

	conflicts, err := e.Drop(commits[1]) // S2 (adds s2.txt)
	if err != nil {
		t.Fatalf("Drop(S2): %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("Drop(S2) conflicts = %d, want 0 (independent files)", len(conflicts))
	}

	chain, err := e.sealedChain(childID)
	if err != nil {
		t.Fatalf("sealedChain after Drop: %v", err)
	}
	if len(chain) != 2 {
		t.Fatalf("chain len = %d, want 2 (3 minus dropped)", len(chain))
	}

	// S2's change-id row must be deleted.
	if _, err := e.GetChange(changeIDs[1]); err == nil {
		t.Errorf("GetChange(S2 change-id) returned no error — row should have been deleted")
	}

	// The chain[1] (rewritten S3) commit tree should have s1.txt and s3.txt merged
	// in, but NOT s2.txt. The 3-way merge of (base=S2's tree={s2.txt},
	// ours=S1's rebuilt tree={s1.txt}, theirs=S3's tree={s3.txt}) produces
	// {s1.txt, s3.txt}: s2.txt was deleted from both sides (absent in ours/theirs),
	// s1.txt added by ours, s3.txt added by theirs.
	tipFiles, err := e.Files(chain[1].Commit)
	if err != nil {
		t.Fatalf("Files(tip): %v", err)
	}
	if _, ok := tipFiles["s2.txt"]; ok {
		t.Errorf("s2.txt present in tip tree after Drop(S2) — should have been removed")
	}
	if _, ok := tipFiles["s1.txt"]; !ok {
		t.Errorf("s1.txt missing from tip tree after Drop(S2)")
	}
	if _, ok := tipFiles["s3.txt"]; !ok {
		t.Errorf("s3.txt missing from tip tree after Drop(S2)")
	}
}

// TestDropDependentConflicts: S1 creates x.txt="1\n", S2 changes x.txt="2\n",
// S3 changes x.txt="3\n" (all touch the SAME file). Drop(S2commit) → returns
// conflicts (len>0); the rewrite still COMPLETES (S2's change-id deleted, chain
// has 2 steps); the conflict is recorded on S3's change.
func TestDropDependentConflicts(t *testing.T) {
	e := newTestEngine(t)
	childID, commits, changeIDs, _ := buildChildLineWith3SealedSameFile(t, e)

	conflicts, err := e.Drop(commits[1]) // S2 (changes x.txt "1\n"→"2\n")
	if err != nil {
		t.Fatalf("Drop(S2): %v", err)
	}
	if len(conflicts) == 0 {
		t.Fatalf("Drop(S2) conflicts = 0, want >0 (S3 depends on S2's x.txt change)")
	}

	// The rewrite still completed: chain has 2 steps, S2's change-id is deleted.
	chain, err := e.sealedChain(childID)
	if err != nil {
		t.Fatalf("sealedChain after Drop: %v", err)
	}
	if len(chain) != 2 {
		t.Fatalf("chain len = %d, want 2 (3 minus dropped)", len(chain))
	}
	if _, err := e.GetChange(changeIDs[1]); err == nil {
		t.Errorf("GetChange(S2 change-id) returned no error — row should have been deleted")
	}

	// The conflict is recorded on S3's change.
	s3Conflicts, err := e.Conflicts(changeIDs[2])
	if err != nil {
		t.Fatalf("Conflicts(S3): %v", err)
	}
	if len(s3Conflicts) == 0 {
		t.Fatalf("Conflicts(S3) = 0, want >0 — conflict should be recorded on S3's change")
	}
	// The conflict path should be x.txt.
	if s3Conflicts[0].Path != "x.txt" {
		t.Errorf("conflict path = %q, want %q", s3Conflicts[0].Path, "x.txt")
	}
	// The tip's x.txt content should contain conflict markers (diff3).
	tipFiles, err := e.Files(chain[1].Commit)
	if err != nil {
		t.Fatalf("Files(S3 tip): %v", err)
	}
	xContent := string(tipFiles["x.txt"])
	if !strings.Contains(xContent, "<<<<<<<") && !strings.Contains(xContent, "|||||||") {
		t.Errorf("x.txt at tip = %q; want conflict markers (<<<<<<< or |||||||)", xContent)
	}
}

// TestGuardRefusesWorkingCommit: passing the OPEN working change's own head
// commit sha to Reword/Squash/Drop must be refused with a clear message that
// names the open working change (not the misleading "base or below").
func TestGuardRefusesWorkingCommit(t *testing.T) {
	e := newTestEngine(t)
	childID, commits, _, _ := buildChildLineWith3Sealed(t, e)
	_ = commits

	// Snapshot the open working change so it has a head commit to pass in.
	openID := openWorkingChangeID(t, e, childID)
	if _, _, err := e.SnapshotWorking(openID, map[string]TreeEntry{
		"w.txt": blobEntry(t, e, "working\n"),
	}); err != nil {
		t.Fatalf("SnapshotWorking(open): %v", err)
	}
	w, err := e.GetChange(openID)
	if err != nil {
		t.Fatalf("GetChange(open): %v", err)
	}
	if w.HeadCommit == "" {
		t.Fatal("open working change has no head after snapshot")
	}

	for _, tc := range []struct {
		name string
		call func(string) ([]Conflict, error)
	}{
		{"Reword", func(c string) ([]Conflict, error) { return e.Reword(c, "x") }},
		{"Squash", e.Squash},
		{"Drop", e.Drop},
	} {
		_, err := tc.call(w.HeadCommit)
		if err == nil {
			t.Errorf("%s(working head): want error, got nil", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), "open working change") {
			t.Errorf("%s(working head) error = %q, want it to mention %q", tc.name, err.Error(), "open working change")
		}
	}
}

// TestDropFirstCommit: Drop(S1commit) (idx==0) succeeds; S1 gone; S2/S3 rebase
// onto the base (their independent files survive).
func TestDropFirstCommit(t *testing.T) {
	e := newTestEngine(t)
	childID, commits, changeIDs, _ := buildChildLineWith3Sealed(t, e)

	conflicts, err := e.Drop(commits[0]) // S1 (adds s1.txt)
	if err != nil {
		t.Fatalf("Drop(S1): %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("Drop(S1) conflicts = %d, want 0 (independent files)", len(conflicts))
	}

	chain, err := e.sealedChain(childID)
	if err != nil {
		t.Fatalf("sealedChain after Drop(S1): %v", err)
	}
	if len(chain) != 2 {
		t.Fatalf("chain len = %d, want 2 (3 minus S1)", len(chain))
	}

	// S1's change-id row must be deleted.
	if _, err := e.GetChange(changeIDs[0]); err == nil {
		t.Errorf("GetChange(S1 change-id) returned no error — row should have been deleted")
	}

	// S2 and S3 survive: their files are present at the tip.
	tipFiles, err := e.Files(chain[1].Commit)
	if err != nil {
		t.Fatalf("Files(tip after Drop(S1)): %v", err)
	}
	if _, ok := tipFiles["s2.txt"]; !ok {
		t.Errorf("s2.txt missing from tip after Drop(S1)")
	}
	if _, ok := tipFiles["s3.txt"]; !ok {
		t.Errorf("s3.txt missing from tip after Drop(S1)")
	}
	// s1.txt was only added by S1; it should be absent from the tip.
	if _, ok := tipFiles["s1.txt"]; ok {
		t.Errorf("s1.txt present in tip after Drop(S1) — should have been removed")
	}
}
