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
	modes, err := eng.FileModes(commitSha)
	if err != nil {
		return fmt.Errorf("worktree.Materialize: %w", err)
	}
	blobs := filepath.Join(cacheDir, "blobs")
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		return fmt.Errorf("worktree.Materialize: %w", err)
	}
	// Build into a unique sibling temp dir first, then swap — so a failure
	// mid-build leaves the existing working copy intact (the slow writes happen
	// before anything destructive). A sibling of dir is on the same filesystem →
	// os.Rename is atomic. MkdirTemp gives a fresh unique name so concurrent or
	// retried Materialize calls can't collide on a fixed path.
	tmp, err := os.MkdirTemp(filepath.Dir(dir), filepath.Base(dir)+".cairn-tmp-")
	if err != nil {
		return fmt.Errorf("worktree.Materialize: %w", err)
	}
	// MkdirTemp creates the dir 0o700; the working copy expects 0o755.
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.RemoveAll(tmp)
		return fmt.Errorf("worktree.Materialize: %w", err)
	}
	for p, data := range files {
		full := filepath.Join(tmp, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			os.RemoveAll(tmp)
			return fmt.Errorf("worktree.Materialize: %w", err)
		}

		// Symlink: write the target string as an actual symlink, bypassing the
		// blob cache/reflink path (a symlink's content is its target).
		if modes[p] == change.ModeSymlink {
			_ = os.Remove(full) // tmp is fresh, but be defensive
			if err := os.Symlink(string(data), full); err != nil {
				os.RemoveAll(tmp)
				return fmt.Errorf("worktree.Materialize: %w", err)
			}
			continue
		}

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
		if err := reflinkOrCopy(cacheBlob, full); err != nil {
			os.RemoveAll(tmp)
			return fmt.Errorf("worktree.Materialize: %w", err)
		}
		if modes[p] == change.ModeExecutable {
			if err := os.Chmod(full, 0o755); err != nil {
				os.RemoveAll(tmp)
				return fmt.Errorf("worktree.Materialize: %w", err)
			}
		}
	}
	// Swap: remove the old dir, then atomically rename temp into place.
	if err := os.RemoveAll(dir); err != nil {
		os.RemoveAll(tmp)
		return fmt.Errorf("worktree.Materialize: %w", err)
	}
	if err := os.Rename(tmp, dir); err != nil {
		os.RemoveAll(tmp)
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

// hasTrackedPrefix reports whether any tracked path lies under the directory
// dirRel (slash-separated, relative to the scan root) — i.e. some key has the
// prefix dirRel+"/". Used so an ignored directory is only skipped wholesale
// when nothing tracked lives inside it; otherwise we must descend to preserve
// the tracked files (git only ignores untracked paths).
func hasTrackedPrefix(tracked map[string]struct{}, dirRel string) bool {
	prefix := dirRel + "/"
	for k := range tracked {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

// Scan reads all regular files (and symlinks) under dir into a content map
// keyed by slash-separated path relative to dir, plus a sparse modes map
// carrying the non-regular kind/permission of each entry (absent ⇒ regular).
// It honors .gitignore and .cairnignore at the root of dir, and
// unconditionally skips any .git/ or .cairn/ directory subtree. Symlinks are
// stored as their target string and never followed.
//
// tracked is the set of paths already committed at the branch's tip. Following
// git semantics, ignore patterns only affect UNTRACKED paths: a path present
// in tracked is never dropped, even when it (or its directory) matches an
// ignore pattern. A nil tracked set means "nothing tracked" → every ignored
// path is skipped (the historical behaviour).
func Scan(dir string, tracked map[string]struct{}) (map[string][]byte, map[string]change.EntryMode, error) {
	// Load ignore patterns from root-level .gitignore and .cairnignore.
	var patterns []gitignore.Pattern
	for _, name := range []string{".gitignore", ".cairnignore"} {
		ps, err := loadIgnorePatterns(filepath.Join(dir, name))
		if err != nil {
			return nil, nil, fmt.Errorf("worktree.Scan: %w", err)
		}
		patterns = append(patterns, ps...)
	}
	m := gitignore.NewMatcher(patterns)

	out := map[string][]byte{}
	modes := map[string]change.EntryMode{}
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
			// Apply gitignore matcher for directories: if matched, skip the
			// subtree ONLY when no tracked path lies under it. If a tracked file
			// lives inside, descend so it survives (the per-file tracked check
			// keeps it; untracked siblings are still filtered).
			if m.Match(parts, true) {
				if !hasTrackedPrefix(tracked, slashRel) {
					return filepath.SkipDir
				}
			}
			return nil
		}

		// File entry: skip if matched by an ignore pattern, but only when the
		// path is UNTRACKED. An already-tracked file is never ignored (git
		// semantics) so a committed-then-ignored path is not silently dropped.
		if m.Match(parts, false) {
			if _, ok := tracked[slashRel]; !ok {
				return nil
			}
		}

		// Symlink: store its target string, mark ModeSymlink, never follow it.
		// (A symlink is not a dir, so WalkDir won't descend it — no loop risk.)
		if d.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			out[slashRel] = []byte(target)
			modes[slashRel] = change.ModeSymlink
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[slashRel] = data
		if info, ierr := d.Info(); ierr == nil && info.Mode()&0o111 != 0 {
			modes[slashRel] = change.ModeExecutable
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("worktree.Scan: %w", err)
	}
	return out, modes, nil
}
