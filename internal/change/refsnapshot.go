package change

import (
	"errors"
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"
)

// The push's network phase deliberately runs OUTSIDE the working-copy lock
// (#98), so other commands are not held behind a slow upload. But go-git's
// Push reads EVERY local ref to build its "haves" set, and dotgit writes a
// loose ref by truncating and rewriting the file — so another process's
// Export (under the lock, rewriting refs/heads/* for a push of its own) could
// be caught mid-write and the upload failed with "ref file is empty" (#177).
// Reproduced at ~1% under stress; seen in CI.
//
// The fix is to hand the network phase a view of the refs FROZEN while the
// lock was still held: snapshotRefs captures them in PreparePush, right after
// the pins are written, and frozenRefStorer serves that snapshot for every
// read while passing writes (go-git's post-push remote-tracking updates)
// through to the real storer. Nothing another process does to loose refs
// during the upload can then be observed by it.

// snapshotRefs copies every reference the storer holds. A ref whose file is
// caught empty by a concurrent writer — possible for refs/remotes/* during
// another process's post-push tracking update, which runs under the remote
// lock but not the working-copy lock — is retried once and otherwise skipped:
// a missing remote-tracking ref only shrinks the haves set (a larger upload),
// it cannot make the push wrong. Any other error is real.
func snapshotRefs(st storer.ReferenceStorer) ([]*plumbing.Reference, error) {
	var out []*plumbing.Reference
	iter, err := st.IterReferences()
	if err != nil {
		return nil, fmt.Errorf("snapshotRefs: %w", err)
	}
	err = iter.ForEach(func(r *plumbing.Reference) error {
		out = append(out, r)
		return nil
	})
	iter.Close()
	if err == nil {
		return out, nil
	}
	if !errors.Is(err, dotgit.ErrEmptyRefFile) {
		return nil, fmt.Errorf("snapshotRefs: %w", err)
	}
	// Torn read mid-iteration: take it again, and if the same window is hit
	// twice, accept the partial view — see the doc comment for why that is safe.
	out = out[:0]
	iter, err = st.IterReferences()
	if err != nil {
		return nil, fmt.Errorf("snapshotRefs: %w", err)
	}
	_ = iter.ForEach(func(r *plumbing.Reference) error {
		out = append(out, r)
		return nil
	})
	iter.Close()
	return out, nil
}

// frozenRefStorer is the real storer with its reference READS replaced by a
// snapshot. Objects, config, shallow, index and reference WRITES all reach the
// underlying storage unchanged.
type frozenRefStorer struct {
	storage.Storer
	refs map[plumbing.ReferenceName]*plumbing.Reference
	list []*plumbing.Reference
}

func newFrozenRefStorer(real storage.Storer, refs []*plumbing.Reference) *frozenRefStorer {
	m := make(map[plumbing.ReferenceName]*plumbing.Reference, len(refs))
	for _, r := range refs {
		m[r.Name()] = r
	}
	return &frozenRefStorer{Storer: real, refs: m, list: refs}
}

func (f *frozenRefStorer) Reference(name plumbing.ReferenceName) (*plumbing.Reference, error) {
	if r, ok := f.refs[name]; ok {
		return r, nil
	}
	return nil, plumbing.ErrReferenceNotFound
}

func (f *frozenRefStorer) IterReferences() (storer.ReferenceIter, error) {
	return storer.NewReferenceSliceIter(f.list), nil
}
