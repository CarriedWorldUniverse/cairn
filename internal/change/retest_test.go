package change

import (
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// TestCreateLineForksFromSealedTip guards the express-fork bug: a line must fork
// from its parent's SEALED tip, never the parent's "(working)" auto-snapshot —
// otherwise that placeholder-authored working commit becomes a permanent ancestor
// of the new line and surfaces in a pushed PR.
func TestCreateLineForksFromSealedTip(t *testing.T) {
	e := newTestEngine(t)
	e.SetIdentity("Real Dev", "real@dev.io")
	main, _ := e.LineByName("main")

	ch1, _ := e.CreateChange(main.ID, "dev")
	res, err := e.Commit(ch1.ID, map[string][]byte{"a.txt": []byte("v1\n")}, nil, "real work")
	if err != nil {
		t.Fatal(err)
	}
	sealed := res.HeadCommit

	// A working snapshot on a fresh change, authored by the placeholder identity —
	// main's tip becomes a "(working)" commit on top of the sealed one.
	e.SetIdentity("cairn", "cairn@users.noreply.cairn")
	ch2, _ := e.CreateChange(main.ID, "dev")
	_, working, err := e.SnapshotWorking(ch2.ID, map[string]TreeEntry{"a.txt": blobEntry(t, e, "v1\n")})
	if err != nil {
		t.Fatal(err)
	}
	main, _ = e.LineByName("main")
	if main.TipCommit != working {
		t.Fatalf("setup: main tip = %s, want the working commit %s", main.TipCommit, working)
	}
	if working == sealed {
		t.Fatal("setup: working snapshot should differ from the sealed tip")
	}

	feat, err := e.CreateLine("feature", main.ID)
	if err != nil {
		t.Fatal(err)
	}
	if feat.BaseCommit != sealed || feat.TipCommit != sealed {
		t.Fatalf("CreateLine forked at base=%s tip=%s, want sealed %s (working was %s)",
			feat.BaseCommit, feat.TipCommit, sealed, working)
	}
}

// makeAnnotatedTag builds and stores an annotated tag OBJECT (not a lightweight
// ref) pointing at commitSHA, returning the tag object's hash — what a cloned
// annotated tag's ref/catalogue row points at.
func makeAnnotatedTag(t *testing.T, e *Engine, commitSHA, name string) string {
	t.Helper()
	tag := &object.Tag{
		Name:       name,
		Tagger:     object.Signature{Name: "t", Email: "t@t", When: time.Unix(0, 0).UTC()},
		Message:    "annotated " + name + "\n",
		TargetType: plumbing.CommitObject,
		Target:     plumbing.NewHash(commitSHA),
	}
	obj := e.git.Storer.NewEncodedObject()
	if err := tag.Encode(obj); err != nil {
		t.Fatal(err)
	}
	h, err := e.git.Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatal(err)
	}
	return h.String()
}

// TestTopoCommitsPeelsAnnotatedTag guards the reauthor crash: an annotated tag
// anchor points at a tag object, not a commit, so loading it as a commit fails
// ("object not found"). topoCommits must peel it to the underlying commit, not
// crash.
func TestTopoCommitsPeelsAnnotatedTag(t *testing.T) {
	e := newTestEngine(t)
	e.SetIdentity("Dev", "dev@x.io")
	main, _ := e.LineByName("main")
	ch, _ := e.CreateChange(main.ID, "dev")
	res, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("x\n")}, nil, "c1")
	if err != nil {
		t.Fatal(err)
	}
	commitSHA := res.HeadCommit
	tagOID := makeAnnotatedTag(t, e, commitSHA, "v1")
	if tagOID == commitSHA {
		t.Fatal("setup: annotated tag OID should differ from the commit")
	}

	// peelToCommit resolves the tag object to its commit.
	if got, ok := e.peelToCommit(tagOID); !ok || got != commitSHA {
		t.Fatalf("peelToCommit(annotated tag) = (%s,%v), want (%s,true)", got, ok, commitSHA)
	}
	// topoCommits with the tag OID as an anchor must not crash and must reach the commit.
	order, err := e.topoCommits([]string{tagOID})
	if err != nil {
		t.Fatalf("topoCommits crashed on an annotated-tag anchor: %v", err)
	}
	found := false
	for _, o := range order {
		if o == commitSHA {
			found = true
		}
	}
	if !found {
		t.Fatalf("annotated tag did not peel to its commit; order=%v", order)
	}
	// A dangling/garbage anchor is skipped, not fatal.
	if got, ok := e.peelToCommit("0000000000000000000000000000000000000000"); ok {
		t.Fatalf("peelToCommit(zero) = (%s,true), want skip", got)
	}
}
