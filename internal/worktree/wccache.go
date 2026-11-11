package worktree

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io/fs"
	"os"
	"runtime"
	"sync"
	"sync/atomic"

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

// wcCacheFile is the cache as it was stored when the file was JSON (it is
// still read; see loadWCCache): the per-path fingerprints plus the working
// commit those fingerprints were last snapshotted INTO.
//
// Head is what lets syncBranch skip re-snapshotting an unchanged branch (#155).
// An unchanged scan proves the entries are identical to last time; Head proves
// nothing else moved the working change in the meantime (a stash, an undo, a
// sibling process). Both must hold, or the snapshot runs as before.
type wcCacheFile struct {
	Entries map[string]wcCacheEntry `json:"e"`
	Head    string                  `json:"h"`
}

// wcCacheMagic opens a cache file in the binary encoding. The first byte can
// never begin a JSON document, so the two encodings cannot be confused.
const wcCacheMagic = "\x00cairn-wc-cache\x00v1\n"

// loadWCCache reads the snapshot cache at path, returning the per-path
// fingerprints and the working commit they were snapshotted into. A missing
// file yields an empty map (not an error): a first-ever scan has no cache.
//
// The file is in one of three encodings, told apart by how it starts:
//
//   - binary (wcCacheMagic) — what saveWCCache writes. A branch's cache is
//     read in full by every command that looks at the working copy, and at
//     16,000 files the JSON it used to be took longer to parse than the whole
//     tree took to stat;
//   - JSON with a recorded head — what every cache written between #155 and
//     the binary encoding looks like;
//   - JSON as a bare map with no head — every cache written before #155.
//     Those load with head "", which fails the skip guard, so the first sync
//     after an upgrade snapshots exactly as it always did and records a head
//     for next time.
//
// Whichever it was, the next save rewrites it as binary. A cairn too old to
// know the binary encoding fails to parse it, which syncBranch already treats
// as "no cache" — a full rescan that re-reads the tree and writes JSON again.
// Degrading to the slow-but-correct path is the only safe way to fail here.
func loadWCCache(path string) (map[string]wcCacheEntry, string, error) {
	entries, head, _, err := loadWCCacheEncoded(path)
	return entries, head, err
}

// loadWCCacheEncoded is loadWCCache that also reports whether the file was
// in an older (JSON) encoding — so the one caller that owns the file can
// rewrite it even when nothing in it changed, rather than leave a branch
// that is never edited parsing JSON for ever.
func loadWCCacheEncoded(path string) (entries map[string]wcCacheEntry, head string, legacy bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]wcCacheEntry{}, "", false, nil
	}
	if err != nil {
		return nil, "", false, fmt.Errorf("worktree.loadWCCache: %w", err)
	}
	if bytes.HasPrefix(data, []byte(wcCacheMagic)) {
		entries, head, err := decodeWCCache(data[len(wcCacheMagic):])
		if err != nil {
			return nil, "", false, fmt.Errorf("worktree.loadWCCache: %w", err)
		}
		return entries, head, false, nil
	}
	var f wcCacheFile
	if err := json.Unmarshal(data, &f); err == nil && f.Entries != nil {
		return f.Entries, f.Head, true, nil
	}
	// Pre-#155 format: a bare path→fingerprint map, no head.
	out := map[string]wcCacheEntry{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, "", false, fmt.Errorf("worktree.loadWCCache: %w", err)
	}
	return out, "", true, nil
}

// saveWCCache writes the snapshot cache to path atomically (temp + rename).
func saveWCCache(path string, c map[string]wcCacheEntry, head string) error {
	if err := writeFileAtomic(path, encodeWCCache(c, head)); err != nil {
		return fmt.Errorf("worktree.saveWCCache: %w", err)
	}
	return nil
}

