package change

import (
	"bytes"
	"testing"
)

// fakeBinaryA and fakeBinaryB are minimal binary blobs that differ on both sides.
// The NUL byte ensures isBinary returns true for both.
var fakeBinaryA = []byte{0x89, 'P', 'N', 'G', 0x00, 0x01, 0x02, 0x03}
var fakeBinaryB = []byte{0x89, 'P', 'N', 'G', 0x00, 0x04, 0x05, 0x06}

// TestMergeBinaryConflictNoMarkers verifies that a binary file modified on both
// sides (genuine 3-way divergence) is recorded as a whole-file conflict and that
// the merged tree bytes do NOT contain ASCII conflict markers.
func TestMergeBinaryConflictNoMarkers(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")

	// Seed the base state on main: binary file at its original content.
	seedLineTip(t, e, main.ID, map[string][]byte{
		"image.png": fakeBinaryA,
	})

	// Create a child line that forks from main at this base.
	exp, err := e.CreateLine("exp", main.ID)
	if err != nil {
		t.Fatalf("CreateLine: %v", err)
	}

	// Advance main's tip with a DIFFERENT binary content (parent side diverges).
	mc, _ := e.CreateChange(main.ID, "agent-main")
	if _, err := e.Commit(mc.ID, map[string][]byte{
		"image.png": fakeBinaryB,
	}, ""); err != nil {
		t.Fatalf("advance main with binary: %v", err)
	}

	// Now commit on exp with yet another different binary (change side diverges).
	// This produces a genuine 3-way binary divergence: base=A, parent=B, change=C.
	fakeBinaryC := []byte{0x89, 'P', 'N', 'G', 0x00, 0x07, 0x08, 0x09}
	ch, _ := e.CreateChange(exp.ID, "agent-exp")
	r, err := e.Commit(ch.ID, map[string][]byte{
		"image.png": fakeBinaryC,
	}, "")
	if err != nil {
		t.Fatalf("Commit (binary diverge): %v", err)
	}

	// Must record at least one conflict.
	if len(r.Conflicts) < 1 {
		t.Fatalf("expected >= 1 conflict, got %d", len(r.Conflicts))
	}
	if r.Conflicts[0].Path != "image.png" {
		t.Fatalf("conflict path = %q, want image.png", r.Conflicts[0].Path)
	}

	// has_conflict must be set on the change.
	got, _ := e.GetChange(ch.ID)
	if !got.HasConflict {
		t.Fatalf("change should have has_conflict set after binary conflict")
	}

	// The merged bytes in the tree must be one side verbatim and must NOT contain
	// the ASCII conflict marker "<<<<<<<".
	treeSha, err := e.commitTree(got.HeadCommit)
	if err != nil {
		t.Fatalf("commitTree: %v", err)
	}
	files, err := e.readTree(treeSha)
	if err != nil {
		t.Fatalf("readTree: %v", err)
	}
	mergedBytes, ok := files["image.png"]
	if !ok {
		t.Fatalf("image.png missing from merged tree")
	}
	if bytes.Contains(mergedBytes, []byte("<<<<<<<")) {
		t.Fatalf("merged binary contains conflict markers: %q", mergedBytes)
	}
	// The bytes must equal one of the two sides verbatim (change side = fakeBinaryC,
	// parent side = fakeBinaryB). We keep the change/theirs side.
	if !bytes.Equal(mergedBytes, fakeBinaryC) && !bytes.Equal(mergedBytes, fakeBinaryB) {
		t.Fatalf("merged binary bytes %v are not verbatim from either side", mergedBytes)
	}
}

// TestMergeBinaryUnchangedOneSide verifies that a binary file changed on only
// ONE side (the change side) results in no conflict — the changed side is taken.
func TestMergeBinaryUnchangedOneSide(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")

	// Seed base with a binary file.
	seedLineTip(t, e, main.ID, map[string][]byte{
		"data.bin": fakeBinaryA,
	})

	// Create a child line forking from main.
	exp, err := e.CreateLine("exp2", main.ID)
	if err != nil {
		t.Fatalf("CreateLine: %v", err)
	}

	// Only the change side (exp) modifies the binary — parent (main) leaves it as base.
	// No commit on main after the seed, so parent side still has fakeBinaryA.
	ch, _ := e.CreateChange(exp.ID, "agent-exp2")
	r, err := e.Commit(ch.ID, map[string][]byte{
		"data.bin": fakeBinaryB,
	}, "")
	if err != nil {
		t.Fatalf("Commit (binary one-side): %v", err)
	}

	// No conflict expected — only one side changed.
	if len(r.Conflicts) != 0 {
		t.Fatalf("expected 0 conflicts for one-sided binary change, got %d", len(r.Conflicts))
	}

	got, _ := e.GetChange(ch.ID)
	if got.HasConflict {
		t.Fatalf("change should NOT have has_conflict set for one-sided binary change")
	}

	// The merged tree should carry the changed binary content (fakeBinaryB).
	treeSha, err := e.commitTree(got.HeadCommit)
	if err != nil {
		t.Fatalf("commitTree: %v", err)
	}
	files, err := e.readTree(treeSha)
	if err != nil {
		t.Fatalf("readTree: %v", err)
	}
	if !bytes.Equal(files["data.bin"], fakeBinaryB) {
		t.Fatalf("one-sided binary change: got %v, want %v", files["data.bin"], fakeBinaryB)
	}
}
