package grpcapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ledgerclient "github.com/CarriedWorldUniverse/cairn/internal/ledger"
	"github.com/CarriedWorldUniverse/cairn/internal/repo"
	cairnv1 "github.com/CarriedWorldUniverse/cwb-proto/gen/go/cwb/cairn/v1"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// fakeLedger records calls and returns scripted results.
type fakeLedger struct {
	calls        int
	gotFwd       http.Header
	gotIn        ledgerclient.IssueInput
	result       ledgerclient.IssueResult
	err          error
	commentCalls int
	commentErr   error
}

func (f *fakeLedger) CreateIssue(_ context.Context, fwd http.Header, in ledgerclient.IssueInput) (ledgerclient.IssueResult, error) {
	f.calls++
	f.gotFwd, f.gotIn = fwd, in
	return f.result, f.err
}
func (f *fakeLedger) CommentIssue(_ context.Context, _ http.Header, _, _ string) error {
	f.commentCalls++
	return f.commentErr
}

type clients struct {
	repo cairnv1.RepoServiceClient
	pull cairnv1.PullServiceClient
	org  cairnv1.OrgServiceClient
}

func newTest(t *testing.T, led IssueCreator) (clients, *repo.Service) {
	t.Helper()
	dir := t.TempDir()
	core, err := repo.Open(filepath.Join(dir, "cairn.db"), filepath.Join(dir, "repos"))
	if err != nil {
		t.Fatalf("repo.Open: %v", err)
	}
	t.Cleanup(func() { _ = core.Close() })

	lis := bufconn.Listen(1 << 20)
	g := grpc.NewServer()
	New(core, led, "").Register(g)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return clients{cairnv1.NewRepoServiceClient(conn), cairnv1.NewPullServiceClient(conn), cairnv1.NewOrgServiceClient(conn)}, core
}

// mdCtx builds an outgoing context carrying the gateway-style cwb-* identity.
func mdCtx(org, scopes string) context.Context {
	return metadata.NewOutgoingContext(context.Background(),
		metadata.Pairs("cwb-subject", "agent-1", "cwb-org", org, "cwb-scopes", scopes))
}

func code(err error) codes.Code { return status.Code(err) }

func seedRepoWithBranch(t *testing.T, core *repo.Service, org, slug, branch string) repo.Repo {
	t.Helper()
	r, err := core.CreateRepo(context.Background(), org, slug)
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	mustSeedRef(t, core, r.ID, "refs/heads/main")
	mustSeedRef(t, core, r.ID, "refs/heads/"+branch)
	return r
}

