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

// validTreeEntryName rejects a single tree-entry name that is unsafe to
// materialize into a worktree path (#126). A hostile remote can author a tree
// whose entry names are "..", "", ".", or contain a separator/NUL; cairn's read
// walk joins names into slash-paths that worktree.Materialize filepath.Joins
// against the branch folder, so an unchecked ".." escapes the folder and lets a
// pull/clone write arbitrary files. Validate per COMPONENT at every tree
// boundary (read AND write) so no consumer can be handed a traversal path.
// windowsReservedDeviceNames are the legacy DOS device names Windows reserves
// at the filesystem-driver level — opening "CON", or "CON.txt", or
// "CON.anything" addresses the console device, not a regular file, REGARDLESS
// of the directory it's "in". Matched case-insensitively against the name's
// base (the part before its FIRST '.', matching Windows' own matching rule)
// so both the bare name and any dotted/extended form are caught.
var windowsReservedDeviceNames = map[string]struct{}{
	"CON": {}, "PRN": {}, "AUX": {}, "NUL": {},
	"COM1": {}, "COM2": {}, "COM3": {}, "COM4": {}, "COM5": {}, "COM6": {}, "COM7": {}, "COM8": {}, "COM9": {},
	"LPT1": {}, "LPT2": {}, "LPT3": {}, "LPT4": {}, "LPT5": {}, "LPT6": {}, "LPT7": {}, "LPT8": {}, "LPT9": {},
}

func validTreeEntryName(name string) error {
	switch name {
	case "":
		return fmt.Errorf("empty tree entry name")
	case ".", "..":
		return fmt.Errorf("tree entry name %q is a path-traversal component", name)
	}
	if strings.ContainsAny(name, "/\\\x00") {
		return fmt.Errorf("tree entry name %q contains a separator or NUL", name)
	}
	// Windows strips TRAILING '.' and ' ' characters from each path component
	// at the filesystem-driver level before resolving it — a normalization
	// that applies regardless of which app opens the path. So a name that
	// only LOOKS benign here can reduce, on Windows, to "", ".", or ".." once
	// those trailing characters are dropped (e.g. ".. ", "...", "..  "),
	// making it a traversal component there even though it isn't one of the
	// literal names already rejected above. Re-run the same check against the
	// trailing-stripped form. This only strips a TRAILING run, so legitimate
	// names like "..a", "a.", ".hidden" (nothing trailing to strip, or the
	// stripped form still isn't "", ".", "..") are untouched.
	if stripped := strings.TrimRight(name, ". "); stripped != name {
		switch stripped {
		case "", ".", "..":
			return fmt.Errorf("tree entry name %q strips (on Windows) to a path-traversal component", name)
		}
	}
	// ':' introduces an NTFS Alternate Data Stream (e.g. "file.txt:hidden" is
	// a second, hidden data fork of "file.txt", not a separate file) — a
	// well-known Windows-specific smuggling vector cairn has no legitimate
	// use for. Reject it outright.
	if strings.Contains(name, ":") {
		return fmt.Errorf("tree entry name %q contains ':' (NTFS alternate data stream)", name)
	}
	// Windows reserved device name (see windowsReservedDeviceNames): match
	// the base up to the FIRST '.', case-insensitively, matching Windows' own
	// device-name resolution rule.
	base := name
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	if _, reserved := windowsReservedDeviceNames[strings.ToUpper(base)]; reserved {
		return fmt.Errorf("tree entry name %q is a reserved Windows device name", name)
	}
	return nil
}

// validTreePath validates a full "/"-separated tree path component-by-component
// via validTreeEntryName, also rejecting a leading "/" (absolute). It is the
// whole-path form of the guard, for boundaries that see joined paths (readTree's
// tree.Files() flattening, the writeTree/writeTreeRefs input maps).
func validTreePath(path string) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}
	if strings.HasPrefix(path, "/") {
		return fmt.Errorf("path %q begins with /", path)
	}
	for _, seg := range strings.Split(path, "/") {
		if err := validTreeEntryName(seg); err != nil {
			return fmt.Errorf("path %q: %w", path, err)
		}
	}
	return nil
}

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
	for path := range entries {
		if err := validTreePath(path); err != nil {
			return plumbing.ZeroHash, fmt.Errorf("change.writeTreeRefs: %w", err)
		}
	}
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

