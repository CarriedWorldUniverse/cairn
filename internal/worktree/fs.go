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
	"github.com/CarriedWorldUniverse/cairn/internal/winretry"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

// warnf reports a non-fatal scan condition (currently: an unreadable untracked
// path being skipped, #130) to the operator. It's a package-level var, not a
// direct fmt.Fprintf call, so tests can swap it to capture the message instead
// of writing to stderr.
var warnf = func(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "cairn: warning: "+format+"\n", args...)
}

// maxIndividualSkipWarnings caps the per-file warnf lines a single scan emits
// for unreadable-untracked skips (#130): a directory full of OS-locked junk
// (e.g. a whole ungitignored .vs/) must not flood the terminal one line per
// file. Every skipped path is still recorded in skipTracker.Paths — the
// structural list threaded up to CommitResult/StatusInfo — regardless of the
// warnf cap. This is intentionally a SEPARATE cap from cmd/cairn's
// printSkippedUnreadable showMax: this one bounds noisy per-scan stderr
// chatter during the walk itself, that one bounds the one-shot stdout summary
// printed afterward from the (already complete) structural list — no reason
// for the two to move together, so they aren't tied to one constant.
const maxIndividualSkipWarnings = 10

// skipTracker accumulates the unreadable-untracked paths skipped during ONE
// Scan/CachedScan walk (#130): every skip is recorded in Paths (a directory
// skip is recorded with a trailing "/"), while individual warnf lines are
// capped at maxIndividualSkipWarnings; finish emits one summary line for the
// remainder, if any. The zero value is ready to use. A nil *skipTracker is
// valid and simply does not track/cap anything (a caller — e.g. materialize's
// deletion-pass walk, which doesn't surface a skipped list — can pass a fresh
// throwaway instance, or the same shared instance across the one walk it
// cares about).
type skipTracker struct {
	Paths  []string
	warned int
}

// skip records one skipped path (already warnf-message-shaped, e.g. with a
// trailing "/" for a directory) and, unless the per-walk cap has been
// reached, emits a warnf line about it. Both the path AND the error text are
// sanitized before printing: a *fs.PathError's Error() string embeds the raw
// (absolute) path verbatim, so an ANSI-payload filename would otherwise still
// reach the terminal via the %v even with the path argument itself quoted.
func (s *skipTracker) skip(slashRel string, err error) {
	if s == nil {
		return
	}
	s.Paths = append(s.Paths, slashRel)
	if s.warned < maxIndividualSkipWarnings {
		warnf("skipping unreadable untracked path %s: %s", DisplayPath(slashRel), DisplayErrText(err))
		s.warned++
	}
}

// finish emits the "…and N more" summary line when skip was called more
// times than the warnf cap allowed. Call once, after the walk that used this
// tracker completes.
func (s *skipTracker) finish() {
	if s == nil {
		return
	}
	if extra := len(s.Paths) - s.warned; extra > 0 {
		warnf("… and %d more unreadable untracked path(s) skipped", extra)
	}
}

// DisplayPath renders p for a warning/log/stdout line: quoted (%q) whenever it
// contains a byte a terminal could misinterpret — an ASCII control character
// (< 0x20) or DEL (0x7f), e.g. an ESC-sequence-laden filename crafted to
// inject terminal escape codes into `cairn commit`/`cairn status` output —
// and left bare otherwise, so the overwhelmingly common case (an ordinary
// path) stays clean and copy-pasteable.
func DisplayPath(p string) string {
	if containsUnsafeByte(p) {
		return fmt.Sprintf("%q", p)
	}
	return p
}

// DisplayErrText renders err's message for a warning/log/stdout line, applying
// the identical quoting rule as DisplayPath: a plain %v is NOT safe for an
// error alongside an untrusted path, because e.g. *fs.PathError.Error()
// embeds the raw (absolute) path verbatim — an ANSI-escape-laden filename
// would inject into the terminal via the error text even when the path
// argument printed next to it is already quoted. err == nil renders as "".
func DisplayErrText(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if containsUnsafeByte(msg) {
		return fmt.Sprintf("%q", msg)
	}
	return msg
}

