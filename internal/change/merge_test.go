package change

import (
	"testing"
)

// seedLineTip opens a change on lineID, commits files, and returns the new head.
// Commit advances the owning line's tip, so this directly seeds a line's state.
func seedLineTip(t *testing.T, e *Engine, lineID string, files map[string][]byte) string {
	t.Helper()
	ch, _ := e.CreateChange(lineID, "seed")
	r, err := e.Commit(ch.ID, files)
	if err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	return r.HeadCommit // Commit already advances the line tip
}

func TestCommitMergeForwardNonOverlap(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	seedLineTip(t, e, main.ID, map[string][]byte{
		"a.txt": []byte("a\n"),
		"b.txt": []byte("b\n"),
	})

	exp, err := e.CreateLine("exp", main.ID)
	if err != nil {
		t.Fatalf("CreateLine: %v", err)
	}

	ch, _ := e.CreateChange(exp.ID, "agent")
	r, err := e.Commit(ch.ID, map[string][]byte{
		"a.txt": []byte("a\n"),
		"b.txt": []byte("B\n"),
	})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(r.Conflicts) != 0 {
		t.Fatalf("expected no conflicts, got %d", len(r.Conflicts))
	}

	got, _ := e.GetChange(ch.ID)
	treeSha, err := e.commitTree(got.HeadCommit)
	if err != nil {
		t.Fatalf("commitTree: %v", err)
	}
	files, err := e.readTree(treeSha)
	if err != nil {
		t.Fatalf("readTree: %v", err)
	}
	if string(files["a.txt"]) != "a\n" {
		t.Fatalf("a.txt = %q, want %q", files["a.txt"], "a\n")
	}
	if string(files["b.txt"]) != "B\n" {
		t.Fatalf("b.txt = %q, want %q", files["b.txt"], "B\n")
	}
}

func TestCommitMergeForwardConflict(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	seedLineTip(t, e, main.ID, map[string][]byte{
		"f.txt": []byte("base\n"),
	})

	// Spec collision path (§4): both sides fork the SAME base, then diverge.
	// exp forks main at base; afterward A's edit lands on main (base -> X) while
	// B (on exp) edits the same region (base -> Y). exp's merge-forward then sees
	// three-way (base, X, Y) over an overlapping region -> a conflict object.
	exp, err := e.CreateLine("exp", main.ID)
	if err != nil {
		t.Fatalf("CreateLine: %v", err)
	}

	// Advance main's tip via the line-tip rule: a change on main commits X.
	mc, _ := e.CreateChange(main.ID, "agent-main")
	if _, err := e.Commit(mc.ID, map[string][]byte{"f.txt": []byte("X\n")}); err != nil {
		t.Fatalf("advance main: %v", err)
	}

	ch, _ := e.CreateChange(exp.ID, "agent-exp")
	r, err := e.Commit(ch.ID, map[string][]byte{"f.txt": []byte("Y\n")})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(r.Conflicts) < 1 {
		t.Fatalf("expected >= 1 conflict, got %d", len(r.Conflicts))
	}
	if r.Conflicts[0].Path != "f.txt" {
		t.Fatalf("conflict path = %q, want f.txt", r.Conflicts[0].Path)
	}

	got, _ := e.GetChange(ch.ID)
	if !got.HasConflict {
		t.Fatalf("change should have has_conflict set")
	}
}
