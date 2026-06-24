package change

import (
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

// TestProperty9_GitCompat is property 9 of the Phase-1 convergence proof, kept
// in-package so it can reach the unexported e.git store. It builds a small graph
// (a tagged main, an open conflicted change on a forked line), exports, then
// asserts via real git refs that refs/heads/main, refs/tags/<name>, and
// refs/cairn/change/<id> all resolve to the expected shas, and that the
// conflicted change's committed tree carries diff3 conflict markers.
func TestProperty9_GitCompat(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")

	// main: base file, then advance and tag the tip.
	seedLineTip(t, e, main.ID, map[string][]byte{"f.txt": []byte("base\n")})

	// exp forks at the shared base before main advances.
	exp, _ := e.CreateLine("exp", main.ID)

	// main advances base -> X.
	mc, _ := e.CreateChange(main.ID, "m")
	if _, err := e.Commit(mc.ID, map[string][]byte{"f.txt": []byte("X\n")}, nil, ""); err != nil {
		t.Fatalf("main advance: %v", err)
	}
	main, _ = e.LineByName("main")
	mainTip := main.TipCommit
	if err := e.Tag("v1", mainTip, "tagger"); err != nil {
		t.Fatalf("Tag: %v", err)
	}

	// exp edits the same path -> Y: a genuine 3-way conflict.
	ec, _ := e.CreateChange(exp.ID, "e")
	r, err := e.Commit(ec.ID, map[string][]byte{"f.txt": []byte("Y\n")}, nil, "")
	if err != nil {
		t.Fatalf("exp commit: %v", err)
	}
	if len(r.Conflicts) != 1 {
		t.Fatalf("expected exactly 1 conflict, got %d: %+v", len(r.Conflicts), r.Conflicts)
	}
	change, _ := e.GetChange(ec.ID)
	changeHead := change.HeadCommit

	if err := e.Export(); err != nil {
		t.Fatalf("Export: %v", err)
	}

	// refs/heads/main resolves to main's tip.
	ref, err := e.git.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatalf("Reference(refs/heads/main): %v", err)
	}
	if ref.Hash().String() != mainTip {
		t.Fatalf("refs/heads/main = %s, want %s", ref.Hash(), mainTip)
	}

	// refs/heads/exp resolves to exp's tip (open line).
	expRef, err := e.git.Reference(plumbing.NewBranchReferenceName("exp"), true)
	if err != nil {
		t.Fatalf("Reference(refs/heads/exp): %v", err)
	}
	expLine, _ := e.lineByID(exp.ID)
	if expRef.Hash().String() != expLine.TipCommit {
		t.Fatalf("refs/heads/exp = %s, want %s", expRef.Hash(), expLine.TipCommit)
	}

	// refs/tags/v1 resolves to the tagged main tip.
	tagRef, err := e.git.Reference(plumbing.NewTagReferenceName("v1"), true)
	if err != nil {
		t.Fatalf("Reference(refs/tags/v1): %v", err)
	}
	if tagRef.Hash().String() != mainTip {
		t.Fatalf("refs/tags/v1 = %s, want %s", tagRef.Hash(), mainTip)
	}

	// refs/cairn/change/<id> resolves to the open change's head.
	chRef, err := e.git.Reference(plumbing.ReferenceName("refs/cairn/change/"+ec.ID), true)
	if err != nil {
		t.Fatalf("Reference(refs/cairn/change/%s): %v", ec.ID, err)
	}
	if chRef.Hash().String() != changeHead {
		t.Fatalf("refs/cairn/change/%s = %s, want %s", ec.ID, chRef.Hash(), changeHead)
	}

	// The conflicted change's committed tree carries diff3 conflict markers.
	files, err := e.Files(changeHead)
	if err != nil {
		t.Fatalf("Files(change head): %v", err)
	}
	if !strings.Contains(string(files["f.txt"]), "<<<<<<<") {
		t.Fatalf("conflicted tree f.txt has no diff3 marker: %q", files["f.txt"])
	}
}
