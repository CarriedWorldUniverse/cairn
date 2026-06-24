package change

import (
	"strings"
	"testing"
)

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
		if _, _, err := e.SnapshotWorking(cur, map[string]TreeEntry{
			files[i]: blobEntry(t, e, m+" content\n"),
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

	// Root-line sealed commit → refused (cannot edit the root line).
	main, _ := e.LineByName("main")
	rootTip := seedLineTip(t, e, main.ID, map[string][]byte{"r.txt": []byte("r\n")})
	if _, _, _, err := e.guardEditable(rootTip); err == nil {
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
