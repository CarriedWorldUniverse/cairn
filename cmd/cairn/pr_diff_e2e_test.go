package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/cairn/internal/grpcapi"
	ledgerclient "github.com/CarriedWorldUniverse/cairn/internal/ledger"
	"github.com/CarriedWorldUniverse/cairn/internal/repo"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"google.golang.org/grpc"
)

// fakePRLedger is a no-op grpcapi.IssueCreator: `pr diff` never touches the
// ledger (only open/merge do), so every call here is unexpected.
type fakePRLedger struct{}

func (fakePRLedger) CreateIssue(context.Context, http.Header, ledgerclient.IssueInput) (ledgerclient.IssueResult, error) {
	return ledgerclient.IssueResult{Key: "WID-1"}, nil
}
func (fakePRLedger) CommentIssue(context.Context, http.Header, string, string) error { return nil }

// startPRDiffServer stands up cairn-server's gRPC PullService on a real TCP
// listener (not bufconn — `pr diff` dials a --server address like production)
// and seeds one repo with the given branches (each an empty seed commit, no
// content — `pr diff` only needs the branches to EXIST server-side; the real
// diff content comes from the git remote via --remote, a separate address
// scheme by design, see prUsage).
func startPRDiffServer(t *testing.T, org, slug string, branches ...string) string {
	t.Helper()
	dir := t.TempDir()
	core, err := repo.Open(filepath.Join(dir, "cairn.db"), filepath.Join(dir, "repos"))
	if err != nil {
		t.Fatalf("repo.Open: %v", err)
	}
	t.Cleanup(func() { _ = core.Close() })

	rp, err := core.CreateRepo(context.Background(), org, slug)
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	for _, b := range branches {
		seedServerRef(t, core, rp.ID, "refs/heads/"+b)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	g := grpc.NewServer()
	grpcapi.New(core, fakePRLedger{}, "").Register(g)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)
	return lis.Addr().String()
}

// seedServerRef writes an empty seed commit directly at refName in the
// server-side repo's git storage — mirrors internal/grpcapi's own test helper
// (mustSeedRef), reimplemented here since it's unexported.
func seedServerRef(t *testing.T, core *repo.Service, repoID, refName string) {
	t.Helper()
	path, err := core.StoragePathForID(context.Background(), repoID)
	if err != nil {
		t.Fatalf("StoragePathForID: %v", err)
	}
	g, err := git.PlainOpen(path)
	if err != nil {
		t.Fatalf("PlainOpen: %v", err)
	}
	st := g.Storer
	commit := &object.Commit{
		Author:    object.Signature{Name: "t", Email: "t@t", When: time.Unix(0, 0).UTC()},
		Committer: object.Signature{Name: "t", Email: "t@t", When: time.Unix(0, 0).UTC()},
		Message:   "seed " + refName,
		TreeHash:  plumbing.ZeroHash,
	}
	enc := st.NewEncodedObject()
	if err := commit.Encode(enc); err != nil {
		t.Fatalf("encode commit: %v", err)
	}
	h, err := st.SetEncodedObject(enc)
	if err != nil {
		t.Fatalf("set object: %v", err)
	}
	if err := st.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(refName), h)); err != nil {
		t.Fatalf("set ref: %v", err)
	}
}

