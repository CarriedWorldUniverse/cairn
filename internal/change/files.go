package change

import (
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Files returns the path->bytes file map of the tree at the given commit sha.
// This loads the FULL CONTENT of every blob in the tree — expensive at scale.
// Callers that only need to know WHICH paths exist / their SHA+mode (classify,
// compare, tracked-set membership) should use FilesMeta instead and load
// content lazily, only for paths that actually need it.
func (e *Engine) Files(commitSha string) (map[string][]byte, error) {
	tree, err := e.commitTree(commitSha)
	if err != nil {
		return nil, fmt.Errorf("change.Files: %w", err)
	}
	return e.readTree(tree)
}

// FilesMeta returns the path->TreeEntry (blob SHA + mode, NOT content) map of
// the tree at the given commit sha. It is the content-lazy counterpart to
// Files: a tree walk that never calls GetBlob, so it costs O(tree entries),
// not O(tree bytes).
func (e *Engine) FilesMeta(commitSha string) (map[string]TreeEntry, error) {
	tree, err := e.commitTree(commitSha)
	if err != nil {
		return nil, fmt.Errorf("change.FilesMeta: %w", err)
	}
	entries, err := e.readTreeRefs(tree)
	if err != nil {
		return nil, fmt.Errorf("change.FilesMeta: %w", err)
	}
	return entries, nil
}

// FileModes returns the non-regular modes (executable/symlink) per path for a
// commit's tree. Regular files are omitted (absent ⇒ regular).
func (e *Engine) FileModes(commitSha string) (map[string]EntryMode, error) {
	treeHash, err := e.commitTree(commitSha)
	if err != nil {
		return nil, fmt.Errorf("change.FileModes: %w", err)
	}
	modes, err := e.fileModesFromTree(treeHash)
	if err != nil {
		return nil, fmt.Errorf("change.FileModes: %w", err)
	}
	return modes, nil
}

// fileModesFromTree returns the non-regular modes (executable/symlink) per path
// for a TREE (not a commit). Regular files are omitted (absent ⇒ regular). It is
// the shared mode-reader behind both FileModes (commit→tree) and the merge path,
// which works in tree shas.
func (e *Engine) fileModesFromTree(treeHash string) (map[string]EntryMode, error) {
	tree, err := e.git.TreeObject(plumbing.NewHash(treeHash))
	if err != nil {
		return nil, fmt.Errorf("change.fileModesFromTree: %w", err)
	}
	out := map[string]EntryMode{}
	err = tree.Files().ForEach(func(f *object.File) error {
		switch f.Mode {
		case filemode.Executable:
			out[f.Name] = ModeExecutable
		case filemode.Symlink:
			out[f.Name] = ModeSymlink
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("change.fileModesFromTree: %w", err)
	}
	return out, nil
}
