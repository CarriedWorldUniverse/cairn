package worktree

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The #155 skip is only safe if it NEVER hides a real change. These tests are
// the safety case, not the speed case: each one puts the working copy in a
// state where skipping would produce a stale working change, and asserts the
// sync still reflects reality.

func seedBranch(t *testing.T) (*Repo, string) {
	t.Helper()
	skipOnWindows(t)
	url, _ := makeOriginRepoWT(t)
	root := filepath.Join(t.TempDir(), "wc")
	r, err := Clone(url, root, "tester", nil)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	def, err := r.DefaultBranch()
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	if err := r.SyncWorking(); err != nil { // establish a recorded head
		t.Fatalf("SyncWorking: %v", err)
	}
	return r, def
}

func headOf(t *testing.T, r *Repo, branch string) string {
	t.Helper()
	entry := r.st.Expressed[branch]
	ch, err := r.eng.GetChange(entry.ChangeID)
	if err != nil {
		t.Fatalf("GetChange: %v", err)
	}
	return ch.HeadCommit
}

// An EDITED file must still be picked up — the scan changes, so the guard's
// first condition fails and the snapshot runs.
func TestSyncSkipStillSeesAnEditedFile(t *testing.T) {
	r, def := seedBranch(t)
	before := headOf(t, r, def)

	p := filepath.Join(r.Root(), r.st.Expressed[def].Path, "readme.txt")
	if err := os.WriteFile(p, []byte("edited by the test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.SyncWorking(); err != nil {
		t.Fatalf("SyncWorking: %v", err)
	}
	if after := headOf(t, r, def); after == before {
		t.Fatal("an edited file did not move the working head — the skip hid a real change")
	}
	diffs, err := r.WorkingDiff(def)
	if err != nil {
		t.Fatalf("WorkingDiff: %v", err)
	}
	if len(diffs) == 0 {
		t.Fatal("WorkingDiff reported no change after an edit")
	}
}

// A NEW file must be picked up: the scan gains a path, so cacheChanged trips.
func TestSyncSkipStillSeesANewFile(t *testing.T) {
	r, def := seedBranch(t)
	before := headOf(t, r, def)
	p := filepath.Join(r.Root(), r.st.Expressed[def].Path, "brand-new.txt")
	if err := os.WriteFile(p, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.SyncWorking(); err != nil {
		t.Fatalf("SyncWorking: %v", err)
	}
	if after := headOf(t, r, def); after == before {
		t.Fatal("a new file did not move the working head — the skip hid it")
	}
}

// A DELETED file must be picked up: len(newCache) drops, which CachedScan
// already reports as changed.
func TestSyncSkipStillSeesADeletedFile(t *testing.T) {
	r, def := seedBranch(t)
	before := headOf(t, r, def)
	p := filepath.Join(r.Root(), r.st.Expressed[def].Path, "readme.txt")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := r.SyncWorking(); err != nil {
		t.Fatalf("SyncWorking: %v", err)
	}
	if after := headOf(t, r, def); after == before {
		t.Fatal("a deleted file did not move the working head — the skip hid it")
	}
}

// The guard's SECOND condition: the folder is untouched, but something else
// moved the working head. The recorded head no longer matches, so the sync must
// re-snapshot rather than trust the unchanged scan.
func TestSyncSkipRefusesWhenTheHeadMovedUnderIt(t *testing.T) {
	r, def := seedBranch(t)

	// Corrupt only the RECORDED head, simulating any out-of-band move of the
	// working change (stash, undo, a sibling process) without touching a file.
	cachePath := filepath.Join(r.Root(), ".cairn", "wc-cache", def+".json")
	entries, head, err := loadWCCache(cachePath)
	if err != nil {
		t.Fatalf("loadWCCache: %v", err)
	}
	if head == "" {
		t.Fatal("no head recorded after the seed sync — the guard could never engage")
	}
	if err := saveWCCache(cachePath, entries, "0000000000000000000000000000000000000000"); err != nil {
		t.Fatalf("saveWCCache: %v", err)
	}

	if err := r.SyncWorking(); err != nil {
		t.Fatalf("SyncWorking: %v", err)
	}
	_, after, err := loadWCCache(cachePath)
	if err != nil {
		t.Fatalf("loadWCCache: %v", err)
	}
	if after != head {
		t.Fatalf("after a mismatched head the sync recorded %q, want the real head %q — "+
			"it must re-snapshot and re-record, not trust the stale value", after, head)
	}
}

// A pre-#155 cache (bare map, no head) must load and force a snapshot, not
// error and not skip.
func TestSyncSkipUpgradesAnOldFormatCache(t *testing.T) {
	r, def := seedBranch(t)
	cachePath := filepath.Join(r.Root(), ".cairn", "wc-cache", def+".json")
	entries, head, err := loadWCCache(cachePath)
	if err != nil || head == "" {
		t.Fatalf("seed cache: head=%q err=%v", head, err)
	}
	// Rewrite in the OLD format: a bare path→fingerprint map.
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	gotEntries, gotHead, err := loadWCCache(cachePath)
	if err != nil {
		t.Fatalf("old-format load failed: %v", err)
	}
	if gotHead != "" {
		t.Fatalf("old-format cache reported head %q, want empty (which forces a snapshot)", gotHead)
	}
	if len(gotEntries) != len(entries) {
		t.Fatalf("old-format entries = %d, want %d", len(gotEntries), len(entries))
	}
	// Syncing must succeed and re-record a head, upgrading the file in place.
	if err := r.SyncWorking(); err != nil {
		t.Fatalf("SyncWorking on an old-format cache: %v", err)
	}
	if _, upgraded, _ := loadWCCache(cachePath); upgraded != head {
		t.Fatalf("cache not upgraded: head=%q want %q", upgraded, head)
	}
}
