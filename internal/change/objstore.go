package change

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/CarriedWorldUniverse/cairn/internal/packread"
	"github.com/CarriedWorldUniverse/cairn/internal/winretry"
	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5/plumbing"
	gitcache "github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

// objStore is the engine's go-git object store with its packfiles held OPEN.
//
// go-git's default filesystem storage re-opens the packfile for every single
// object it reads: an open(2), a chain of lstat(2)s resolving the path, a fresh
// scanner, a close(2). On a small repo that is invisible; on a 60k-commit clone
// it was the whole cost of every history walk — `tree` spent 47 of its 48
// seconds there, almost none of it reading objects. KeepDescriptors makes
// go-git open each pack once and keep it.
//
// The price is that a kept packfile is ONE shared scanner, which is not safe to
// read from two goroutines — and both cairn (parallel materialize, parallel
// working-copy scan) and go-git itself (the delta selector when building a
// pack to push) read objects concurrently. So every method that reaches a
// packfile is serialized here. Reads are short (an object is inflated into
// memory and handed back; the lock is not held while the caller uses it), and
// they are far cheaper than the open-per-object they replace, so serializing
// them costs less than the parallelism it gives up.
//
// It embeds *filesystem.Storage so that everything else — references, config,
// shallow, index, the optional interfaces go-git type-asserts for — is
// untouched.
//
// In front of all that sits fast (internal/packread): a reader that finds an
// object by binary search in the mapped .idx and inflates it in place. It
// serves nearly every read — without the lock, so parallel readers really are
// parallel — and without go-git ever loading a pack index, which alone costs
// ~150ms per process on a large clone. go-git is asked only for what fast
// declines, and for everything that is not a plain object read.
type objStore struct {
	*filesystem.Storage
	mu     sync.Mutex
	fast   *packread.Store
	gitDir string
	// gitGen is the fast store's pack generation as of go-git's last
	// (re)index: when fast has since seen new packs, go-git's own pack index
	// is stale too and is rebuilt on a miss (see gitStale).
	gitGen uint64
}

// debugObjects (CAIRN_DEBUG_OBJECTS=1) traces every object lookup to stderr:
// which layer served it, or why each declined.
var debugObjects = os.Getenv("CAIRN_DEBUG_OBJECTS") == "1"

func tracef(format string, args ...any) {
	if debugObjects {
		fmt.Fprintf(os.Stderr, "cairn: objects: "+format+"\n", args...)
	}
}

// gitStale reports (and records) that fast has seen packs go-git has not been
// told about, after Reindex-ing go-git's pack index so its next lookup lists
// objects/pack afresh. Caller holds s.mu.
func (s *objStore) gitStale() bool {
	g := s.fast.Generation()
	if g == s.gitGen {
		return false
	}
	s.gitGen = g
	s.Storage.ObjectStorage.Reindex()
	return true
}

// explainMiss says which layers were consulted for h and why each missed. It
// re-runs the lookups, so call it only on an error path.
func (s *objStore) explainMiss(h plumbing.Hash) string {
	_, _, why, _ := s.fast.GetWhy(h)
	s.mu.Lock()
	s.gitStale()
	gerr := s.Storage.HasEncodedObject(h)
	s.mu.Unlock()
	return fmt.Sprintf("object %s not found; consulted: fast pack reader and loose objects (%s); go-git store (%v)", h, why, gerr)
}

// newObjStore opens the object store rooted at fs, whose on-disk location is
// gitDir (the bare repository directory).
func newObjStore(fs billy.Filesystem, gitDir string) *objStore {
	return &objStore{
		Storage: filesystem.NewStorageWithOptions(fs, gitcache.NewObjectLRUDefault(),
			filesystem.Options{KeepDescriptors: true}),
		fast:   packread.Open(filepath.Join(gitDir, "objects")),
		gitDir: gitDir,
	}
}

