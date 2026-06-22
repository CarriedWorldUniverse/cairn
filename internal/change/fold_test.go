package change

import (
	"errors"
	"testing"
)

func mustS(s string, err error) string { if err != nil { panic(err) }; return s }

func TestFoldFastForwardsParent(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	seedLineTip(t, e, main.ID, map[string][]byte{"a.txt": []byte("a\n")})
	exp, _ := e.CreateLine("exp", main.ID)
	ch, _ := e.CreateChange(exp.ID, "e")
	// Commit advances exp's tip automatically (merge-forward + line-tip rule).
	if _, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("a\n"), "n.txt": []byte("new\n")}); err != nil {
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
	e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("WILD\n")})
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
	e.Commit(mc.ID, map[string][]byte{"f.txt": []byte("X\n")})
	ec, _ := e.CreateChange(exp.ID, "e")
	e.Commit(ec.ID, map[string][]byte{"f.txt": []byte("Y\n")}) // conflict on exp
	if err := e.FoldLine(exp.ID); !errors.Is(err, ErrHasConflict) {
		t.Fatalf("FoldLine with open conflict: want ErrHasConflict, got %v", err)
	}
	// parent untouched (still at X, not folded to a conflicted tip)
	// (no assertion on exact sha needed beyond: fold returned the error)
}