// makeBareRepoWithBranches builds a bare git repo (a real remote `pr diff`
// fetches) with two branches: base at content[0], and a second branch tip
// that adds one commit on top (content[1]) — a real, diffable divergence.
// Both branches share the base commit so the diff is a clean, small hunk.
func makeBareRepoWithBranches(t *testing.T, baseBranch, tipBranch string, baseContent, addedContent string) string {
	t.Helper()
	bare := t.TempDir()
	if _, err := git.PlainInit(bare, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	work := t.TempDir()
	wrepo, err := git.PlainInit(work, false)
	if err != nil {
		t.Fatalf("PlainInit work: %v", err)
	}
	wt, err := wrepo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	sig := &object.Signature{Name: "o", Email: "o@x", When: time.Unix(0, 0).UTC()}

	// Point HEAD at baseBranch BEFORE the first commit, so it lands directly
	// on baseBranch (not go-git's default "master").
	if err := wrepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName(baseBranch))); err != nil {
		t.Fatalf("point HEAD at %s: %v", baseBranch, err)
	}
	if err := os.WriteFile(filepath.Join(work, "shared.txt"), []byte(baseContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("shared.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("base", &git.CommitOptions{Author: sig}); err != nil {
		t.Fatalf("commit base: %v", err)
	}
	head, err := wrepo.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	baseHash := head.Hash()

	// tipBranch forks from the same base commit and adds one more commit.
	if err := wrepo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName(tipBranch), baseHash)); err != nil {
		t.Fatalf("set %s ref: %v", tipBranch, err)
	}
	if err := wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(tipBranch)}); err != nil {
		t.Fatalf("checkout %s: %v", tipBranch, err)
	}
	if err := os.WriteFile(filepath.Join(work, "shared.txt"), []byte(addedContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("shared.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("tip", &git.CommitOptions{Author: sig}); err != nil {
		t.Fatalf("commit tip: %v", err)
	}

	if _, err := wrepo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{bare}}); err != nil {
		t.Fatalf("CreateRemote: %v", err)
	}
	if err := wrepo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{
		config.RefSpec("refs/heads/*:refs/heads/*"),
	}}); err != nil {
		t.Fatalf("push seed: %v", err)
	}
	return bare
}

// makeBareRepoWithDivergedTarget builds a bare repo where targetBranch
// advances with an UNRELATED commit AFTER sourceBranch forks from it — the
// shape that exposes the difference between a tip-to-tip diff and a
// merge-base (target...source) diff: target's post-fork commit must never
// appear in a target...source diff, but WOULD appear (as a spurious deletion
// hunk) in a naive tip-to-tip diff, since source's tree simply lacks whatever
// file target gained after the fork.
func makeBareRepoWithDivergedTarget(t *testing.T, targetBranch, sourceBranch string) string {
	t.Helper()
	bare := t.TempDir()
	if _, err := git.PlainInit(bare, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	work := t.TempDir()
	wrepo, err := git.PlainInit(work, false)
	if err != nil {
		t.Fatalf("PlainInit work: %v", err)
	}
	wt, err := wrepo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	sig := &object.Signature{Name: "o", Email: "o@x", When: time.Unix(0, 0).UTC()}

	if err := wrepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName(targetBranch))); err != nil {
		t.Fatalf("point HEAD at %s: %v", targetBranch, err)
	}
	if err := os.WriteFile(filepath.Join(work, "shared.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("shared.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("base", &git.CommitOptions{Author: sig}); err != nil {
		t.Fatalf("commit base: %v", err)
	}
	head, err := wrepo.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	baseHash := head.Hash()

	// sourceBranch forks from base and adds its OWN change.
	if err := wrepo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName(sourceBranch), baseHash)); err != nil {
		t.Fatalf("set %s ref: %v", sourceBranch, err)
	}
	if err := wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(sourceBranch)}); err != nil {
		t.Fatalf("checkout %s: %v", sourceBranch, err)
	}
	if err := os.WriteFile(filepath.Join(work, "shared.txt"), []byte("one\ntwo\nFEATURE_CHANGE\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("shared.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("source work", &git.CommitOptions{Author: sig}); err != nil {
		t.Fatalf("commit source: %v", err)
	}

	// targetBranch advances AFTER the fork with a commit source never saw.
	if err := wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(targetBranch)}); err != nil {
		t.Fatalf("checkout %s: %v", targetBranch, err)
	}
	if err := os.WriteFile(filepath.Join(work, "target-only.txt"), []byte("MAIN_ONLY_CONTENT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("target-only.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("target advances", &git.CommitOptions{Author: sig}); err != nil {
		t.Fatalf("commit target advance: %v", err)
	}

	if _, err := wrepo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{bare}}); err != nil {
		t.Fatalf("CreateRemote: %v", err)
	}
	if err := wrepo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{
		config.RefSpec("refs/heads/*:refs/heads/*"),
	}}); err != nil {
		t.Fatalf("push seed: %v", err)
	}
	return bare
}

