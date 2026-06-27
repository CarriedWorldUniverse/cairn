package repo

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// setHead points refs/heads/<branch> in barePath at h — simulating what the
// disclose re-push delivers to the public bare (the real tip re-enters public).
func setHead(t *testing.T, barePath, branch string, h plumbing.Hash) {
	t.Helper()
	g, err := git.PlainOpen(barePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/"+branch), h)); err != nil {
		t.Fatal(err)
	}
}

func skipNoGit(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("server git shell-out is not exercised on Windows CI")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("system git not available")
	}
}

// relocateOneEmbargo stages an embargo head ref for `branch` (body → a distinct
// commit) into the public bare, then relocates it into the embargo bare — leaving
// the embargo bare holding refs/cairn/embargo/heads/<branch> and the public bare
// with no ref reaching it (the still-embargoed state). Returns the commit sha.
func relocateOneEmbargo(t *testing.T, svc *Service, repoID, branch, body string) plumbing.Hash {
	t.Helper()
	pub := svc.storagePath(repoID)
	refName := change.EmbargoRefPrefix + "heads/" + branch
	sha := stageEmbargoRef(t, pub, refName, body)
	if _, err := svc.RelocateEmbargoRefs(context.Background(), repoID); err != nil {
		t.Fatalf("RelocateEmbargoRefs: %v", err)
	}
	return sha
}

// TestPruneDisclosedEmbargo_RetainsStillEmbargoed: a branch whose tip is NOT
// reachable from any public head stays gated (no premature un-gate), even though
// the object physically lingers (dangling) in the public bare after relocation.
func TestPruneDisclosedEmbargo_RetainsStillEmbargoed(t *testing.T) {
	skipNoGit(t)
	svc := newTestService(t)
	ctx := context.Background()
	r, err := svc.CreateRepo(ctx, "org-1", "widgets")
	if err != nil {
		t.Fatal(err)
	}
	embSHA := relocateOneEmbargo(t, svc, r.ID, "main", "GATED_FIX\n")

	// Public has an UNRELATED head (so pubTips is non-empty) that does not reach the
	// embargoed commit — the prune must keep the gate.
	unrelated := stageEmbargoRef(t, svc.storagePath(r.ID), "refs/heads/other", "unrelated\n")
	setHead(t, svc.storagePath(r.ID), "other", unrelated)

	d, err := svc.PruneDisclosedEmbargo(ctx, r.ID)
	if err != nil {
		t.Fatalf("PruneDisclosedEmbargo: %v", err)
	}
	if d != 0 {
		t.Fatalf("disclosed %d, want 0 (still embargoed)", d)
	}
	// The gated ref is intact and a recipient is still served the embargo bare.
	emb, _ := git.PlainOpen(svc.EmbargoStoragePath(r.ID))
	if _, err := emb.Reference(plumbing.ReferenceName(change.EmbargoRefPrefix+"heads/main"), false); err != nil {
		t.Errorf("gated embargo ref was wrongly removed: %v", err)
	}
	if err := svc.GrantEmbargoRecipient(ctx, r.ID, "agent-7", "ops"); err != nil {
		t.Fatal(err)
	}
	if got := svc.BareForServe(ctx, r.ID, "agent-7", "git-upload-pack"); got != svc.EmbargoStoragePath(r.ID) {
		t.Errorf("recipient served %s, want embargo bare (still gated)", got)
	}
	_ = embSHA
}

// TestPruneDisclosedEmbargo_DisclosesReachable: once a branch's tip re-enters the
// public projection, its gated ref is renamed to a normal head in the embargo bare
// and dropped from the gated set, flipping BareForServe to the public bare.
func TestPruneDisclosedEmbargo_DisclosesReachable(t *testing.T) {
	skipNoGit(t)
	svc := newTestService(t)
	ctx := context.Background()
	r, err := svc.CreateRepo(ctx, "org-1", "widgets")
	if err != nil {
		t.Fatal(err)
	}
	embSHA := relocateOneEmbargo(t, svc, r.ID, "main", "DISCLOSED_FIX\n")

	// Simulate the disclose re-push: the real tip re-enters the public bare.
	setHead(t, svc.storagePath(r.ID), "main", embSHA)

	d, err := svc.PruneDisclosedEmbargo(ctx, r.ID)
	if err != nil {
		t.Fatalf("PruneDisclosedEmbargo: %v", err)
	}
	if d != 1 {
		t.Fatalf("disclosed %d, want 1", d)
	}
	emb, _ := git.PlainOpen(svc.EmbargoStoragePath(r.ID))
	// Gated ref gone; the disclosed branch survives as a normal head (recipient
	// visibility) pointing at the same tip.
	if _, err := emb.Reference(plumbing.ReferenceName(change.EmbargoRefPrefix+"heads/main"), false); err == nil {
		t.Error("gated embargo ref should be gone after disclose")
	}
	h, err := emb.Reference("refs/heads/main", false)
	if err != nil || h.Hash() != embSHA {
		t.Fatalf("disclosed branch not retained as normal head: ref=%v err=%v want %s", h, err, embSHA)
	}
	// Gate flips: a former recipient is now served the public bare.
	if err := svc.GrantEmbargoRecipient(ctx, r.ID, "agent-7", "ops"); err != nil {
		t.Fatal(err)
	}
	if got := svc.BareForServe(ctx, r.ID, "agent-7", "git-upload-pack"); got != svc.storagePath(r.ID) {
		t.Errorf("recipient served %s, want public bare (fully disclosed)", got)
	}
}

