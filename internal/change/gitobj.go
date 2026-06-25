package change

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// treeEntryLess orders tree entries the way git canonically does: by name, but a
// directory (sub-tree) entry sorts as if its name ended in "/". This matters when
// a directory name is a prefix of a sibling file name — e.g. dir "app" vs file
// "app.go": git compares "app/" vs "app.go", and '.' (0x2e) < '/' (0x2f), so the
// file sorts first. A plain string comparison gets this backwards and go-git's
// tree encoder rejects the result with "entries in tree are not sorted".
func treeEntryLess(a, b object.TreeEntry) bool {
	an, bn := a.Name, b.Name
	if a.Mode == filemode.Dir {
		an += "/"
	}
	if b.Mode == filemode.Dir {
		bn += "/"
	}
	return an < bn
}

// writeBlob stores data as a git blob object and returns its hash.
func (e *Engine) writeBlob(data []byte) (plumbing.Hash, error) {
	obj := e.git.Storer.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	w, err := obj.Writer()
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("change.writeBlob: writer: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return plumbing.ZeroHash, fmt.Errorf("change.writeBlob: write: %w", err)
	}
	if err := w.Close(); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("change.writeBlob: close: %w", err)
	}
	h, err := e.git.Storer.SetEncodedObject(obj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("change.writeBlob: store: %w", err)
	}
	return h, nil
}

// WriteBlob stores data as a git blob object and returns its hash as a hex
// string. It is the exported wrapper over writeBlob used by callers outside the
// change package (e.g. worktree.CachedScan stores a blob on a cache miss).
func (e *Engine) WriteBlob(data []byte) (string, error) {
	h, err := e.writeBlob(data)
	if err != nil {
		return "", err
	}
	return h.String(), nil
}

// writeTreeRefs builds (nested) tree objects from a path->TreeEntry map (paths
// are "/"-separated) and returns the root tree hash. Unlike buildTree it does
// NOT write blobs: each entry already carries the hex SHA of a blob guaranteed
// to be in the object store, referenced directly via plumbing.NewHash. The
// immediate/subdir split, file-vs-dir collision guard, mode emission, sort, and
// tree encoding mirror buildTree exactly so the resulting tree hash is identical
// to the writeBlob+buildTree path for the same logical contents.
func (e *Engine) writeTreeRefs(entries map[string]TreeEntry) (plumbing.Hash, error) {
	// Split into immediate entries and grouped subdirectory contents, re-keying
	// subdir entries by the remaining path (same as buildTree does for data).
	subdirs := map[string]map[string]TreeEntry{}
	immediate := map[string]TreeEntry{}
	for path, entry := range entries {
		if i := strings.IndexByte(path, '/'); i >= 0 {
			dir := path[:i]
			rest := path[i+1:]
			if subdirs[dir] == nil {
				subdirs[dir] = map[string]TreeEntry{}
			}
			subdirs[dir][rest] = entry
		} else {
			immediate[path] = entry
		}
	}

	// A name cannot be both a file and a subdirectory at the same tree level;
	// that would emit two entries with the same name (an invalid git tree).
	for name := range immediate {
		if _, ok := subdirs[name]; ok {
			return plumbing.ZeroHash, fmt.Errorf("cannot commit: %q exists as both a file and a directory at the same level", name)
		}
	}

	var treeEntries []object.TreeEntry
	for name, entry := range immediate {
		m := filemode.Regular
		switch entry.Mode {
		case ModeExecutable:
			m = filemode.Executable
		case ModeSymlink:
			m = filemode.Symlink
		}
		treeEntries = append(treeEntries, object.TreeEntry{Name: name, Mode: m, Hash: plumbing.NewHash(entry.SHA)})
	}
	for dir, contents := range subdirs {
		h, err := e.writeTreeRefs(contents)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		treeEntries = append(treeEntries, object.TreeEntry{Name: dir, Mode: filemode.Dir, Hash: h})
	}

	// git requires tree entries in its canonical (directory-aware) order.
	sort.Slice(treeEntries, func(i, j int) bool { return treeEntryLess(treeEntries[i], treeEntries[j]) })

	tree := &object.Tree{Entries: treeEntries}
	obj := e.git.Storer.NewEncodedObject()
	if err := tree.Encode(obj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("change.writeTreeRefs: encode: %w", err)
	}
	h, err := e.git.Storer.SetEncodedObject(obj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("change.writeTreeRefs: store: %w", err)
	}
	return h, nil
}

