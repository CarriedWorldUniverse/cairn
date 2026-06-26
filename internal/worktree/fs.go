package worktree

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
	"github.com/CarriedWorldUniverse/cairn/internal/winretry"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

// Materialize writes the files of commitSha into dir IN PLACE, touching only what
// changed: it writes/updates files whose content or mode differs, deletes tracked
// files no longer present, and leaves everything else alone. It deliberately does
// NOT tear the folder down and rebuild it. Two reasons, both load-bearing on a
// real tree on Windows:
//
//  1. Removing the whole folder fails when anything holds a handle on it — a
//     shell sitting in the folder, or an editor with a file open (the unlinkat
//     "being used by another process" seen on commit at scale).
//  2. A full rebuild rewrites every file, changing every mtime, which defeats the
//     snapshot stat-cache → every later command re-hashes the whole tree.
//
// Ignored files (build output, .vs/ that an editor or Visual Studio holds open)
// and .git/.cairn are never touched — the deletion pass uses the same walk as the
// snapshot, which skips them. Changed files are written through the shared blob
// cache (content-addressed, reflinked copy-on-write) into a temp sibling and
// renamed over, so a reader never sees a half-written file.
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
	// Ensure the branch folder exists; never remove it (see the doc comment).
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("worktree.Materialize: %w", err)
	}

	// 1. Write/update each target file only when it differs from what's on disk.
	for p, data := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("worktree.Materialize: %w", err)
		}
		if modes[p] == change.ModeSymlink {
			if symlinkUpToDate(full, string(data)) {
				continue
			}
			_ = os.Remove(full)
			if err := os.Symlink(string(data), full); err != nil {
				return fmt.Errorf("worktree.Materialize: %w", err)
			}
			continue
		}
		if regularUpToDate(full, data, modes[p] == change.ModeExecutable) {
			continue
		}
		sum := sha256.Sum256(data)
		cacheBlob := filepath.Join(blobs, hex.EncodeToString(sum[:]))
		if _, serr := os.Stat(cacheBlob); errors.Is(serr, os.ErrNotExist) {
			if err := writeFileAtomic(cacheBlob, data); err != nil {
				return fmt.Errorf("worktree.Materialize: %w", err)
			}
		} else if serr != nil {
			return fmt.Errorf("worktree.Materialize: %w", serr)
		}
		if err := placeFile(cacheBlob, full, modes[p] == change.ModeExecutable); err != nil {
			return fmt.Errorf("worktree.Materialize: %w", err)
		}
	}

	// 2. Delete on-disk files no longer in the target. The walk uses the target as
	//    the tracked set, so .git/.cairn and IGNORED files (build output, .vs/) are
	//    skipped — left untouched, which is both correct and why the full-teardown
	//    unlinkat is gone.
	target := make(map[string]struct{}, len(files))
	for p := range files {
		target[p] = struct{}{}
	}
	var stale []string
	if werr := walkWorktree(dir, target, func(slashRel, path string, _ fs.DirEntry) error {
		if _, ok := target[slashRel]; !ok {
			stale = append(stale, path)
		}
		return nil
	}); werr != nil {
		return fmt.Errorf("worktree.Materialize: %w", werr)
	}
	for _, path := range stale {
		if err := winretry.Do(func() error { return os.Remove(path) }); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("worktree.Materialize: %w", err)
		}
	}
	// 3. Prune directories the deletion emptied (best-effort; a still-held or
	//    non-empty dir is simply left).
	pruneEmptyDirs(dir, target)
	return nil
}

// regularUpToDate reports whether the regular file at full already has exactly
// data as its content and the right executable bit — so it can be left untouched
// (preserving its mtime, which keeps the snapshot stat-cache warm).
func regularUpToDate(full string, data []byte, wantExec bool) bool {
	fi, err := os.Lstat(full)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	if fi.Size() != int64(len(data)) {
		return false
	}
	if (fi.Mode()&0o111 != 0) != wantExec {
		return false
	}
	cur, err := os.ReadFile(full)
	return err == nil && bytes.Equal(cur, data)
}

// symlinkUpToDate reports whether full is already a symlink pointing at target.
func symlinkUpToDate(full, target string) bool {
	fi, err := os.Lstat(full)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return false
	}
	got, err := os.Readlink(full)
	return err == nil && got == target
}

// placeFile installs the cached blob at full (atomic replace via a temp sibling +
// rename, so a concurrent reader never sees a half-written file and an open
// destination doesn't block an in-place truncate on Windows), with the exec bit.
func placeFile(cacheBlob, full string, exec bool) error {
	tmp := full + ".cairn-wtmp"
	_ = os.Remove(tmp)
	if err := reflinkOrCopy(cacheBlob, tmp); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if exec {
		mode = 0o755
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return renameWithRetry(tmp, full)
}

// pruneEmptyDirs removes directories the deletion pass emptied, deepest-first.
// It never removes dir itself, and skips .git/.cairn and any directory that still
// contains a target path (so it won't disturb ignored subtrees like .vs/, which
// hold no target path but are also never walked into). Best-effort: a removal
// that fails (non-empty, or held open) is silently left.
func pruneEmptyDirs(dir string, target map[string]struct{}) {
	var dirs []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if path != dir && (name == ".git" || name == ".cairn") {
				return filepath.SkipDir
			}
			if path != dir {
				dirs = append(dirs, path)
			}
		}
		return nil
	})
	// Deepest first.
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i]) // only succeeds when empty
	}
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
	if err := renameWithRetry(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// loadIgnoreFile reads patterns from a gitignore-style file (one per line, blank
// lines and '#' comments skipped). Each pattern is scoped to domain (the
// directory's slash-split path, nil for the root), so a pattern in a nested
// .gitignore only affects that directory's subtree. Missing files yield nil.
func loadIgnoreFile(path string, domain []string) ([]gitignore.Pattern, error) {
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
		// ParsePattern copies domain, so passing a reused slice is safe.
		patterns = append(patterns, gitignore.ParsePattern(line, domain))
	}
	return patterns, sc.Err()
}

