package worktree

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

func newCacheTestEngine(t *testing.T) *change.Engine {
	t.Helper()
	eng, err := change.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

// fileMtimeNs returns the file's modification time in unix nanoseconds.
func fileMtimeNs(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	return info.ModTime().UnixNano()
}

func TestWCCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	c := map[string]wcCacheEntry{
		"a.txt":    {MtimeNs: 123, Size: 4, BlobSHA: "deadbeef", Mode: change.ModeRegular},
		"bin/run":  {MtimeNs: 456, Size: 9, BlobSHA: "cafebabe", Mode: change.ModeExecutable},
		"dir/link": {MtimeNs: 789, Size: 5, BlobSHA: "0badf00d", Mode: change.ModeSymlink},
	}
	if err := saveWCCache(path, c, "headsha"); err != nil {
		t.Fatalf("saveWCCache: %v", err)
	}
	got, head, err := loadWCCache(path)
	if err != nil {
		t.Fatalf("loadWCCache: %v", err)
	}
	if !reflect.DeepEqual(got, c) {
		t.Fatalf("round-trip mismatch:\n got %v\nwant %v", got, c)
	}
	if head != "headsha" {
		t.Fatalf("head round-trip = %q, want %q", head, "headsha")
	}
}

func TestWCCacheLoadMissing(t *testing.T) {
	got, head, err := loadWCCache(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("loadWCCache missing: %v", err)
	}
	if head != "" {
		t.Fatalf("missing file yielded head %q, want empty", head)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty map for missing file, got %v", got)
	}
}

