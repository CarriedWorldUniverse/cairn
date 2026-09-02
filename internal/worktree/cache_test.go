package worktree

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

func TestMaterializeCachesBlobsDeduped(t *testing.T) {
	eng, err := change.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	main, _ := eng.LineByName("main")
	ch, _ := eng.CreateChange(main.ID, "t")
	same := []byte("shared\n")
	r, err := eng.Commit(ch.ID, map[string][]byte{"a.txt": same, "b.txt": same, "c.txt": []byte("other\n")}, nil, "")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	cacheDir := filepath.Join(t.TempDir(), "cache")
	dir := filepath.Join(t.TempDir(), "wc")
	if err := Materialize(eng, cacheDir, r.HeadCommit, dir); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(cacheDir, "blobs"))
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	// The cache is filled ONLY where reflink makes filling it nearly free
	// (#151). Without reflink, populating it would mean a second full copy of
	// every file — the exact cost that change removed — so it deliberately
	// stays cold and the next materialize re-reads the blob instead.
	if !reflinkSupported(filepath.Join(cacheDir, "blobs")) {
		if len(entries) != 0 {
			t.Fatalf("cache blobs = %d on a filesystem without reflink, want 0: "+
				"filling the cache there costs a second full copy per file", len(entries))
		}
		return
	}
	if len(entries) != 2 {
		t.Fatalf("cache blobs = %d, want 2 (dedup)", len(entries))
	}
	sum := sha256.Sum256(same)
	if _, err := os.Stat(filepath.Join(cacheDir, "blobs", hex.EncodeToString(sum[:]))); err != nil {
		t.Fatalf("shared blob missing: %v", err)
	}
}

func TestMaterializeCoWIsolation(t *testing.T) {
	eng, _ := change.Open(t.TempDir())
	t.Cleanup(func() { _ = eng.Close() })
	main, _ := eng.LineByName("main")
	ch, _ := eng.CreateChange(main.ID, "t")
	content := []byte("v1\n")
	r, err := eng.Commit(ch.ID, map[string][]byte{"f.txt": content}, nil, "")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	cacheDir := filepath.Join(t.TempDir(), "cache")
	a := filepath.Join(t.TempDir(), "A")
	b := filepath.Join(t.TempDir(), "B")
	if err := Materialize(eng, cacheDir, r.HeadCommit, a); err != nil {
		t.Fatal(err)
	}
	if err := Materialize(eng, cacheDir, r.HeadCommit, b); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a, "f.txt"), []byte("CHANGED\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if gotB, _ := os.ReadFile(filepath.Join(b, "f.txt")); string(gotB) != "v1\n" {
		t.Fatalf("B mutated to %q (CoW isolation broken)", gotB)
	}
	// The isolation asserted above is the invariant that must hold everywhere,
	// and it does: without a cache each folder is written independently. The
	// cache-blob check below only applies where the cache is populated at all
	// (see TestMaterializeCachesBlobsDeduped).
	if !reflinkSupported(filepath.Join(cacheDir, "blobs")) {
		return
	}
	sum := sha256.Sum256(content)
	if gotCache, _ := os.ReadFile(filepath.Join(cacheDir, "blobs", hex.EncodeToString(sum[:]))); string(gotCache) != "v1\n" {
		t.Fatalf("cache blob mutated")
	}
}

func TestMaterializeLeavesNoTempBlobs(t *testing.T) {
	eng, _ := change.Open(t.TempDir())
	t.Cleanup(func() { _ = eng.Close() })
	main, _ := eng.LineByName("main")
	ch, _ := eng.CreateChange(main.ID, "t")
	r, err := eng.Commit(ch.ID, map[string][]byte{"a.txt": []byte("a\n")}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	cacheDir := filepath.Join(t.TempDir(), "cache")
	if err := Materialize(eng, cacheDir, r.HeadCommit, filepath.Join(t.TempDir(), "wc")); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(cacheDir, "blobs"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".blob-") || strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("leftover temp blob: %s", e.Name())
		}
	}
}