func mustSeedRef(t *testing.T, core *repo.Service, repoID, refName string) {
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

// --- CreateRepo + auth matrix ---

func TestCreateRepo(t *testing.T) {
	c, _ := newTest(t, &fakeLedger{})

	// happy
	resp, err := c.repo.CreateRepo(mdCtx("org-1", "repo:write"), &cairnv1.CreateRepoRequest{Org: "org-1", Slug: "widgets"})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if resp.Repo.GetSlug() != "widgets" || resp.Repo.GetOrg() != "org-1" || resp.Repo.GetDefaultBranch() == "" {
		t.Fatalf("unexpected repo: %+v", resp.Repo)
	}

	// missing identity -> Unauthenticated
	if _, err := c.repo.CreateRepo(context.Background(), &cairnv1.CreateRepoRequest{Org: "org-1", Slug: "x"}); code(err) != codes.Unauthenticated {
		t.Errorf("no-identity code = %v, want Unauthenticated", code(err))
	}
	// missing scope -> PermissionDenied
	if _, err := c.repo.CreateRepo(mdCtx("org-1", "repo:read"), &cairnv1.CreateRepoRequest{Org: "org-1", Slug: "x"}); code(err) != codes.PermissionDenied {
		t.Errorf("no-scope code = %v, want PermissionDenied", code(err))
	}
	// org mismatch -> PermissionDenied
	if _, err := c.repo.CreateRepo(mdCtx("org-1", "repo:write"), &cairnv1.CreateRepoRequest{Org: "org-2", Slug: "x"}); code(err) != codes.PermissionDenied {
		t.Errorf("org-mismatch code = %v, want PermissionDenied", code(err))
	}
	// missing slug -> InvalidArgument
	if _, err := c.repo.CreateRepo(mdCtx("org-1", "repo:write"), &cairnv1.CreateRepoRequest{Org: "org-1"}); code(err) != codes.InvalidArgument {
		t.Errorf("no-slug code = %v, want InvalidArgument", code(err))
	}
}

// --- OpenPull ---

func TestOpenPull(t *testing.T) {
	led := &fakeLedger{result: ledgerclient.IssueResult{Key: "WID-1"}}
	c, core := newTest(t, led)
	seedRepoWithBranch(t, core, "org-1", "widgets", "feature")

	good := &cairnv1.OpenPullRequest{Org: "org-1", Slug: "widgets", Source: "feature", Target: "main", Title: "Add X", Project: "WID"}

	resp, err := c.pull.OpenPull(mdCtx("org-1", "repo:write"), good)
	if err != nil {
		t.Fatalf("OpenPull: %v", err)
	}
	if resp.Pull.GetLedgerIssueKey() != "WID-1" || resp.Pull.GetState() != "open" {
		t.Fatalf("unexpected pull: %+v", resp.Pull)
	}
	if led.calls != 1 {
		t.Fatalf("ledger CreateIssue calls = %d, want 1", led.calls)
	}
	// the forwarded identity carries the verified subject/org.
	if led.gotFwd.Get("X-CWB-Subject") != "agent-1" || led.gotFwd.Get("X-CWB-Org") != "org-1" {
		t.Errorf("forwarded headers = %v", led.gotFwd)
	}

	// idempotent: same source/target returns the existing PR, no second issue.
	if _, err := c.pull.OpenPull(mdCtx("org-1", "repo:write"), good); err != nil {
		t.Fatalf("idempotent OpenPull: %v", err)
	}
	if led.calls != 1 {
		t.Errorf("ledger calls after idempotent reopen = %d, want still 1", led.calls)
	}

	// validation: missing project -> InvalidArgument
	if _, err := c.pull.OpenPull(mdCtx("org-1", "repo:write"), &cairnv1.OpenPullRequest{Org: "org-1", Slug: "widgets", Source: "feature", Target: "main", Title: "t"}); code(err) != codes.InvalidArgument {
		t.Errorf("missing-project code = %v, want InvalidArgument", code(err))
	}
	// unknown source branch -> NotFound
	if _, err := c.pull.OpenPull(mdCtx("org-1", "repo:write"), &cairnv1.OpenPullRequest{Org: "org-1", Slug: "widgets", Source: "nope", Target: "main", Title: "t", Project: "WID"}); code(err) != codes.NotFound {
		t.Errorf("unknown-source code = %v, want NotFound", code(err))
	}
	// reader scope cannot open -> PermissionDenied
	if _, err := c.pull.OpenPull(mdCtx("org-1", "repo:read"), good); code(err) != codes.PermissionDenied {
		t.Errorf("reader OpenPull code = %v, want PermissionDenied", code(err))
	}
}

// --- GetPull ---

func TestGetPull(t *testing.T) {
	led := &fakeLedger{result: ledgerclient.IssueResult{Key: "WID-1"}}
	c, core := newTest(t, led)
	seedRepoWithBranch(t, core, "org-1", "widgets", "feature")
	open, err := c.pull.OpenPull(mdCtx("org-1", "repo:write"),
		&cairnv1.OpenPullRequest{Org: "org-1", Slug: "widgets", Source: "feature", Target: "main", Title: "Add X", Project: "WID"})
	if err != nil {
		t.Fatalf("OpenPull: %v", err)
	}

	got, err := c.pull.GetPull(mdCtx("org-1", "repo:read"), &cairnv1.GetPullRequest{Org: "org-1", Slug: "widgets", Id: open.Pull.GetId()})
	if err != nil {
		t.Fatalf("GetPull: %v", err)
	}
	if got.Pull.GetId() != open.Pull.GetId() {
		t.Errorf("GetPull id = %q, want %q", got.Pull.GetId(), open.Pull.GetId())
	}
	// unknown id -> NotFound
	if _, err := c.pull.GetPull(mdCtx("org-1", "repo:read"), &cairnv1.GetPullRequest{Org: "org-1", Slug: "widgets", Id: "nope"}); code(err) != codes.NotFound {
		t.Errorf("unknown-pull code = %v, want NotFound", code(err))
	}
}

// --- MergePull error paths (the ff-happy path is covered live by conformance) ---

func TestMergePull_Errors(t *testing.T) {
	led := &fakeLedger{result: ledgerclient.IssueResult{Key: "WID-1"}}
	c, core := newTest(t, led)
	seedRepoWithBranch(t, core, "org-1", "widgets", "feature")
	open, err := c.pull.OpenPull(mdCtx("org-1", "repo:write"),
		&cairnv1.OpenPullRequest{Org: "org-1", Slug: "widgets", Source: "feature", Target: "main", Title: "Add X", Project: "WID"})
	if err != nil {
		t.Fatalf("OpenPull: %v", err)
	}

	// feature and main are independent seed commits (divergent) -> not a
	// fast-forward -> Aborted (409 through the gateway).
	if _, err := c.pull.MergePull(mdCtx("org-1", "repo:write"), &cairnv1.MergePullRequest{Org: "org-1", Slug: "widgets", Id: open.Pull.GetId()}); code(err) != codes.Aborted {
		t.Errorf("divergent merge code = %v, want Aborted", code(err))
	}
	// unknown pull -> NotFound
	if _, err := c.pull.MergePull(mdCtx("org-1", "repo:write"), &cairnv1.MergePullRequest{Org: "org-1", Slug: "widgets", Id: "nope"}); code(err) != codes.NotFound {
		t.Errorf("unknown merge code = %v, want NotFound", code(err))
	}
}

// seedFFRepo seeds a repo with main=A, feature=A→B (feature strictly ahead of
// main), so MergePull's FastForward call succeeds once checks allow it.
func seedFFRepo(t *testing.T, core *repo.Service, org, slug string) repo.Repo {
	t.Helper()
	r, err := core.CreateRepo(context.Background(), org, slug)
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	path, err := core.StoragePathForID(context.Background(), r.ID)
	if err != nil {
		t.Fatalf("StoragePathForID: %v", err)
	}
	g, err := git.PlainOpen(path)
	if err != nil {
		t.Fatalf("PlainOpen: %v", err)
	}
	mk := func(msg string, parents ...plumbing.Hash) plumbing.Hash {
		c := &object.Commit{
			Author:       object.Signature{Name: "t", Email: "t@t", When: time.Unix(0, 0).UTC()},
			Committer:    object.Signature{Name: "t", Email: "t@t", When: time.Unix(0, 0).UTC()},
			Message:      msg,
			TreeHash:     plumbing.ZeroHash,
			ParentHashes: parents,
		}
		enc := g.Storer.NewEncodedObject()
		if err := c.Encode(enc); err != nil {
			t.Fatalf("encode commit: %v", err)
		}
		h, err := g.Storer.SetEncodedObject(enc)
		if err != nil {
			t.Fatalf("set object: %v", err)
		}
		return h
	}
	a := mk("A")
	b := mk("B", a)
	setRef := func(name string, h plumbing.Hash) {
		if err := g.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName(name), h)); err != nil {
			t.Fatalf("set ref %s: %v", name, err)
		}
	}
	setRef("main", a)
	setRef("feature", b)
	return r
}

