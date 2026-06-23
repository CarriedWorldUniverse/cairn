package change

import (
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

func TestExportRefs(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	tip := seedLineTip(t, e, main.ID, map[string][]byte{"a.txt": []byte("a\n")})
	if err := e.Tag("v1.0.0", tip, "rel"); err != nil {
		t.Fatalf("Tag: %v", err)
	}
	exp, _ := e.CreateLine("exp", main.ID)
	ch, _ := e.CreateChange(exp.ID, "e")
	r, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("a\n"), "b.txt": []byte("b\n")}, "")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if err := e.Export(); err != nil {
		t.Fatalf("Export: %v", err)
	}
	// refs/heads/main -> tip
	ref, err := e.git.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil || ref.Hash().String() != tip {
		t.Fatalf("refs/heads/main = %v (err %v), want %s", ref, err, tip)
	}
	// refs/heads/exp -> exp tip (exp advanced via the commit)
	expLine, _ := e.lineByID(exp.ID)
	eref, err := e.git.Reference(plumbing.NewBranchReferenceName("exp"), true)
	if err != nil || eref.Hash().String() != expLine.TipCommit {
		t.Fatalf("refs/heads/exp = %v (err %v), want %s", eref, err, expLine.TipCommit)
	}
	// refs/tags/v1.0.0 present
	if _, err := e.git.Reference(plumbing.NewTagReferenceName("v1.0.0"), true); err != nil {
		t.Fatalf("tag ref missing: %v", err)
	}
	// refs/cairn/change/<id> -> change head
	cref, err := e.git.Reference(plumbing.ReferenceName("refs/cairn/change/"+ch.ID), true)
	if err != nil || cref.Hash().String() != r.HeadCommit {
		t.Fatalf("change ref = %v (err %v), want %s", cref, err, r.HeadCommit)
	}
}

func TestExportAbandonedLineNotExported(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	exp, _ := e.CreateLine("exp", main.ID)
	ch, _ := e.CreateChange(exp.ID, "e")
	e.Commit(ch.ID, map[string][]byte{"x.txt": []byte("x\n")}, "")
	if err := e.AbandonLine(exp.ID); err != nil {
		t.Fatalf("AbandonLine: %v", err)
	}
	if err := e.Export(); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if _, err := e.git.Reference(plumbing.NewBranchReferenceName("exp"), true); err == nil {
		t.Fatal("refs/heads/exp must not exist for an abandoned line")
	}
}

func TestExportFoldedLineNotExported(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	seedLineTip(t, e, main.ID, map[string][]byte{"a.txt": []byte("a\n")})
	exp, _ := e.CreateLine("exp", main.ID)
	ch, _ := e.CreateChange(exp.ID, "e")
	e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("a\n"), "n.txt": []byte("new\n")}, "")
	if err := e.FoldLine(exp.ID); err != nil {
		t.Fatalf("FoldLine: %v", err)
	}
	if err := e.Export(); err != nil {
		t.Fatalf("Export: %v", err)
	}
	// folded line's branch ref dropped
	if _, err := e.git.Reference(plumbing.NewBranchReferenceName("exp"), true); err == nil {
		t.Fatal("refs/heads/exp must not exist for a folded line")
	}
	// folded line's change ref dropped (change marked folded)
	if _, err := e.git.Reference(plumbing.ReferenceName("refs/cairn/change/"+ch.ID), true); err == nil {
		t.Fatal("refs/cairn/change/<id> must not exist for a folded line's change")
	}
	// main still exported and now contains n.txt (the fold fast-forwarded it)
	if _, err := e.git.Reference(plumbing.NewBranchReferenceName("main"), true); err != nil {
		t.Fatalf("refs/heads/main missing: %v", err)
	}
}

func TestExportPrunesFoldedRefsOnReexport(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	seedLineTip(t, e, main.ID, map[string][]byte{"a.txt": []byte("a\n")})
	exp, _ := e.CreateLine("exp", main.ID)
	ch, _ := e.CreateChange(exp.ID, "e")
	e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("a\n"), "n.txt": []byte("new\n")}, "")
	if err := e.Export(); err != nil {
		t.Fatalf("export1: %v", err)
	} // exp ref now exists
	if err := e.FoldLine(exp.ID); err != nil {
		t.Fatalf("fold: %v", err)
	}
	if err := e.Export(); err != nil {
		t.Fatalf("export2: %v", err)
	}
	if _, err := e.git.Reference(plumbing.NewBranchReferenceName("exp"), true); err == nil {
		t.Fatal("refs/heads/exp must be pruned after fold + re-export")
	}
	if _, err := e.git.Reference(plumbing.ReferenceName("refs/cairn/change/"+ch.ID), true); err == nil {
		t.Fatal("refs/cairn/change/<id> must be pruned after fold + re-export")
	}
}