// TestE2E_PRDiff_MergeBaseExcludesTargetOnlyChanges asserts `pr diff` uses
// target...source (merge-base) semantics: when target advances with a commit
// source never saw (AFTER source forked from it), that unrelated change must
// NOT appear in the diff.
//
// Checked against the OLD tip-to-tip behavior: with cmdPRDiff calling
// DiffCommits(targetRef, sourceRef) instead of DiffMergeBase, this test
// FAILS — "target-only.txt"/"MAIN_ONLY_CONTENT" leaks into the output as a
// spurious deletion hunk, because a literal tree diff of target's tip against
// source's tip sees target-only.txt as present in target and absent in
// source, with no way to know it was never source's to begin with.
func TestE2E_PRDiff_MergeBaseExcludesTargetOnlyChanges(t *testing.T) {
	skipOnWindows(t)
	bare := makeBareRepoWithDivergedTarget(t, "main", "feature")
	addr := startPRDiffServer(t, "org-1", "widgets", "main", "feature")

	dir := filepath.Join(t.TempDir(), "work")
	mustRun(t, "clone", bare, dir)
	mustRun(t, "remote", "add", "--repo", dir, "origin", bare)
	mustRun(t, "fetch", "--repo", dir, "origin")

	opened := mustRunOut(t, "pr", "open", "feature", "main", "-m", "Add feature", "--project", "WID",
		"--org", "org-1", "--repo-slug", "widgets", "--server", addr, "--insecure", "--subject", "agent-1")
	id := firstField(t, opened)

	out := mustRunOut(t, "pr", "diff", id,
		"--repo", dir, "--org", "org-1", "--repo-slug", "widgets", "--server", addr, "--insecure", "--subject", "agent-1")

	if !strings.Contains(out, "FEATURE_CHANGE") {
		t.Fatalf("pr diff missing source's own change; got:\n%s", out)
	}
	if strings.Contains(out, "MAIN_ONLY_CONTENT") || strings.Contains(out, "target-only.txt") {
		t.Fatalf("pr diff leaked target's post-fork-only change as a spurious hunk (tip-to-tip, not merge-base); got:\n%s", out)
	}
}

// TestE2E_PRDiff_NoCommonAncestor asserts a clear error (not a crash or an
// empty/nonsensical diff) when target and source share no history.
func TestE2E_PRDiff_NoCommonAncestor(t *testing.T) {
	skipOnWindows(t)
	bare := t.TempDir()
	if _, err := git.PlainInit(bare, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	sig := &object.Signature{Name: "o", Email: "o@x", When: time.Unix(0, 0).UTC()}

	pushOrphanBranch := func(branch, content string) {
		work := t.TempDir()
		wrepo, err := git.PlainInit(work, false)
		if err != nil {
			t.Fatalf("PlainInit work: %v", err)
		}
		wt, err := wrepo.Worktree()
		if err != nil {
			t.Fatalf("Worktree: %v", err)
		}
		if err := wrepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName(branch))); err != nil {
			t.Fatalf("point HEAD at %s: %v", branch, err)
		}
		if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.Add("f.txt"); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.Commit(branch, &git.CommitOptions{Author: sig}); err != nil {
			t.Fatalf("commit %s: %v", branch, err)
		}
		if _, err := wrepo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{bare}}); err != nil {
			t.Fatalf("CreateRemote: %v", err)
		}
		if err := wrepo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{
			config.RefSpec("refs/heads/*:refs/heads/*"),
		}}); err != nil {
			t.Fatalf("push %s: %v", branch, err)
		}
	}
	// Two independent, unrelated commit histories on separate branches — no
	// shared ancestor.
	pushOrphanBranch("main", "main root\n")
	pushOrphanBranch("orphan", "orphan root\n")

	addr := startPRDiffServer(t, "org-1", "widgets", "main", "orphan")

	dir := filepath.Join(t.TempDir(), "work")
	mustRun(t, "clone", bare, dir)
	mustRun(t, "remote", "add", "--repo", dir, "origin", bare)
	mustRun(t, "fetch", "--repo", dir, "origin")

	opened := mustRunOut(t, "pr", "open", "orphan", "main", "-m", "Add orphan", "--project", "WID",
		"--org", "org-1", "--repo-slug", "widgets", "--server", addr, "--insecure", "--subject", "agent-1")
	id := firstField(t, opened)

	err := run([]string{"pr", "diff", id,
		"--repo", dir, "--org", "org-1", "--repo-slug", "widgets", "--server", addr, "--insecure", "--subject", "agent-1"})
	if err == nil {
		t.Fatal("pr diff of branches with no common ancestor: want error, got nil")
	}
	if !strings.Contains(err.Error(), "no common history") {
		t.Fatalf("pr diff no-common-ancestor error = %q, want it to mention \"no common history\"", err.Error())
	}
}

