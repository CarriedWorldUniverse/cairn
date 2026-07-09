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