// containsUnsafeByte reports whether s contains a byte a terminal could
// misinterpret: an ASCII control character (< 0x20) or DEL (0x7f).
func containsUnsafeByte(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

// unreadableErr classifies a read/stat failure for slashRel hit mid-scan: a
// TRACKED path's unreadable content is always fatal (skipping it would make the
// rebuilt snapshot silently drop/delete a committed file — a wc-cache or
// worktree scan is not allowed to "un-commit" anything), so its error is
// returned unchanged. An UNTRACKED path's failure is instead recorded on skip
// (warned, capped, and added to its Paths — #130) and swallowed (nil), matching
// git's own tolerance of unreadable untracked files (e.g. an OS-locked file
// under an ungitignored Visual Studio .vs/ folder on Windows) — the caller is
// expected to otherwise skip/omit the path when this returns nil.
func unreadableErr(tracked map[string]struct{}, slashRel string, err error, skip *skipTracker) error {
	if _, ok := tracked[slashRel]; ok {
		return err
	}
	skip.skip(slashRel, err)
	return nil
}

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
	return materialize(eng, cacheDir, commitSha, dir, nil)
}

// MaterializeSynced is Materialize's fast-path variant for a folder that was
// JUST scanned (e.g. worktree.Repo.Commit's syncBranch immediately before
// Seal): hint is that scan's per-path stat fingerprint (mtimeNs, size, blob
// SHA, mode), typically the branch's on-disk wc-cache. For a path present in
// hint whose CURRENT on-disk stat (one Lstat) still matches the hint AND whose
// hint SHA/mode already equals the target tree's entry, Materialize trusts it
// outright — no content read, no hash — because the hint was built from a scan
// of this SAME directory moments ago, under the same wc.lock hold this call is
// also under. Every other path falls back to the normal lazy on-disk hash
// comparison (see regularUpToDateBySHA). A nil/empty hint behaves exactly like
// Materialize.
func MaterializeSynced(eng *change.Engine, cacheDir, commitSha, dir string, hint map[string]wcCacheEntry) error {
	return materialize(eng, cacheDir, commitSha, dir, hint)
}

// containedJoin joins a "/"-separated tree path onto dir and verifies the result
// stays within dir — the sink-side guard against path traversal (#126). The
// change layer's validTreeEntryName already rejects ".." entry names at the read
// boundary, so this is defense in depth: it turns any future gap that yields an
// escaping path into a refusal here rather than an out-of-folder write.
func containedJoin(dir, slashRel string) (string, error) {
	full := filepath.Join(dir, filepath.FromSlash(slashRel))
	rel, err := filepath.Rel(dir, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes the branch folder", slashRel)
	}
	return full, nil
}

// removeSymlinkComponents walks the directory components of full strictly
// between dir and filepath.Dir(full) inclusive, and removes any component that
// is CURRENTLY on disk a symlink (#126, cross-pull symlink-then-directory
// escape). containedJoin's guard is purely lexical (filepath.Rel against the
// intended path); it cannot see that a PRIOR materialize pass left an on-disk
// symlink at one of full's parent components (e.g. a tree that had "linkdir"
// as a symlink to "../../outside"). If a LATER tree replaces that same name
// with a directory (e.g. "linkdir/pwned"), os.MkdirAll's internal Stat FOLLOWS
// the symlink and happily "creates" (finds existing) the directory at whatever
// the symlink points to — so the subsequent file write goes through the
// symlink and lands outside dir entirely. Removing a symlink component here is
// safe: it is cairn-managed content inside the branch folder being superseded
// by a real directory in the target tree, and it's exactly what the deletion
// pass (walkWorktree, which uses Lstat/WalkDir semantics and never follows
// symlinks) would remove anyway once the tree no longer names that path as a
// symlink — this just does so before MkdirAll could write through it.
//
// This only runs for a path actually being WRITTEN (called from inside the
// write branch, after the up-to-date checks already returned false), so an
// unchanged file never pays this walk.
func removeSymlinkComponents(dir, full string) error {
	target := filepath.Dir(full)
	rel, err := filepath.Rel(dir, target)
	if err != nil || rel == "." {
		return nil
	}
	cur := dir
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil // nothing here yet; MkdirAll will create it cleanly
			}
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			if err := os.Remove(cur); err != nil {
				return err
			}
			continue
		}
		if !fi.IsDir() {
			// A regular file occupies this component's name; leave it — the
			// subsequent MkdirAll will surface the appropriate "not a
			// directory" error itself.
			return nil
		}
	}
	return nil
}