// readBlob reads the contents of a git blob by hash.
func (e *Engine) readBlob(sha string) ([]byte, error) {
	b, err := e.git.BlobObject(plumbing.NewHash(sha))
	if err != nil {
		return nil, fmt.Errorf("change.readBlob: %w", err)
	}
	r, err := b.Reader()
	if err != nil {
		return nil, fmt.Errorf("change.readBlob: reader: %w", err)
	}
	defer func() { _ = r.Close() }()
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("change.readBlob: read: %w", err)
	}
	return data, nil
}

// writeTree builds blob and (nested) tree objects from a path->bytes map (paths
// are "/"-separated) and returns the root tree hash. The sparse modes map
// carries per-path kind/permission (absent ⇒ regular; nil ⇒ all regular).
func (e *Engine) writeTree(files map[string][]byte, modes map[string]EntryMode) (plumbing.Hash, error) {
	for path := range files {
		if path == "" {
			return plumbing.ZeroHash, fmt.Errorf("change.writeTree: empty path")
		}
		if strings.HasPrefix(path, "/") {
			return plumbing.ZeroHash, fmt.Errorf("change.writeTree: path %q begins with /", path)
		}
		if strings.Contains(path, "//") {
			return plumbing.ZeroHash, fmt.Errorf("change.writeTree: path %q contains empty segment", path)
		}
	}
	return e.buildTree(files, modes)
}

// buildTree recursively constructs a tree from a flat path->bytes map at the
// current level. Keys may contain "/" denoting subdirectories. The modes map
// is split in lockstep with files so each entry emits its real git mode
// (regular/executable/symlink). A nil modes map yields all-regular entries.
func (e *Engine) buildTree(files map[string][]byte, modes map[string]EntryMode) (plumbing.Hash, error) {
	// Split into immediate files and grouped subdirectory contents, splitting
	// modes in lockstep so each subtree carries its own paths' modes.
	subdirs := map[string]map[string][]byte{}
	subdirModes := map[string]map[string]EntryMode{}
	immediate := map[string][]byte{}
	immediateModes := map[string]EntryMode{}
	for path, data := range files {
		if i := strings.IndexByte(path, '/'); i >= 0 {
			dir := path[:i]
			rest := path[i+1:]
			if subdirs[dir] == nil {
				subdirs[dir] = map[string][]byte{}
				subdirModes[dir] = map[string]EntryMode{}
			}
			subdirs[dir][rest] = data
			if modes != nil {
				if mode, ok := modes[path]; ok {
					subdirModes[dir][rest] = mode
				}
			}
		} else {
			immediate[path] = data
			if modes != nil {
				if mode, ok := modes[path]; ok {
					immediateModes[path] = mode
				}
			}
		}
	}

	// A name cannot be both a file and a subdirectory at the same tree level;
	// that would emit two entries with the same name (an invalid git tree).
	for name := range immediate {
		if _, ok := subdirs[name]; ok {
			return plumbing.ZeroHash, fmt.Errorf("cannot commit: %q exists as both a file and a directory at the same level", name)
		}
	}

	var entries []object.TreeEntry
	for name, data := range immediate {
		h, err := e.writeBlob(data)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		// A reader of a nil sub-map returns the zero value (ModeRegular).
		m := filemode.Regular
		switch immediateModes[name] {
		case ModeExecutable:
			m = filemode.Executable
		case ModeSymlink:
			m = filemode.Symlink
		}
		entries = append(entries, object.TreeEntry{Name: name, Mode: m, Hash: h})
	}
	for dir, contents := range subdirs {
		h, err := e.buildTree(contents, subdirModes[dir])
		if err != nil {
			return plumbing.ZeroHash, err
		}
		entries = append(entries, object.TreeEntry{Name: dir, Mode: filemode.Dir, Hash: h})
	}

	// git requires tree entries in its canonical (directory-aware) order.
	sort.Slice(entries, func(i, j int) bool { return treeEntryLess(entries[i], entries[j]) })

	tree := &object.Tree{Entries: entries}
	obj := e.git.Storer.NewEncodedObject()
	if err := tree.Encode(obj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("change.writeTree: encode: %w", err)
	}
	h, err := e.git.Storer.SetEncodedObject(obj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("change.writeTree: store: %w", err)
	}
	return h, nil
}

