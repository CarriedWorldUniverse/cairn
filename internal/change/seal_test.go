package change

import (
	"testing"
)

// TestSealMarksSealedAndOpensNew: after snapshotting an open change, Seal stamps
// the message onto the working commit, marks the change sealed, advances the line
// tip to the sealed commit, and opens a fresh open change on the same line with no
// head yet.
func TestSealMarksSealedAndOpensNew(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	id := openChange(t, e, main.ID)

	_, working, err := e.SnapshotWorking(id, map[string]TreeEntry{"a": blobEntry(t, e, "blob1")})
	if err != nil {
		t.Fatalf("SnapshotWorking: %v", err)
	}

	newID, conflicts, err := e.Seal(id, "first")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("Seal conflicts = %d, want 0", len(conflicts))
	}
	if newID == "" {
		t.Fatal("Seal returned empty newChangeID")
	}
	if newID == id {
		t.Fatalf("Seal newChangeID = %s equals the sealed change id", newID)
	}

	// The original change is now sealed with the stamped message.
	sealed, err := e.GetChange(id)
	if err != nil {
		t.Fatalf("GetChange(sealed): %v", err)
	}
	if !sealed.Sealed {
		t.Fatal("original change Sealed=false, want true")
	}
	ci, err := e.commitInfo(sealed.HeadCommit)
	if err != nil {
		t.Fatalf("commitInfo(sealed head): %v", err)
	}
	if ci.Message != "first" {
		t.Fatalf("sealed head message = %q, want %q", ci.Message, "first")
	}
	// The sealed commit shares the working commit's tree (message stamp only,
	// no merge for a root line).
	wTree, _ := e.commitTree(working)
	sTree, _ := e.commitTree(sealed.HeadCommit)
	if wTree != sTree {
		t.Fatalf("sealed tree %s != working tree %s", sTree, wTree)
	}

	// The new change is open (not sealed), same line, no head yet.
	fresh, err := e.GetChange(newID)
	if err != nil {
		t.Fatalf("GetChange(new): %v", err)
	}
	if fresh.Sealed {
		t.Fatal("new change Sealed=true, want false")
	}
	if fresh.LineID != main.ID {
		t.Fatalf("new change line = %s, want %s", fresh.LineID, main.ID)
	}
	if fresh.HeadCommit != "" {
		t.Fatalf("new change head = %q, want empty", fresh.HeadCommit)
	}

	// The line tip is the sealed commit.
	mainAfter, _ := e.LineByName("main")
	if mainAfter.TipCommit != sealed.HeadCommit {
		t.Fatalf("line tip = %s, want sealed commit %s", mainAfter.TipCommit, sealed.HeadCommit)
	}

	// Log top entry is the sealed commit with message "first".
	entries, err := e.Log(mainAfter.TipCommit, 0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(entries) == 0 || entries[0].Subject != "first" {
		t.Fatalf("Log[0] = %+v, want subject %q", entries, "first")
	}
}

// TestSealThenSnapshotOnNewChange: after Seal, snapshotting the new change writes
// a (working) commit on top of the sealed commit; Log shows (working) at tip with
// the sealed "first" below it.
func TestSealThenSnapshotOnNewChange(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	id := openChange(t, e, main.ID)

	if _, _, err := e.SnapshotWorking(id, map[string]TreeEntry{"a": blobEntry(t, e, "blob1")}); err != nil {
		t.Fatalf("SnapshotWorking 1: %v", err)
	}
	newID, _, err := e.Seal(id, "first")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	sealed, _ := e.GetChange(id)

	_, working2, err := e.SnapshotWorking(newID, map[string]TreeEntry{"a": blobEntry(t, e, "blob2")})
	if err != nil {
		t.Fatalf("SnapshotWorking 2: %v", err)
	}

	// The new working commit sits on top of the sealed commit.
	parent, err := e.firstParent(working2)
	if err != nil {
		t.Fatalf("firstParent(working2): %v", err)
	}
	if parent != sealed.HeadCommit {
		t.Fatalf("working2 first-parent = %s, want sealed commit %s", parent, sealed.HeadCommit)
	}

	// Log: (working) at tip, "first" below.
	entries, err := e.Log(working2, 0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(entries) < 2 {
		t.Fatalf("Log returned %d entries, want >= 2", len(entries))
	}
	if entries[0].Subject != "(working)" || !entries[0].Working {
		t.Fatalf("Log[0] = %+v, want (working) and Working=true", entries[0])
	}
	if entries[1].Subject != "first" {
		t.Fatalf("Log[1].Subject = %q, want %q", entries[1].Subject, "first")
	}
}

// TestSealMergeForwardConflict: a child line whose seal conflicts with the parent
// records the conflict, marks has_conflict, still seals, and returns the conflict.
// Mirrors TestCommitMergeForwardConflict but via SnapshotWorking+Seal.
func TestSealMergeForwardConflict(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	seedLineTip(t, e, main.ID, map[string][]byte{
		"f.txt": []byte("base\n"),
	})

	exp, err := e.CreateLine("exp", main.ID)
	if err != nil {
		t.Fatalf("CreateLine: %v", err)
	}

	// Advance main's tip: a change on main commits X over the shared base.
	mc, _ := e.CreateChange(main.ID, "agent-main")
	if _, err := e.Commit(mc.ID, map[string][]byte{"f.txt": []byte("X\n")}, nil, ""); err != nil {
		t.Fatalf("advance main: %v", err)
	}

	// On exp, snapshot Y over the same region, then Seal -> conflict.
	ch, _ := e.CreateChange(exp.ID, "agent-exp")
	if _, _, err := e.SnapshotWorking(ch.ID, map[string]TreeEntry{"f.txt": blobEntry(t, e, "Y\n")}); err != nil {
		t.Fatalf("SnapshotWorking: %v", err)
	}

	newID, conflicts, err := e.Seal(ch.ID, "exp work")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if len(conflicts) < 1 {
		t.Fatalf("expected >= 1 conflict, got %d", len(conflicts))
	}
	if conflicts[0].Path != "f.txt" {
		t.Fatalf("conflict path = %q, want f.txt", conflicts[0].Path)
	}

	// The sealed change carries has_conflict, and the conflict is persisted.
	got, _ := e.GetChange(ch.ID)
	if !got.Sealed {
		t.Fatal("change should be sealed")
	}
	if !got.HasConflict {
		t.Fatal("change should have has_conflict set")
	}
	open, err := e.Conflicts(ch.ID)
	if err != nil {
		t.Fatalf("Conflicts: %v", err)
	}
	if len(open) < 1 {
		t.Fatalf("persisted conflicts = %d, want >= 1", len(open))
	}

	// A fresh change still opens.
	fresh, err := e.GetChange(newID)
	if err != nil {
		t.Fatalf("GetChange(new): %v", err)
	}
	if fresh.Sealed || fresh.HeadCommit != "" || fresh.LineID != exp.ID {
		t.Fatalf("new change = %+v, want open/empty-head on exp line", fresh)
	}
}