// materialize is the shared implementation behind Materialize/MaterializeSynced.
// It is meta-first: it reads the target tree as path->TreeEntry (SHA+mode,
// see change.Engine.FilesMeta) rather than full content, and loads a blob's
// bytes (change.Engine.ReadBlob) ONLY for a path that regularUpToDateBySHA /
// symlinkUpToDateBySHA determine actually needs writing — so an unchanged file
// (the common case on a steady-state Commit/Pull/Express) costs at most one
// Lstat (hint hit) or one local file read+hash (fallback), never a git-store
// blob fetch+decompress.
func materialize(eng *change.Engine, cacheDir, commitSha, dir string, hint map[string]wcCacheEntry) error {
	meta, err := eng.FilesMeta(commitSha)
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
	for p, entry := range meta {
		full, err := containedJoin(dir, p)
		if err != nil {
			// Defense in depth: change.FilesMeta already rejects traversal entry
			// names (#126), so an escape here means a new tree-read path bypassed
			// that guard — refuse to write rather than escape the branch folder.
			return fmt.Errorf("worktree.Materialize: %w", err)
		}
		if entry.Mode == change.ModeSymlink {
			if symlinkUpToDateBySHA(full, entry.SHA) {
				continue
			}
			data, err := eng.ReadBlob(entry.SHA)
			if err != nil {
				return fmt.Errorf("worktree.Materialize: %w", err)
			}
			// Actually writing this path: neutralize any on-disk symlink left
			// in an ancestor component before creating dirs (#126, see
			// removeSymlinkComponents doc).
			if err := removeSymlinkComponents(dir, full); err != nil {
				return fmt.Errorf("worktree.Materialize: %w", err)
			}
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return fmt.Errorf("worktree.Materialize: %w", err)
			}
			_ = os.Remove(full)
			if err := os.Symlink(string(data), full); err != nil {
				return fmt.Errorf("worktree.Materialize: %w", err)
			}
			continue
		}
		if regularUpToDateBySHA(full, entry, hint[p]) {
			continue
		}
		// Actually writing this path: neutralize any on-disk symlink left in
		// an ancestor component before creating dirs (#126, see
		// removeSymlinkComponents doc).
		if err := removeSymlinkComponents(dir, full); err != nil {
			return fmt.Errorf("worktree.Materialize: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("worktree.Materialize: %w", err)
		}
		data, err := eng.ReadBlob(entry.SHA)
		if err != nil {
			return fmt.Errorf("worktree.Materialize: %w", err)
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
		if err := placeFile(cacheBlob, full, entry.Mode == change.ModeExecutable); err != nil {
			return fmt.Errorf("worktree.Materialize: %w", err)
		}
	}

	// 2. Delete on-disk files no longer in the target. The walk uses the target as
	//    the tracked set, so .git/.cairn and IGNORED files (build output, .vs/) are
	//    skipped — left untouched, which is both correct and why the full-teardown
	//    unlinkat is gone.
	target := make(map[string]struct{}, len(meta))
	for p := range meta {
		target[p] = struct{}{}
	}
	var stale []string
	// This walk only decides what to DELETE; it doesn't surface a skipped list
	// to any caller (unlike Scan/CachedScan, #130), so the tracker here is a
	// throwaway — it still gets warnf's per-walk cap for free.
	delSkip := &skipTracker{}
	if werr := walkWorktree(dir, target, delSkip, func(slashRel, path string, d fs.DirEntry) error {
		if _, ok := target[slashRel]; ok {
			return nil
		}
		// An untracked path not in the target tree is normally stale (deleted
		// below). But an OS-locked untracked path (chmod 0000 on Linux, a
		// sharing violation on Windows, #130 — e.g. the very file/symlink a
		// same-directory Commit's snapshot scan just had to skip) must be LEFT
		// ALONE here too, exactly like Scan/CachedScan tolerate it: silently
		// erasing it the moment the folder gets re-materialized would be
		// STRICTLY WORSE than the original bug (a hard-abort at least never
		// lost data). A regular file is probed with a cheap open+close (never
		// a full read); a symlink is probed with Readlink — which reads the
		// LINK ITSELF (the small stored target string), never following it
		// into the target, so this is exactly as safe/cheap for a symlink as
		// the open+close probe is for a regular file (Windows reparse-point
		// locks make a symlink's own Readlink fail the same way an OS-locked
		// regular file's Open does).
		switch {
		case d.Type().IsRegular():
			if f, oerr := os.Open(path); oerr != nil {
				delSkip.skip(slashRel, oerr)
				return nil
			} else {
				_ = f.Close()
			}
		case d.Type()&os.ModeSymlink != 0:
			if _, oerr := os.Readlink(path); oerr != nil {
				delSkip.skip(slashRel, oerr)
				return nil
			}
		}
		stale = append(stale, path)
		return nil
	}); werr != nil {
		return fmt.Errorf("worktree.Materialize: %w", werr)
	}
	delSkip.finish()
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

// regularUpToDateBySHA reports whether the regular file at full already
// carries exactly entry's content and mode, so it can be left untouched
// (preserving mtime, which keeps the snapshot stat-cache warm) — without ever
// fetching/decompressing the git-store blob for an up-to-date file:
//   - hint fast path: when hint (this path's freshly-scanned fingerprint; the
//     zero value when absent) has a non-empty BlobSHA and its recorded
//     (mtimeNs, size, mode) still match a fresh Lstat of full, AND its SHA
//     already equals entry.SHA with the same mode, the file is trusted
//     outright — one Lstat, no content read at all.
//   - fallback: read the on-disk file (unavoidable — it's the very content
//     being verified) and hash it locally with the same blob-hash function git
//     uses (plumbing.ComputeHash), comparing against entry.SHA. This still
//     costs a local disk read, but never touches the git object store for a
//     path that turns out unchanged.
func regularUpToDateBySHA(full string, entry change.TreeEntry, hint wcCacheEntry) bool {
	fi, err := os.Lstat(full)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	wantExec := entry.Mode == change.ModeExecutable
	if (fi.Mode()&0o111 != 0) != wantExec {
		return false
	}
	if hint.BlobSHA != "" && hint.Mode == entry.Mode && hint.BlobSHA == entry.SHA &&
		hint.MtimeNs == fi.ModTime().UnixNano() && hint.Size == fi.Size() {
		return true
	}
	cur, err := os.ReadFile(full)
	if err != nil {
		return false
	}
	return plumbing.ComputeHash(plumbing.BlobObject, cur).String() == entry.SHA
}

// symlinkUpToDateBySHA reports whether full is already a symlink whose target
// string hashes (as a git blob) to sha — so it can be left untouched without
// ever fetching the git-store blob for an up-to-date symlink. Reading the
// on-disk target via Readlink is a cheap syscall (unlike a regular file, there
// is no separate "content" to avoid loading).
func symlinkUpToDateBySHA(full, sha string) bool {
	fi, err := os.Lstat(full)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return false
	}
	target, err := os.Readlink(full)
	if err != nil {
		return false
	}
	return plumbing.ComputeHash(plumbing.BlobObject, []byte(target)).String() == sha
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
// path is skipped (the historical behaviour). An UNTRACKED path (or directory)
// that turns out unreadable is warned about (capped, see skipTracker) and
// silently omitted rather than aborting the whole scan (#130) — its
// slash-separated path (directories with a trailing "/") is returned in the
// third result so a caller can surface it structurally (not stderr-only,
// e.g. `cairn commit`/`cairn status`); a TRACKED path's unreadable content
// remains a hard error.
func Scan(dir string, tracked map[string]struct{}) (map[string][]byte, map[string]change.EntryMode, []string, error) {
	out := map[string][]byte{}
	modes := map[string]change.EntryMode{}
	skip := &skipTracker{}
	err := walkWorktree(dir, tracked, skip, func(slashRel, path string, d fs.DirEntry) error {
		// Symlink: store its target string, mark ModeSymlink, never follow it.
		// (A symlink is not a dir, so WalkDir won't descend it — no loop risk.)
		if d.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return unreadableErr(tracked, slashRel, err, skip)
			}
			out[slashRel] = []byte(target)
			modes[slashRel] = change.ModeSymlink
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return unreadableErr(tracked, slashRel, err, skip)
		}
		out[slashRel] = data
		if info, ierr := d.Info(); ierr == nil && info.Mode()&0o111 != 0 {
			modes[slashRel] = change.ModeExecutable
		}
		return nil
	})
	if err != nil {
		// Aborting on a hard error (a TRACKED path was unreadable, or the root
		// itself was): the whole scan result is discarded, so don't emit a
		// "...and N more" summary for a Paths list nobody will see.
		return nil, nil, nil, fmt.Errorf("worktree.Scan: %w", err)
	}
	skip.finish()
	return out, modes, skip.Paths, nil
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
//
// An unreadable DIRECTORY (a ReadDir/stat failure reported mid-walk, #130) is
// recorded on skip (warned, capped, added to skip.Paths with a trailing "/")
// and skipped via filepath.SkipDir when no tracked path lies under it
// (hasTrackedPrefix); if a tracked path DOES live under it, that's a hard
// error (descending it is exactly how a tracked file would be found and
// preserved, so silently skipping could lose it). The scan ROOT itself failing
// is always a hard error regardless of tracked. skip is shared with fn's own
// unreadableErr calls (both file- and directory-level skips share one
// per-walk warnf cap); a nil skip is valid and just skips silently sans
// tracking/cap (see skipTracker).
func walkWorktree(dir string, tracked map[string]struct{}, skip *skipTracker, fn func(slashRel, path string, d fs.DirEntry) error) error {
	rootPatterns, err := loadDirPatterns(dir, nil)
	if err != nil {
		return err
	}
	// stack[len-1] is the frame for the directory currently being descended; the
	// root frame (depth 0) is never popped.
	stack := []ignoreFrame{{depth: 0, patterns: rootPatterns, matcher: gitignore.NewMatcher(rootPatterns)}}

	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			if err != nil {
				return err
			}
			return relErr
		}
		if rel == "." {
			// The root dir; its frame is already on the stack. A non-nil err here
			// means the scan root itself is unreadable/missing — always fatal,
			// regardless of tracked (there is nowhere left to fall back to).
			return err
		}
		slashRel := filepath.ToSlash(rel)

		if err != nil {
			// WalkDir reports a ReadDir/stat failure by invoking fn with the
			// failing directory's own path/DirEntry and this non-nil err (see
			// filepath.WalkDir's docs). d may be nil in some failure modes, so
			// don't rely on it here.
			if hasTrackedPrefix(tracked, slashRel) {
				return fmt.Errorf("unreadable directory %s: %w", slashRel, err)
			}
			skip.skip(slashRel+"/", err)
			return filepath.SkipDir
		}

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
			// A permission failure here (e.g. a chmod-0000 directory: opening its
			// .gitignore fails before WalkDir even attempts its own ReadDir) gets
			// the same unreadable-directory treatment as a ReadDir error reported
			// directly by WalkDir (#130).
			dirPatterns, derr := loadDirPatterns(path, parts)
			if derr != nil {
				if hasTrackedPrefix(tracked, slashRel) {
					return fmt.Errorf("unreadable directory %s: %w", slashRel, derr)
				}
				skip.skip(slashRel+"/", derr)
				return filepath.SkipDir
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