// loadDirPatterns loads one directory's ignore patterns: .gitignore then
// .cairnignore (cairn rules appended after, so they win ties within a directory
// via gitignore's last-match-wins), both scoped to domain.
func loadDirPatterns(absDir string, domain []string) ([]gitignore.Pattern, error) {
	var ps []gitignore.Pattern
	// TODO(scope-b): at the root (domain==nil) prepend .cairn/info/exclude here
	// (lowest precedence) if repo-local uncommitted excludes are ever adopted.
	for _, name := range []string{".gitignore", ".cairnignore"} {
		p, err := loadIgnoreFile(filepath.Join(absDir, name), domain)
		if err != nil {
			return nil, err
		}
		ps = append(ps, p...)
	}
	return ps, nil
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
// It honors .gitignore and .cairnignore in every directory (each scoped to its
// subtree, with git precedence + negation), and unconditionally skips any .git/
// or .cairn/ directory subtree. Symlinks are stored as their target string and
// never followed.
//
// tracked is the set of paths already committed at the branch's tip. Following
// git semantics, ignore patterns only affect UNTRACKED paths: a path present
// in tracked is never dropped, even when it (or its directory) matches an
// ignore pattern. A nil tracked set means "nothing tracked" → every ignored
// path is skipped (the historical behaviour).
func Scan(dir string, tracked map[string]struct{}) (map[string][]byte, map[string]change.EntryMode, error) {
	out := map[string][]byte{}
	modes := map[string]change.EntryMode{}
	err := walkWorktree(dir, tracked, func(slashRel, path string, d fs.DirEntry) error {
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

// ignoreFrame is one directory's cumulative ignore matcher during the walk: the
// patterns from this directory's .gitignore/.cairnignore appended onto every
// ancestor's, in shallow→deep order. gitignore matches last-match-wins, so a
// deeper directory (and a later line) overrides a shallower one and negation
// re-includes work. depth is the directory's path-component count (root = 0) so
// the stack unwinds correctly as the pre-order walk returns up the tree.
type ignoreFrame struct {
	depth    int
	patterns []gitignore.Pattern
	matcher  gitignore.Matcher
}

// walkWorktree performs the shared worktree walk: it honours .gitignore AND
// .cairnignore in EVERY directory (each scoped to its own subtree, with git's
// precedence + negation), loading them lazily during this single pre-order pass
// (no second tree traversal). It unconditionally skips .git/.cairn subtrees and
// honours the tracked set per git semantics (tracked paths, and ignored dirs
// containing tracked paths, survive). fn is invoked once per surviving file/
// symlink with its slash-separated relative path, absolute path, and dir-entry.
//
// Caveat (matches git): a negation cannot re-include a file under a directory
// ignored as a directory, because that directory is not descended — except when
// it holds a tracked path, which cairn descends to preserve.
func walkWorktree(dir string, tracked map[string]struct{}, fn func(slashRel, path string, d fs.DirEntry) error) error {
	rootPatterns, err := loadDirPatterns(dir, nil)
	if err != nil {
		return err
	}
	// stack[len-1] is the frame for the directory currently being descended; the
	// root frame (depth 0) is never popped.
	stack := []ignoreFrame{{depth: 0, patterns: rootPatterns, matcher: gitignore.NewMatcher(rootPatterns)}}

	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil // the root dir; its frame is already on the stack
		}
		slashRel := filepath.ToSlash(rel)
		parts := strings.Split(slashRel, "/")

		// Unwind frames for subtrees we have left: the applicable frame is the one
		// for this entry's PARENT directory (depth == len(parts)-1).
		parentDepth := len(parts) - 1
		for len(stack) > 1 && stack[len(stack)-1].depth > parentDepth {
			stack = stack[:len(stack)-1]
		}
		top := stack[len(stack)-1]

		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == ".cairn" {
				return filepath.SkipDir
			}
			for _, part := range parts {
				if part == ".git" || part == ".cairn" {
					return filepath.SkipDir
				}
			}
			// Skip decision for this dir uses the PARENT matcher (a directory cannot
			// ignore itself via its own .gitignore). Only skip when nothing tracked
			// lives inside; otherwise descend so tracked files survive.
			if top.matcher.Match(parts, true) && !hasTrackedPrefix(tracked, slashRel) {
				return filepath.SkipDir
			}
			// Descend: push this dir's frame (parent patterns ++ this dir's own).
			dirPatterns, derr := loadDirPatterns(path, parts)
			if derr != nil {
				return derr
			}
			if len(dirPatterns) == 0 {
				// No ignore files here — reuse the parent's matcher (hot path; most
				// directories have none), only tracking depth.
				stack = append(stack, ignoreFrame{depth: len(parts), patterns: top.patterns, matcher: top.matcher})
			} else {
				merged := make([]gitignore.Pattern, 0, len(top.patterns)+len(dirPatterns))
				merged = append(merged, top.patterns...)
				merged = append(merged, dirPatterns...)
				stack = append(stack, ignoreFrame{depth: len(parts), patterns: merged, matcher: gitignore.NewMatcher(merged)})
			}
			return nil
		}

		// File/symlink: skip if matched, but never drop a TRACKED path (git
		// semantics — a committed-then-ignored path is not silently lost).
		if top.matcher.Match(parts, false) {
			if _, ok := tracked[slashRel]; !ok {
				return nil
			}
		}
		return fn(slashRel, path, d)
	})
}
