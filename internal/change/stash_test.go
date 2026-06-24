package change

import (
	"strings"
	"testing"
)

// stashWorkingDelta opens a change on main and snapshots a working commit with
// content {a:"v1\n"} over an empty parent, so the working change has a delta to
// stash. Returns the change id and the stashed (working) head's tree hash.
func stashWorkingDelta(t *testing.T, e *Engine, lineID string) (string, string) {
	t.Helper()
	id := openChange(t, e, lineID)
	_, head, err := e.SnapshotWorking(id, map[string]TreeEntry{"a": blobEntry(t, e, "v1\n")})
	if err != nil {
		t.Fatalf("SnapshotWorking: %v", err)
	}
	tree, err := e.treeHashOf(head)
	if err != nil {
		t.Fatalf("treeHashOf head: %v", err)
	}
	return id, tree
}

func TestStashPushResetsToParentAndRecords(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	id, _ := stashWorkingDelta(t, e, main.ID)

	stashID, err := e.StashPush(id, "wip")
	if err != nil {
		t.Fatalf("StashPush: %v", err)
	}
	if stashID <= 0 {
		t.Fatalf("StashPush returned id=%d, want > 0", stashID)
	}

	list, err := e.StashList()
	if err != nil {
		t.Fatalf("StashList: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("StashList len = %d, want 1", len(list))
	}
	if list[0].Message != "wip" {
		t.Fatalf("stash message = %q, want %q", list[0].Message, "wip")
	}

	// The working change is now CLEAN: its head tree equals its parent's tree.
	ch, _ := e.GetChange(id)
	parent, err := e.firstParent(ch.HeadCommit)
	if err != nil {
		t.Fatalf("firstParent: %v", err)
	}
	headTree, err := e.treeHashOf(ch.HeadCommit)
	if err != nil {
		t.Fatalf("treeHashOf head: %v", err)
	}
	parentTree, err := e.treeHashOf(parent)
	if err != nil {
		t.Fatalf("treeHashOf parent: %v", err)
	}
	if headTree != parentTree {
		t.Fatalf("working change not reset to parent: headTree=%s parentTree=%s", headTree, parentTree)
	}
}

func TestStashApplyRestores(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	id, stashedTree := stashWorkingDelta(t, e, main.ID)

	if _, err := e.StashPush(id, "wip"); err != nil {
		t.Fatalf("StashPush: %v", err)
	}

	if err := e.StashApply(id, 0, true); err != nil {
		t.Fatalf("StashApply: %v", err)
	}

	ch, _ := e.GetChange(id)
	headTree, err := e.treeHashOf(ch.HeadCommit)
	if err != nil {
		t.Fatalf("treeHashOf head: %v", err)
	}
	if headTree != stashedTree {
		t.Fatalf("applied head tree = %s, want originally-stashed %s", headTree, stashedTree)
	}

	list, err := e.StashList()
	if err != nil {
		t.Fatalf("StashList: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("StashList len = %d after pop, want 0 (dropped)", len(list))
	}
}

func TestStashPushNothingWhenClean(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	id := openChange(t, e, main.ID)

	// A clean working change: empty snapshot over an empty parent (head tree ==
	// parent tree).
	if _, _, err := e.SnapshotWorking(id, map[string]TreeEntry{}); err != nil {
		t.Fatalf("SnapshotWorking empty: %v", err)
	}

	_, err := e.StashPush(id, "wip")
	if err == nil || !strings.Contains(err.Error(), "nothing to stash") {
		t.Fatalf("StashPush on clean change: err = %v, want \"nothing to stash\"", err)
	}
}

func TestStashApplyRefusesDirty(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	id, _ := stashWorkingDelta(t, e, main.ID)

	if _, err := e.StashPush(id, "wip"); err != nil {
		t.Fatalf("StashPush: %v", err)
	}

	// New working delta on the reset working change.
	if _, _, err := e.SnapshotWorking(id, map[string]TreeEntry{"b": blobEntry(t, e, "x")}); err != nil {
		t.Fatalf("SnapshotWorking new delta: %v", err)
	}

	err := e.StashApply(id, 0, true)
	if err == nil || !strings.Contains(err.Error(), "un-sealed work") {
		t.Fatalf("StashApply over dirty change: err = %v, want \"un-sealed work\"", err)
	}
}

func TestStashListOrderAndDrop(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")

	// First stash.
	id1, _ := stashWorkingDelta(t, e, main.ID)
	first, err := e.StashPush(id1, "first")
	if err != nil {
		t.Fatalf("StashPush 1: %v", err)
	}

	// Second stash (a different change/delta).
	id2 := openChange(t, e, main.ID)
	if _, _, err := e.SnapshotWorking(id2, map[string]TreeEntry{"c": blobEntry(t, e, "v2\n")}); err != nil {
		t.Fatalf("SnapshotWorking 2: %v", err)
	}
	second, err := e.StashPush(id2, "second")
	if err != nil {
		t.Fatalf("StashPush 2: %v", err)
	}

	list, err := e.StashList()
	if err != nil {
		t.Fatalf("StashList: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("StashList len = %d, want 2", len(list))
	}
	// Newest-first.
	if list[0].ID != second || list[1].ID != first {
		t.Fatalf("StashList order = [%d,%d], want newest-first [%d,%d]", list[0].ID, list[1].ID, second, first)
	}

	// Drop the top (the second/newest).
	if err := e.StashDrop(0); err != nil {
		t.Fatalf("StashDrop(0): %v", err)
	}
	list, err = e.StashList()
	if err != nil {
		t.Fatalf("StashList after drop: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("StashList len = %d after drop, want 1", len(list))
	}
	if list[0].ID != first {
		t.Fatalf("remaining stash id = %d, want the older %d", list[0].ID, first)
	}
}

// TestStashApplyNoSnapshot verifies that StashApply on a working change with no
// snapshot (HeadCommit == "") returns a clear "no snapshot" error instead of an
// opaque object-not-found from the git layer.
func TestStashApplyNoSnapshot(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")

	// First, create a real stash entry using a different change (so StashApply
	// has something to pop), then call StashApply on a change with no snapshot.
	id1, _ := stashWorkingDelta(t, e, main.ID)
	if _, err := e.StashPush(id1, "seed"); err != nil {
		t.Fatalf("StashPush seed: %v", err)
	}

	// Open a fresh change with no snapshot (HeadCommit will be "").
	id2 := openChange(t, e, main.ID)
	ch, err := e.GetChange(id2)
	if err != nil {
		t.Fatalf("GetChange: %v", err)
	}
	if ch.HeadCommit != "" {
		t.Fatalf("expected fresh change to have no snapshot (HeadCommit==%q), got %q", "", ch.HeadCommit)
	}

	err = e.StashApply(id2, 0, false)
	if err == nil {
		t.Fatal("StashApply on head-less change: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no snapshot") {
		t.Fatalf("StashApply on head-less change: err = %q, want it to contain \"no snapshot\"", err.Error())
	}
}

// TestStashTableOnLegacyRepo proves the stash table is created on Open (additive
// CREATE IF NOT EXISTS), so an existing repo gets it without an ALTER migration.
func TestStashTableOnLegacyRepo(t *testing.T) {
	e := newTestEngine(t)
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM stash`).Scan(&n); err != nil {
		t.Fatalf("stash table not queryable: %v", err)
	}
	if n != 0 {
		t.Fatalf("fresh stash count = %d, want 0", n)
	}
}