// writeCommit stores a commit object snapshotting treeSha for changeID and
// returns its hex sha. Author and committer share a single timestamp (e.now())
// so identical inputs hash identically.
func (e *Engine) writeCommit(treeSha, changeID, message string, parents []string) (string, error) {
	when := e.now()
	name, email := e.idName, e.idEmail
	if name == "" {
		name = "cairn"
	}
	if email == "" {
		email = name + "@users.noreply.cairn"
	}
	sig := object.Signature{Name: name, Email: email, When: when}
	if message == "" {
		message = "snapshot"
	}
	fullMsg := message + "\n\nChange-Id: " + changeID + "\n"
	parentHashes := make([]plumbing.Hash, 0, len(parents))
	for _, p := range parents {
		parentHashes = append(parentHashes, plumbing.NewHash(p))
	}
	c := &object.Commit{
		Author:       sig,
		Committer:    sig,
		Message:      fullMsg,
		TreeHash:     plumbing.NewHash(treeSha),
		ParentHashes: parentHashes,
	}
	obj := e.git.Storer.NewEncodedObject()
	if err := c.Encode(obj); err != nil {
		return "", fmt.Errorf("change.writeCommit: encode: %w", err)
	}
	h, err := e.git.Storer.SetEncodedObject(obj)
	if err != nil {
		return "", fmt.Errorf("change.writeCommit: store: %w", err)
	}
	return h.String(), nil
}

// commitTree returns the hex tree hash of the given commit.
func (e *Engine) commitTree(commitSha string) (string, error) {
	c, err := e.git.CommitObject(plumbing.NewHash(commitSha))
	if err != nil {
		return "", fmt.Errorf("change.commitTree: %w", err)
	}
	return c.TreeHash.String(), nil
}

// mergeBase returns the hex sha of the best common ancestor of commits a and b,
// or "" if either input is empty or no common ancestor exists.
func (e *Engine) mergeBase(a, b string) (string, error) {
	if a == "" || b == "" {
		return "", nil
	}
	ca, err := e.git.CommitObject(plumbing.NewHash(a))
	if err != nil {
		return "", fmt.Errorf("change.mergeBase: commit %s: %w", a, err)
	}
	cb, err := e.git.CommitObject(plumbing.NewHash(b))
	if err != nil {
		return "", fmt.Errorf("change.mergeBase: commit %s: %w", b, err)
	}
	bases, err := ca.MergeBase(cb)
	if err != nil {
		return "", fmt.Errorf("change.mergeBase: %w", err)
	}
	if len(bases) == 0 {
		return "", nil
	}
	// Phase-1 lines are single-parent (linear ancestry), so MergeBase yields at
	// most one common ancestor; taking bases[0] is unambiguous here.
	return bases[0].Hash.String(), nil
}

// readTree reads a tree (recursively) into a flat path->bytes map keyed by the
// full "/"-separated path of each file.
func (e *Engine) readTree(treeHash string) (map[string][]byte, error) {
	tree, err := e.git.TreeObject(plumbing.NewHash(treeHash))
	if err != nil {
		return nil, fmt.Errorf("change.readTree: %w", err)
	}
	out := map[string][]byte{}
	err = tree.Files().ForEach(func(f *object.File) error {
		content, err := f.Contents()
		if err != nil {
			return fmt.Errorf("change.readTree: contents %q: %w", f.Name, err)
		}
		out[f.Name] = []byte(content)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
