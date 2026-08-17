package change

import (
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
)

// gitlinkSHA is a commit id from ANOTHER repository — the defining property of a
// gitlink and the reason no object with this hash exists in the store under test.
// (It is the real entry from pingdotgg/t3code that surfaced #140.)
const gitlinkSHA = "c9f5e549cf023632c3df948c207a58336192b3c7"

// TestGitlinkTreeRoundTrip is the core of #140: a gitlink entry must survive a
// read → write cycle unchanged, in both cairn's vocabulary (ModeGitlink) and
// git's wire encoding (mode 160000), and must re-write to the IDENTICAL tree
// hash — the invariant readTreeRefs/writeTreeRefs documents. Before the fix,
// mode 160000 fell through to ModeRegular on read and was re-emitted as a
// regular file, so the entry was silently corrupted even when nothing read it.
func TestGitlinkTreeRoundTrip(t *testing.T) {
	e := newTestEngine(t)

	blob, err := e.writeBlob([]byte("a\n"))
	if err != nil {
		t.Fatalf("writeBlob: %v", err)
	}
	entries := map[string]TreeEntry{
		"a.txt":         {SHA: blob.String(), Mode: ModeRegular},
		"vendor/nested": {SHA: gitlinkSHA, Mode: ModeGitlink},
	}

	root, err := e.writeTreeRefs(entries)
	if err != nil {
		t.Fatalf("writeTreeRefs with a gitlink: %v", err)
	}

	got, err := e.readTreeRefs(root.String())
	if err != nil {
		t.Fatalf("readTreeRefs: %v", err)
	}
	if len(got) != len(entries) {
		t.Fatalf("read back %d entries, want %d: %v", len(got), len(entries), got)
	}
	for p, want := range entries {
		if got[p] != want {
			t.Errorf("entry %q = %+v, want %+v", p, got[p], want)
		}
	}

	// Re-writing what we read must reproduce the same tree — no drift.
	again, err := e.writeTreeRefs(got)
	if err != nil {
		t.Fatalf("re-writeTreeRefs: %v", err)
	}
	if again != root {
		t.Errorf("re-write tree = %s, want %s (round-trip is not hash-stable)", again, root)
	}

	// git compatibility: the on-disk entry must be mode 160000, so a git client
	// reading this tree sees a submodule, not a regular file.
	rootTree, err := e.git.TreeObject(root)
	if err != nil {
		t.Fatalf("TreeObject(root): %v", err)
	}
	var vendorHash plumbing.Hash
	for _, ent := range rootTree.Entries {
		if ent.Name == "vendor" {
			vendorHash = ent.Hash
		}
	}
	if vendorHash.IsZero() {
		t.Fatal("no vendor/ subtree in the written tree")
	}
	vendor, err := e.git.TreeObject(vendorHash)
	if err != nil {
		t.Fatalf("TreeObject(vendor): %v", err)
	}
	found := false
	for _, ent := range vendor.Entries {
		if ent.Name != "nested" {
			continue
		}
		found = true
		if ent.Mode != filemode.Submodule {
			t.Errorf("vendor/nested mode = %v, want %v (160000)", ent.Mode, filemode.Submodule)
		}
		if ent.Hash.String() != gitlinkSHA {
			t.Errorf("vendor/nested hash = %s, want %s", ent.Hash, gitlinkSHA)
		}
	}
	if !found {
		t.Error("vendor/nested entry missing from the written subtree")
	}
}

