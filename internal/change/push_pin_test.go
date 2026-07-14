package change

import (
	"fmt"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// pinnedPushRefs returns every refs/cairn/push/* ref currently in e's store.
func pinnedPushRefs(t *testing.T, e *Engine) []string {
	t.Helper()
	iter, err := e.git.References()
	if err != nil {
		t.Fatalf("References: %v", err)
	}
	var out []string
	if err := iter.ForEach(func(ref *plumbing.Reference) error {
		if strings.HasPrefix(ref.Name().String(), "refs/cairn/push/") {
			out = append(out, ref.Name().String())
		}
		return nil
	}); err != nil {
		t.Fatalf("iterate refs: %v", err)
	}
	return out
}

func seedEngineWithCommit(t *testing.T, bareDir string) *Engine {
	t.Helper()
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	main, _ := e.LineByName("main")
	ch, _ := e.CreateChange(main.ID, "a")
	if _, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("a\n")}, nil, ""); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := e.AddRemote("origin", bareDir, "git"); err != nil {
		t.Fatalf("AddRemote: %v", err)
	}
	return e
}

// TestPreparePushPinsThenFinishCleansUp is the pinned-ref cleanup acceptance
// test (issue #98 Phase B) for the success path: after a full
// PreparePush/NetworkPush/FinishPush cycle succeeds, no refs/cairn/push/*
// pins remain.
func TestPreparePushPinsThenFinishCleansUp(t *testing.T) {
	skipOnWindowsPush(t)
	bareDir := t.TempDir()
	if _, err := git.PlainInit(bareDir, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	e := seedEngineWithCommit(t, bareDir)

	pp, err := e.PreparePushToRemote("origin", false)
	if err != nil {
		t.Fatalf("PreparePush: %v", err)
	}
	// While the PreparedPush is outstanding (network phase not yet run), the
	// pinned refs exist.
	if pins := pinnedPushRefs(t, e); len(pins) == 0 {
		t.Fatal("expected pinned refs to exist after PreparePush, found none")
	}

	if err := e.NetworkPush(pp); err != nil {
		t.Fatalf("NetworkPush: %v", err)
	}
	if err := e.FinishPush(pp); err != nil {
		t.Fatalf("FinishPush: %v", err)
	}

	if pins := pinnedPushRefs(t, e); len(pins) != 0 {
		t.Fatalf("pinned refs leaked after a successful push: %v", pins)
	}

	bare, err := git.PlainOpen(bareDir)
	if err != nil {
		t.Fatalf("PlainOpen bare: %v", err)
	}
	if _, err := bare.Reference(plumbing.NewBranchReferenceName("main"), true); err != nil {
		t.Fatalf("bare refs/heads/main missing after push: %v", err)
	}
}

// TestPreparePushPinsCleanedUpOnNetworkFailure is the pinned-ref cleanup
// acceptance test for the failure path: a network-phase error still leaves
// no refs/cairn/push/* pins behind once FinishPush runs (the caller's defer).
func TestPreparePushPinsCleanedUpOnNetworkFailure(t *testing.T) {
	skipOnWindowsPush(t)
	bareDir := t.TempDir()
	if _, err := git.PlainInit(bareDir, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	e := seedEngineWithCommit(t, bareDir)

	if err := e.PushToRemote("origin", false); err != nil {
		t.Fatalf("seed push: %v", err)
	}
	advanceBareMainIndependently(t, bareDir)

	main, _ := e.LineByName("main")
	ch, _ := e.OpenChangeForLine(main.ID)
	if _, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("2\n")}, nil, ""); err != nil {
		t.Fatalf("commit2: %v", err)
	}

	pp, err := e.PreparePushToRemote("origin", false)
	if err != nil {
		t.Fatalf("PreparePush: %v", err)
	}
	if pins := pinnedPushRefs(t, e); len(pins) == 0 {
		t.Fatal("expected pinned refs to exist after PreparePush, found none")
	}

	netErr := e.NetworkPush(pp)
	if netErr == nil {
		t.Fatal("expected non-fast-forward NetworkPush error")
	}
	if err := e.FinishPush(pp); err != nil {
		t.Fatalf("FinishPush: %v", err)
	}

	if pins := pinnedPushRefs(t, e); len(pins) != 0 {
		t.Fatalf("pinned refs leaked after a failed push: %v", pins)
	}
}

