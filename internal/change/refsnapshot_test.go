package change

import (
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// A frozen view must not change when the real storer does — that is the
// whole point: the network phase reads refs as they stood under the lock.
func TestFrozenRefStorerIgnoresLaterWrites(t *testing.T) {
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = e.Close() }()
	name := plumbing.ReferenceName("refs/heads/frozen-test")
	h1 := plumbing.NewHash("1111111111111111111111111111111111111111")
	h2 := plumbing.NewHash("2222222222222222222222222222222222222222")
	if err := e.git.Storer.SetReference(plumbing.NewHashReference(name, h1)); err != nil {
		t.Fatal(err)
	}
	snap, err := snapshotRefs(e.git.Storer)
	if err != nil {
		t.Fatalf("snapshotRefs: %v", err)
	}
	frozen := newFrozenRefStorer(e.git.Storer, snap)

	// The world moves on underneath.
	if err := e.git.Storer.SetReference(plumbing.NewHashReference(name, h2)); err != nil {
		t.Fatal(err)
	}
	if err := e.git.Storer.RemoveReference(plumbing.ReferenceName("refs/heads/main")); err != nil && err != plumbing.ErrReferenceNotFound {
		t.Fatal(err)
	}

	got, err := frozen.Reference(name)
	if err != nil || got.Hash() != h1 {
		t.Fatalf("frozen Reference = %v (%v), want the snapshot's %s", got, err, h1)
	}
	iter, err := frozen.IterReferences()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[plumbing.ReferenceName]plumbing.Hash{}
	_ = iter.ForEach(func(r *plumbing.Reference) error { seen[r.Name()] = r.Hash(); return nil })
	if seen[name] != h1 {
		t.Fatalf("frozen IterReferences shows %s, want %s", seen[name], h1)
	}
	// Writes still pass through to the real store.
	h3 := plumbing.NewHash("3333333333333333333333333333333333333333")
	if err := frozen.SetReference(plumbing.NewHashReference(name, h3)); err != nil {
		t.Fatal(err)
	}
	if real, _ := e.git.Storer.Reference(name); real == nil || real.Hash() != h3 {
		t.Fatalf("write through the frozen storer did not reach the real one")
	}
}

// The snapshot is taken after pinning, so the pins the refspecs name are in it.
func TestPreparedPushSnapshotIncludesPins(t *testing.T) {
	e, remote := pushFixture(t)
	pp, err := e.PreparePushToRemoteBranch(remote, "feat", false)
	if err != nil {
		t.Fatalf("PreparePush: %v", err)
	}
	defer func() { _ = e.FinishPush(pp) }()
	if len(pp.pins) == 0 {
		t.Fatal("no pins")
	}
	frozen := newFrozenRefStorer(e.git.Storer, pp.refs)
	for _, p := range pp.pins {
		if _, err := frozen.Reference(p.pinned); err != nil {
			t.Fatalf("pin %s missing from the frozen view: %v", p.pinned, err)
		}
	}
	// And a plain push through the frozen view still lands.
	if err := e.NetworkPush(pp); err != nil {
		t.Fatalf("NetworkPush via frozen refs: %v", err)
	}
	_ = git.NoErrAlreadyUpToDate
}