// TestMergeGitlinkResolvesByReference covers the merge paths a gitlink can reach
// without ever loading content. A pointer moved on one side only must take that
// side; before the fix the "genuine divergence" branch called readBlob on a
// commit hash and reproduced #140's error on pull/fold.
func TestMergeGitlinkResolvesByReference(t *testing.T) {
	const bumped = "1111111111111111111111111111111111111111"
	const alsoBumped = "2222222222222222222222222222222222222222"

	gitlink := func(sha string) map[string]TreeEntry {
		return map[string]TreeEntry{"vendor/nested": {SHA: sha, Mode: ModeGitlink}}
	}

	cases := []struct {
		name               string
		base, ours, theirs map[string]TreeEntry
		wantSHA            string // "" = absent from the merged tree
		wantConflict       bool
	}{
		{
			name: "both sides agree",
			base: gitlink(gitlinkSHA), ours: gitlink(gitlinkSHA), theirs: gitlink(gitlinkSHA),
			wantSHA: gitlinkSHA,
		},
		{
			name: "theirs moved the pointer",
			base: gitlink(gitlinkSHA), ours: gitlink(gitlinkSHA), theirs: gitlink(bumped),
			wantSHA: bumped,
		},
		{
			name: "ours moved the pointer",
			base: gitlink(gitlinkSHA), ours: gitlink(bumped), theirs: gitlink(gitlinkSHA),
			wantSHA: bumped,
		},
		{
			name: "both moved it differently",
			base: gitlink(gitlinkSHA), ours: gitlink(bumped), theirs: gitlink(alsoBumped),
			wantSHA: alsoBumped, wantConflict: true,
		},
		{
			name: "removed on theirs, untouched on ours",
			base: gitlink(gitlinkSHA), ours: gitlink(gitlinkSHA), theirs: map[string]TreeEntry{},
			wantSHA: "",
		},
		{
			name: "added on theirs only",
			base: map[string]TreeEntry{}, ours: map[string]TreeEntry{}, theirs: gitlink(gitlinkSHA),
			wantSHA: gitlinkSHA,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEngine(t)
			main, err := e.LineByName("main")
			if err != nil {
				t.Fatalf("LineByName: %v", err)
			}
			ch, err := e.CreateChange(main.ID, "t")
			if err != nil {
				t.Fatalf("CreateChange: %v", err)
			}

			tree := func(entries map[string]TreeEntry) string {
				h, werr := e.writeTreeRefs(entries)
				if werr != nil {
					t.Fatalf("writeTreeRefs: %v", werr)
				}
				return h.String()
			}

			mergedTree, conflicts, err := e.mergeTrees(ch.ID, tree(tc.base), tree(tc.ours), tree(tc.theirs))
			if err != nil {
				t.Fatalf("mergeTrees: %v (a gitlink must never be content-merged)", err)
			}
			merged, err := e.readTreeRefs(mergedTree)
			if err != nil {
				t.Fatalf("readTreeRefs(merged): %v", err)
			}
			ent, present := merged["vendor/nested"]
			if tc.wantSHA == "" {
				if present {
					t.Fatalf("vendor/nested present (%+v), want it dropped", ent)
				}
			} else {
				if !present {
					t.Fatal("vendor/nested missing from the merged tree")
				}
				if ent.SHA != tc.wantSHA {
					t.Errorf("merged SHA = %s, want %s", ent.SHA, tc.wantSHA)
				}
				if ent.Mode != ModeGitlink {
					t.Errorf("merged mode = %v, want ModeGitlink", ent.Mode)
				}
			}
			if got := len(conflicts) > 0; got != tc.wantConflict {
				t.Errorf("conflict recorded = %v, want %v (%d conflicts)", got, tc.wantConflict, len(conflicts))
			}
		})
	}
}

// TestDiffGitlinkReportsWithoutReadingContent guards `cairn diff` against #140:
// a moved submodule pointer is reported as a change, not surfaced as a missing
// object.
func TestDiffGitlinkReportsWithoutReadingContent(t *testing.T) {
	e := newTestEngine(t)
	const bumped = "1111111111111111111111111111111111111111"

	old := map[string]TreeEntry{"vendor/nested": {SHA: gitlinkSHA, Mode: ModeGitlink}}
	new := map[string]TreeEntry{"vendor/nested": {SHA: bumped, Mode: ModeGitlink}}

	diffs, err := e.diffTreesMeta(old, new, "old", "new")
	if err != nil {
		t.Fatalf("diffTreesMeta over a gitlink: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("got %d diffs, want 1: %+v", len(diffs), diffs)
	}
	if diffs[0].Path != "vendor/nested" || diffs[0].Status != Modified || !diffs[0].Binary {
		t.Errorf("diff = %+v, want vendor/nested Modified Binary", diffs[0])
	}

	// An unchanged pointer is not a diff.
	same, err := e.diffTreesMeta(old, old, "old", "new")
	if err != nil {
		t.Fatalf("diffTreesMeta over an unchanged gitlink: %v", err)
	}
	if len(same) != 0 {
		t.Errorf("unchanged gitlink produced %d diffs: %+v", len(same), same)
	}
}
