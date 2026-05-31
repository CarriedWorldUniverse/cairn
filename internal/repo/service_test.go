package repo

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// newTestService gives each test an isolated on-disk store + repo root.
func newTestService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	svc, err := Open(filepath.Join(dir, "cairn.db"), filepath.Join(dir, "repos"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

// TestCreateRepoHEADMatchesDefaultBranch guards the fix for the clone-checks-
// out-nothing bug: a freshly created bare repo's symbolic HEAD must point at the
// declared default branch ("main"), not go-git's PlainInit default ("master").
// Otherwise a client that pushes "main" leaves HEAD dangling and `git clone`
// checks out an unborn branch. (Caught by the cwb-conformance cairn layer.)
func TestCreateRepoHEADMatchesDefaultBranch(t *testing.T) {
	svc := newTestService(t)
	r, err := svc.CreateRepo(context.Background(), "org-1", "headcheck")
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	g, err := git.PlainOpen(r.StoragePath)
	if err != nil {
		t.Fatalf("PlainOpen: %v", err)
	}
	head, err := g.Reference(plumbing.HEAD, false) // false: do NOT resolve — want the symref target
	if err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	want := plumbing.NewBranchReferenceName(r.DefaultBranch)
	if head.Target() != want {
		t.Fatalf("HEAD -> %q, want %q", head.Target(), want)
	}
}

func TestCreateGetListRepo(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	r, err := svc.CreateRepo(ctx, "org-1", "widgets")
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if r.ID == "" || r.DefaultBranch != "main" {
		t.Fatalf("unexpected repo: %+v", r)
	}

	got, err := svc.GetRepo(ctx, "org-1", "widgets")
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	if got.ID != r.ID {
		t.Fatalf("GetRepo id mismatch: %s != %s", got.ID, r.ID)
	}

	list, err := svc.ListRepos(ctx, "org-1")
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListRepos len = %d, want 1", len(list))
	}

	// Duplicate slug in the same org must fail (UNIQUE(org_id, slug)).
	if _, err := svc.CreateRepo(ctx, "org-1", "widgets"); err == nil {
		t.Fatal("CreateRepo duplicate: want error, got nil")
	}
}

func TestListRefsEmptyThenSeeded(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	r, err := svc.CreateRepo(ctx, "org-1", "widgets")
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	// A fresh bare repo has no refs.
	refs, err := svc.ListRefs(ctx, r.ID)
	if err != nil {
		t.Fatalf("ListRefs: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("fresh repo refs = %d, want 0", len(refs))
	}

	// Seed one commit on main via the test helper; then a single head appears.
	sha := seedCommit(t, svc, r.ID, "main", "hello")
	refs, err = svc.ListRefs(ctx, r.ID)
	if err != nil {
		t.Fatalf("ListRefs after seed: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("seeded refs = %d, want 1", len(refs))
	}
	if refs[0].Name != "refs/heads/main" || refs[0].Hash != sha {
		t.Fatalf("unexpected ref: %+v (want refs/heads/main @ %s)", refs[0], sha)
	}

	// GetRef resolves the same head.
	got, err := svc.GetRef(ctx, r.ID, "refs/heads/main")
	if err != nil {
		t.Fatalf("GetRef: %v", err)
	}
	if got.Hash != sha {
		t.Fatalf("GetRef hash = %s, want %s", got.Hash, sha)
	}
}

func TestDeleteRepoRemovesStorage(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	r, err := svc.CreateRepo(ctx, "org-1", "gone")
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if err := svc.DeleteRepo(ctx, r.ID); err != nil {
		t.Fatalf("DeleteRepo: %v", err)
	}
	if _, err := svc.GetRepoByID(ctx, r.ID); err == nil {
		t.Fatal("GetRepoByID after delete: want error")
	}
}

func TestRecordPush(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	r, err := svc.CreateRepo(ctx, "org-1", "audited")
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	err = svc.RecordPush(ctx, PushEvent{
		RepoID: r.ID, Ref: "refs/heads/main",
		OldSHA:        "0000000000000000000000000000000000000000",
		NewSHA:        "1111111111111111111111111111111111111111",
		PusherAgentID: "agent-7", Forced: false,
	})
	if err != nil {
		t.Fatalf("RecordPush: %v", err)
	}
}

func TestCreateRepoRunsHookInstaller(t *testing.T) {
	svc := newTestService(t)
	var gotID, gotDir string
	svc.SetHookInstaller(func(id, dir string) error { gotID, gotDir = id, dir; return nil })
	r, err := svc.CreateRepo(context.Background(), "org-1", "hooked")
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if gotID != r.ID || gotDir == "" {
		t.Fatalf("installer not called correctly: id=%s dir=%s", gotID, gotDir)
	}
}
