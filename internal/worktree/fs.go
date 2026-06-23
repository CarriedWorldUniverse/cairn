package worktree

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

// Materialize writes the files of commitSha into dir, replacing any existing
// contents so stale files from a prior materialization are cleared. Each
// distinct file body is content-addressed (sha256) into a shared blob cache
// under cacheDir/blobs, and the working-copy file is produced by reflinking
// (copy-on-write) the cached blob, falling back to a plain copy on filesystems
// without reflink support.
func Materialize(eng *change.Engine, cacheDir, commitSha, dir string) error {
	files, err := eng.Files(commitSha)
	if err != nil {
		return fmt.Errorf("worktree.Materialize: %w", err)
	}
	blobs := filepath.Join(cacheDir, "blobs")
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		return fmt.Errorf("worktree.Materialize: %w", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("worktree.Materialize: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("worktree.Materialize: %w", err)
	}
	for p, data := range files {
		sum := sha256.Sum256(data)
		key := hex.EncodeToString(sum[:])
		cacheBlob := filepath.Join(blobs, key)
		if _, err := os.Stat(cacheBlob); errors.Is(err, os.ErrNotExist) {
			if err := writeFileAtomic(cacheBlob, data); err != nil {
				return fmt.Errorf("worktree.Materialize: %w", err)
			}
		} else if err != nil {
			return fmt.Errorf("worktree.Materialize: %w", err)
		}
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("worktree.Materialize: %w", err)
		}
		if err := reflinkOrCopy(cacheBlob, full); err != nil {
			return fmt.Errorf("worktree.Materialize: %w", err)
		}
	}
	return nil
}

// writeFileAtomic writes data to path via a temp file in the same directory
// followed by a rename, so a concurrent reader never observes a partially
// written blob.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".blob-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
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
