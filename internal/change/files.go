package change

import (
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Files returns the path->bytes file map of the tree at the given commit sha.
func (e *Engine) Files(commitSha string) (map[string][]byte, error) {
	tree, err := e.commitTree(commitSha)
	if err != nil {
		return nil, fmt.Errorf("change.Files: %w", err)
	}
	return e.readTree(tree)
}

// FileModes returns the non-regular modes (executable/symlink) per path for a
// commit's tree. Regular files are omitted (absent ⇒ regular).
func (e *Engine) FileModes(commitSha string) (map[string]EntryMode, error) {
	treeHash, err := e.commitTree(commitSha)
	if err != nil {
		return nil, fmt.Errorf("change.FileModes: %w", err)
	}
	tree, err := e.git.TreeObject(plumbing.NewHash(treeHash))
	if err != nil {
		return nil, fmt.Errorf("change.FileModes: %w", err)
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
		return nil, fmt.Errorf("change.FileModes: %w", err)
	}
	return out, nil
}