// --- RecordPullCheck / ListPullChecks / MergePull check-gating ---

func TestPullChecksGateMerge(t *testing.T) {
	led := &fakeLedger{result: ledgerclient.IssueResult{Key: "WID-1"}}
	c, core := newTest(t, led)
	seedFFRepo(t, core, "org-1", "widgets")
	open, err := c.pull.OpenPull(mdCtx("org-1", "repo:write"),
		&cairnv1.OpenPullRequest{Org: "org-1", Slug: "widgets", Source: "feature", Target: "main", Title: "Add X", Project: "WID"})
	if err != nil {
		t.Fatalf("OpenPull: %v", err)
	}
	pid := open.Pull.GetId()

	// Record a failing check.
	rec, err := c.pull.RecordPullCheck(mdCtx("org-1", "repo:write"), &cairnv1.RecordPullCheckRequest{
		Org: "org-1", Slug: "widgets", Id: pid, Name: "ci", State: "fail", Summary: "build broke", EvidenceUrl: "https://ci/1",
	})
	if err != nil {
		t.Fatalf("RecordPullCheck: %v", err)
	}
	if rec.Check.GetRecordedBy() != "agent-1" || rec.Check.GetRecordedAt() == "" {
		t.Fatalf("RecordPullCheck response missing recorder/time: %+v", rec.Check)
	}
	if rec.Check.GetState() != "fail" || rec.Check.GetName() != "ci" {
		t.Fatalf("unexpected check: %+v", rec.Check)
	}

	// ListPullChecks reflects it, with recorder identity + timestamp.
	list, err := c.pull.ListPullChecks(mdCtx("org-1", "repo:read"), &cairnv1.ListPullChecksRequest{Org: "org-1", Slug: "widgets", Id: pid})
	if err != nil {
		t.Fatalf("ListPullChecks: %v", err)
	}
	if len(list.Checks) != 1 || list.Checks[0].GetName() != "ci" || list.Checks[0].GetRecordedBy() != "agent-1" {
		t.Fatalf("ListPullChecks = %+v", list.Checks)
	}

	// Merge refused: a failing check names itself and its state.
	_, err = c.pull.MergePull(mdCtx("org-1", "repo:write"), &cairnv1.MergePullRequest{Org: "org-1", Slug: "widgets", Id: pid})
	if code(err) != codes.FailedPrecondition {
		t.Fatalf("merge-with-failing-check code = %v, want FailedPrecondition", code(err))
	}
	if err == nil || !containsAll(err.Error(), "ci", "fail") {
		t.Fatalf("merge-refusal error = %v, want it to name check %q and state %q", err, "ci", "fail")
	}

	// Re-record the same name as "pass" (upsert: still one check) -> merge succeeds.
	rec2, err := c.pull.RecordPullCheck(mdCtx("org-1", "repo:write"), &cairnv1.RecordPullCheckRequest{
		Org: "org-1", Slug: "widgets", Id: pid, Name: "ci", State: "pass", Summary: "green",
	})
	if err != nil {
		t.Fatalf("RecordPullCheck (pass): %v", err)
	}
	if rec2.Check.GetId() != rec.Check.GetId() {
		t.Fatalf("upsert produced a new check id: %s != %s", rec2.Check.GetId(), rec.Check.GetId())
	}
	list2, err := c.pull.ListPullChecks(mdCtx("org-1", "repo:read"), &cairnv1.ListPullChecksRequest{Org: "org-1", Slug: "widgets", Id: pid})
	if err != nil || len(list2.Checks) != 1 {
		t.Fatalf("ListPullChecks after upsert: %v %+v", err, list2.Checks)
	}

	if _, err := c.pull.MergePull(mdCtx("org-1", "repo:write"), &cairnv1.MergePullRequest{Org: "org-1", Slug: "widgets", Id: pid}); err != nil {
		t.Fatalf("MergePull after check passes: %v", err)
	}
}