// fastObject is an object served by packread: its bytes, already inflated,
// under a hash that is known rather than recomputed.
type fastObject struct {
	hash plumbing.Hash
	typ  plumbing.ObjectType
	data []byte
}

func (o *fastObject) Hash() plumbing.Hash           { return o.hash }
func (o *fastObject) Type() plumbing.ObjectType     { return o.typ }
func (o *fastObject) SetType(t plumbing.ObjectType) { o.typ = t }
func (o *fastObject) Size() int64                   { return int64(len(o.data)) }
func (o *fastObject) SetSize(int64)                 {}
func (o *fastObject) Reader() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(o.data)), nil
}
func (o *fastObject) Writer() (io.WriteCloser, error) {
	return nil, errors.New("change: stored object is read-only")
}

func (s *objStore) EncodedObject(t plumbing.ObjectType, h plumbing.Hash) (plumbing.EncodedObject, error) {
	if typ, data, why, ok := s.fast.GetWhy(h); ok {
		if t != plumbing.AnyObject && typ != t {
			tracef("%s: fast hit but type %s != wanted %s", h, typ, t)
			return nil, plumbing.ErrObjectNotFound
		}
		if debugObjects {
			tracef("%s: served by fast pack reader (%s)", h, typ)
		}
		return &fastObject{hash: h, typ: typ, data: data}, nil
	} else if debugObjects {
		tracef("%s: fast pack reader missed (%s); asking go-git", h, why)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o, err := s.Storage.EncodedObject(t, h)
	if errors.Is(err, plumbing.ErrObjectNotFound) && s.gitStale() {
		o, err = s.Storage.EncodedObject(t, h)
	}
	if debugObjects {
		if err != nil {
			tracef("%s: go-git missed: %v", h, err)
		} else {
			tracef("%s: served by go-git", h)
		}
	}
	return o, err
}

func (s *objStore) DeltaObject(t plumbing.ObjectType, h plumbing.Hash) (plumbing.EncodedObject, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Storage.DeltaObject(t, h)
}

func (s *objStore) EncodedObjectSize(h plumbing.Hash) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Storage.EncodedObjectSize(h)
}

func (s *objStore) HasEncodedObject(h plumbing.Hash) error {
	if s.fast.Has(h) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.Storage.HasEncodedObject(h)
	if errors.Is(err, plumbing.ErrObjectNotFound) && s.gitStale() {
		err = s.Storage.HasEncodedObject(h)
	}
	return err
}