func TestCachedScanReusesUnchanged(t *testing.T) {
	skipOnWindows(t)
	eng := newCacheTestEngine(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	start1 := time.Now().UnixNano() + int64(time.Second)
	entries1, cache1, changed1, _, err := CachedScan(eng, dir, nil, nil, nil, nil, start1)
	if err != nil {
		t.Fatalf("scan1: %v", err)
	}
	sha := entries1["a.txt"].SHA
	if sha == "" {
		t.Fatalf("scan1 produced no SHA for a.txt: %v", entries1)
	}
	if !changed1 {
		t.Fatalf("scan1 with empty cache should report cacheChanged=true")
	}

	// A cache HIT must not read the file. chmod 000 used to prove that, but a
	// chmod now (rightly) moves ctime and so is a miss; instead plant a
	// sentinel SHA in the cached entry: a hit returns it verbatim, a re-read
	// would return the real blob SHA.
	const sentinel = "1111111111111111111111111111111111111111"
	e := cache1["a.txt"]
	e.BlobSHA = sentinel
	cache1["a.txt"] = e

	start2 := time.Now().UnixNano() + int64(time.Second)
	entries2, _, changed2, _, err := CachedScan(eng, dir, nil, nil, nil, cache1, start2)
	if err != nil {
		t.Fatalf("scan2 (should be a cache hit, no read): %v", err)
	}
	if entries2["a.txt"].SHA != sentinel {
		t.Fatalf("cache hit should reuse the cached SHA without reading: got %s want %s", entries2["a.txt"].SHA, sentinel)
	}
	if changed2 {
		t.Fatalf("scan2 with warm cache and no changes should report cacheChanged=false")
	}
}

func TestCachedScanDetectsChange(t *testing.T) {
	eng := newCacheTestEngine(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	start1 := time.Now().UnixNano() + int64(time.Second)
	entries1, cache1, _, _, err := CachedScan(eng, dir, nil, nil, nil, nil, start1)
	if err != nil {
		t.Fatalf("scan1: %v", err)
	}
	sha1 := entries1["a.txt"].SHA

	// Rewrite with new content of a different size.
	if err := os.WriteFile(path, []byte("a much longer beta body\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	start2 := time.Now().UnixNano() + int64(time.Second)
	entries2, cache2, changed2, _, err := CachedScan(eng, dir, nil, nil, nil, cache1, start2)
	if err != nil {
		t.Fatalf("scan2: %v", err)
	}
	if entries2["a.txt"].SHA == sha1 {
		t.Fatalf("changed file should yield a new SHA, got same %s", sha1)
	}
	if cache2["a.txt"].BlobSHA != entries2["a.txt"].SHA {
		t.Fatalf("cache not updated: cache %s entry %s", cache2["a.txt"].BlobSHA, entries2["a.txt"].SHA)
	}
	if !changed2 {
		t.Fatalf("scan2 after file edit should report cacheChanged=true")
	}
}

func TestCachedScanRacy(t *testing.T) {
	eng := newCacheTestEngine(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Seed a cache entry whose (mtime,size) match the on-disk file but whose
	// BlobSHA is bogus — a hit would return the bogus SHA. With scanStartNs <=
	// the file's mtime the entry is racy → must be re-read, yielding the real SHA.
	mtimeNs := fileMtimeNs(t, path)
	info, _ := os.Stat(path)
	cache := map[string]wcCacheEntry{
		"a.txt": {MtimeNs: mtimeNs, Size: info.Size(), BlobSHA: "0000000000000000000000000000000000000000", Mode: change.ModeRegular},
	}
	// scanStartNs at or below the file's mtime ⇒ racy ⇒ re-read.
	entries, _, _, _, err := CachedScan(eng, dir, nil, nil, nil, cache, mtimeNs)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if entries["a.txt"].SHA == "0000000000000000000000000000000000000000" {
		t.Fatalf("racy entry must be re-read, not served from cache")
	}
	// Sanity: it should equal the real stored blob SHA.
	realSHA, _ := eng.WriteBlob([]byte("alpha\n"))
	if entries["a.txt"].SHA != realSHA {
		t.Fatalf("re-read SHA %s != real %s", entries["a.txt"].SHA, realSHA)
	}
}

func TestCachedScanDropsVanished(t *testing.T) {
	eng := newCacheTestEngine(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatalf("write b: %v", err)
	}
	start1 := time.Now().UnixNano() + int64(time.Second)
	_, cache1, _, _, err := CachedScan(eng, dir, nil, nil, nil, nil, start1)
	if err != nil {
		t.Fatalf("scan1: %v", err)
	}
	if _, ok := cache1["b.txt"]; !ok {
		t.Fatalf("scan1 cache should contain b.txt")
	}

	if err := os.Remove(filepath.Join(dir, "b.txt")); err != nil {
		t.Fatalf("remove b: %v", err)
	}
	start2 := time.Now().UnixNano() + int64(time.Second)
	entries2, cache2, changed2, _, err := CachedScan(eng, dir, nil, nil, nil, cache1, start2)
	if err != nil {
		t.Fatalf("scan2: %v", err)
	}
	if _, ok := entries2["b.txt"]; ok {
		t.Fatalf("vanished b.txt should be absent from entries")
	}
	if _, ok := cache2["b.txt"]; ok {
		t.Fatalf("vanished b.txt should be dropped from cache")
	}
	if !changed2 {
		t.Fatalf("scan2 after file removal should report cacheChanged=true")
	}
}

func TestCachedScanSymlink(t *testing.T) {
	skipOnWindows(t)
	eng := newCacheTestEngine(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink("a.txt", link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	start := time.Now().UnixNano() + int64(time.Second)
	entries, _, _, _, err := CachedScan(eng, dir, nil, nil, nil, nil, start)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if entries["link"].Mode != change.ModeSymlink {
		t.Fatalf("link mode = %v, want ModeSymlink", entries["link"].Mode)
	}
	wantSHA, _ := eng.WriteBlob([]byte("a.txt"))
	if entries["link"].SHA != wantSHA {
		t.Fatalf("link SHA %s != blob-of-target %s", entries["link"].SHA, wantSHA)
	}
}

func TestCachedScanExecMode(t *testing.T) {
	skipOnWindows(t)
	eng := newCacheTestEngine(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "run")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	start := time.Now().UnixNano() + int64(time.Second)
	entries, cache, _, _, err := CachedScan(eng, dir, nil, nil, nil, nil, start)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if entries["run"].Mode != change.ModeExecutable {
		t.Fatalf("run mode = %v, want ModeExecutable", entries["run"].Mode)
	}
	if cache["run"].Mode != change.ModeExecutable {
		t.Fatalf("cache run mode = %v, want ModeExecutable", cache["run"].Mode)
	}
}

func TestCachedScanHonorsIgnore(t *testing.T) {
	eng := newCacheTestEngine(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatalf("write ignore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "debug.log"), []byte("noise\n"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	start := time.Now().UnixNano() + int64(time.Second)
	entries, _, _, _, err := CachedScan(eng, dir, nil, nil, nil, nil, start)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if _, ok := entries["debug.log"]; ok {
		t.Fatalf("ignored *.log should be absent from entries: %v", entries)
	}
	if _, ok := entries["a.txt"]; !ok {
		t.Fatalf("a.txt should be present")
	}
}

// TestCachedScanCacheChangedFlag verifies that cacheChanged is false on a
// warm-cache no-edit rescan, and true after editing a file.
func TestCachedScanCacheChangedFlag(t *testing.T) {
	skipOnWindows(t)
	eng := newCacheTestEngine(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// First scan: cold cache → cacheChanged must be true.
	start1 := time.Now().UnixNano() + int64(time.Second)
	_, cache1, changed1, _, err := CachedScan(eng, dir, nil, nil, nil, nil, start1)
	if err != nil {
		t.Fatalf("scan1: %v", err)
	}
	if !changed1 {
		t.Fatalf("cold-cache scan should report cacheChanged=true")
	}

	// Second scan: warm cache, nothing touched → cacheChanged must be false.
	start2 := time.Now().UnixNano() + int64(time.Second)
	_, _, changed2, _, err := CachedScan(eng, dir, nil, nil, nil, cache1, start2)
	if err != nil {
		t.Fatalf("scan2: %v", err)
	}
	if changed2 {
		t.Fatalf("warm-cache no-edit scan should report cacheChanged=false, got true")
	}

	// Edit the file, then rescan → cacheChanged must be true again.
	if err := os.WriteFile(path, []byte("world\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	start3 := time.Now().UnixNano() + int64(time.Second)
	_, _, changed3, _, err := CachedScan(eng, dir, nil, nil, nil, cache1, start3)
	if err != nil {
		t.Fatalf("scan3: %v", err)
	}
	if !changed3 {
		t.Fatalf("scan after edit should report cacheChanged=true")
	}
}

// trackedDirSet replaced a linear scan asked once per directory; it must
// answer exactly what that scan answered.
func TestTrackedDirSetMatchesPrefixScan(t *testing.T) {
	hasTrackedPrefix := func(tracked map[string]struct{}, dirRel string) bool {
		prefix := dirRel + "/"
		for k := range tracked {
			if strings.HasPrefix(k, prefix) {
				return true
			}
		}
		return false
	}
	tracked := map[string]struct{}{}
	for _, p := range []string{
		"top.txt", "a/x", "a/b/c/d/deep.go", "a/b/sibling", "ab/not-a", "a.b/c", "z/y/x/w", "dir/file", "dir/file2",
	} {
		tracked[p] = struct{}{}
	}
	dirs := trackedDirSet(tracked)
	for _, probe := range []string{
		"a", "a/b", "a/b/c", "a/b/c/d", "a/b/c/d/deep.go", "ab", "a.b", "z", "z/y", "z/y/x", "z/y/x/w",
		"dir", "dir/file", "top.txt", "nope", "a/nope", "a/b/c/nope", "", "b", "a/b/sibling", "z/y/x/w/v",
	} {
		_, got := dirs[probe]
		if want := hasTrackedPrefix(tracked, probe); got != want {
			t.Errorf("trackedDirSet[%q] = %v, prefix scan says %v", probe, got, want)
		}
	}
	if n := len(trackedDirSet(nil)); n != 0 {
		t.Errorf("trackedDirSet(nil) has %d entries", n)
	}
}
