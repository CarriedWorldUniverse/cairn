package grpcapi

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
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
