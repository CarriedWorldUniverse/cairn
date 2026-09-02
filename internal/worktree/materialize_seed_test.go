package worktree

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// Seeding the wc-cache from materialize is a CORRECTNESS change before it is a
// speed one: materialize claims "these paths have exactly this content", and
// every later scan trusts that claim without reading the files. If the claim is
// ever wrong, cairn silently snapshots stale content — strictly worse than the
// slow scan it replaces. These tests are the safety case.
//
// The governing rule is that a seeded entry must be NO MORE TRUSTED than one
// CachedScan would have written for itself: same fingerprint fields, and still
// subject to the mtime < scanStartNs racy-window guard.

// materializeSeededInto is the shared arrangement: a freshly cloned branch,
// re-materialized through the seeding path, returning the seeded fingerprints
// and the folder they describe.
func materializeSeededInto(t *testing.T) (*Repo, string, string, map[string]wcCacheEntry) {
	t.Helper()
	r, def := seedBranch(t)
	entry := r.st.Expressed[def]
	dir := filepath.Join(r.Root(), entry.Path)
	line, err := r.eng.LineByName(def)
	if err != nil {
		t.Fatalf("LineByName: %v", err)
	}
	fps, err := MaterializeSeeded(r.eng, r.cacheDir(), line.TipCommit, dir, nil)
	if err != nil {
		t.Fatalf("MaterializeSeeded: %v", err)
	}
	if len(fps) == 0 {
		t.Fatal("materialize seeded no fingerprints at all")
	}
	return r, def, dir, fps
}

// THE central claim. A seeded fingerprint must be byte-identical to what a cold
// CachedScan of the same folder records — same mtime, size, blob SHA and mode.
// If these ever diverge, every other safety property here is built on sand:
// the seed would be asserting something the scan itself would not.
func TestSeededCacheMatchesAColdScan(t *testing.T) {
	r, _, dir, seeded := materializeSeededInto(t)

	// A cold scan: no prior cache, so every path is read and hashed for real.
	_, scanned, _, _, err := CachedScan(r.eng, dir, nil, nil, nil, time.Now().UnixNano())
	if err != nil {
		t.Fatalf("CachedScan: %v", err)
	}
	if !reflect.DeepEqual(seeded, scanned) {
		t.Fatalf("seeded fingerprints differ from a cold scan's\n seeded: %#v\nscanned: %#v", seeded, scanned)
	}
}

// The payoff, stated as a test so it cannot silently regress: once seeded, the
// next scan is all hits — nothing re-read, nothing re-encoded, so CachedScan
// reports the cache unchanged. This is the 40k file reads + 40k serial blob
// writes that the seed removes.
func TestSeedMakesTheNextScanAllHits(t *testing.T) {
	r, _, dir, seeded := materializeSeededInto(t)

	// The scan must start strictly after the materialized mtimes, exactly as a
	// real command's scan would.
	time.Sleep(10 * time.Millisecond)
	_, _, changed, _, err := CachedScan(r.eng, dir, nil, nil, seeded, time.Now().UnixNano())
	if err != nil {
		t.Fatalf("CachedScan: %v", err)
	}
	if changed {
		t.Fatal("scan after a seed reported changes — the seed did not take, so every file was re-read")
	}
}

