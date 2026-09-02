package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The slow verbs open the repo through a working-copy sync that, on a big tree,
// can outlast everything the verb does afterwards. Timing the verb from AFTER
// that call produced a "total" which excluded it — so express could report
// "total 9m17s" having just printed "synced working copy in 10m43s", a total
// smaller than one of its own phases (#154).
//
// This pins the fix where it can actually regress: the instant the verb is
// timed from must be captured BEFORE the sync, not after it.
func TestVerbTotalIncludesTheSyncPrelude(t *testing.T) {
	root := t.TempDir()
	mustRun(t, "init", root)

	// Enough files that a cold sync is measurably slow: the test can only tell
	// a before-sync timestamp from an after-sync one if the sync itself costs
	// something.
	dir := filepath.Join(root, "main")
	for i := 0; i < 3000; i++ {
		p := filepath.Join(dir, fmt.Sprintf("f%04d.txt", i))
		if err := os.WriteFile(p, []byte(fmt.Sprintf("payload %d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustRun(t, "commit", "--repo", root, "main")

	// Force the sync to be cold: with no stat-cache every file is re-read and
	// re-encoded, which is exactly the prelude that must be inside the total.
	if err := os.RemoveAll(filepath.Join(root, ".cairn", "wc-cache")); err != nil {
		t.Fatal(err)
	}

	before := time.Now()
	r, started, err := openRepoSyncedVerbose(root, "tester")
	returned := time.Now()
	if err != nil {
		t.Fatalf("openRepoSyncedVerbose: %v", err)
	}
	defer r.Close()

	// Measure the call from OUR OWN clock, never from `started` — deriving the
	// yardstick from the value under test makes a broken `started` skip the
	// test instead of failing it.
	callCost := returned.Sub(before)
	if callCost < 30*time.Millisecond {
		t.Skipf("open+sync completed in %s — too fast to distinguish a before-sync start from an after-sync one", callCost)
	}
	// A start captured before the sync sits right next to our own `before`; one
	// captured after it sits a whole sync away.
	if lead := started.Sub(before); lead > callCost/2 {
		t.Fatalf("verb clock started %s into a call that took %s — the start looks like it was "+
			"captured after the sync, so the reported total would exclude it", lead, callCost)
	}
}
