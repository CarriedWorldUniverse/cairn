package change

import (
	"os"
	"testing"
)

// TestMergeBaseInMatchesPairwiseMergeBase is the correctness gate on the #148
// optimisation: mergeBaseIn (one shared trunk ancestry) must return EXACTLY
// what the pairwise Commit.MergeBase it replaces returns, for every branch.
// Speed is worthless if the recorded base moves, because base_commit is what
// `ahead` and every divergence report are measured from.
//
// CAIRN_MERGEBASE_FIXTURE points it at a large real repo's bare store when one
// is available; otherwise it runs on the in-test fixture.
func TestMergeBaseInMatchesPairwiseMergeBase(t *testing.T) {
	skipOnWindows(t)

	url := os.Getenv("CAIRN_MERGEBASE_FIXTURE")
	if url == "" {
		u, _ := makeOriginRepoFull(t)
		url = u
	}

	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if err := e.fetchRemote(url); err != nil {
		t.Fatalf("fetchRemote: %v", err)
	}
	def, err := e.detectDefault()
	if err != nil {
		t.Fatalf("detectDefault: %v", err)
	}
	heads, err := e.listHeads()
	if err != nil {
		t.Fatalf("listHeads: %v", err)
	}
	defTip := heads[def]

	trunk, err := e.ancestorSet(defTip)
	if err != nil {
		t.Fatalf("ancestorSet: %v", err)
	}

	checked, mismatched := 0, 0
	for name, sha := range heads {
		if name == def {
			continue
		}
		want, wantErr := e.mergeBase(sha, defTip)
		got, gotErr := e.mergeBaseIn(sha, trunk)
		if (wantErr == nil) != (gotErr == nil) {
			t.Errorf("%s: error mismatch: pairwise=%v shared=%v", name, wantErr, gotErr)
			continue
		}
		checked++
		if got != want {
			mismatched++
			if mismatched <= 5 {
				t.Errorf("%s: merge base moved: shared=%q pairwise=%q", name, got, want)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no branches compared — the fixture cannot detect a regression")
	}
	t.Logf("compared %d branch(es); %d mismatch(es)", checked, mismatched)
}
