package change

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
)

// rawObject returns the type and inflated bytes of object h. The bytes belong
// to the object store's cache and must not be modified.
func (e *Engine) rawObject(t plumbing.ObjectType, h plumbing.Hash) ([]byte, error) {
	if e.store != nil {
		if typ, data, ok := e.store.fast.Get(h); ok {
			if typ != t {
				return nil, plumbing.ErrObjectNotFound
			}
			return data, nil
		}
	}
	obj, err := e.git.Storer.EncodedObject(t, h)
	if err != nil {
		return nil, e.notFound(err, h)
	}
	if f, ok := obj.(*fastObject); ok {
		return f.data, nil
	}
	r, err := obj.Reader()
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		return nil, err
	}
	return data, nil
}

// rawTreeEntry is one entry of a tree object. name aliases the tree's bytes.
type rawTreeEntry struct {
	mode filemode.FileMode
	name []byte
	hash plumbing.Hash
}

var errMalformedTree = errors.New("malformed tree object")

// parseTree decodes a tree object: a run of "<octal mode> <name>\x00<20-byte
// hash>". It is the walk go-git's Tree.Decode does, without building a Tree —
// no per-entry string, no name index — because the callers here read every
// tree of a commit and want the entries once.
func parseTree(data []byte, fn func(rawTreeEntry) error) error {
	for len(data) > 0 {
		sp := bytes.IndexByte(data, ' ')
		if sp <= 0 {
			return errMalformedTree
		}
		var mode uint32
		for _, c := range data[:sp] {
			if c < '0' || c > '7' || mode > 1<<28 {
				return errMalformedTree
			}
			mode = mode<<3 | uint32(c-'0')
		}
		data = data[sp+1:]
		nul := bytes.IndexByte(data, 0)
		if nul < 0 || len(data) < nul+1+20 {
			return errMalformedTree
		}
		ent := rawTreeEntry{mode: filemode.FileMode(mode), name: data[:nul]}
		copy(ent.hash[:], data[nul+1:nul+21])
		data = data[nul+21:]
		if err := fn(ent); err != nil {
			return err
		}
	}
	return nil
}

// entryMode maps a tree entry's git mode onto cairn's. Anything that is not
// executable, a symlink or a gitlink is a regular file — including git's
// deprecated group-writable 100664.
func entryMode(m filemode.FileMode) EntryMode {
	switch m {
	case filemode.Executable:
		return ModeExecutable
	case filemode.Symlink:
		return ModeSymlink
	case filemode.Submodule:
		// Gitlink: keep the entry, but mark it so no content path ever
		// treats its commit SHA as a blob in this store (#140). Before
		// this case existed it fell through to ModeRegular and the first
		// materialize died on readBlob, taking every later command with
		// it because materialize runs on all of them.
		return ModeGitlink
	}
	return ModeRegular
}