// TestE2E_PRDiff_DeletedSourceBranch asserts a clear error (not a silent,
// stale diff) when the PR's source branch has been deleted on the remote
// since it was last fetched — the pruned fetch in cmdPRDiff must drop the
// now-orphaned tracking ref rather than leave it resolving to its last-known
// tip.
func TestE2E_PRDiff_DeletedSourceBranch(t *testing.T) {
	skipOnWindows(t)
	bare := makeBareRepoWithBranches(t, "main", "feature", "x\n", "y\n")
	addr := startPRDiffServer(t, "org-1", "widgets", "main", "feature")

	dir := filepath.Join(t.TempDir(), "work")
	mustRun(t, "clone", bare, dir)
	mustRun(t, "remote", "add", "--repo", dir, "origin", bare)
	mustRun(t, "fetch", "--repo", dir, "origin") // populates a tracking ref for feature

	opened := mustRunOut(t, "pr", "open", "feature", "main", "-m", "Add X", "--project", "WID",
		"--org", "org-1", "--repo-slug", "widgets", "--server", addr, "--insecure", "--subject", "agent-1")
	id := firstField(t, opened)

	bareRepo, err := git.PlainOpen(bare)
	if err != nil {
		t.Fatalf("PlainOpen bare: %v", err)
	}
	if err := bareRepo.Storer.RemoveReference(plumbing.NewBranchReferenceName("feature")); err != nil {
		t.Fatalf("delete feature on remote: %v", err)
	}

	err = run([]string{"pr", "diff", id,
		"--repo", dir, "--org", "org-1", "--repo-slug", "widgets", "--server", addr, "--insecure", "--subject", "agent-1"})
	if err == nil {
		t.Fatal("pr diff after remote branch deletion: want error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "not found") {
		t.Fatalf("pr diff deleted-branch error = %q, want it to mention \"not found\"", err.Error())
	}
}

// TestE2E_PRDiff_MatchesDirectDiff asserts `cairn pr diff` output contains the
// same hunk content as diffing the two tracking refs directly.
func TestE2E_PRDiff_MatchesDirectDiff(t *testing.T) {
	skipOnWindows(t)
	bare := makeBareRepoWithBranches(t, "main", "feature", "one\ntwo\nthree\n", "one\ntwo\nCHANGED\nthree\n")
	addr := startPRDiffServer(t, "org-1", "widgets", "main", "feature")

	dir := filepath.Join(t.TempDir(), "work")
	mustRun(t, "clone", bare, dir) // expresses "main", the default branch

	mustRun(t, "remote", "add", "--repo", dir, "origin", bare)
	mustRun(t, "fetch", "--repo", dir, "origin")

	opened := mustRunOut(t, "pr", "open", "feature", "main", "-m", "Add X", "--project", "WID",
		"--org", "org-1", "--repo-slug", "widgets", "--server", addr, "--insecure", "--subject", "agent-1")
	id := firstField(t, opened)

	out := mustRunOut(t, "pr", "diff", id,
		"--repo", dir, "--org", "org-1", "--repo-slug", "widgets", "--server", addr, "--insecure", "--subject", "agent-1")

	if !strings.Contains(out, "CHANGED") {
		t.Fatalf("pr diff output missing the changed line; got:\n%s", out)
	}
	if !strings.Contains(out, "-three") && !strings.Contains(out, "+CHANGED") {
		t.Fatalf("pr diff output does not look like a unified diff hunk; got:\n%s", out)
	}

	// The direct diff (worktree.DiffCommits on the same tracking refs) must
	// contain the same content — not just "non-empty".
	r, err := openRepo(dir, defaultAuthor())
	if err != nil {
		t.Fatalf("openRepo: %v", err)
	}
	defer r.Close()
	diffs, err := r.DiffCommits("refs/remotes/origin/main", "refs/remotes/origin/feature")
	if err != nil {
		t.Fatalf("direct DiffCommits: %v", err)
	}
	if len(diffs) != 1 || !strings.Contains(diffs[0].Unified, "CHANGED") {
		t.Fatalf("direct diff = %+v, want one file diff containing CHANGED", diffs)
	}
	if strings.TrimSpace(out) != strings.TrimSpace(diffs[0].Unified) {
		t.Fatalf("pr diff output != direct diff.\npr diff:\n%s\ndirect:\n%s", out, diffs[0].Unified)
	}
}

// TestE2E_PRDiff_NeverExpressedLocally asserts `pr diff` is correct even when
// NEITHER the source nor the target line was ever expressed in the local
// clone — `cairn init` (which expresses only its own default line, distinct
// from the PR's branches) + `remote add` + `fetch`, no `clone`/`express` of
// either PR branch at all.
func TestE2E_PRDiff_NeverExpressedLocally(t *testing.T) {
	skipOnWindows(t)
	bare := makeBareRepoWithBranches(t, "release", "topic", "alpha\nbeta\n", "alpha\nBETA-PRIME\nbeta\n")
	addr := startPRDiffServer(t, "org-1", "widgets", "release", "topic")

	dir := filepath.Join(t.TempDir(), "work")
	mustRun(t, "init", dir) // expresses its own root line ("main"), unrelated to release/topic
	mustRun(t, "remote", "add", "--repo", dir, "origin", bare)
	mustRun(t, "fetch", "--repo", dir, "origin") // tracking refs only — no reconcile, no express

	// Neither "release" nor "topic" has an expressed folder in dir.
	for _, b := range []string{"release", "topic"} {
		if _, err := os.Stat(filepath.Join(dir, b)); err == nil {
			t.Fatalf("branch %q must not be expressed locally for this test", b)
		}
	}

	opened := mustRunOut(t, "pr", "open", "topic", "release", "-m", "Add topic", "--project", "WID",
		"--org", "org-1", "--repo-slug", "widgets", "--server", addr, "--insecure", "--subject", "agent-1")
	id := firstField(t, opened)

	out := mustRunOut(t, "pr", "diff", id,
		"--repo", dir, "--org", "org-1", "--repo-slug", "widgets", "--server", addr, "--insecure", "--subject", "agent-1")

	if !strings.Contains(out, "BETA-PRIME") {
		t.Fatalf("pr diff (never-expressed lines) missing the changed line; got:\n%s", out)
	}
}

// TestE2E_PRDiff_UnknownID asserts a non-existent pull id fails clearly and
// non-zero, rather than printing an empty/garbage diff.
func TestE2E_PRDiff_UnknownID(t *testing.T) {
	skipOnWindows(t)
	bare := makeBareRepoWithBranches(t, "main", "feature", "x\n", "y\n")
	addr := startPRDiffServer(t, "org-1", "widgets", "main", "feature")

	dir := filepath.Join(t.TempDir(), "work")
	mustRun(t, "clone", bare, dir)
	mustRun(t, "remote", "add", "--repo", dir, "origin", bare)
	mustRun(t, "fetch", "--repo", dir, "origin")

	err := run([]string{"pr", "diff", "nope-does-not-exist",
		"--repo", dir, "--org", "org-1", "--repo-slug", "widgets", "--server", addr, "--insecure", "--subject", "agent-1"})
	if err == nil {
		t.Fatal("pr diff of an unknown id: want error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "not found") {
		t.Fatalf("pr diff unknown-id error = %q, want it to mention \"not found\"", err.Error())
	}
}

// firstField returns the first whitespace-separated field of s (the pull id,
// the first column of printPull's tab-separated line).
func firstField(t *testing.T, s string) string {
	t.Helper()
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) == 0 {
		t.Fatalf("expected at least one field in %q", s)
	}
	return fields[0]
}
