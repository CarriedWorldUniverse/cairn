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
// are "/"-separated) and returns the root tree hash.
func (e *Engine) writeTree(files map[string][]byte) (plumbing.Hash, error) {
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
	return e.buildTree(files)
}

// buildTree recursively constructs a tree from a flat path->bytes map at the
// current level. Keys may contain "/" denoting subdirectories.
func (e *Engine) buildTree(files map[string][]byte) (plumbing.Hash, error) {
	// Split into immediate files and grouped subdirectory contents.
	subdirs := map[string]map[string][]byte{}
	immediate := map[string][]byte{}
	for path, data := range files {
		if i := strings.IndexByte(path, '/'); i >= 0 {
			dir := path[:i]
			rest := path[i+1:]
			if subdirs[dir] == nil {
				subdirs[dir] = map[string][]byte{}
			}
			subdirs[dir][rest] = data
		} else {
			immediate[path] = data
		}
	}

	// A name cannot be both a file and a subdirectory at the same tree level;
	// that would emit two entries with the same name (an invalid git tree).
	for name := range immediate {
		if _, ok := subdirs[name]; ok {
			return plumbing.ZeroHash, fmt.Errorf("change.writeTree: name %q used as both file and directory", name)
		}
	}

	var entries []object.TreeEntry
	for name, data := range immediate {
		h, err := e.writeBlob(data)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		entries = append(entries, object.TreeEntry{Name: name, Mode: filemode.Regular, Hash: h})
	}
	for dir, contents := range subdirs {
		h, err := e.buildTree(contents)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		entries = append(entries, object.TreeEntry{Name: dir, Mode: filemode.Dir, Hash: h})
	}

	// git requires tree entries sorted by name.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })

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
