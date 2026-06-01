package ledger_test

import (
	"context"
	"net"
	"net/http"
	"testing"

	cwbv1 "github.com/CarriedWorldUniverse/cwb-proto/gen/go/cwb/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/CarriedWorldUniverse/cairn/internal/ledger"
)

// stubIssueServer is a minimal in-process IssueServiceServer for testing.
// It records the last request and incoming metadata.
type stubIssueServer struct {
	cwbv1.UnimplementedIssueServiceServer

	gotCreateReq *cwbv1.CreateIssueRequest
	gotCreateMD  metadata.MD
	replyKey     string

	gotCommentReq *cwbv1.CommentIssueRequest
	gotCommentMD  metadata.MD
}

func (s *stubIssueServer) CreateIssue(ctx context.Context, req *cwbv1.CreateIssueRequest) (*cwbv1.CreateIssueResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	s.gotCreateMD = md
	s.gotCreateReq = req
	return &cwbv1.CreateIssueResponse{
		Issue: &cwbv1.Issue{Key: s.replyKey},
	}, nil
}

func (s *stubIssueServer) CommentIssue(ctx context.Context, req *cwbv1.CommentIssueRequest) (*cwbv1.CommentIssueResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	s.gotCommentMD = md
	s.gotCommentReq = req
	return &cwbv1.CommentIssueResponse{}, nil
}

// startStub launches the stub gRPC server on a loopback port and returns the
// *Client wired against it (via NewClientWithConn) plus a cleanup function.
func startStub(t *testing.T, stub *stubIssueServer) (*ledger.Client, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	cwbv1.RegisterIssueServiceServer(srv, stub)
	go srv.Serve(ln) //nolint:errcheck

	conn, err := grpc.NewClient(ln.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial stub: %v", err)
	}

	cli := ledger.NewClientWithConn(conn)
	return cli, func() {
		conn.Close()
		srv.Stop()
	}
}

func TestCreateIssue_ForwardsMetadataAndReturnsKey(t *testing.T) {
	stub := &stubIssueServer{replyKey: "CWB-42"}
	cli, cleanup := startStub(t, stub)
	defer cleanup()

	fwd := http.Header{}
	fwd.Set("X-Cwb-Org", "acme")
	fwd.Set("X-Cwb-Subject", "agent:shadow")
	fwd.Set("X-Cwb-Kind", "agent")
	fwd.Set("X-Cwb-Scopes", "repo:write issue:write")

	res, err := cli.CreateIssue(context.Background(), fwd, ledger.IssueInput{
		Project:          "CWB",
		Type:             "Story",
		Summary:          "test PR",
		Description:      "desc",
		DefinitionOfDone: "green",
		ExternalRefs: []ledger.ExternalRef{
			{Tracker: "cairn", Key: "acme/repo@feat", URL: "https://x", Description: "feat->main"},
		},
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if res.Key != "CWB-42" {
		t.Errorf("key: got %q, want %q", res.Key, "CWB-42")
	}

	// Verify forwarded metadata arrived at the stub.
	md := stub.gotCreateMD
	assertMD(t, md, "cwb-org", "acme")
	assertMD(t, md, "cwb-subject", "agent:shadow")
	assertMD(t, md, "cwb-kind", "agent")
	assertMD(t, md, "cwb-scopes", "repo:write issue:write")

	// Verify request fields.
	req := stub.gotCreateReq
	if req.Project != "CWB" {
		t.Errorf("project: got %q, want %q", req.Project, "CWB")
	}
	if req.Summary != "test PR" {
		t.Errorf("summary: got %q", req.Summary)
	}
	if len(req.ExternalRefs) != 1 || req.ExternalRefs[0].Key != "acme/repo@feat" {
		t.Errorf("external_refs: %+v", req.ExternalRefs)
	}
}

func TestCreateIssue_NoSpuriousMetadata(t *testing.T) {
	// X-Cwb-Responsible-Human is empty — must not appear in outgoing metadata.
	stub := &stubIssueServer{replyKey: "CWB-1"}
	cli, cleanup := startStub(t, stub)
	defer cleanup()

	fwd := http.Header{}
	fwd.Set("X-Cwb-Org", "acme")
	fwd.Set("X-Cwb-Subject", "agent:shadow")
	// X-Cwb-Responsible-Human intentionally absent.

	_, err := cli.CreateIssue(context.Background(), fwd, ledger.IssueInput{
		Project: "CWB", Type: "Story", Summary: "s",
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	md := stub.gotCreateMD
	if vals := md.Get("cwb-responsible-human"); len(vals) > 0 {
		t.Errorf("expected no cwb-responsible-human in metadata, got %v", vals)
	}
}

func TestCommentIssue_ForwardsMetadata(t *testing.T) {
	stub := &stubIssueServer{}
	cli, cleanup := startStub(t, stub)
	defer cleanup()

	fwd := http.Header{}
	fwd.Set("X-Cwb-Org", "nexus")
	fwd.Set("X-Cwb-Subject", "agent:keel")
	fwd.Set("X-Cwb-Responsible-Human", "jacinta")

	err := cli.CommentIssue(context.Background(), fwd, "CWB-99", "merged feat into main @ abc123def456")
	if err != nil {
		t.Fatalf("CommentIssue: %v", err)
	}

	md := stub.gotCommentMD
	assertMD(t, md, "cwb-org", "nexus")
	assertMD(t, md, "cwb-subject", "agent:keel")
	assertMD(t, md, "cwb-responsible-human", "jacinta")

	req := stub.gotCommentReq
	if req.Key != "CWB-99" {
		t.Errorf("key: got %q, want %q", req.Key, "CWB-99")
	}
	if req.Body != "merged feat into main @ abc123def456" {
		t.Errorf("body: got %q", req.Body)
	}
}

// assertMD checks that a metadata key has exactly one value matching want.
func assertMD(t *testing.T, md metadata.MD, key, want string) {
	t.Helper()
	vals := md.Get(key)
	if len(vals) == 0 {
		t.Errorf("metadata %q: not found (got keys %v)", key, metaKeys(md))
		return
	}
	if vals[0] != want {
		t.Errorf("metadata %q: got %q, want %q", key, vals[0], want)
	}
}

func metaKeys(md metadata.MD) []string {
	keys := make([]string, 0, len(md))
	for k := range md {
		keys = append(keys, k)
	}
	return keys
}
