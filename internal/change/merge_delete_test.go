package change

import (
	"testing"
)

// mergeTreesForTest writes three path→content trees and runs mergeTrees.
func mergeTreesForTest(t *testing.T, e *Engine, base, ours, theirs map[string][]byte) (map[string][]byte, []Conflict) {
	t.Helper()
	writeT := func(m map[string][]byte) string {
		if m == nil {
			return ""
		}
		h, err := e.writeTree(m, nil)
		if err != nil {
			t.Fatalf("writeTree: %v", err)
		}
		return h.String()
	}
	mergedTree, conflicts, err := e.mergeTrees("test-change", writeT(base), writeT(ours), writeT(theirs))
	if err != nil {
		t.Fatalf("mergeTrees: %v", err)
	}
	files, err := e.readTree(mergedTree)
	if err != nil {
		t.Fatalf("readTree(merged): %v", err)
	}
	return files, conflicts
}

// TestMergeTreesDeletePropagation is the #103 regression: a deletion on one
// side of a file the other side never touched must WIN, not be resurrected.
func TestMergeTreesDeletePropagation(t *testing.T) {
	e := newTestEngine(t)
	a := []byte("a\n")
	b := []byte("b\n")

	t.Run("ours deleted, theirs untouched -> gone (#103 pull case)", func(t *testing.T) {
		merged, conflicts := mergeTreesForTest(t, e,
			map[string][]byte{"old.txt": a, "keep.txt": b}, // base
			map[string][]byte{"keep.txt": b, "new.txt": a}, // ours: deleted old, added new
			map[string][]byte{"old.txt": a, "keep.txt": b}, // theirs == base (clean snapshot)
		)
		if len(conflicts) != 0 {
			t.Fatalf("conflicts = %d, want 0", len(conflicts))
		}
		if _, ok := merged["old.txt"]; ok {
			t.Errorf("old.txt resurrected — deletion on ours must propagate")
		}
		if _, ok := merged["new.txt"]; !ok {
			t.Errorf("new.txt missing")
		}
		if _, ok := merged["keep.txt"]; !ok {
			t.Errorf("keep.txt missing")
		}
	})

	t.Run("theirs deleted, ours untouched -> gone (local delete survives reconcile)", func(t *testing.T) {
		merged, conflicts := mergeTreesForTest(t, e,
			map[string][]byte{"old.txt": a, "keep.txt": b},
			map[string][]byte{"old.txt": a, "keep.txt": b}, // ours == base
			map[string][]byte{"keep.txt": b},               // theirs deleted old
		)
		if len(conflicts) != 0 {
			t.Fatalf("conflicts = %d, want 0", len(conflicts))
		}
		if _, ok := merged["old.txt"]; ok {
			t.Errorf("old.txt resurrected — deletion on theirs must propagate")
		}
	})

	t.Run("modify vs delete -> content kept + conflict recorded", func(t *testing.T) {
		merged, conflicts := mergeTreesForTest(t, e,
			map[string][]byte{"f.txt": a},
			map[string][]byte{"f.txt": []byte("a modified\n")}, // ours modified
			map[string][]byte{},                                // theirs deleted
		)
		if len(conflicts) != 1 {
			t.Fatalf("conflicts = %d, want 1 (modify/delete)", len(conflicts))
		}
		if string(merged["f.txt"]) != "a modified\n" {
			t.Errorf("surviving content should be the modified side, got %q", merged["f.txt"])
		}
	})

	t.Run("delete vs modify (mirror) -> content kept + conflict", func(t *testing.T) {
		merged, conflicts := mergeTreesForTest(t, e,
			map[string][]byte{"f.txt": a},
			map[string][]byte{},                                // ours deleted
			map[string][]byte{"f.txt": []byte("a modified\n")}, // theirs modified
		)
		if len(conflicts) != 1 {
			t.Fatalf("conflicts = %d, want 1 (delete/modify)", len(conflicts))
		}
		if string(merged["f.txt"]) != "a modified\n" {
			t.Errorf("surviving content should be the modified side, got %q", merged["f.txt"])
		}
	})

	t.Run("mode-only change vs delete -> kept + conflict, mode preserved", func(t *testing.T) {
		// bytes identical everywhere; ours flips f.sh to executable, theirs
		// deletes it. A mode-only edit is a modification: the delete must NOT
		// silently win (it would drop the chmod with no record).
		writeM := func(files map[string][]byte, modes map[string]EntryMode) string {
			h, err := e.writeTree(files, modes)
			if err != nil {
				t.Fatalf("writeTree: %v", err)
			}
			return h.String()
		}
		baseT := writeM(map[string][]byte{"f.sh": a}, nil)
		oursT := writeM(map[string][]byte{"f.sh": a}, map[string]EntryMode{"f.sh": ModeExecutable})
		theirsT := writeM(map[string][]byte{}, nil)
		mergedTree, conflicts, err := e.mergeTrees("test-change", baseT, oursT, theirsT)
		if err != nil {
			t.Fatalf("mergeTrees: %v", err)
		}
		files, err := e.readTree(mergedTree)
		if err != nil {
			t.Fatalf("readTree: %v", err)
		}
		if len(conflicts) != 1 {
			t.Fatalf("conflicts = %d, want 1 (mode-change/delete)", len(conflicts))
		}
		if string(files["f.sh"]) != string(a) {
			t.Errorf("surviving content lost, got %q", files["f.sh"])
		}
		modes, err := e.fileModesFromTree(mergedTree)
		if err != nil {
			t.Fatalf("fileModesFromTree: %v", err)
		}
		if modes["f.sh"] != ModeExecutable {
			t.Errorf("executable mode dropped from surviving side")
		}
	})

	t.Run("delete vs mode-only change (mirror) -> kept + conflict", func(t *testing.T) {
		writeM := func(files map[string][]byte, modes map[string]EntryMode) string {
			h, err := e.writeTree(files, modes)
			if err != nil {
				t.Fatalf("writeTree: %v", err)
			}
			return h.String()
		}
		baseT := writeM(map[string][]byte{"f.sh": a}, nil)
		oursT := writeM(map[string][]byte{}, nil)
		theirsT := writeM(map[string][]byte{"f.sh": a}, map[string]EntryMode{"f.sh": ModeExecutable})
		_, conflicts, err := e.mergeTrees("test-change", baseT, oursT, theirsT)
		if err != nil {
			t.Fatalf("mergeTrees: %v", err)
		}
		if len(conflicts) != 1 {
			t.Fatalf("conflicts = %d, want 1 (delete/mode-change)", len(conflicts))
		}
	})

	t.Run("true adds on either side survive", func(t *testing.T) {
		merged, conflicts := mergeTreesForTest(t, e,
			map[string][]byte{},
			map[string][]byte{"ao.txt": a},
			map[string][]byte{"at.txt": b},
		)
		if len(conflicts) != 0 {
			t.Fatalf("conflicts = %d, want 0", len(conflicts))
		}
		if _, ok := merged["ao.txt"]; !ok {
			t.Errorf("ours add lost")
		}
		if _, ok := merged["at.txt"]; !ok {
			t.Errorf("theirs add lost")
		}
	})
}

