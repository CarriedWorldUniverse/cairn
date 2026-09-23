package change

import (
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

// Generations are computed once and kept: the commit_gen table must hold them
// after the engine closes, and a reopened engine must read them back rather
// than walk the history again.
func TestGenerationsPersistAcrossReopen(t *testing.T) {
	tp := newTopo(t)
	tp.commit(t, "A")
	tp.branch(t, "feature")
	tp.commit(t, "F1")
	tp.checkout(t, "main")
	tp.commit(t, "B")
	tp.checkout(t, "feature")
	tp.mergeFrom(t, "main", "M") // M(F1, B): generation 3

	dir := t.TempDir()
	e, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.ImportFromRemote(tp.dir); err != nil {
		t.Fatalf("import: %v", err)
	}
	want := map[string]uint32{"A": 1, "F1": 2, "B": 2, "M": 3}
	for label, g := range want {
		got, err := e.generation(plumbing.NewHash(tp.sha[label]))
		if err != nil {
			t.Fatal(err)
		}
		if got != g {
			t.Fatalf("generation(%s) = %d, want %d", label, got, g)
		}
	}
	_ = e.Close()

	e2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e2.Close() }()
	var stored uint32
	if err := e2.db.QueryRow(`SELECT gen FROM commit_gen WHERE sha=?`, tp.sha["M"]).Scan(&stored); err != nil {
		t.Fatalf("generation of M was not persisted: %v", err)
	}
	if stored != 3 {
		t.Fatalf("stored generation of M = %d, want 3", stored)
	}
}