// TestPruneDisclosedEmbargo_Partial: with two embargoed branches, disclosing one
// retires only that branch; the other stays gated and the gate keeps serving the
// embargo bare — where the disclosed branch is still visible as a normal head.
func TestPruneDisclosedEmbargo_Partial(t *testing.T) {
	skipNoGit(t)
	svc := newTestService(t)
	ctx := context.Background()
	r, err := svc.CreateRepo(ctx, "org-1", "widgets")
	if err != nil {
		t.Fatal(err)
	}
	caSHA := relocateOneEmbargo(t, svc, r.ID, "A", "branch-A-fix\n")
	cbSHA := relocateOneEmbargo(t, svc, r.ID, "B", "branch-B-fix\n")

	// Disclose only branch A.
	setHead(t, svc.storagePath(r.ID), "A", caSHA)

	d, err := svc.PruneDisclosedEmbargo(ctx, r.ID)
	if err != nil {
		t.Fatalf("PruneDisclosedEmbargo: %v", err)
	}
	if d != 1 {
		t.Fatalf("disclosed %d, want 1 (only A)", d)
	}
	emb, _ := git.PlainOpen(svc.EmbargoStoragePath(r.ID))
	// A: visible normal head; B: still gated.
	if h, err := emb.Reference("refs/heads/A", false); err != nil || h.Hash() != caSHA {
		t.Errorf("branch A not visible as normal head: %v (err %v)", h, err)
	}
	if h, err := emb.Reference(plumbing.ReferenceName(change.EmbargoRefPrefix+"heads/B"), false); err != nil || h.Hash() != cbSHA {
		t.Errorf("branch B should still be gated: %v (err %v)", h, err)
	}
	// Gate still serves embargo (B gated).
	if err := svc.GrantEmbargoRecipient(ctx, r.ID, "agent-7", "ops"); err != nil {
		t.Fatal(err)
	}
	if got := svc.BareForServe(ctx, r.ID, "agent-7", "git-upload-pack"); got != svc.EmbargoStoragePath(r.ID) {
		t.Errorf("recipient served %s, want embargo bare (B still gated)", got)
	}
}

// TestPruneDisclosedEmbargo_NoBare: a plain repo (no embargo bare) is a clean
// no-op.
func TestPruneDisclosedEmbargo_NoBare(t *testing.T) {
	skipNoGit(t)
	svc := newTestService(t)
	ctx := context.Background()
	r, err := svc.CreateRepo(ctx, "org-1", "widgets")
	if err != nil {
		t.Fatal(err)
	}
	if d, err := svc.PruneDisclosedEmbargo(ctx, r.ID); err != nil || d != 0 {
		t.Fatalf("PruneDisclosedEmbargo on plain repo = (%d,%v), want (0,nil)", d, err)
	}
}

// TestGCRepo_ReapsFullyDisclosed: after full disclosure (no gated heads), gc reaps
// the redundant embargo bare directory.
func TestGCRepo_ReapsFullyDisclosed(t *testing.T) {
	skipNoGit(t)
	svc := newTestService(t)
	ctx := context.Background()
	r, err := svc.CreateRepo(ctx, "org-1", "widgets")
	if err != nil {
		t.Fatal(err)
	}
	embSHA := relocateOneEmbargo(t, svc, r.ID, "main", "FIX\n")
	setHead(t, svc.storagePath(r.ID), "main", embSHA)
	if _, err := svc.PruneDisclosedEmbargo(ctx, r.ID); err != nil {
		t.Fatal(err)
	}

	reaped, err := svc.GCRepo(ctx, r.ID, true)
	if err != nil {
		t.Fatalf("GCRepo: %v", err)
	}
	if !reaped {
		t.Fatal("expected the fully-disclosed embargo bare to be reaped")
	}
	if _, err := os.Stat(svc.EmbargoStoragePath(r.ID)); !os.IsNotExist(err) {
		t.Errorf("embargo bare still present after reap: %v", err)
	}
}

// TestGCRepo_PublicGCSpareEmbargo: gc on the public bare (even --prune=now) cannot
// harm the self-sufficient embargo bare — its gated ref and object survive intact.
func TestGCRepo_PublicGCSpareEmbargo(t *testing.T) {
	skipNoGit(t)
	svc := newTestService(t)
	ctx := context.Background()
	r, err := svc.CreateRepo(ctx, "org-1", "widgets")
	if err != nil {
		t.Fatal(err)
	}
	cbSHA := relocateOneEmbargo(t, svc, r.ID, "B", "still-gated\n")

	// Embargo bare is still gated (no disclosure), so gc must NOT reap it.
	reaped, err := svc.GCRepo(ctx, r.ID, true)
	if err != nil {
		t.Fatalf("GCRepo: %v", err)
	}
	if reaped {
		t.Fatal("a still-gated embargo bare must not be reaped")
	}
	emb, err := git.PlainOpen(svc.EmbargoStoragePath(r.ID))
	if err != nil {
		t.Fatalf("embargo bare gone: %v", err)
	}
	ref, err := emb.Reference(plumbing.ReferenceName(change.EmbargoRefPrefix+"heads/B"), false)
	if err != nil || ref.Hash() != cbSHA {
		t.Fatalf("gated ref lost after public gc: %v (err %v)", ref, err)
	}
	if _, err := emb.CommitObject(cbSHA); err != nil {
		t.Fatalf("embargo object lost after public gc: %v", err)
	}
}