// encodeWCCache renders the cache in the binary encoding: the magic, the
// head, the entry count, then per entry the path, mtime, size, blob SHA and
// mode. Strings are a uvarint length and the bytes; integers are varints.
// A trailing CRC-32 covers everything after the magic.
func encodeWCCache(c map[string]wcCacheEntry, head string) []byte {
	buf := make([]byte, 0, len(wcCacheMagic)+len(head)+16+len(c)*96)
	buf = append(buf, wcCacheMagic...)
	str := func(s string) {
		buf = binary.AppendUvarint(buf, uint64(len(s)))
		buf = append(buf, s...)
	}
	str(head)
	buf = binary.AppendUvarint(buf, uint64(len(c)))
	for p, e := range c {
		str(p)
		buf = binary.AppendVarint(buf, e.MtimeNs)
		buf = binary.AppendVarint(buf, e.Size)
		str(e.BlobSHA)
		buf = binary.AppendVarint(buf, int64(e.Mode))
	}
	return binary.LittleEndian.AppendUint32(buf, crc32.ChecksumIEEE(buf[len(wcCacheMagic):]))
}

var errWCCacheCorrupt = errors.New("corrupt cache")

// decodeWCCache parses the binary encoding (after the magic). Any damage —
// a bad checksum, a length running off the end, trailing bytes — is an
// error, which the caller turns into a full rescan.
func decodeWCCache(data []byte) (map[string]wcCacheEntry, string, error) {
	if len(data) < 4 {
		return nil, "", errWCCacheCorrupt
	}
	body, sum := data[:len(data)-4], binary.LittleEndian.Uint32(data[len(data)-4:])
	if crc32.ChecksumIEEE(body) != sum {
		return nil, "", errWCCacheCorrupt
	}
	// One conversion for the whole file: every path and SHA below is a
	// substring of it rather than an allocation of its own.
	text := string(body)
	pos := 0
	bad := false
	uvarint := func() uint64 {
		v, n := binary.Uvarint(body[pos:])
		if n <= 0 {
			bad = true
			return 0
		}
		pos += n
		return v
	}
	varint := func() int64 {
		v, n := binary.Varint(body[pos:])
		if n <= 0 {
			bad = true
			return 0
		}
		pos += n
		return v
	}
	str := func() string {
		n := uvarint()
		if bad || n > uint64(len(body)-pos) {
			bad = true
			return ""
		}
		s := text[pos : pos+int(n)]
		pos += int(n)
		return s
	}
	head := str()
	count := uvarint()
	// Every entry is at least five bytes, which bounds a count worth
	// believing before a map is sized by it.
	if bad || count > uint64(len(body)-pos)/5+1 {
		return nil, "", errWCCacheCorrupt
	}
	entries := make(map[string]wcCacheEntry, count)
	for i := uint64(0); i < count; i++ {
		p := str()
		var e wcCacheEntry
		e.MtimeNs = varint()
		e.Size = varint()
		e.BlobSHA = str()
		e.Mode = change.EntryMode(varint())
		if bad {
			return nil, "", errWCCacheCorrupt
		}
		entries[p] = e
	}
	if pos != len(body) {
		return nil, "", errWCCacheCorrupt
	}
	return entries, head, nil
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
// caller (time.Now().UnixNano()) before the walk and passed in. An UNTRACKED
// path that turns out unreadable (stat or content) is warned about (capped,
// see skipTracker) and dropped from both entries and the rebuilt cache — its
// slash-separated path (directories with a trailing "/") is returned in the
// skipped result so a caller can surface it structurally (#130); a TRACKED
// path's unreadable content remains a hard error.
// trackedModes carries the committed tip's mode for each tracked path. It is
// consulted only when dir's filesystem cannot represent the executable bit
// (execBitProbe, #161): there, a tracked path keeps the mode the tree already
// holds — git's core.fileMode=false — instead of the bit the filesystem cannot
// show, and an untracked path is regular. Where the bit is representable the
// filesystem is authoritative, exactly as before. nil is fine.
func CachedScan(eng *change.Engine, dir string, tracked map[string]struct{}, trackedModes map[string]change.EntryMode, gitlinks map[string]struct{}, cache map[string]wcCacheEntry, scanStartNs int64) (map[string]change.TreeEntry, map[string]wcCacheEntry, bool, []string, error) {
	execOK := execBitProbe(dir)
	// scanItem is one surviving worktree entry, collected by the (serial)
	// directory walk. Everything per-file after that — the lstat that yields
	// its fingerprint, and for a cache miss reading its content — is done in
	// PARALLEL: on Windows each file open is intercepted by the antivirus
	// scanner and is latency- not CPU-bound, so concurrent reads hide that
	// latency, and everywhere the lstat of every file in the tree is the one
	// cost a scan can never skip, so it is spread across the cores rather than
	// paid one syscall at a time inside the walk. The go-git blob writes stay
	// serial below (the object store is not concurrency-safe).
	type scanItem struct {
		slashRel string
		path     string
		d        fs.DirEntry
		mtimeNs  int64
		size     int64
		symlink  bool
		mode     change.EntryMode
		statErr  error
	}
	var items []scanItem
	skip := &skipTracker{}
	if err := walkWorktree(dir, tracked, gitlinks, skip, func(slashRel, path string, d fs.DirEntry) error {
		items = append(items, scanItem{slashRel: slashRel, path: path, d: d, symlink: d.Type()&os.ModeSymlink != 0})
		return nil
	}); err != nil {
		// Aborting on a hard error: the whole scan result is discarded, so
		// don't emit a "...and N more" summary nobody will see.
		return nil, nil, false, nil, fmt.Errorf("worktree.CachedScan: %w", err)
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
		info, err := items[i].d.Info()
		if err != nil {
			items[i].statErr = err
			return
		}
		items[i].mtimeNs, items[i].size = info.ModTime().UnixNano(), info.Size()
		switch {
		case execOK:
			if info.Mode()&0o111 != 0 {
				items[i].mode = change.ModeExecutable
			}
		case trackedModes[items[i].slashRel] == change.ModeExecutable:
			items[i].mode = change.ModeExecutable
		}
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
		if it.statErr != nil {
			// The entry was listed but could not be stat'ed (it vanished, or
			// its directory became unreadable, mid-scan). Fatal for a tracked
			// path; an untracked one is recorded and left out of the scan.
			if err := unreadableErr(tracked, it.slashRel, it.statErr, skip); err != nil {
				return nil, nil, false, nil, fmt.Errorf("worktree.CachedScan: %w", err)
			}
			continue
		}
		r := results[i]
		if r.err != nil {
			if _, ok := tracked[it.slashRel]; ok {
				// Aborting: same reasoning as above — no summary for a discarded result.
				return nil, nil, false, nil, fmt.Errorf("worktree.CachedScan: %s: %w", it.path, r.err)
			}
			// Untracked and unreadable (#130): record + warn (capped) and drop it
			// from both the returned entries and the rebuilt cache, rather than
			// aborting the whole scan.
			skip.skip(it.slashRel, r.err)
			changed = true
			continue
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
			// Aborting: same reasoning as above.
			return nil, nil, false, nil, fmt.Errorf("worktree.CachedScan: %w", err)
		}
		entries[it.slashRel] = change.TreeEntry{SHA: sha, Mode: mode}
		newCache[it.slashRel] = wcCacheEntry{MtimeNs: it.mtimeNs, Size: it.size, BlobSHA: sha, Mode: mode}
		changed = true
	}
	// A vanished path reduces len(newCache) below len(cache); detect that too.
	if !changed && len(newCache) != len(cache) {
		changed = true
	}
	skip.finish()
	return entries, newCache, changed, skip.Paths, nil
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
	// Indices are handed out in runs, so a scan of tens of thousands of
	// already-cached files (each a single lstat) is not dominated by handing
	// out indices; a short list — a handful of files to actually read — still
	// goes out one at a time, to as many workers as there are files.
	chunk := n / (workers * 8)
	if chunk < 1 {
		chunk = 1
	} else if chunk > 64 {
		chunk = 64
	}
	var next atomic.Int64
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for {
				end := int(next.Add(int64(chunk)))
				start := end - chunk
				if start >= n {
					return
				}
				if end > n {
					end = n
				}
				for i := start; i < end; i++ {
					fn(i)
				}
			}
		}()
	}
	wg.Wait()
}
