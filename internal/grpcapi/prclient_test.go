package grpcapi

import (
	"context"
	"net"
	"testing"

	ledgerclient "github.com/CarriedWorldUniverse/cairn/internal/ledger"
	"github.com/CarriedWorldUniverse/cairn/internal/prclient"
	"github.com/CarriedWorldUniverse/cairn/internal/repo"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// rewindMainToFeature points refs/heads/main directly at feature's tip —
// simulating a rebase so a subsequent MergePull is a clean fast-forward.
func rewindMainToFeature(t *testing.T, core *repo.Service, org, slug string) {
	t.Helper()
	rp, err := core.GetRepo(context.Background(), org, slug)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	featRef, err := core.GetRef(context.Background(), rp.ID, "refs/heads/feature")
	if err != nil {
		t.Fatalf("GetRef feature: %v", err)
	}
	path, err := core.StoragePathForID(context.Background(), rp.ID)
	if err != nil {
		t.Fatalf("StoragePathForID: %v", err)
	}
	g, err := git.PlainOpen(path)
	if err != nil {
		t.Fatalf("PlainOpen: %v", err)
	}
	ref := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), plumbing.NewHash(featRef.Hash))
	if err := g.Storer.SetReference(ref); err != nil {
		t.Fatalf("SetReference main: %v", err)
	}
}

// newPRTest stands up an in-process cairn gRPC server (repo core + ledger)
// and returns a prclient.Client dialed against it over bufconn — the exact
// client wiring cmd/cairn's `pr` verbs use, so these tests exercise the CLI's
// gRPC transport path end to end, not just the handler layer.
func newPRTest(t *testing.T, led IssueCreator) (*prclient.Client, *repo.Service) {
	t.Helper()
	dir := t.TempDir()
	core, err := repo.Open(dir+"/cairn.db", dir+"/repos")
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
	return prclient.NewClientWithConn(conn), core
}

// prCtx builds an outgoing context carrying the identity prclient.WithIdentity
// attaches for every `pr` call — mirrors mdCtx but through the client's own
// helper, so it's the CLI's real code path under test.
func prCtx(org, subject string, scopes ...string) context.Context {
	return prclient.WithIdentity(context.Background(), prclient.Identity{Subject: subject, Org: org, Scopes: scopes})
}

// TestPRClient_OpenListViewMerge round-trips open -> list -> view -> merge
// through prclient.Client against an in-process server, ending with a clean
// fast-forward merge.
func TestPRClient_OpenListViewMerge(t *testing.T) {
	led := &fakeLedger{result: ledgerclient.IssueResult{Key: "WID-1"}}
	cli, core := newPRTest(t, led)
	seedRepoWithBranch(t, core, "org-1", "widgets", "feature")

	writeCtx := prCtx("org-1", "agent-1", "repo:write")
	readCtx := prCtx("org-1", "agent-1", "repo:read")

	opened, err := cli.Open(writeCtx, "org-1", "widgets", "feature", "main", "Add X", "desc", "dod", "WID")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if opened.GetState() != "open" || opened.GetLedgerIssueKey() != "WID-1" {
		t.Fatalf("unexpected opened pull: %+v", opened)
	}

	pulls, err := cli.List(readCtx, "org-1", "widgets", "all")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(pulls) != 1 || pulls[0].GetId() != opened.GetId() {
		t.Fatalf("List = %+v, want [%s]", pulls, opened.GetId())
	}

	viewed, err := cli.View(readCtx, "org-1", "widgets", opened.GetId())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if viewed.GetId() != opened.GetId() || viewed.GetTitle() != "Add X" {
		t.Fatalf("View = %+v", viewed)
	}

	// feature and main are independent seed commits — not fast-forwardable yet.
	// Fast-forward main's ref to feature's so the merge below succeeds cleanly,
	// mirroring how a real PR would be rebased before merge.
	rewindMainToFeature(t, core, "org-1", "widgets")

	merged, err := cli.Merge(writeCtx, "org-1", "widgets", opened.GetId())
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if merged.GetState() != "merged" || merged.GetTarget() != "main" {
		t.Fatalf("unexpected merge result: %+v", merged)
	}
}

// TestPRClient_OpenIdempotent opens the same (repo, source, target) twice and
// asserts the second call returns the SAME pull id (server-side idempotency),
// not a duplicate.
func TestPRClient_OpenIdempotent(t *testing.T) {
	led := &fakeLedger{result: ledgerclient.IssueResult{Key: "WID-1"}}
	cli, core := newPRTest(t, led)
	seedRepoWithBranch(t, core, "org-1", "widgets", "feature")
	ctx := prCtx("org-1", "agent-1", "repo:write")

	first, err := cli.Open(ctx, "org-1", "widgets", "feature", "main", "Add X", "", "", "WID")
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	second, err := cli.Open(ctx, "org-1", "widgets", "feature", "main", "Add X (reopen)", "", "", "WID")
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if first.GetId() != second.GetId() {
		t.Fatalf("Open not idempotent: first id %q, second id %q", first.GetId(), second.GetId())
	}
	if led.calls != 1 {
		t.Fatalf("ledger CreateIssue calls = %d, want 1 (no duplicate issue on reopen)", led.calls)
	}
}

// TestPRClient_MergeDivergedSurfacesRebaseGuidance asserts the server's
// codes.Aborted "not fast-forwardable; rebase X onto Y" text reaches the
// caller unchanged through prclient — the same error mapPRErr in cmd/cairn
// unwraps for the operator.
func TestPRClient_MergeDivergedSurfacesRebaseGuidance(t *testing.T) {
	led := &fakeLedger{result: ledgerclient.IssueResult{Key: "WID-1"}}
	cli, core := newPRTest(t, led)
	seedRepoWithBranch(t, core, "org-1", "widgets", "feature")
	writeCtx := prCtx("org-1", "agent-1", "repo:write")

	opened, err := cli.Open(writeCtx, "org-1", "widgets", "feature", "main", "Add X", "", "", "WID")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	_, err = cli.Merge(writeCtx, "org-1", "widgets", opened.GetId())
	if err == nil {
		t.Fatalf("Merge of a diverged source: want error, got nil")
	}
	if code(err) != codes.Aborted {
		t.Fatalf("Merge diverged code = %v, want Aborted", code(err))
	}
	const want = "not fast-forwardable; rebase feature onto main"
	if got := err.Error(); !contains(got, want) {
		t.Fatalf("Merge error = %q, want it to contain %q", got, want)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
