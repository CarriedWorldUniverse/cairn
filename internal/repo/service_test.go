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

func TestDeleteRepo_RemovesDependentRows(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	r, err := svc.CreateRepo(ctx, "org-1", "to-be-wiped")
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	// Insert a push_event via the service method.
	if err := svc.RecordPush(ctx, PushEvent{
		RepoID:        r.ID,
		Ref:           "refs/heads/main",
		OldSHA:        "0000000000000000000000000000000000000000",
		NewSHA:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		PusherAgentID: "agent-1",
		Forced:        false,
	}); err != nil {
		t.Fatalf("RecordPush: %v", err)
	}

	// Insert a pull_request via the service method.
	pr := &Pull{
		RepoID:         r.ID,
		Source:         "feature",
		Target:         "main",
		Title:          "Test PR",
		LedgerIssueKey: "NEX-999",
		OpenedBy:       "agent-1",
	}
	if err := svc.CreatePull(ctx, pr); err != nil {
		t.Fatalf("CreatePull: %v", err)
	}

	// Insert a pull_check via the service method.
	if err := svc.RecordPullCheck(ctx, &PullCheck{
		PullID: pr.ID, Name: "ci", State: CheckStatePass, RecordedBy: "agent-1",
	}); err != nil {
		t.Fatalf("RecordPullCheck: %v", err)
	}

	// Disable FK enforcement on this connection before deleting: schema.sql
	// also declares ON DELETE CASCADE on pull_check/pull_request/push_event,
	// which — when the pool happens to reuse a connection with PRAGMA
	// foreign_keys=ON — would silently clean up dependents even if
	// DeleteRepo's own explicit DELETEs were removed, masking a regression.
	// Turning FK off here forces this test to depend solely on DeleteRepo's
	// own explicit statements, per the "cascade is unreliable across pool
	// connections" comment on DeleteRepo itself.
	if _, err := svc.db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatalf("PRAGMA foreign_keys=OFF: %v", err)
	}

	// Delete the repo.
	if err := svc.DeleteRepo(ctx, r.ID); err != nil {
		t.Fatalf("DeleteRepo: %v", err)
	}

	// Assert no push_event rows remain for r.ID.
	var pushCount int
	if err := svc.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM push_event WHERE repo_id=?`, r.ID,
	).Scan(&pushCount); err != nil {
		t.Fatalf("count push_event: %v", err)
	}
	if pushCount != 0 {
		t.Fatalf("push_event rows remaining: got %d, want 0", pushCount)
	}

	// Assert no pull_request rows remain for r.ID.
	var prCount int
	if err := svc.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pull_request WHERE repo_id=?`, r.ID,
	).Scan(&prCount); err != nil {
		t.Fatalf("count pull_request: %v", err)
	}
	if prCount != 0 {
		t.Fatalf("pull_request rows remaining: got %d, want 0", prCount)
	}

	// Assert no pull_check rows remain for pr.ID.
	var checkCount int
	if err := svc.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pull_check WHERE pull_id=?`, pr.ID,
	).Scan(&checkCount); err != nil {
		t.Fatalf("count pull_check: %v", err)
	}
	if checkCount != 0 {
		t.Fatalf("pull_check rows remaining: got %d, want 0", checkCount)
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
