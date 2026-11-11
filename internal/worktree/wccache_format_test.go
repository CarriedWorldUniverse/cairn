package worktree

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

func sampleCache(n int) map[string]wcCacheEntry {
	c := make(map[string]wcCacheEntry, n)
	for i := 0; i < n; i++ {
		c[fmt.Sprintf("src/pkg%d/sub/file%d.go", i%97, i)] = wcCacheEntry{
			MtimeNs: 1700000000000000000 + int64(i)*1000003,
			Size:    int64(i * 37),
			BlobSHA: fmt.Sprintf("%040x", i*2654435761),
			Mode:    change.EntryMode(i % 3),
		}
	}
	// The awkward ones: an empty path component cannot occur, but empty
	// files, negative and zero times, and non-ASCII names can.
	c["empty"] = wcCacheEntry{}
	c["päth/ünïcode ✓.txt"] = wcCacheEntry{MtimeNs: -5, Size: 1 << 40, BlobSHA: "x", Mode: change.ModeSymlink}
	return c
}

// The cache is written in the binary encoding and every older encoding still
// loads — a cache is only ever an optimisation, but an unreadable one costs a
// full re-read of the tree, so an upgrade must not throw them away.
func TestWCCacheReadsEveryEncoding(t *testing.T) {
	want := sampleCache(500)
	dir := t.TempDir()

	binPath := filepath.Join(dir, "bin.json")
	if err := saveWCCache(binPath, want, "abc123"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(binPath)
	if !bytes.HasPrefix(raw, []byte(wcCacheMagic)) {
		t.Fatal("saveWCCache did not write the binary encoding")
	}
	got, head, legacy, err := loadWCCacheEncoded(binPath)
	if err != nil || head != "abc123" || legacy || !reflect.DeepEqual(got, want) {
		t.Fatalf("binary round trip: head=%q legacy=%v err=%v equal=%v", head, legacy, err, reflect.DeepEqual(got, want))
	}

	// JSON with a head: what every release before this one wrote.
	jsonPath := filepath.Join(dir, "json.json")
	data, _ := json.Marshal(wcCacheFile{Entries: want, Head: "abc123"})
	if err := os.WriteFile(jsonPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, head, legacy, err = loadWCCacheEncoded(jsonPath)
	if err != nil || head != "abc123" || !legacy || !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON load: head=%q legacy=%v err=%v equal=%v", head, legacy, err, reflect.DeepEqual(got, want))
	}

	// An empty cache round-trips too.
	emptyPath := filepath.Join(dir, "empty.json")
	if err := saveWCCache(emptyPath, map[string]wcCacheEntry{}, ""); err != nil {
		t.Fatal(err)
	}
	if got, head, err := loadWCCache(emptyPath); err != nil || head != "" || len(got) != 0 {
		t.Fatalf("empty round trip: %v %q %v", got, head, err)
	}
}

// A damaged binary cache must be an ERROR — which syncBranch turns into a
// full rescan — never a partial or invented set of fingerprints, since a
// wrong fingerprint is a file whose edit goes unnoticed.
func TestWCCacheRejectsDamage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")
	if err := saveWCCache(path, sampleCache(200), "head"); err != nil {
		t.Fatal(err)
	}
	good, _ := os.ReadFile(path)
	cases := map[string][]byte{
		"truncated":         good[:len(good)/2],
		"magic only":        good[:len(wcCacheMagic)],
		"one byte short":    good[:len(good)-1],
		"trailing garbage":  append(append([]byte(nil), good...), 0, 1, 2),
		"flipped bit":       flipByte(good, len(good)/3),
		"flipped head byte": flipByte(good, len(wcCacheMagic)+2),
		"absurd count":      append([]byte(wcCacheMagic), 0, 0xff, 0xff, 0xff, 0xff, 0x0f, 0, 0, 0, 0),
	}
	for name, data := range cases {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if entries, _, err := loadWCCache(path); err == nil {
			t.Errorf("%s: loaded %d entries from a damaged cache", name, len(entries))
		}
	}
}

func flipByte(b []byte, i int) []byte {
	out := append([]byte(nil), b...)
	out[i] ^= 0x10
	return out
}

// A branch whose cache is still JSON gets it rewritten by the next sync even
// though nothing changed — otherwise a branch nobody edits would parse JSON
// on every command for ever — and the rewrite must not disturb the skip
// guard: same entries, same head, and the sync after it still skips.
func TestSyncUpgradesAJSONCacheInPlace(t *testing.T) {
	r, def := seedBranch(t)
	cachePath := r.wcCachePath(def)
	entries, head, err := loadWCCache(cachePath)
	if err != nil || head == "" {
		t.Fatalf("seed cache: head=%q err=%v", head, err)
	}
	data, _ := json.Marshal(wcCacheFile{Entries: entries, Head: head})
	if err := os.WriteFile(cachePath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	before := headOf(t, r, def)
	if err := r.SyncWorking(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(cachePath)
	if !bytes.HasPrefix(raw, []byte(wcCacheMagic)) {
		t.Fatal("the JSON cache was not rewritten in the binary encoding")
	}
	gotEntries, gotHead, legacy, err := loadWCCacheEncoded(cachePath)
	if err != nil || legacy || gotHead != head || !reflect.DeepEqual(gotEntries, entries) {
		t.Fatalf("upgraded cache differs: head %q→%q legacy=%v err=%v", head, gotHead, legacy, err)
	}
	if after := headOf(t, r, def); after != before {
		t.Fatalf("upgrading the cache moved the working head %s → %s", before, after)
	}
}

func BenchmarkLoadWCCache(b *testing.B) {
	c := sampleCache(16000)
	dir := b.TempDir()
	binPath, jsonPath := filepath.Join(dir, "bin"), filepath.Join(dir, "json")
	if err := saveWCCache(binPath, c, "head"); err != nil {
		b.Fatal(err)
	}
	data, _ := json.Marshal(wcCacheFile{Entries: c, Head: "head"})
	if err := os.WriteFile(jsonPath, data, 0o644); err != nil {
		b.Fatal(err)
	}
	for name, p := range map[string]string{"binary": binPath, "json": jsonPath} {
		b.Run(name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				if _, _, err := loadWCCache(p); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
