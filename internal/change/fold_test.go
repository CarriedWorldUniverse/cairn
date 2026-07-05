package change

import (
	"errors"
	"testing"
)

func mustS(s string, err error) string {
	if err != nil {
		panic(err)
	}
	return s
}

func TestFoldFastForwardsParent(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	seedLineTip(t, e, main.ID, map[string][]byte{"a.txt": []byte("a\n")})
	exp, _ := e.CreateLine("exp", main.ID)
	ch, _ := e.CreateChange(exp.ID, "e")
	// Commit advances exp's tip automatically (merge-forward + line-tip rule).
	if _, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("a\n"), "n.txt": []byte("new\n")}, nil, ""); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := e.FoldLine(exp.ID); err != nil {
		t.Fatalf("FoldLine: %v", err)
	}
	main2, _ := e.LineByName("main")
	files, _ := e.readTree(mustS(e.commitTree(main2.TipCommit)))
	if string(files["n.txt"]) != "new\n" {
		t.Fatalf("fold did not bring n.txt into main: %v", files)
	}
	// exp marked folded
	expAfter, _ := e.lineByID(exp.ID)
	if expAfter.Status != "folded" {
		t.Fatalf("exp status = %q, want folded", expAfter.Status)
	}
}

func TestAbandonLeavesParentUntouched(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	baseTip := seedLineTip(t, e, main.ID, map[string][]byte{"a.txt": []byte("a\n")})
	exp, _ := e.CreateLine("exp", main.ID)
	ch, _ := e.CreateChange(exp.ID, "e")
	e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("WILD\n")}, nil, "")
	if err := e.AbandonLine(exp.ID); err != nil {
		t.Fatalf("AbandonLine: %v", err)
	}
	main2, _ := e.LineByName("main")
	if main2.TipCommit != baseTip {
		t.Fatalf("main tip moved: %s != %s", main2.TipCommit, baseTip)
	}
	expAfter, _ := e.lineByID(exp.ID)
	if expAfter.Status != "abandoned" {
		t.Fatalf("exp status = %q, want abandoned", expAfter.Status)
	}
	chAfter, _ := e.GetChange(ch.ID)
	if chAfter.Status != "abandoned" {
		t.Fatalf("change status = %q, want abandoned", chAfter.Status)
	}
}

func TestFoldRootLineReturnsError(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	err := e.FoldLine(main.ID)
	if err == nil || errors.Is(err, ErrHasConflict) {
		t.Fatalf("FoldLine on root: want non-nil non-ErrHasConflict error, got %v", err)
	}
}

func TestFoldRejectedWithOpenConflict(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	seedLineTip(t, e, main.ID, map[string][]byte{"f.txt": []byte("base\n")})
	exp, _ := e.CreateLine("exp", main.ID)
	mc, _ := e.CreateChange(main.ID, "m")
	e.Commit(mc.ID, map[string][]byte{"f.txt": []byte("X\n")}, nil, "")
	ec, _ := e.CreateChange(exp.ID, "e")
	e.Commit(ec.ID, map[string][]byte{"f.txt": []byte("Y\n")}, nil, "") // conflict on exp
	if err := e.FoldLine(exp.ID); !errors.Is(err, ErrHasConflict) {
		t.Fatalf("FoldLine with open conflict: want ErrHasConflict, got %v", err)
	}
	// parent untouched (still at X, not folded to a conflicted tip)
	// (no assertion on exact sha needed beyond: fold returned the error)
}

// The sibling-fold clobber (hit live 2026-07-05): two lines expressed from the
// same parent tip; folding A advances the parent, and folding B — whose merge
// base is now stale — used to blindly rewind parent.tip to B's tip, silently
// discarding A's fold. FoldLine must instead adopt the advanced parent (a
// clean 3-way) so BOTH lines' work survives.
func TestFoldSiblingLinesBothSurvive(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	seedLineTip(t, e, main.ID, map[string][]byte{"base.txt": []byte("base\n")})

	la, _ := e.CreateLine("unit-a", main.ID)
	lb, _ := e.CreateLine("unit-b", main.ID)
	chA, _ := e.CreateChange(la.ID, "a")
	chB, _ := e.CreateChange(lb.ID, "b")
	if _, err := e.Commit(chA.ID, map[string][]byte{"base.txt": []byte("base\n"), "a.txt": []byte("unit A\n")}, nil, ""); err != nil {
		t.Fatalf("commit A: %v", err)
	}
	if _, err := e.Commit(chB.ID, map[string][]byte{"base.txt": []byte("base\n"), "b.txt": []byte("unit B\n")}, nil, ""); err != nil {
		t.Fatalf("commit B: %v", err)
	}

	if err := e.FoldLine(la.ID); err != nil {
		t.Fatalf("fold A: %v", err)
	}
	if err := e.FoldLine(lb.ID); err != nil {
		t.Fatalf("fold B: %v", err)
	}

	main2, _ := e.LineByName("main")
	files, _ := e.readTree(mustS(e.commitTree(main2.TipCommit)))
	if string(files["a.txt"]) != "unit A\n" {
		t.Fatalf("sibling fold clobbered unit A: %v", keysOf(files))
	}
	if string(files["b.txt"]) != "unit B\n" {
		t.Fatalf("unit B missing after its own fold: %v", keysOf(files))
	}
	if string(files["base.txt"]) != "base\n" {
		t.Fatalf("base content lost: %v", keysOf(files))
	}
}

// A stale sibling whose adoption CONFLICTS must refuse the fold with the
// parent untouched — never a silent overwrite in either direction.
func TestFoldSiblingConflictRefusedParentUntouched(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	seedLineTip(t, e, main.ID, map[string][]byte{"f.txt": []byte("base\n")})

	la, _ := e.CreateLine("left", main.ID)
	lb, _ := e.CreateLine("right", main.ID)
	chA, _ := e.CreateChange(la.ID, "a")
	chB, _ := e.CreateChange(lb.ID, "b")
	if _, err := e.Commit(chA.ID, map[string][]byte{"f.txt": []byte("left version\n")}, nil, ""); err != nil {
		t.Fatalf("commit A: %v", err)
	}
	if _, err := e.Commit(chB.ID, map[string][]byte{"f.txt": []byte("right version\n")}, nil, ""); err != nil {
		t.Fatalf("commit B: %v", err)
	}
	if err := e.FoldLine(la.ID); err != nil {
		t.Fatalf("fold A: %v", err)
	}
	parentTipAfterA, _ := e.LineByName("main")

	if err := e.FoldLine(lb.ID); err == nil {
		t.Fatal("conflicted sibling fold must be refused")
	}
	main2, _ := e.LineByName("main")
	if main2.TipCommit != parentTipAfterA.TipCommit {
		t.Fatalf("refused fold must leave parent untouched: %s -> %s", parentTipAfterA.TipCommit, main2.TipCommit)
	}
	files, _ := e.readTree(mustS(e.commitTree(main2.TipCommit)))
	if string(files["f.txt"]) != "left version\n" {
		t.Fatalf("parent content changed on refused fold: %q", files["f.txt"])
	}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