// readTreeLevel reads ONE tree object and returns its entries, validated: a
// name must be safe to materialize, and must not appear twice.
//
// A name cannot legitimately appear twice at one tree level — as two
// entries with the same name (regardless of kind), or as both a leaf and
// a subtree. go-git's decoder does not enforce this on READ (only the
// write side, buildTree/writeTreeRefs, rejects it when cairn itself
// builds a tree), so a hostile remote can hand us an on-the-wire tree
// object with e.g. two "evil" entries — one a symlink, one a directory —
// and a flattening walk would silently yield both "evil" (as a symlink) and
// "evil/pwned" (walking into the directory), a collision fix A's
// materialize-side guard treats as defense in depth but that should never
// reach a consumer in the first place (#126). Reject outright at the read
// boundary, mirroring the write-side "file and directory at the same
// level" guard.
func (e *Engine) readTreeLevel(treeHash plumbing.Hash) ([]rawTreeEntry, error) {
	data, err := e.rawObject(plumbing.TreeObject, treeHash)
	if err != nil {
		return nil, fmt.Errorf("change.readTreeRefs: tree %s: %w", treeHash, err)
	}
	var entries []rawTreeEntry
	if err := parseTree(data, func(ent rawTreeEntry) error {
		entries = append(entries, ent)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("change.readTreeRefs: tree %s: %w", treeHash, err)
	}
	// Canonical trees are sorted, so a duplicate name is normally adjacent to
	// its twin — but a hostile tree need not be sorted, so adjacency is only
	// the fast path: any entry out of order sends the level through a set.
	sorted := true
	for i := range entries {
		if err := validTreeEntryName(string(entries[i].name)); err != nil {
			return nil, fmt.Errorf("change.readTreeRefs: %w", err)
		}
		if i > 0 {
			switch c := bytes.Compare(entries[i-1].name, entries[i].name); {
			case c == 0:
				return nil, fmt.Errorf("change.readTreeRefs: tree %s has a duplicate entry name %q (invalid tree)", treeHash, entries[i].name)
			case c > 0:
				sorted = false
			}
		}
	}
	if !sorted {
		seen := make(map[string]struct{}, len(entries))
		for i := range entries {
			name := string(entries[i].name)
			if _, dup := seen[name]; dup {
				return nil, fmt.Errorf("change.readTreeRefs: tree %s has a duplicate entry name %q (invalid tree)", treeHash, name)
			}
			seen[name] = struct{}{}
		}
	}
	return entries, nil
}

func joinTreePath(prefix string, name []byte) string {
	if prefix == "" {
		return string(name)
	}
	return prefix + "/" + string(name)
}

// collectTreeRefs flattens the tree at treeHash into out, keyed by the full
// "/"-separated path under prefix.
func (e *Engine) collectTreeRefs(treeHash plumbing.Hash, prefix string, out map[string]TreeEntry) error {
	entries, err := e.readTreeLevel(treeHash)
	if err != nil {
		return err
	}
	for i := range entries {
		ent := &entries[i]
		path := joinTreePath(prefix, ent.name)
		if ent.mode == filemode.Dir {
			if err := e.collectTreeRefs(ent.hash, path, out); err != nil {
				return err
			}
			continue
		}
		out[path] = TreeEntry{SHA: ent.hash.String(), Mode: entryMode(ent.mode)}
	}
	return nil
}

// diffTreeRefs returns, for two trees, the entries of each that lie where the
// trees DIFFER: every path that is in only one of them or whose entry is not
// identical in both. Paths under a subtree both sides share are in neither
// map, and the subtree is never read.
//
// That is what makes a diff cost what changed rather than what exists. Two
// commits of a 16,000-file tree that differ in one file share every tree
// object except the handful on the path to that file; flattening both trees
// in full to find it read ~3,000 tree objects, this reads a dozen. Each tree
// that IS read gets the same validation as a full walk.
//
// An empty hash is the empty tree.
func (e *Engine) diffTreeRefs(a, b string) (old, new map[string]TreeEntry, err error) {
	old, new = map[string]TreeEntry{}, map[string]TreeEntry{}
	var ha, hb plumbing.Hash
	if a != "" {
		ha = plumbing.NewHash(a)
	}
	if b != "" {
		hb = plumbing.NewHash(b)
	}
	if err := e.diffTreeLevel(ha, a != "", hb, b != "", "", old, new); err != nil {
		return nil, nil, err
	}
	return old, new, nil
}

func (e *Engine) diffTreeLevel(a plumbing.Hash, hasA bool, b plumbing.Hash, hasB bool, prefix string, old, new map[string]TreeEntry) error {
	if hasA && hasB && a == b {
		return nil
	}
	if !hasA {
		if !hasB {
			return nil
		}
		return e.collectTreeRefs(b, prefix, new)
	}
	if !hasB {
		return e.collectTreeRefs(a, prefix, old)
	}
	ea, err := e.readTreeLevel(a)
	if err != nil {
		return err
	}
	eb, err := e.readTreeLevel(b)
	if err != nil {
		return err
	}
	inB := make(map[string]*rawTreeEntry, len(eb))
	for i := range eb {
		inB[string(eb[i].name)] = &eb[i]
	}
	for i := range ea {
		x := &ea[i]
		path := joinTreePath(prefix, x.name)
		y, both := inB[string(x.name)]
		if both {
			delete(inB, string(x.name))
		}
		xDir := x.mode == filemode.Dir
		switch {
		case both && xDir && y.mode == filemode.Dir:
			if err := e.diffTreeLevel(x.hash, true, y.hash, true, path, old, new); err != nil {
				return err
			}
			continue
		case both && !xDir && y.mode != filemode.Dir && x.hash == y.hash && entryMode(x.mode) == entryMode(y.mode):
			continue // the same file on both sides
		}
		if err := e.addTreeSide(x, path, old); err != nil {
			return err
		}
		if both {
			if err := e.addTreeSide(y, path, new); err != nil {
				return err
			}
		}
	}
	for i := range eb {
		y := &eb[i]
		if _, only := inB[string(y.name)]; !only {
			continue
		}
		if err := e.addTreeSide(y, joinTreePath(prefix, y.name), new); err != nil {
			return err
		}
	}
	return nil
}

// addTreeSide records one side's entry at path: a file as itself, a
// directory as everything under it.
func (e *Engine) addTreeSide(ent *rawTreeEntry, path string, out map[string]TreeEntry) error {
	if ent.mode == filemode.Dir {
		return e.collectTreeRefs(ent.hash, path, out)
	}
	out[path] = TreeEntry{SHA: ent.hash.String(), Mode: entryMode(ent.mode)}
	return nil
}

// prefetchTrees reads the tree at root and every tree below it, in parallel,
// so that the walk which follows finds each one already inflated in the
// object store's cache.
//
// It changes nothing about that walk — its order, its validation, the error
// it reports are all still decided serially, by collectTreeRefs. This only
// moves the expensive part (inflating some thousands of tree objects and
// resolving their delta chains, which is nearly all a flattening walk costs)
// onto every core instead of one. Anything it cannot read or parse it skips:
// the real walk will meet the same object and say what is wrong with it.
//
// Only an engine with the lock-free reader does this; through go-git's
// locked store there is no parallelism to be had.
func (e *Engine) prefetchTrees(root plumbing.Hash) {
	if e.store == nil {
		return
	}
	workers := runtime.GOMAXPROCS(0)
	if workers > 16 {
		workers = 16
	}
	if workers < 2 {
		return
	}
	var (
		wg   sync.WaitGroup
		sem  = make(chan struct{}, workers)
		mu   sync.Mutex
		seen = map[plumbing.Hash]struct{}{root: {}}
	)
	var visit func(h plumbing.Hash)
	visit = func(h plumbing.Hash) {
		_, data, ok := e.store.fast.Get(h)
		if !ok {
			return
		}
		_ = parseTree(data, func(ent rawTreeEntry) error {
			if ent.mode != filemode.Dir {
				return nil
			}
			mu.Lock()
			_, dup := seen[ent.hash]
			seen[ent.hash] = struct{}{}
			mu.Unlock()
			if dup {
				return nil
			}
			select {
			case sem <- struct{}{}:
				wg.Add(1)
				go func(h plumbing.Hash) {
					defer wg.Done()
					visit(h)
					<-sem
				}(ent.hash)
			default:
				visit(ent.hash) // every worker is busy: keep going here
			}
			return nil
		})
	}
	visit(root)
	wg.Wait()
}