func TestMergePull_NoChecksMergesAsBefore(t *testing.T) {
	led := &fakeLedger{result: ledgerclient.IssueResult{Key: "WID-1"}}
	c, core := newTest(t, led)
	seedFFRepo(t, core, "org-1", "widgets")
	open, err := c.pull.OpenPull(mdCtx("org-1", "repo:write"),
		&cairnv1.OpenPullRequest{Org: "org-1", Slug: "widgets", Source: "feature", Target: "main", Title: "Add X", Project: "WID"})
	if err != nil {
		t.Fatalf("OpenPull: %v", err)
	}
	// No checks recorded -> merge proceeds exactly as before this feature.
	resp, err := c.pull.MergePull(mdCtx("org-1", "repo:write"), &cairnv1.MergePullRequest{Org: "org-1", Slug: "widgets", Id: open.Pull.GetId()})
	if err != nil {
		t.Fatalf("MergePull (no checks): %v", err)
	}
	if resp.Result.GetState() != "merged" {
		t.Fatalf("unexpected merge result: %+v", resp.Result)
	}
}

func TestRecordPullCheck_Validation(t *testing.T) {
	led := &fakeLedger{result: ledgerclient.IssueResult{Key: "WID-1"}}
	c, core := newTest(t, led)
	seedFFRepo(t, core, "org-1", "widgets")
	open, err := c.pull.OpenPull(mdCtx("org-1", "repo:write"),
		&cairnv1.OpenPullRequest{Org: "org-1", Slug: "widgets", Source: "feature", Target: "main", Title: "Add X", Project: "WID"})
	if err != nil {
		t.Fatalf("OpenPull: %v", err)
	}
	pid := open.Pull.GetId()

	// invalid state -> InvalidArgument
	if _, err := c.pull.RecordPullCheck(mdCtx("org-1", "repo:write"), &cairnv1.RecordPullCheckRequest{Org: "org-1", Slug: "widgets", Id: pid, Name: "ci", State: "bogus"}); code(err) != codes.InvalidArgument {
		t.Errorf("bogus state code = %v, want InvalidArgument", code(err))
	}
	// missing name -> InvalidArgument
	if _, err := c.pull.RecordPullCheck(mdCtx("org-1", "repo:write"), &cairnv1.RecordPullCheckRequest{Org: "org-1", Slug: "widgets", Id: pid, State: "pass"}); code(err) != codes.InvalidArgument {
		t.Errorf("missing name code = %v, want InvalidArgument", code(err))
	}
	// reader scope cannot record -> PermissionDenied
	if _, err := c.pull.RecordPullCheck(mdCtx("org-1", "repo:read"), &cairnv1.RecordPullCheckRequest{Org: "org-1", Slug: "widgets", Id: pid, Name: "ci", State: "pass"}); code(err) != codes.PermissionDenied {
		t.Errorf("reader RecordPullCheck code = %v, want PermissionDenied", code(err))
	}
	// unknown pull -> NotFound
	if _, err := c.pull.RecordPullCheck(mdCtx("org-1", "repo:write"), &cairnv1.RecordPullCheckRequest{Org: "org-1", Slug: "widgets", Id: "nope", Name: "ci", State: "pass"}); code(err) != codes.NotFound {
		t.Errorf("unknown pull code = %v, want NotFound", code(err))
	}
}