// TestNetworkPushVerifiesPostPushLanding is the post-push-verify acceptance
// test (issue #117's root cause): with the real push skipped via the test
// seam (simulating go-git swallowing a subprocess failure and reporting
// success), NetworkPush must still detect the remote ref never moved and
// error out naming the ref.
func TestNetworkPushVerifiesPostPushLanding(t *testing.T) {
	skipOnWindowsPush(t)
	bareDir := t.TempDir()
	if _, err := git.PlainInit(bareDir, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	e := seedEngineWithCommit(t, bareDir)

	pp, err := e.PreparePushToRemote("origin", false)
	if err != nil {
		t.Fatalf("PreparePush: %v", err)
	}
	defer func() { _ = e.FinishPush(pp) }()

	restore := SetSkipRealPushHook(func() bool { return true })
	defer restore()

	err = e.NetworkPush(pp)
	if err == nil {
		t.Fatal("expected NetworkPush to detect the ref did not land, got nil error")
	}
	if !strings.Contains(err.Error(), "refs/heads/main") {
		t.Fatalf("error does not name the offending ref: %v", err)
	}
	// This is the "ref missing from the remote entirely" branch (the bare
	// remote never received anything); it must read distinctly from the
	// "known locally, but not a descendant" and "not known locally" branches.
	if !strings.Contains(err.Error(), "is missing") {
		t.Fatalf("error does not read as the ref-missing case: %v", err)
	}
}

// TestNetworkPushHonorsDelayHook exercises the network-delay test seam used
// by the worktree-layer overlap tests: NetworkPush must actually block for
// the hook's duration.
func TestNetworkPushHonorsDelayHook(t *testing.T) {
	skipOnWindowsPush(t)
	bareDir := t.TempDir()
	if _, err := git.PlainInit(bareDir, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	e := seedEngineWithCommit(t, bareDir)

	pp, err := e.PreparePushToRemote("origin", false)
	if err != nil {
		t.Fatalf("PreparePush: %v", err)
	}
	defer func() { _ = e.FinishPush(pp) }()

	called := false
	restore := SetNetworkDelayHook(func() { called = true })
	defer restore()

	if err := e.NetworkPush(pp); err != nil {
		t.Fatalf("NetworkPush: %v", err)
	}
	if !called {
		t.Fatal("network delay hook was never invoked")
	}
}

// TestPinOutgoingRefsExcludesForeignPushPins is the "pin leak" fix (review
// must-fix #1a): a foreign refs/cairn/push/<op-id>/* ref already sitting in
// the local store (a concurrent op, a crash orphan, or one imported from a
// polluted remote) must NEVER be swept up by pinOutgoingRefs — a
// full-fidelity cairn push's "+refs/cairn/*:refs/cairn/*" refspec would
// otherwise match it too (go-git's glob has no "/" boundary awareness) and
// NetworkPush would permanently publish it to the remote.
func TestPinOutgoingRefsExcludesForeignPushPins(t *testing.T) {
	skipOnWindowsPush(t)
	bareDir := t.TempDir()
	if _, err := git.PlainInit(bareDir, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	main, _ := e.LineByName("main")
	ch, _ := e.CreateChange(main.ID, "a")
	if _, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("a\n")}, nil, ""); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := e.AddRemote("origin", bareDir, "cairn"); err != nil { // full-fidelity: exercises the wide "+refs/cairn/*:refs/cairn/*" refspec
		t.Fatalf("AddRemote(cairn): %v", err)
	}

	// Seed a foreign pin: some OTHER op's (or an orphaned) pinned ref.
	main, _ = e.LineByName("main")
	foreignPin := plumbing.ReferenceName("refs/cairn/push/deadbeefdeadbeef/heads/main")
	if err := e.git.Storer.SetReference(plumbing.NewHashReference(foreignPin, plumbing.NewHash(main.TipCommit))); err != nil {
		t.Fatalf("seed foreign pin: %v", err)
	}

	pp, err := e.PreparePushToRemote("origin", false)
	if err != nil {
		t.Fatalf("PreparePush: %v", err)
	}
	for _, p := range pp.pins {
		if strings.HasPrefix(p.pinned.String(), "refs/cairn/push/deadbeefdeadbeef/") ||
			strings.HasPrefix(p.target.String(), "refs/cairn/push/") {
			t.Fatalf("foreign pin %s leaked into this op's PreparedPush.pins: %+v", foreignPin, p)
		}
	}

	if err := e.NetworkPush(pp); err != nil {
		t.Fatalf("NetworkPush: %v", err)
	}
	if err := e.FinishPush(pp); err != nil {
		t.Fatalf("FinishPush: %v", err)
	}

	bare, err := git.PlainOpen(bareDir)
	if err != nil {
		t.Fatalf("PlainOpen bare: %v", err)
	}
	iter, err := bare.References()
	if err != nil {
		t.Fatalf("bare References: %v", err)
	}
	if err := iter.ForEach(func(ref *plumbing.Reference) error {
		if strings.HasPrefix(ref.Name().String(), "refs/cairn/push/") {
			t.Fatalf("foreign/pin ref %s was published to the remote — pin leak", ref.Name())
		}
		return nil
	}); err != nil {
		t.Fatalf("iterate bare refs: %v", err)
	}

	// The foreign pin itself is left alone locally (not our job to GC crash
	// orphans — a follow-up ticket), only never re-published.
	if pins := pinnedPushRefs(t, e); len(pins) != 1 || pins[0] != foreignPin.String() {
		t.Fatalf("expected only the untouched foreign pin to remain locally, got %v", pins)
	}
}

// TestFetchPrunesImportedForeignPushPins is the "pin leak" fix's import-side
// defense-in-depth (review must-fix #1b): if a remote is polluted with a
// refs/cairn/push/* ref (from any of the reasons above, on ANY client that
// ever pushed it), fetching from it must not let that ref land locally —
// which would make the leak self-perpetuating (import → later re-publish by
// a full-fidelity push).
func TestFetchPrunesImportedForeignPushPins(t *testing.T) {
	skipOnWindowsPush(t)
	bareDir := t.TempDir()
	if _, err := git.PlainInit(bareDir, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	e := seedEngineWithCommit(t, bareDir)
	if err := e.AddRemote("origin", bareDir, "cairn"); err != nil {
		t.Fatalf("AddRemote(cairn): %v", err)
	}
	if err := e.PushToRemote("origin", false); err != nil {
		t.Fatalf("seed push: %v", err)
	}

	// Pollute the bare remote directly with a foreign pin ref.
	main, _ := e.LineByName("main")
	bare, err := git.PlainOpen(bareDir)
	if err != nil {
		t.Fatalf("PlainOpen bare: %v", err)
	}
	poison := plumbing.ReferenceName("refs/cairn/push/foreignop123456/heads/main")
	if err := bare.Storer.SetReference(plumbing.NewHashReference(poison, plumbing.NewHash(main.TipCommit))); err != nil {
		t.Fatalf("pollute bare remote: %v", err)
	}

	if err := e.FetchTracking("origin"); err != nil {
		t.Fatalf("FetchTracking: %v", err)
	}

	if pins := pinnedPushRefs(t, e); len(pins) != 0 {
		t.Fatalf("foreign pin ref landed locally after fetch from a polluted remote: %v", pins)
	}
}

// TestImportFromRemotePrunesForeignPushPins is the clone/import-path
// counterpart of TestFetchPrunesImportedForeignPushPins (review nit (a)):
// importer.go's fetchRemote (the clone/import path) also uses a
// "+refs/cairn/*:refs/cairn/*" refspec and must get the same
// pruneImportedPushPins defense, not just fetchTracking.
func TestImportFromRemotePrunesForeignPushPins(t *testing.T) {
	skipOnWindows(t)
	origin := makeOriginRepo(t)

	// Pollute the origin directly with a foreign pin ref.
	originRepo, err := git.PlainOpen(origin)
	if err != nil {
		t.Fatalf("PlainOpen origin: %v", err)
	}
	head, err := originRepo.Head()
	if err != nil {
		t.Fatalf("origin Head: %v", err)
	}
	poison := plumbing.ReferenceName("refs/cairn/push/foreignop123456/heads/main")
	if err := originRepo.Storer.SetReference(plumbing.NewHashReference(poison, head.Hash())); err != nil {
		t.Fatalf("pollute origin: %v", err)
	}

	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	if _, err := e.ImportFromRemote(origin); err != nil {
		t.Fatalf("ImportFromRemote: %v", err)
	}

	if pins := pinnedPushRefs(t, e); len(pins) != 0 {
		t.Fatalf("foreign pin ref landed locally after importing from a polluted remote: %v", pins)
	}
}

// TestFetchDoesNotPruneOwnInFlightPushPins proves the fetch-side defense
// (#1b) is scoped to what THIS fetch imported, not "every refs/cairn/push/*
// ref that happens to exist": a same-repo, currently in-flight PreparePush's
// own pins (created before this fetch runs, never touching the remote) must
// survive a concurrent fetch untouched.
func TestFetchDoesNotPruneOwnInFlightPushPins(t *testing.T) {
	skipOnWindowsPush(t)
	bareDir := t.TempDir()
	if _, err := git.PlainInit(bareDir, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	e := seedEngineWithCommit(t, bareDir)
	if err := e.PushToRemote("origin", false); err != nil { // give the bare remote something to fetch
		t.Fatalf("seed push: %v", err)
	}

	pp, err := e.PreparePushToRemote("origin", false)
	if err != nil {
		t.Fatalf("PreparePush: %v", err)
	}
	before := pinnedPushRefs(t, e)
	if len(before) == 0 {
		t.Fatal("expected in-flight pins before the fetch")
	}

	if err := e.FetchTracking("origin"); err != nil {
		t.Fatalf("FetchTracking: %v", err)
	}

	after := pinnedPushRefs(t, e)
	if len(after) != len(before) {
		t.Fatalf("in-flight pins pruned by a concurrent fetch: before=%v after=%v", before, after)
	}

	if err := e.NetworkPush(pp); err != nil {
		t.Fatalf("NetworkPush: %v", err)
	}
	if err := e.FinishPush(pp); err != nil {
		t.Fatalf("FinishPush: %v", err)
	}
}

// TestFetchSurvivesPinCreatedDuringFetchNetworkPhase is the TOCTOU fix
// (security re-review): pruneImportedPushPins' before/after ref-name diff
// alone cannot tell "a foreign pin this fetch just imported from the remote"
// apart from "a pin a concurrent PreparePush on THIS clone created WHILE the
// fetch's network round-trip was in flight" — pins are created under
// wc.lock, a fetch runs under remote.lock, and nothing serializes the two.
// This simulates exactly that: a new local pin is created (via the fetch
// delay hook, positioned after fetchTracking's before-snapshot but before
// its rem.Fetch call — see the hook's placement in sync.go) DURING the
// fetch. Since that pin was never pushed anywhere, the remote never
// advertises it, so the fix's "only prune if also advertised by the remote"
// check must let it survive.
func TestFetchSurvivesPinCreatedDuringFetchNetworkPhase(t *testing.T) {
	skipOnWindowsPush(t)
	bareDir := t.TempDir()
	if _, err := git.PlainInit(bareDir, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	e := seedEngineWithCommit(t, bareDir)
	if err := e.PushToRemote("origin", false); err != nil { // give the bare remote something to fetch
		t.Fatalf("seed push: %v", err)
	}

	var racePP *PreparedPush
	restore := SetFetchDelayHook(func() {
		pp, err := e.PreparePushToRemote("origin", false)
		if err != nil {
			t.Errorf("race PreparePush: %v", err)
			return
		}
		racePP = pp
	})
	defer restore()

	if err := e.FetchTracking("origin"); err != nil {
		t.Fatalf("FetchTracking: %v", err)
	}
	if racePP == nil {
		t.Fatal("race PreparePush inside the fetch delay hook never ran")
	}

	survived := pinnedPushRefs(t, e)
	if len(survived) == 0 {
		t.Fatal("pin created during the fetch's network round-trip was pruned — TOCTOU regression: a legitimate concurrent pin was mistaken for an imported foreign one")
	}
	for _, p := range racePP.pins {
		found := false
		for _, s := range survived {
			if s == p.pinned.String() {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("race-created pin %s did not survive the fetch", p.pinned)
		}
	}

	if err := e.NetworkPush(racePP); err != nil {
		t.Fatalf("NetworkPush(racePP): %v", err)
	}
	if err := e.FinishPush(racePP); err != nil {
		t.Fatalf("FinishPush(racePP): %v", err)
	}
}

// TestVerifyPushAcceptsLegitimateConcurrentAdvance is the verifyPush
// false-negative fix (review must-fix #2): if the remote's ref advanced past
// our pinned SHA because ANOTHER process legitimately pushed further (a
// commit we have locally, descending from ours), that must be treated as
// success — not "our push didn't land".
func TestVerifyPushAcceptsLegitimateConcurrentAdvance(t *testing.T) {
	skipOnWindowsPush(t)
	bareDir := t.TempDir()
	if _, err := git.PlainInit(bareDir, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	e := seedEngineWithCommit(t, bareDir)
	if err := e.PushToRemote("origin", false); err != nil {
		t.Fatalf("seed push: %v", err)
	}
	main, _ := e.LineByName("main")
	aSHA := main.TipCommit

	// Advance main locally (our own object store now knows this descendant),
	// but do NOT push it — simulating another process having pushed it.
	ch, _ := e.OpenChangeForLine(main.ID)
	if _, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("b\n")}, nil, ""); err != nil {
		t.Fatalf("commit2: %v", err)
	}
	main2, _ := e.LineByName("main")
	bSHA := main2.TipCommit

	// Move the BARE remote's ref directly to the descendant — bypassing our
	// NetworkPush entirely, as if a concurrent pusher raced and won.
	bare, err := git.PlainOpen(bareDir)
	if err != nil {
		t.Fatalf("PlainOpen bare: %v", err)
	}
	if err := bare.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName("main"), plumbing.NewHash(bSHA))); err != nil {
		t.Fatalf("advance bare ref: %v", err)
	}

	rem, err := e.git.Remote("origin")
	if err != nil {
		t.Fatalf("Remote: %v", err)
	}
	pp := &PreparedPush{
		label:      "test",
		remoteName: "origin",
		pins: []pushPin{{
			target:   plumbing.NewBranchReferenceName("main"),
			expected: plumbing.NewHash(aSHA),
		}},
	}
	if err := e.verifyPush(pp, rem); err != nil {
		t.Fatalf("verifyPush rejected a legitimate concurrent fast-forward: %v", err)
	}
}

// TestVerifyPushRejectsUnrelatedDivergence is the counterpart to the above:
// when the remote's ref is a commit we DO have locally but it is NOT a
// descendant of our pinned SHA (unrelated history), verifyPush must still
// report a failure — the ancestor check is not a blanket "known locally ⇒
// success" pass.
func TestVerifyPushRejectsUnrelatedDivergence(t *testing.T) {
	skipOnWindowsPush(t)
	bareDir := t.TempDir()
	if _, err := git.PlainInit(bareDir, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	e := seedEngineWithCommit(t, bareDir)
	if err := e.PushToRemote("origin", false); err != nil {
		t.Fatalf("seed push: %v", err)
	}
	main, _ := e.LineByName("main")
	aSHA := main.TipCommit

	// Build a totally unrelated commit (no shared history) directly in e's
	// own object store, so it IS "known locally" but is not our descendant.
	treeSha, err := e.writeTree(map[string][]byte{"unrelated.txt": []byte("z\n")}, nil)
	if err != nil {
		t.Fatalf("writeTree: %v", err)
	}
	unrelatedSHA, err := e.writeCommit(treeSha.String(), "", "unrelated", nil)
	if err != nil {
		t.Fatalf("writeCommit: %v", err)
	}

	bare, err := git.PlainOpen(bareDir)
	if err != nil {
		t.Fatalf("PlainOpen bare: %v", err)
	}
	if err := bare.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName("main"), plumbing.NewHash(unrelatedSHA))); err != nil {
		t.Fatalf("advance bare ref: %v", err)
	}

	rem, err := e.git.Remote("origin")
	if err != nil {
		t.Fatalf("Remote: %v", err)
	}
	pp := &PreparedPush{
		label:      "test",
		remoteName: "origin",
		pins: []pushPin{{
			target:   plumbing.NewBranchReferenceName("main"),
			expected: plumbing.NewHash(aSHA),
		}},
	}
	err = e.verifyPush(pp, rem)
	if err == nil {
		t.Fatal("expected verifyPush to reject an unrelated remote advance, got nil")
	}
	if !strings.Contains(err.Error(), "refs/heads/main") {
		t.Fatalf("error does not name the offending ref: %v", err)
	}
	// Review must-fix (b): this "known locally, but diverged" case must read
	// distinctly from the "not known locally at all" case below — pre-fix,
	// both used the exact same "known locally, but not a descendant" text
	// regardless of whether the SHA was actually known locally.
	if !strings.Contains(err.Error(), "known locally, but not a descendant") {
		t.Fatalf("error does not read as the known-locally-but-diverged case: %v", err)
	}
	if strings.Contains(err.Error(), "not known locally") {
		t.Fatalf("error wrongly uses the not-known-locally wording for a commit we DO have: %v", err)
	}
}

// TestVerifyPushReportsUnknownRemoteAdvanceDistinctly is the counterpart
// exercising the OTHER branch of the same review must-fix (b): when the
// remote's ref advanced to a SHA we do not have in our local object store at
// all (so we genuinely cannot tell "another push raced ours" from "ours
// never landed"), the error text must be the "not known locally" wording —
// not the "known locally, but not a descendant" text, which pre-fix fired
// for this case too since it was keyed only on "does the ref exist on the
// remote", not "do we actually have this commit".
func TestVerifyPushReportsUnknownRemoteAdvanceDistinctly(t *testing.T) {
	skipOnWindowsPush(t)
	bareDir := t.TempDir()
	if _, err := git.PlainInit(bareDir, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	e := seedEngineWithCommit(t, bareDir)
	if err := e.PushToRemote("origin", false); err != nil {
		t.Fatalf("seed push: %v", err)
	}
	main, _ := e.LineByName("main")
	aSHA := main.TipCommit

	// A hash that is well-formed but corresponds to NO object e has ever
	// written — simulating "some other process pushed a commit we've never
	// seen (or fetched)".
	unknownSHA := plumbing.NewHash("cafebabecafebabecafebabecafebabecafebabe").String()

	bare, err := git.PlainOpen(bareDir)
	if err != nil {
		t.Fatalf("PlainOpen bare: %v", err)
	}
	if err := bare.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName("main"), plumbing.NewHash(unknownSHA))); err != nil {
		t.Fatalf("advance bare ref: %v", err)
	}

	rem, err := e.git.Remote("origin")
	if err != nil {
		t.Fatalf("Remote: %v", err)
	}
	pp := &PreparedPush{
		label:      "test",
		remoteName: "origin",
		pins: []pushPin{{
			target:   plumbing.NewBranchReferenceName("main"),
			expected: plumbing.NewHash(aSHA),
		}},
	}
	err = e.verifyPush(pp, rem)
	if err == nil {
		t.Fatal("expected verifyPush to reject an unknown remote advance, got nil")
	}
	if !strings.Contains(err.Error(), "refs/heads/main") {
		t.Fatalf("error does not name the offending ref: %v", err)
	}
	if !strings.Contains(err.Error(), "not known locally") {
		t.Fatalf("error does not read as the not-known-locally case: %v", err)
	}
	if strings.Contains(err.Error(), "known locally, but not a descendant") {
		t.Fatalf("error wrongly uses the known-locally wording for a SHA we've never seen: %v", err)
	}
}