// TestMergeTreesModeVsMode is the #124-review regression: when both sides
// carry IDENTICAL content (so the fast "both sides agree" SHA-equality check
// would otherwise fire) but their MODES differ, mergeTrees must not silently
// take one side wholesale (dropping the other side's mode change with no
// record). The untouched side's mode must lose to the side that actually
// changed it — in EITHER direction — and a mode change on both sides at once
// must be recorded as a real conflict, not silently resolved either way.
func TestMergeTreesModeVsMode(t *testing.T) {
	e := newTestEngine(t)
	c := []byte("#!/bin/sh\necho hi\n")

	writeM := func(files map[string][]byte, modes map[string]EntryMode) string {
		h, err := e.writeTree(files, modes)
		if err != nil {
			t.Fatalf("writeTree: %v", err)
		}
		return h.String()
	}
	modeOf := func(tree, path string) EntryMode {
		modes, err := e.fileModesFromTree(tree)
		if err != nil {
			t.Fatalf("fileModesFromTree: %v", err)
		}
		return modes[path] // absent -> ModeRegular, the sparse-map default
	}

	t.Run("theirs chmod, ours untouched -> theirs' mode wins, 0 conflicts", func(t *testing.T) {
		baseT := writeM(map[string][]byte{"f.sh": c}, nil)
		oursT := writeM(map[string][]byte{"f.sh": c}, nil) // == base, untouched
		theirsT := writeM(map[string][]byte{"f.sh": c}, map[string]EntryMode{"f.sh": ModeExecutable})
		mergedTree, conflicts, err := e.mergeTrees("test-change", baseT, oursT, theirsT)
		if err != nil {
			t.Fatalf("mergeTrees: %v", err)
		}
		if len(conflicts) != 0 {
			t.Fatalf("conflicts = %d, want 0", len(conflicts))
		}
		if got := modeOf(mergedTree, "f.sh"); got != ModeExecutable {
			t.Errorf("mode = %v, want ModeExecutable (theirs' chmod dropped)", got)
		}
	})

	t.Run("ours chmod, theirs untouched -> ours' mode wins, 0 conflicts (mirror)", func(t *testing.T) {
		// This direction regressed on old main (theirs unconditionally won any
		// mode-carrying tie) — regression-locks the #124-review fix.
		baseT := writeM(map[string][]byte{"f.sh": c}, nil)
		oursT := writeM(map[string][]byte{"f.sh": c}, map[string]EntryMode{"f.sh": ModeExecutable})
		theirsT := writeM(map[string][]byte{"f.sh": c}, nil) // == base, untouched
		mergedTree, conflicts, err := e.mergeTrees("test-change", baseT, oursT, theirsT)
		if err != nil {
			t.Fatalf("mergeTrees: %v", err)
		}
		if len(conflicts) != 0 {
			t.Fatalf("conflicts = %d, want 0", len(conflicts))
		}
		if got := modeOf(mergedTree, "f.sh"); got != ModeExecutable {
			t.Errorf("mode = %v, want ModeExecutable (ours' chmod dropped)", got)
		}
	})

	t.Run("both flip mode differently -> conflict recorded, content kept", func(t *testing.T) {
		baseT := writeM(map[string][]byte{"f.sh": c}, nil)
		oursT := writeM(map[string][]byte{"f.sh": c}, map[string]EntryMode{"f.sh": ModeExecutable})
		theirsT := writeM(map[string][]byte{"f.sh": c}, map[string]EntryMode{"f.sh": ModeSymlink})
		mergedTree, conflicts, err := e.mergeTrees("test-change", baseT, oursT, theirsT)
		if err != nil {
			t.Fatalf("mergeTrees: %v", err)
		}
		if len(conflicts) != 1 {
			t.Fatalf("conflicts = %d, want 1 (mode-vs-mode)", len(conflicts))
		}
		files, err := e.readTree(mergedTree)
		if err != nil {
			t.Fatalf("readTree: %v", err)
		}
		if string(files["f.sh"]) != string(c) {
			t.Errorf("surviving content changed, got %q want %q", files["f.sh"], c)
		}
	})

	t.Run("content and mode both equal -> fast path unchanged, 0 conflicts", func(t *testing.T) {
		baseT := writeM(map[string][]byte{"f.sh": c}, map[string]EntryMode{"f.sh": ModeExecutable})
		oursT := writeM(map[string][]byte{"f.sh": c}, map[string]EntryMode{"f.sh": ModeExecutable})
		theirsT := writeM(map[string][]byte{"f.sh": c}, map[string]EntryMode{"f.sh": ModeExecutable})
		mergedTree, conflicts, err := e.mergeTrees("test-change", baseT, oursT, theirsT)
		if err != nil {
			t.Fatalf("mergeTrees: %v", err)
		}
		if len(conflicts) != 0 {
			t.Fatalf("conflicts = %d, want 0", len(conflicts))
		}
		if got := modeOf(mergedTree, "f.sh"); got != ModeExecutable {
			t.Errorf("mode = %v, want ModeExecutable preserved", got)
		}
	})
}
