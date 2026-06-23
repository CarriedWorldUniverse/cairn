package worktree

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

// Materialize writes the files of commitSha into dir, replacing any existing
// contents so stale files from a prior materialization are cleared.
func Materialize(eng *change.Engine, commitSha, dir string) error {
	files, err := eng.Files(commitSha)
	if err != nil {
		return fmt.Errorf("worktree.Materialize: %w", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("worktree.Materialize: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("worktree.Materialize: %w", err)
	}
	for p, data := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("worktree.Materialize: %w", err)
		}
		if err := os.WriteFile(full, data, 0o644); err != nil {
			return fmt.Errorf("worktree.Materialize: %w", err)
		}
	}
	return nil
}

// Scan reads all regular files under dir into a map keyed by slash-separated
// path relative to dir.
func Scan(dir string) (map[string][]byte, error) {
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("worktree.Scan: %w", err)
	}
	return out, nil
}