// TestPullChecksGateMerge_PendingBlocks proves "pending" (not just "fail")
// blocks MergePull. Mutation-proof: temporarily changing nonPassingChecks to
// treat only CheckStateFail as blocking makes this test FAIL (verified by
// hand during review; see the builder's report for the exact revert/observe
// steps) — restoring the real (state != pass) check makes it PASS again.
func TestPullChecksGateMerge_PendingBlocks(t *testing.T) {
	led := &fakeLedger{result: ledgerclient.IssueResult{Key: "WID-1"}}
	c, core := newTest(t, led)
	seedFFRepo(t, core, "org-1", "widgets")
	open, err := c.pull.OpenPull(mdCtx("org-1", "repo:write"),
		&cairnv1.OpenPullRequest{Org: "org-1", Slug: "widgets", Source: "feature", Target: "main", Title: "Add X", Project: "WID"})
	if err != nil {
		t.Fatalf("OpenPull: %v", err)
	}
	pid := open.Pull.GetId()

	if _, err := c.pull.RecordPullCheck(mdCtx("org-1", "repo:write"), &cairnv1.RecordPullCheckRequest{
		Org: "org-1", Slug: "widgets", Id: pid, Name: "security-review", State: "pending", Summary: "awaiting reviewer",
	}); err != nil {
		t.Fatalf("RecordPullCheck: %v", err)
	}

	_, err = c.pull.MergePull(mdCtx("org-1", "repo:write"), &cairnv1.MergePullRequest{Org: "org-1", Slug: "widgets", Id: pid})
	if code(err) != codes.FailedPrecondition {
		t.Fatalf("merge-with-pending-check code = %v, want FailedPrecondition", code(err))
	}
	if err == nil || !containsAll(err.Error(), "security-review", "pending") {
		t.Fatalf("merge-refusal error = %v, want it to name check %q and state %q", err, "security-review", "pending")
	}
}

