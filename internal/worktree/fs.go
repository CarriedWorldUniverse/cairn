package worktree

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
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
	// Build into a sibling temp dir first, then swap — so a failure mid-build
	// leaves the existing working copy intact (the slow writes happen before
	// anything destructive). Sibling of dir means same filesystem → os.Rename
	// is atomic.
	tmp := dir + ".cairn-tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return fmt.Errorf("worktree.Materialize: %w", err)
	}
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return fmt.Errorf("worktree.Materialize: %w", err)
	}
	for p, data := range files {
		sum := sha256.Sum256(data)
		key := hex.EncodeToString(sum[:])
		cacheBlob := filepath.Join(blobs, key)
		if _, err := os.Stat(cacheBlob); errors.Is(err, os.ErrNotExist) {
			if err := writeFileAtomic(cacheBlob, data); err != nil {
				os.RemoveAll(tmp)
				return fmt.Errorf("worktree.Materialize: %w", err)
			}
		} else if err != nil {
			os.RemoveAll(tmp)
			return fmt.Errorf("worktree.Materialize: %w", err)
		}
		full := filepath.Join(tmp, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			os.RemoveAll(tmp)
			return fmt.Errorf("worktree.Materialize: %w", err)
		}
		if err := reflinkOrCopy(cacheBlob, full); err != nil {
			os.RemoveAll(tmp)
			return fmt.Errorf("worktree.Materialize: %w", err)
		}
	}
	// Swap: remove the old dir, then atomically rename temp into place.
	if err := os.RemoveAll(dir); err != nil {
		os.RemoveAll(tmp)
		return fmt.Errorf("worktree.Materialize: %w", err)
	}
	if err := os.Rename(tmp, dir); err != nil {
		return fmt.Errorf("worktree.Materialize: %w", err)
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

// loadIgnorePatterns reads patterns from a gitignore-style file (one per line,
// blank lines and lines starting with '#' skipped). Missing files are silently
// ignored. Returns nil patterns if the file does not exist.
func loadIgnorePatterns(path string) ([]gitignore.Pattern, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var patterns []gitignore.Pattern
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, gitignore.ParsePattern(line, nil))
	}
	return patterns, sc.Err()
}

// Scan reads all regular files under dir into a map keyed by slash-separated
// path relative to dir. It honors .gitignore and .cairnignore at the root of
// dir, and unconditionally skips any .git/ or .cairn/ directory subtree.
func Scan(dir string) (map[string][]byte, error) {
	// Load ignore patterns from root-level .gitignore and .cairnignore.
	var patterns []gitignore.Pattern
	for _, name := range []string{".gitignore", ".cairnignore"} {
		ps, err := loadIgnorePatterns(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("worktree.Scan: %w", err)
		}
		patterns = append(patterns, ps...)
	}
	m := gitignore.NewMatcher(patterns)

	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}

		// The root dir itself: rel == "."; nothing to match.
		if rel == "." {
			return nil
		}

		slashRel := filepath.ToSlash(rel)
		parts := strings.Split(slashRel, "/")

		// Unconditionally skip .git and .cairn directories (and any path
		// component that is .git or .cairn, for nested cases like sub/.git/).
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == ".cairn" {
				return filepath.SkipDir
			}
			// Also guard against nested components (e.g. "sub/.git").
			for _, part := range parts {
				if part == ".git" || part == ".cairn" {
					return filepath.SkipDir
				}
			}
			// Apply gitignore matcher for directories: if matched, skip subtree.
			if m.Match(parts, true) {
				return filepath.SkipDir
			}
			return nil
		}

		// File entry: skip if matched by an ignore pattern.
		if m.Match(parts, false) {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[slashRel] = data
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("worktree.Scan: %w", err)
	}
	return out, nil
}