// readTreeRefs reads a tree (recursively) into a flat path->TreeEntry map keyed
// by the full "/"-separated path of each file. It is the mode-preserving inverse
// of writeTreeRefs: each entry carries the blob's hex SHA and its EntryMode
// (regular/executable/symlink), NOT its bytes. Use this (not readTree) whenever a
// tree must be rebuilt — readTree flattens via tree.Files() and re-reads blob
// contents, losing exec/symlink modes and re-materializing bytes. Round-tripping
// readTreeRefs -> writeTreeRefs reproduces the identical tree hash.
func (e *Engine) readTreeRefs(treeHash string) (map[string]TreeEntry, error) {
	out := map[string]TreeEntry{}
	if treeHash == "" {
		return out, nil
	}
	if err := e.collectTreeRefs(treeHash, "", out); err != nil {
		return nil, err
	}
	return out, nil
}

func (e *Engine) collectTreeRefs(treeHash, prefix string, out map[string]TreeEntry) error {
	tree, err := e.git.TreeObject(plumbing.NewHash(treeHash))
	if err != nil {
		return fmt.Errorf("change.readTreeRefs: tree %s: %w", treeHash, err)
	}
	// A name cannot legitimately appear twice at one tree level — as two
	// entries with the same name (regardless of kind), or as both a leaf and
	// a subtree. go-git's decoder does not enforce this on READ (only the
	// write side, buildTree/writeTreeRefs, rejects it when cairn itself
	// builds a tree), so a hostile remote can hand us an on-the-wire tree
	// object with e.g. two "evil" entries — one a symlink, one a directory —
	// and collectTreeRefs would silently yield both "evil" (as a symlink) and
	// "evil/pwned" (walking into the directory), a collision fix A's
	// materialize-side guard treats as defense in depth but that should never
	// reach a consumer in the first place (#126). Reject outright at the read
	// boundary, mirroring the write-side "file and directory at the same
	// level" guard.
	seen := make(map[string]struct{}, len(tree.Entries))
	for _, ent := range tree.Entries {
		if err := validTreeEntryName(ent.Name); err != nil {
			return fmt.Errorf("change.readTreeRefs: %w", err)
		}
		if _, dup := seen[ent.Name]; dup {
			return fmt.Errorf("change.readTreeRefs: tree %s has a duplicate entry name %q (invalid tree)", treeHash, ent.Name)
		}
		seen[ent.Name] = struct{}{}
		path := ent.Name
		if prefix != "" {
			path = prefix + "/" + ent.Name
		}
		if ent.Mode == filemode.Dir {
			if err := e.collectTreeRefs(ent.Hash.String(), path, out); err != nil {
				return err
			}
			continue
		}
		mode := ModeRegular
		switch ent.Mode {
		case filemode.Executable:
			mode = ModeExecutable
		case filemode.Symlink:
			mode = ModeSymlink
		}
		out[path] = TreeEntry{SHA: ent.Hash.String(), Mode: mode}
	}
	return nil
}

// ReadBlob reads the contents of a git blob by hex sha. It is the exported
// wrapper over readBlob used by callers outside the change package that need
// to lazily fetch one path's content after a meta-only comparison (e.g.
// worktree.Materialize, on a path FilesMeta says changed).
func (e *Engine) ReadBlob(sha string) ([]byte, error) {
	return e.readBlob(sha)
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
		if err := validTreePath(path); err != nil {
			return plumbing.ZeroHash, fmt.Errorf("change.writeTree: %w", err)
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
	// go-git's tree.Files() below is a flattening walk of its own that does
	// NOT share collectTreeRefs's per-level duplicate-name guard (#126, item
	// B): a hostile tree with two same-named entries at one level — one a
	// symlink, one a directory — is accepted by go-git's decoder, and
	// tree.Files() happily yields BOTH "evil" (the symlink's target bytes)
	// and "evil/pwned" (walking into the directory entry), the exact
	// collision materialize must never see. readTreeRefs shares the same
	// underlying tree object and performs no blob-content reads, so running
	// it here first (its result is discarded) validates the tree's structure
	// — traversal names AND the duplicate-name guard — at the cost of one
	// extra metadata-only walk, not a second blob-content pass.
	if _, err := e.readTreeRefs(treeHash); err != nil {
		return nil, fmt.Errorf("change.readTree: %w", err)
	}
	out := map[string][]byte{}
	err = tree.Files().ForEach(func(f *object.File) error {
		if err := validTreePath(f.Name); err != nil {
			return fmt.Errorf("change.readTree: %w", err)
		}
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
