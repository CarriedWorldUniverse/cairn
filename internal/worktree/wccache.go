package worktree

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"sync"

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
// blob), and records a fresh cache entry. It returns the entries, a freshly
// rebuilt cache containing only paths actually seen this scan (so vanished paths
// are dropped), and a bool cacheChanged that is true whenever the new cache
// differs from the input (a miss was taken, or a path vanished). Callers can
// skip saveWCCache when cacheChanged is false. scanStartNs is captured by the
// caller (time.Now().UnixNano()) before the walk and passed in.
func CachedScan(eng *change.Engine, dir string, tracked map[string]struct{}, cache map[string]wcCacheEntry, scanStartNs int64) (map[string]change.TreeEntry, map[string]wcCacheEntry, bool, error) {
	// scanItem is one surviving worktree entry plus its cheap stat fingerprint,
	// collected by the (serial) directory walk. The slow per-file step — reading
	// a cache-missed file's content — is then done in PARALLEL, because on Windows
	// each file open is intercepted by the antivirus scanner and is latency- not
	// CPU-bound, so concurrent reads hide that latency. The go-git blob writes
	// stay serial below (the object store is not concurrency-safe).
	type scanItem struct {
		slashRel string
		path     string
		mtimeNs  int64
		size     int64
		symlink  bool
		mode     change.EntryMode
	}
	var items []scanItem
	if err := walkWorktree(dir, tracked, func(slashRel, path string, d fs.DirEntry) error {
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := change.ModeRegular
		if info.Mode()&0o111 != 0 {
			mode = change.ModeExecutable
		}
		items = append(items, scanItem{
			slashRel: slashRel, path: path,
			mtimeNs: info.ModTime().UnixNano(), size: info.Size(),
			symlink: d.Type()&os.ModeSymlink != 0, mode: mode,
		})
		return nil
	}); err != nil {
		return nil, nil, false, fmt.Errorf("worktree.CachedScan: %w", err)
	}

	// Per-item resolution. A cache HIT reuses the stored SHA (no read). A MISS
	// records the freshly-read content (data) for the serial blob-write pass. The
	// `cache` map is only read here, so concurrent access is safe.
	type scanResult struct {
		sha  string // set on a hit
		data []byte // set on a miss (content to write); nil on a hit
		miss bool
		err  error
	}
	results := make([]scanResult, len(items))
	resolve := func(i int) {
		it := items[i]
		prev, hadPrev := cache[it.slashRel]
		mode := it.mode
		if it.symlink {
			mode = change.ModeSymlink
		}
		// A cache entry is trustworthy only if its stat matches AND the file was
		// last modified strictly before the scan began (mtime >= scanStartNs is
		// "racy" — it may have changed in the instant the scan saw it).
		if hadPrev && prev.MtimeNs == it.mtimeNs && prev.Size == it.size &&
			it.mtimeNs < scanStartNs && prev.Mode == mode {
			results[i] = scanResult{sha: prev.BlobSHA}
			return
		}
		if it.symlink {
			target, err := os.Readlink(it.path)
			results[i] = scanResult{data: []byte(target), miss: true, err: err}
			return
		}
		data, err := os.ReadFile(it.path)
		results[i] = scanResult{data: data, miss: true, err: err}
	}
	parallelFor(len(items), resolve)

	// Serial reduce: write blobs for misses (object store is not concurrency-safe)
	// and assemble the entries + rebuilt cache.
	entries := map[string]change.TreeEntry{}
	newCache := map[string]wcCacheEntry{}
	changed := false
	for i, it := range items {
		r := results[i]
		if r.err != nil {
			return nil, nil, false, fmt.Errorf("worktree.CachedScan: %s: %w", it.path, r.err)
		}
		mode := it.mode
		if it.symlink {
			mode = change.ModeSymlink
		}
		if !r.miss {
			entries[it.slashRel] = change.TreeEntry{SHA: r.sha, Mode: mode}
			newCache[it.slashRel] = cache[it.slashRel]
			continue
		}
		sha, err := eng.WriteBlob(r.data)
		if err != nil {
			return nil, nil, false, fmt.Errorf("worktree.CachedScan: %w", err)
		}
		entries[it.slashRel] = change.TreeEntry{SHA: sha, Mode: mode}
		newCache[it.slashRel] = wcCacheEntry{MtimeNs: it.mtimeNs, Size: it.size, BlobSHA: sha, Mode: mode}
		changed = true
	}
	// A vanished path reduces len(newCache) below len(cache); detect that too.
	if !changed && len(newCache) != len(cache) {
		changed = true
	}
	return entries, newCache, changed, nil
}

// parallelFor runs fn(0)..fn(n-1) across a bounded pool of workers. The worker
// count is tuned for I/O latency (more than CPUs) since the slow step is file
// reads, not computation; each index is handled by exactly one worker so callers
// need no locking to write results[i].
func parallelFor(n int, fn func(i int)) {
	if n == 0 {
		return
	}
	workers := runtime.NumCPU() * 4
	if workers > 32 {
		workers = 32
	}
	if workers > n {
		workers = n
	}
	if workers <= 1 {
		for i := 0; i < n; i++ {
			fn(i)
		}
		return
	}
	var next int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				i := next
				next++
				mu.Unlock()
				if int(i) >= n {
					return
				}
				fn(int(i))
			}
		}()
	}
	wg.Wait()
}
