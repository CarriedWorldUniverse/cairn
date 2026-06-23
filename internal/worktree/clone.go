package worktree

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

// Clone creates a fresh working copy at dir from a remote git URL: it imports
// the remote's refs into a new change engine (mapping the remote default branch
// onto the root line and every other head onto a flat child line) and then
// expresses the default branch as a folder on disk. The returned Repo is ready
// to express the other imported lines, commit, fold, etc.
func Clone(url, dir, author string) (*Repo, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("worktree.Clone: %w", err)
	}
	cairnDir := filepath.Join(dir, ".cairn")
	eng, err := change.Open(cairnDir)
	if err != nil {
		return nil, fmt.Errorf("worktree.Clone: %w", err)
	}
	def, err := eng.ImportFromRemote(url)
	if err != nil {
		_ = eng.Close()
		return nil, fmt.Errorf("worktree.Clone: %w", err)
	}
	stPath := filepath.Join(cairnDir, "wc.json")
	st, err := LoadState(stPath)
	if err != nil {
		_ = eng.Close()
		return nil, fmt.Errorf("worktree.Clone: %w", err)
	}
	author = resolveIdentity(eng, author)
	r := &Repo{root: dir, author: author, eng: eng, st: st, stPath: stPath}
	if err := r.Express(def, ""); err != nil {
		_ = eng.Close()
		return nil, fmt.Errorf("worktree.Clone: %w", err)
	}
	return r, nil
}