// TestPinOutgoingRefsRollsBackOnPartialFailure is the partial-pin-rollback
// fix (review must-fix #3): if pinOutgoingRefs fails partway through (here,
// injected via the package-private testPinError test seam — there is no
// cheap real-world way to make one specific SetReference call fail
// deterministically), every pin already created in that call is removed
// before the error is returned. Pre-fix, refs 1..k-1 would stay pinned
// forever: PreparePush returns nil on error, so its caller never gets a
// PreparedPush to hand to FinishPush.
func TestPinOutgoingRefsRollsBackOnPartialFailure(t *testing.T) {
	skipOnWindowsPush(t)
	bareDir := t.TempDir()
	if _, err := git.PlainInit(bareDir, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	// Two lines/refs to pin, so the injected failure on the SECOND one
	// exercises rolling back the FIRST one's already-created pin.
	main, _ := e.LineByName("main")
	ch, _ := e.CreateChange(main.ID, "a")
	if _, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("a\n")}, nil, ""); err != nil {
		t.Fatalf("commit main: %v", err)
	}
	if _, err := e.CreateLine("feat", main.ID); err != nil {
		t.Fatalf("CreateLine: %v", err)
	}
	feat, _ := e.LineByName("feat")
	fch, _ := e.CreateChange(feat.ID, "a")
	if _, err := e.Commit(fch.ID, map[string][]byte{"f.txt": []byte("f\n")}, nil, ""); err != nil {
		t.Fatalf("commit feat: %v", err)
	}
	if err := e.AddRemote("origin", bareDir, "git"); err != nil {
		t.Fatalf("AddRemote: %v", err)
	}

	calls := 0
	testPinError = func(pinned plumbing.ReferenceName) error {
		calls++
		if calls == 2 {
			return fmt.Errorf("injected failure")
		}
		return nil
	}
	t.Cleanup(func() { testPinError = nil })

	_, err = e.PreparePushToRemote("origin", false)
	if err == nil {
		t.Fatal("expected PreparePush to fail via the injected pin error")
	}
	if !strings.Contains(err.Error(), "injected failure") {
		t.Fatalf("unexpected error: %v", err)
	}

	if pins := pinnedPushRefs(t, e); len(pins) != 0 {
		t.Fatalf("partial pins leaked after PreparePush failed mid-loop: %v", pins)
	}
}
