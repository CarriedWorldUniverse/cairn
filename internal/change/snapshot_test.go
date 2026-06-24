package change

import (
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

// changeIDOf reads the Change-Id trailer from a commit's message (test helper).
func changeIDOf(t *testing.T, e *Engine, sha string) string {
	t.Helper()
	c, err := e.git.CommitObject(plumbing.NewHash(sha))
	if err != nil {
		t.Fatalf("CommitObject %s: %v", sha, err)
	}
	const marker = "\n\nChange-Id: "
	i := strings.LastIndex(c.Message, marker)
	if i < 0 {
		t.Fatalf("commit %s has no Change-Id trailer: %q", sha, c.Message)
	}
	return strings.TrimSpace(c.Message[i+len(marker):])
}

// openChange creates an open change on the given line and returns its id.
func openChange(t *testing.T, e *Engine, lineID string) string {
	t.Helper()
	ch, err := e.CreateChange(lineID, "agent")
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	return ch.ID
}

// blobEntry writes data as a blob and returns a regular-mode TreeEntry for it.
func blobEntry(t *testing.T, e *Engine, data string) TreeEntry {
	t.Helper()
	sha, err := e.WriteBlob([]byte(data))
	if err != nil {
		t.Fatalf("WriteBlob: %v", err)
	}
	return TreeEntry{SHA: sha, Mode: ModeRegular}
}

func TestSnapshotWorkingAmends(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	id := openChange(t, e, main.ID)

	// First snapshot: a -> blob1.
	changed, head1, err := e.SnapshotWorking(id, map[string]TreeEntry{"a": blobEntry(t, e, "one")})
	if err != nil {
		t.Fatalf("SnapshotWorking 1: %v", err)
	}
	if !changed {
		t.Fatal("first snapshot: changed=false, want true")
	}
	if head1 == "" {
		t.Fatal("first snapshot: empty head")
	}
	ch, _ := e.GetChange(id)
	if ch.HeadCommit != head1 {
		t.Fatalf("change head = %s, want %s", ch.HeadCommit, head1)
	}
	mainAfter, _ := e.LineByName("main")
	if mainAfter.TipCommit != head1 {
		t.Fatalf("line tip = %s, want head %s", mainAfter.TipCommit, head1)
	}

	// Same content: no-op.
	changed, head2, err := e.SnapshotWorking(id, map[string]TreeEntry{"a": blobEntry(t, e, "one")})
	if err != nil {
		t.Fatalf("SnapshotWorking 2: %v", err)
	}
	if changed {
		t.Fatal("identical snapshot: changed=true, want false (no-op)")
	}
	if head2 != head1 {
		t.Fatalf("identical snapshot head = %s, want unchanged %s", head2, head1)
	}

	// Different content: amend in place.
	changed, head3, err := e.SnapshotWorking(id, map[string]TreeEntry{"a": blobEntry(t, e, "two")})
	if err != nil {
		t.Fatalf("SnapshotWorking 3: %v", err)
	}
	if !changed {
		t.Fatal("changed snapshot: changed=false, want true")
	}
	if head3 == head1 {
		t.Fatal("changed snapshot produced same head; want a new commit")
	}

	// Same change-id across the amend.
	cid1 := changeIDOf(t, e, head1)
	cid3 := changeIDOf(t, e, head3)
	if cid1 != cid3 || cid1 != id {
		t.Fatalf("change-id changed across amend: %s vs %s (want %s)", cid1, cid3, id)
	}

	// AMEND not append: the new head's first-parent equals the old head's
	// first-parent (parent preserved). An append would set parent=head1.
	p1, err := e.firstParent(head1)
	if err != nil {
		t.Fatalf("firstParent head1: %v", err)
	}
	p3, err := e.firstParent(head3)
	if err != nil {
		t.Fatalf("firstParent head3: %v", err)
	}
	if p3 != p1 {
		t.Fatalf("parent not preserved: firstParent(head3)=%s != firstParent(head1)=%s (this would be an APPEND)", p3, p1)
	}
	if p3 == head1 {
		t.Fatalf("head3 was APPENDED onto head1 (parent=%s); want AMEND (parent preserved)", head1)
	}
}

func TestSnapshotWorkingFreshChangeEmpty(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	id := openChange(t, e, main.ID)

	changed, head, err := e.SnapshotWorking(id, map[string]TreeEntry{})
	if err != nil {
		t.Fatalf("SnapshotWorking empty: %v", err)
	}
	if !changed {
		t.Fatal("fresh empty snapshot: changed=false, want true (creates working commit)")
	}
	if head == "" {
		t.Fatal("fresh empty snapshot: empty head, want a (working) commit at the line tip")
	}
	ch, _ := e.GetChange(id)
	if ch.HeadCommit != head {
		t.Fatalf("change head = %q, want %s", ch.HeadCommit, head)
	}
}

func TestSnapshotPreservesDescription(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	id := openChange(t, e, main.ID)

	_, head, err := e.SnapshotWorking(id, map[string]TreeEntry{"a": blobEntry(t, e, "x")})
	if err != nil {
		t.Fatalf("SnapshotWorking: %v", err)
	}
	ci, err := e.commitInfo(head)
	if err != nil {
		t.Fatalf("commitInfo: %v", err)
	}
	if ci.Message != "(working)" {
		t.Fatalf("head message = %q, want %q", ci.Message, "(working)")
	}

	// A second snapshot must preserve the description.
	_, head2, err := e.SnapshotWorking(id, map[string]TreeEntry{"a": blobEntry(t, e, "y")})
	if err != nil {
		t.Fatalf("SnapshotWorking 2: %v", err)
	}
	ci2, err := e.commitInfo(head2)
	if err != nil {
		t.Fatalf("commitInfo 2: %v", err)
	}
	if ci2.Message != "(working)" {
		t.Fatalf("amended head message = %q, want %q", ci2.Message, "(working)")
	}
}

func TestSnapshotOpCoalesces(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	id := openChange(t, e, main.ID)

	base, _ := e.OperationLog()
	preTip, _ := e.LineByName("main")

	// Three consecutive snapshots on one change coalesce to one op.
	if _, _, err := e.SnapshotWorking(id, map[string]TreeEntry{"a": blobEntry(t, e, "1")}); err != nil {
		t.Fatalf("snap 1: %v", err)
	}
	if _, _, err := e.SnapshotWorking(id, map[string]TreeEntry{"a": blobEntry(t, e, "2")}); err != nil {
		t.Fatalf("snap 2: %v", err)
	}
	_, head3, err := e.SnapshotWorking(id, map[string]TreeEntry{"a": blobEntry(t, e, "3")})
	if err != nil {
		t.Fatalf("snap 3: %v", err)
	}

	ops, _ := e.OperationLog()
	if len(ops) != len(base)+1 {
		t.Fatalf("coalesce: op count grew by %d, want 1 (%d -> %d)", len(ops)-len(base), len(base), len(ops))
	}
	last := ops[len(ops)-1]
	if last.OpType != opSnapshot {
		t.Fatalf("last op type = %q, want %q", last.OpType, opSnapshot)
	}
	if last.Detail != id {
		t.Fatalf("snapshot op detail = %q, want change id %q", last.Detail, id)
	}
	// view_after reflects the 3rd snapshot (main tip == head3).
	if last.ViewAfter["main"] != head3 {
		t.Fatalf("coalesced view_after[main] = %s, want head3 %s", last.ViewAfter["main"], head3)
	}

	// A snapshot on a DIFFERENT change starts a separate op.
	id2 := openChange(t, e, main.ID)
	if _, _, err := e.SnapshotWorking(id2, map[string]TreeEntry{"b": blobEntry(t, e, "z")}); err != nil {
		t.Fatalf("snap other change: %v", err)
	}
	ops2, _ := e.OperationLog()
	if len(ops2) != len(ops)+1 {
		t.Fatalf("different change: op count grew by %d, want 1", len(ops2)-len(ops))
	}

	// The burst op's view_before is the pre-burst view (NOT the state after the
	// 2nd snapshot) — that is what makes the burst a single undo step.
	burst := ops[len(ops)-1]
	if burst.ViewBefore["main"] != preTip.TipCommit {
		t.Fatalf("coalesced burst view_before[main] = %q, want pre-burst tip %q (one undo step)",
			burst.ViewBefore["main"], preTip.TipCommit)
	}
}

// TestSnapshotUndoReversesWholeBurst proves a single Undo over a coalesced
// snapshot burst restores the pre-burst tip (the entire auto-snapshot burst is
// one undo step).
func TestSnapshotUndoReversesWholeBurst(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	seedLineTip(t, e, main.ID, map[string][]byte{"seed.txt": []byte("seed\n")})
	preTip, _ := e.LineByName("main")

	id := openChange(t, e, main.ID)
	for _, s := range []string{"1", "2", "3"} {
		if _, _, err := e.SnapshotWorking(id, map[string]TreeEntry{"a": blobEntry(t, e, s)}); err != nil {
			t.Fatalf("snap %s: %v", s, err)
		}
	}
	moved, _ := e.LineByName("main")
	if moved.TipCommit == preTip.TipCommit {
		t.Fatal("precondition: burst should have advanced the tip")
	}

	if err := e.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	restored, _ := e.LineByName("main")
	if restored.TipCommit != preTip.TipCommit {
		t.Fatalf("single Undo over coalesced burst restored tip %s, want pre-burst %s",
			restored.TipCommit, preTip.TipCommit)
	}
}

func TestCommitInfoWorkingFlag(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	id := openChange(t, e, main.ID)

	_, head, err := e.SnapshotWorking(id, map[string]TreeEntry{"a": blobEntry(t, e, "w")})
	if err != nil {
		t.Fatalf("SnapshotWorking: %v", err)
	}

	// isWorkingHead: an open change's head -> true.
	working, err := e.isWorkingHead(head)
	if err != nil {
		t.Fatalf("isWorkingHead: %v", err)
	}
	if !working {
		t.Fatal("open change head: isWorkingHead=false, want true")
	}

	// Log marks it as Working.
	entries, err := e.Log(head, 0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(entries) == 0 || !entries[0].Working {
		t.Fatalf("Log[0].Working = %v, want true", entries)
	}

	// Sealing the change makes the same head no longer a working head.
	if _, err := e.db.Exec(`UPDATE change SET sealed=1 WHERE id=?`, id); err != nil {
		t.Fatalf("seal: %v", err)
	}
	working, err = e.isWorkingHead(head)
	if err != nil {
		t.Fatalf("isWorkingHead after seal: %v", err)
	}
	if working {
		t.Fatal("sealed change head: isWorkingHead=true, want false")
	}
}