// TestPullChecksGateMerge_NamesAllFailing proves the refusal names every
// non-passing check, not just the first.
func TestPullChecksGateMerge_NamesAllFailing(t *testing.T) {
	led := &fakeLedger{result: ledgerclient.IssueResult{Key: "WID-1"}}
	c, core := newTest(t, led)
	seedFFRepo(t, core, "org-1", "widgets")
	open, err := c.pull.OpenPull(mdCtx("org-1", "repo:write"),
		&cairnv1.OpenPullRequest{Org: "org-1", Slug: "widgets", Source: "feature", Target: "main", Title: "Add X", Project: "WID"})
	if err != nil {
		t.Fatalf("OpenPull: %v", err)
	}
	pid := open.Pull.GetId()

	if _, err := c.pull.RecordPullCheck(mdCtx("org-1", "repo:write"), &cairnv1.RecordPullCheckRequest{
		Org: "org-1", Slug: "widgets", Id: pid, Name: "ci", State: "fail",
	}); err != nil {
		t.Fatalf("RecordPullCheck ci: %v", err)
	}
	if _, err := c.pull.RecordPullCheck(mdCtx("org-1", "repo:write"), &cairnv1.RecordPullCheckRequest{
		Org: "org-1", Slug: "widgets", Id: pid, Name: "security-review", State: "pending",
	}); err != nil {
		t.Fatalf("RecordPullCheck security-review: %v", err)
	}
	// A passing check must NOT be named.
	if _, err := c.pull.RecordPullCheck(mdCtx("org-1", "repo:write"), &cairnv1.RecordPullCheckRequest{
		Org: "org-1", Slug: "widgets", Id: pid, Name: "lint", State: "pass",
	}); err != nil {
		t.Fatalf("RecordPullCheck lint: %v", err)
	}

	_, err = c.pull.MergePull(mdCtx("org-1", "repo:write"), &cairnv1.MergePullRequest{Org: "org-1", Slug: "widgets", Id: pid})
	if code(err) != codes.FailedPrecondition {
		t.Fatalf("merge-with-multiple-failing code = %v, want FailedPrecondition", code(err))
	}
	if err == nil || !containsAll(err.Error(), "ci", "fail", "security-review", "pending") {
		t.Fatalf("merge-refusal error = %v, want it to name both failing checks", err)
	}
	if err != nil && strings.Contains(err.Error(), "lint") {
		t.Fatalf("merge-refusal error = %v, must NOT name the passing check", err)
	}
}