// SetEncodedObject stores o — unless it is already stored. An object's name
// is its content, so one that is present is this one, and go-git would only
// deflate it into a temporary file to discover the same and delete it again.
// A snapshot of a large tree re-derives every directory's tree object and
// nearly all of them already exist, which made that wasted work most of the
// snapshot.
func (s *objStore) SetEncodedObject(o plumbing.EncodedObject) (plumbing.Hash, error) {
	switch o.Type() {
	case plumbing.CommitObject, plumbing.TreeObject, plumbing.BlobObject, plumbing.TagObject:
		if h := o.Hash(); s.fast.Has(h) {
			return h, nil
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Storage.SetEncodedObject(o)
}

func (s *objStore) HashesWithPrefix(prefix []byte) ([]plumbing.Hash, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Storage.HashesWithPrefix(prefix)
}

// PackfileWriter is how a fetched pack arrives. go-git's own writer indexes
// the pack as it is received, on one core; for a large clone that indexing is
// most of the clone. This one spools the pack to disk and, when it is
// complete, indexes it with packread.IndexPack — the same index, built on
// every core — then moves pack and index into place.
//
// Anything IndexPack declines (a thin pack, a damaged one, an empty one) is
// replayed through go-git's writer instead, so what counts as an error, and
// how it is reported, is exactly what it was.
func (s *objStore) PackfileWriter() (io.WriteCloser, error) {
	dir := filepath.Join(s.gitDir, "objects", "pack")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, "tmp_pack_cairn_")
	if err != nil {
		return nil, err
	}
	return &packWriter{s: s, f: f, dir: dir}, nil
}

type packWriter struct {
	s      *objStore
	f      *os.File
	dir    string
	n      int64
	closed bool
}

func (w *packWriter) Write(p []byte) (int, error) {
	n, err := w.f.Write(p)
	w.n += int64(n)
	return n, err
}

func (w *packWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	tmp := w.f.Name()
	defer func() { _ = os.Remove(tmp) }() // a no-op once it has been renamed
	if err := w.f.Close(); err != nil {
		return err
	}
	if w.n == 0 {
		return nil // nothing was sent: nothing to store
	}
	// Whatever happens below, the pack directory may have changed.
	defer func() {
		w.s.mu.Lock()
		w.s.Storage.ObjectStorage.Reindex()
		w.s.mu.Unlock()
		w.s.fast.Invalidate()
	}()
	tmpIdx := tmp + ".idx"
	defer func() { _ = os.Remove(tmpIdx) }()
	sum, err := packread.IndexPack(tmp, tmpIdx)
	if err != nil {
		return w.viaGoGit(tmp)
	}
	base := filepath.Join(w.dir, "pack-"+sum.String())
	// Packs are named by their content: one already there is this one.
	if _, err := os.Stat(base + ".pack"); err == nil {
		if _, err := os.Stat(base + ".idx"); err == nil {
			return nil
		}
	}
	// The pack first, then its index. packread (the reader that serves nearly
	// everything) finds a pack by its .idx, so it never sees a pack whose
	// index is not yet in place; the pack it will read is always complete.
	// (go-git lists packs by their .pack file, so for the instant between
	// the renames its listing can see a pack without an index: a miss there
	// is retried once after a Reindex — see gitStale.)
	if err := winretry.Do(func() error { return os.Rename(tmp, base+".pack") }); err != nil {
		return err
	}
	return winretry.Do(func() error { return os.Rename(tmpIdx, base+".idx") })
}

// viaGoGit stores the spooled pack through go-git's indexing writer.
func (w *packWriter) viaGoGit(tmp string) error {
	in, err := os.Open(tmp)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	w.s.mu.Lock()
	out, err := w.s.Storage.PackfileWriter()
	w.s.mu.Unlock()
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// IterEncodedObjects returns an iterator that takes the lock for each step.
// With descriptors kept, go-git's pack iterator reads through the SAME file
// handle as the shared packfile, seeking to each object as it goes — so a step
// must not interleave with another read, but the lock is released between
// steps, and a ForEach callback is free to read objects itself.
func (s *objStore) IterEncodedObjects(t plumbing.ObjectType) (storer.EncodedObjectIter, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, err := s.Storage.IterEncodedObjects(t)
	if err != nil {
		return nil, err
	}
	return &lockedIter{s: s, it: it}, nil
}

type lockedIter struct {
	s  *objStore
	it storer.EncodedObjectIter
}

func (i *lockedIter) Next() (plumbing.EncodedObject, error) {
	i.s.mu.Lock()
	defer i.s.mu.Unlock()
	return i.it.Next()
}

func (i *lockedIter) ForEach(cb func(plumbing.EncodedObject) error) error {
	return storer.ForEachIterator(i, cb)
}

func (i *lockedIter) Close() {
	i.s.mu.Lock()
	defer i.s.mu.Unlock()
	i.it.Close()
}

// Close releases the kept packfile descriptors.
func (s *objStore) Close() error {
	s.fast.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Storage.Close()
}

// notFound decorates an object-not-found error from the engine's object reads
// with which layers were consulted and why each missed. The original error
// stays in the chain, so errors.Is(err, plumbing.ErrObjectNotFound) still
// holds; any other error passes through untouched.
func (e *Engine) notFound(err error, h plumbing.Hash) error {
	if err == nil || e.store == nil || !errors.Is(err, plumbing.ErrObjectNotFound) {
		return err
	}
	return fmt.Errorf("%w: %s", err, e.store.explainMiss(h))
}
