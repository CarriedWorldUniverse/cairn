package worktree

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

// wcCacheEntry records, per worktree path, the stat fingerprint (mtimeNs, size)
// of the file the last time it was scanned plus the git blob SHA and mode that
// were stored for it. A subsequent scan whose stat matches (and is not racy —
// see CachedScan) can reuse BlobSHA without re-reading or re-encoding the file.
type wcCacheEntry struct {
	MtimeNs int64            `json:"m"`
	Size    int64            `json:"s"`
	BlobSHA string           `json:"b"`
	Mode    change.EntryMode `json:"k"`
}

// loadWCCache reads the snapshot cache JSON at path. A missing file yields an
// empty map (not an error): a first-ever scan has no cache.
func loadWCCache(path string) (map[string]wcCacheEntry, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]wcCacheEntry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("worktree.loadWCCache: %w", err)
	}
	out := map[string]wcCacheEntry{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("worktree.loadWCCache: %w", err)
	}
	return out, nil
}

// saveWCCache writes the snapshot cache JSON to path atomically (temp + rename).
func saveWCCache(path string, c map[string]wcCacheEntry) error {
	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("worktree.saveWCCache: %w", err)
	}
	if err := writeFileAtomic(path, data); err != nil {
		return fmt.Errorf("worktree.saveWCCache: %w", err)
	}
	return nil
}

// CachedScan walks dir (gitignore + tracked-set + symlink/exec, same as Scan)
// and returns a change.TreeEntry per surviving path. On a cache hit — the cached
// entry's (mtimeNs,size) match the on-disk file AND mtimeNs < scanStartNs (so a
// file written during the scan window is not trusted) AND the cached mode still
// matches the on-disk kind/exec-bit — it reuses the stored blob SHA WITHOUT
// reading the file. On a miss it reads (or Readlinks) the file, stores the blob
// via eng.WriteBlob (so the returned SHA always refers to an already-stored
// blob), and records a fresh cache entry. It returns the entries plus a freshly
// rebuilt cache containing only paths actually seen this scan (so vanished paths
// are dropped). scanStartNs is captured by the caller (time.Now().UnixNano())
// before the walk and passed in.
func CachedScan(eng *change.Engine, dir string, tracked map[string]struct{}, cache map[string]wcCacheEntry, scanStartNs int64) (map[string]change.TreeEntry, map[string]wcCacheEntry, error) {
	entries := map[string]change.TreeEntry{}
	newCache := map[string]wcCacheEntry{}

	err := walkWorktree(dir, tracked, func(slashRel, path string, d fs.DirEntry) error {
		info, err := d.Info()
		if err != nil {
			return err
		}
		mtimeNs := info.ModTime().UnixNano()
		size := info.Size()
		prev, hadPrev := cache[slashRel]
		// A cache entry is trustworthy only if its stat matches AND the file was
		// last modified strictly before the scan began. mtimeNs >= scanStartNs is
		// "racy": the file may have changed in the same nanosecond the scan saw,
		// so we cannot trust the cached SHA and must re-read.
		statMatch := hadPrev && prev.MtimeNs == mtimeNs && prev.Size == size && mtimeNs < scanStartNs

		if d.Type()&os.ModeSymlink != 0 {
			if statMatch && prev.Mode == change.ModeSymlink {
				entries[slashRel] = change.TreeEntry{SHA: prev.BlobSHA, Mode: change.ModeSymlink}
				newCache[slashRel] = prev
				return nil
			}
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			sha, err := eng.WriteBlob([]byte(target))
			if err != nil {
				return err
			}
			entries[slashRel] = change.TreeEntry{SHA: sha, Mode: change.ModeSymlink}
			newCache[slashRel] = wcCacheEntry{MtimeNs: mtimeNs, Size: size, BlobSHA: sha, Mode: change.ModeSymlink}
			return nil
		}

		// Regular file: determine the on-disk mode from the exec bit.
		mode := change.ModeRegular
		if info.Mode()&0o111 != 0 {
			mode = change.ModeExecutable
		}
		if statMatch && prev.Mode == mode {
			entries[slashRel] = change.TreeEntry{SHA: prev.BlobSHA, Mode: mode}
			newCache[slashRel] = prev
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sha, err := eng.WriteBlob(data)
		if err != nil {
			return err
		}
		entries[slashRel] = change.TreeEntry{SHA: sha, Mode: mode}
		newCache[slashRel] = wcCacheEntry{MtimeNs: mtimeNs, Size: size, BlobSHA: sha, Mode: mode}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("worktree.CachedScan: %w", err)
	}
	return entries, newCache, nil
}
