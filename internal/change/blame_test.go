package change

import (
	"strings"
	"testing"
)

// TestBlame_TwoAuthors commits f.txt across two sealed changes by two different
// identities and verifies per-line provenance.
func TestBlame_TwoAuthors(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")

	// --- First sealed change: Alice writes line1 ---
	e.SetIdentity("Alice", "alice@example.com")
	id1 := openChange(t, e, main.ID)
	_, _, err := e.SnapshotWorking(id1, map[string]TreeEntry{
		"f.txt": blobEntry(t, e, "line1\nline2\n"),
	})
	if err != nil {
		t.Fatalf("SnapshotWorking 1: %v", err)
	}
	_, _, err = e.Seal(id1, "first commit")
	if err != nil {
		t.Fatalf("Seal 1: %v", err)
	}

	// Reload line tip after first seal
	main, _ = e.LineByName("main")
	tip1 := main.TipCommit

	// Verify first seal produced a change-id
	cid1, err := e.ChangeIDOf(tip1)
	if err != nil {
		t.Fatalf("ChangeIDOf tip1: %v", err)
	}
	if cid1 == "" {
		t.Fatal("expected Change-Id on first sealed commit")
	}

	// --- Second sealed change: Bob rewrites line2 ---
	e.SetIdentity("Bob", "bob@example.com")
	// Get the fresh open change that Seal opened
	id2, err := freshChangeOnLine(e, main.ID, id1)
	if err != nil {
		t.Fatalf("finding fresh change: %v", err)
	}
	_, _, err = e.SnapshotWorking(id2, map[string]TreeEntry{
		"f.txt": blobEntry(t, e, "line1\nline2-edited\n"),
	})
	if err != nil {
		t.Fatalf("SnapshotWorking 2: %v", err)
	}
	_, _, err = e.Seal(id2, "second commit")
	if err != nil {
		t.Fatalf("Seal 2: %v", err)
	}

	main, _ = e.LineByName("main")
	tip2 := main.TipCommit

	// --- Blame at tip2 ---
	lines, err := e.Blame(tip2, "f.txt")
	if err != nil {
		t.Fatalf("Blame: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("Blame returned %d lines, want 2", len(lines))
	}

	// line1 was introduced by Alice (first commit)
	if lines[0].Author != "Alice" {
		t.Errorf("line[0].Author = %q, want Alice", lines[0].Author)
	}
	if lines[0].ChangeID == "" {
		t.Error("line[0].ChangeID is empty, want non-empty")
	}
	if !strings.Contains(lines[0].Text, "line1") {
		t.Errorf("line[0].Text = %q, want to contain line1", lines[0].Text)
	}
	if lines[0].Commit == "" {
		t.Error("line[0].Commit is empty")
	}

	// line2-edited was introduced by Bob (second commit)
	if lines[1].Author != "Bob" {
		t.Errorf("line[1].Author = %q, want Bob", lines[1].Author)
	}
	if lines[1].ChangeID == "" {
		t.Error("line[1].ChangeID is empty, want non-empty")
	}
	if !strings.Contains(lines[1].Text, "line2-edited") {
		t.Errorf("line[1].Text = %q, want to contain line2-edited", lines[1].Text)
	}
}

// TestBlame_MissingPath verifies that blaming a non-existent path returns an error.
func TestBlame_MissingPath(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")

	e.SetIdentity("Alice", "alice@example.com")
	id1 := openChange(t, e, main.ID)
	_, _, err := e.SnapshotWorking(id1, map[string]TreeEntry{
		"f.txt": blobEntry(t, e, "hello\n"),
	})
	if err != nil {
		t.Fatalf("SnapshotWorking: %v", err)
	}
	_, _, err = e.Seal(id1, "add f.txt")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	main, _ = e.LineByName("main")
	_, err = e.Blame(main.TipCommit, "nonexistent.txt")
	if err == nil {
		t.Fatal("expected error for missing path, got nil")
	}
}

// TestIsWorkingHead verifies the exported IsWorkingHead wrapper.
func TestIsWorkingHead(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	id := openChange(t, e, main.ID)

	_, working, err := e.SnapshotWorking(id, map[string]TreeEntry{
		"a.txt": blobEntry(t, e, "content\n"),
	})
	if err != nil {
		t.Fatalf("SnapshotWorking: %v", err)
	}

	// working commit should be the working head
	isWorking, err := e.IsWorkingHead(working)
	if err != nil {
		t.Fatalf("IsWorkingHead(working): %v", err)
	}
	if !isWorking {
		t.Error("IsWorkingHead(working) = false, want true")
	}

	// seal it — the sealed commit is no longer the working head
	_, _, err = e.Seal(id, "sealed")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	main, _ = e.LineByName("main")
	isWorking2, err := e.IsWorkingHead(main.TipCommit)
	if err != nil {
		t.Fatalf("IsWorkingHead(sealed): %v", err)
	}
	if isWorking2 {
		t.Error("IsWorkingHead(sealed tip) = true, want false")
	}
}

// freshChangeOnLine finds the open change on lineID that is NOT the old change.
// Seal opens a new change on the same line; we need its ID.
func freshChangeOnLine(e *Engine, lineID, oldChangeID string) (string, error) {
	rows, err := e.db.Query(
		`SELECT id FROM change WHERE line_id=? AND sealed=0 AND id!=? LIMIT 1`,
		lineID, oldChangeID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		return id, nil
	}
	return "", rows.Err()
}
