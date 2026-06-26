package change

import (
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

// bareStoreDir returns the on-disk bare git store backing a test engine (Open
// lays its store at dir/objects.git) — what OpenBare binds to server-side.
func bareStoreDir(e *Engine) string { return filepath.Join(e.dir, "objects.git") }

func setMetaRef(t *testing.T, e *Engine) {
	t.Helper()
	metaSHA, err := e.ExportMeta()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.git.Storer.SetReference(plumbing.NewHashReference(
		plumbing.ReferenceName("refs/cairn/meta"), plumbing.NewHash(metaSHA))); err != nil {
		t.Fatal(err)
	}
}

// TestOpenBareReconstructsGraphFromMeta is the Slice-0 proof: the server can open
// a hosted bare repo and rebuild its line tree from the pushed refs/cairn/meta,
// with no on-disk .cairn catalogue.
func TestOpenBareReconstructsGraphFromMeta(t *testing.T) {
	e := newTestEngine(t)
	e.SetIdentity("Dev", "dev@x.io")
	main, _ := e.LineByName("main")
	ch, _ := e.CreateChange(main.ID, "dev")
	if _, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("v1\n")}, nil, "c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CreateLine("feature", main.ID); err != nil {
		t.Fatal(err)
	}
	setMetaRef(t, e) // what a cairn client's push writes to the server

	se, err := OpenBare(bareStoreDir(e))
	if err != nil {
		t.Fatalf("OpenBare: %v", err)
	}
	defer se.Close()

	loaded, err := se.LoadFromMeta()
	if err != nil {
		t.Fatalf("LoadFromMeta: %v", err)
	}
	if !loaded {
		t.Fatal("LoadFromMeta reported no meta, but the ref was set")
	}

	nodes, err := se.GetLineTree()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, n := range nodes {
		names[n.Line.Name] = true
	}
	if !names["main"] || !names["feature"] {
		t.Fatalf("server-side line tree = %v, want main + feature", names)
	}
	if n, err := se.OpenConflictCount(); err != nil || n != 0 {
		t.Fatalf("OpenConflictCount = (%d,%v), want (0,nil)", n, err)
	}
}

// TestOpenBareNoMetaIsEmpty: a plain-git / not-yet-pushed repo has no cairn meta;
// the server engine opens cleanly and reports no lines (not an error).
func TestOpenBareNoMetaIsEmpty(t *testing.T) {
	e := newTestEngine(t) // a bare store with NO refs/cairn/meta set
	se, err := OpenBare(bareStoreDir(e))
	if err != nil {
		t.Fatalf("OpenBare: %v", err)
	}
	defer se.Close()
	loaded, err := se.LoadFromMeta()
	if err != nil {
		t.Fatalf("LoadFromMeta: %v", err)
	}
	if loaded {
		t.Fatal("LoadFromMeta reported meta on a repo with none")
	}
	nodes, err := se.GetLineTree()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected no lines without meta, got %d", len(nodes))
	}
}