// An ordinary edit landing after materialize must still be seen. This is the
// one that matters in practice: the operator materializes, then edits, then
// runs a command.
func TestSeedNeverHidesAnEditAfterMaterialize(t *testing.T) {
	r, def, dir, fps := materializeSeededInto(t)
	// Persist the seed, so the sync below reads it the way a real command would.
	if err := r.seedWCCache(def, fps); err != nil {
		t.Fatalf("seedWCCache: %v", err)
	}

	before := headOf(t, r, def)
	edited := filepath.Join(dir, "readme.txt")
	if err := os.WriteFile(edited, []byte("edited after materialize\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// See TestSeedNeverHidesASameSizeEdit: a distinct mtime, set explicitly
	// rather than slept for, so the test does not depend on the filesystem's
	// timestamp granularity.
	past := time.Now().Add(-5 * time.Second)
	if err := os.Chtimes(edited, past, past); err != nil {
		t.Fatal(err)
	}
	if err := r.SyncWorking(); err != nil {
		t.Fatalf("SyncWorking: %v", err)
	}
	if after := headOf(t, r, def); after == before {
		t.Fatal("an edit made after materialize was hidden by the seeded cache")
	}
}

// The nastier edit: same byte COUNT, different content. Only the mtime
// distinguishes it, so this proves the seed records a real mtime rather than a
// placeholder — a zero or constant mtime would make this pass unnoticed.
func TestSeedNeverHidesASameSizeEdit(t *testing.T) {
	r, _, dir, seeded := materializeSeededInto(t)

	p := filepath.Join(dir, "readme.txt")
	orig, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	replacement := make([]byte, len(orig))
	for i := range replacement {
		replacement[i] = 'z'
	}
	if err := os.WriteFile(p, replacement, 0o644); err != nil {
		t.Fatal(err)
	}
	// Pin the mtime to a distinct instant in the recent past rather than
	// relying on a sleep to outrun the filesystem's timestamp granularity —
	// which is nanoseconds on some filesystems and a whole second on others.
	// Still safely before the scan start, so this is the ordinary case and not
	// the racy one.
	past := time.Now().Add(-5 * time.Second)
	if err := os.Chtimes(p, past, past); err != nil {
		t.Fatal(err)
	}

	entries, _, changed, _, err := CachedScan(r.eng, dir, nil, nil, seeded, time.Now().UnixNano())
	if err != nil {
		t.Fatalf("CachedScan: %v", err)
	}
	if !changed {
		t.Fatal("a same-size edit was not detected — the seeded fingerprint is not tracking mtime")
	}
	if got := entries["readme.txt"].SHA; got == seeded["readme.txt"].BlobSHA {
		t.Fatal("scan returned the seeded SHA for a file whose content changed")
	}
}

// A seeded entry gets NO special trust: it is still subject to the racy-window
// guard, so a file whose mtime is not strictly before the scan start must be
// re-read rather than believed. Without this, a write racing the materialize
// could be masked forever.
func TestSeedObeysTheRacyWindow(t *testing.T) {
	r, _, dir, seeded := materializeSeededInto(t)

	p := filepath.Join(dir, "readme.txt")
	orig, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	replacement := make([]byte, len(orig))
	for i := range replacement {
		replacement[i] = 'q'
	}
	if err := os.WriteFile(p, replacement, 0o644); err != nil {
		t.Fatal(err)
	}
	// Pin the file's mtime to exactly the scan start: same size as the seed, and
	// an mtime that is NOT strictly before the scan — the racy case.
	st, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	racy := st.ModTime()
	seeded["readme.txt"] = wcCacheEntry{
		MtimeNs: racy.UnixNano(),
		Size:    st.Size(),
		BlobSHA: seeded["readme.txt"].BlobSHA,
		Mode:    seeded["readme.txt"].Mode,
	}

	entries, _, _, _, err := CachedScan(r.eng, dir, nil, nil, seeded, racy.UnixNano())
	if err != nil {
		t.Fatalf("CachedScan: %v", err)
	}
	if got := entries["readme.txt"].SHA; got == seeded["readme.txt"].BlobSHA {
		t.Fatal("a seeded entry inside the racy window was trusted — the guard does not cover seeds")
	}
}

// A seed records NO head. It proves what is on disk, never what was snapshotted
// into the working change, so #155's snapshot skip must not be able to fire off
// one: the next sync still snapshots, and only then records a real head.
func TestSeedRecordsNoHeadSoSnapshotStillRuns(t *testing.T) {
	r, def, _, fps := materializeSeededInto(t)
	if err := r.seedWCCache(def, fps); err != nil {
		t.Fatalf("seedWCCache: %v", err)
	}

	_, head, err := loadWCCache(r.wcCachePath(def))
	if err != nil {
		t.Fatalf("loadWCCache: %v", err)
	}
	if head != "" {
		t.Fatalf("seeded cache recorded head %q; a seed must record none so the snapshot still runs", head)
	}
}

// A materialize that fails partway describes a half-written folder, so it must
// leave no seed behind — a partial cache asserted as complete would make the
// next scan skip files that were never written.
func TestSeedNotWrittenWhenMaterializeFails(t *testing.T) {
	r, def := seedBranch(t)
	entry := r.st.Expressed[def]
	dir := filepath.Join(r.Root(), entry.Path)
	line, err := r.eng.LineByName(def)
	if err != nil {
		t.Fatalf("LineByName: %v", err)
	}

	// Wedge one target path: a non-empty directory where a file must go, which
	// no write or rename can resolve.
	p := filepath.Join(dir, "readme.txt")
	if err := os.RemoveAll(p); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(p, "occupied"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "occupied", "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	before, _, _ := loadWCCache(r.wcCachePath(def))
	if _, err := MaterializeSeeded(r.eng, r.cacheDir(), line.TipCommit, dir, nil); err == nil {
		t.Fatal("materialize was expected to fail on a wedged path")
	}
	after, _, _ := loadWCCache(r.wcCachePath(def))
	if !reflect.DeepEqual(before, after) {
		t.Fatal("a failed materialize changed the wc-cache; it must leave no seed")
	}
}