// TestRecordPullCheck_LedgerCommentFailureStillRecords proves the check
// persists (and is returned/listable) even when the best-effort ledger
// comment fails, matching the discard behaviour documented on RecordPullCheck.
func TestRecordPullCheck_LedgerCommentFailureStillRecords(t *testing.T) {
	led := &fakeLedger{result: ledgerclient.IssueResult{Key: "WID-1"}, commentErr: errors.New("ledger unreachable")}
	c, core := newTest(t, led)
	seedFFRepo(t, core, "org-1", "widgets")
	open, err := c.pull.OpenPull(mdCtx("org-1", "repo:write"),
		&cairnv1.OpenPullRequest{Org: "org-1", Slug: "widgets", Source: "feature", Target: "main", Title: "Add X", Project: "WID"})
	if err != nil {
		t.Fatalf("OpenPull: %v", err)
	}
	pid := open.Pull.GetId()

	rec, err := c.pull.RecordPullCheck(mdCtx("org-1", "repo:write"), &cairnv1.RecordPullCheckRequest{
		Org: "org-1", Slug: "widgets", Id: pid, Name: "ci", State: "pass",
	})
	if err != nil {
		t.Fatalf("RecordPullCheck (ledger comment failing): %v", err)
	}
	if rec.Check.GetState() != "pass" || rec.Check.GetName() != "ci" {
		t.Fatalf("check not recorded despite ledger comment failure: %+v", rec.Check)
	}
	if led.commentCalls != 1 {
		t.Fatalf("ledger CommentIssue calls = %d, want 1 (attempted, best-effort)", led.commentCalls)
	}

	list, err := c.pull.ListPullChecks(mdCtx("org-1", "repo:read"), &cairnv1.ListPullChecksRequest{Org: "org-1", Slug: "widgets", Id: pid})
	if err != nil || len(list.Checks) != 1 {
		t.Fatalf("ListPullChecks after ledger comment failure: %v %+v", err, list.Checks)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// --- ListRepos ---

func TestListRepos(t *testing.T) {
	c, core := newTest(t, &fakeLedger{})
	seedRepoWithBranch(t, core, "org-1", "widgets", "feature")
	seedRepoWithBranch(t, core, "org-1", "gadgets", "feature")

	resp, err := c.repo.ListRepos(mdCtx("org-1", "repo:read"), &cairnv1.ListReposRequest{Org: "org-1"})
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(resp.Repos) != 2 {
		t.Fatalf("want 2 repos, got %d", len(resp.Repos))
	}
	for _, rp := range resp.Repos {
		if rp.GetOrg() != "org-1" || rp.GetSlug() == "" || rp.GetId() == "" || rp.GetDefaultBranch() == "" {
			t.Fatalf("unexpected repo fields: %+v", rp)
		}
	}

	// Wrong scope -> PermissionDenied.
	if _, err := c.repo.ListRepos(mdCtx("org-1", "knowledge:read"), &cairnv1.ListReposRequest{Org: "org-1"}); code(err) != codes.PermissionDenied {
		t.Fatalf("scopeless ListRepos err = %v, want PermissionDenied", err)
	}
}

// --- ListPulls ---

func TestListPulls(t *testing.T) {
	led := &fakeLedger{result: ledgerclient.IssueResult{Key: "WID-1"}}
	c, core := newTest(t, led)
	seedRepoWithBranch(t, core, "org-1", "widgets", "feature")
	open, err := c.pull.OpenPull(mdCtx("org-1", "repo:write"),
		&cairnv1.OpenPullRequest{Org: "org-1", Slug: "widgets", Source: "feature", Target: "main", Title: "Add X", Project: "WID"})
	if err != nil {
		t.Fatalf("OpenPull: %v", err)
	}

	resp, err := c.pull.ListPulls(mdCtx("org-1", "repo:read"), &cairnv1.ListPullsRequest{Org: "org-1", Slug: "widgets", State: "all"})
	if err != nil {
		t.Fatalf("ListPulls: %v", err)
	}
	if len(resp.Pulls) != 1 {
		t.Fatalf("want 1 pull, got %d", len(resp.Pulls))
	}
	if resp.Pulls[0].GetId() != open.Pull.GetId() || resp.Pulls[0].GetState() != "open" {
		t.Fatalf("unexpected pull: %+v", resp.Pulls[0])
	}

	// Unknown repo -> NotFound.
	if _, err := c.pull.ListPulls(mdCtx("org-1", "repo:read"), &cairnv1.ListPullsRequest{Org: "org-1", Slug: "nope"}); code(err) != codes.NotFound {
		t.Fatalf("ListPulls unknown repo err = %v, want NotFound", err)
	}
}

// --- PurgeOrg ---

func TestPurgeOrg(t *testing.T) {
	c, core := newTest(t, &fakeLedger{})
	seedRepoWithBranch(t, core, "org-1", "a", "f")
	seedRepoWithBranch(t, core, "org-1", "b", "f")

	resp, err := c.org.PurgeOrg(mdCtx("org-1", "org:purge"), &cairnv1.PurgeOrgRequest{})
	if err != nil {
		t.Fatalf("PurgeOrg: %v", err)
	}
	if resp.GetPurged() != "org-1" || resp.GetRepos() != 2 {
		t.Fatalf("PurgeOrg resp = %+v, want purged=org-1 repos=2", resp)
	}
	// idempotent: now zero repos
	resp2, err := c.org.PurgeOrg(mdCtx("org-1", "org:purge"), &cairnv1.PurgeOrgRequest{})
	if err != nil || resp2.GetRepos() != 0 {
		t.Fatalf("second PurgeOrg = %+v, %v; want repos=0", resp2, err)
	}
	// without org:purge -> PermissionDenied
	if _, err := c.org.PurgeOrg(mdCtx("org-1", "repo:write"), &cairnv1.PurgeOrgRequest{}); code(err) != codes.PermissionDenied {
		t.Errorf("no-purge-scope code = %v, want PermissionDenied", code(err))
	}
}
