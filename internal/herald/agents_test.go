package herald

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	heraldv1 "github.com/CarriedWorldUniverse/cwb-proto/gen/go/cwb/herald/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestFakeAgentsLookup(t *testing.T) {
	f := NewFakeAgents()
	f.Add(Agent{ID: "agent-1", OrgID: "org-1", Active: true,
		Scopes: []string{"repo:read", "repo:write"}, Fingerprint: "fp-abc"})

	got, err := f.LookupByFingerprint(context.Background(), "fp-abc")
	if err != nil {
		t.Fatalf("LookupByFingerprint: %v", err)
	}
	if got.ID != "agent-1" || !got.Active || !got.HasScope("repo:write") {
		t.Fatalf("unexpected agent: %+v", got)
	}

	if _, err := f.LookupByFingerprint(context.Background(), "missing"); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("missing fp: want ErrAgentNotFound, got %v", err)
	}
}

func TestCachedAgentsShortTTLAndInvalidate(t *testing.T) {
	f := NewFakeAgents()
	f.Add(Agent{ID: "agent-1", OrgID: "org-1", Active: true,
		Scopes: []string{"repo:read"}, Fingerprint: "fp-abc"})

	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	c := NewCachedAgents(f, 30*time.Second)
	c.now = clock.Now

	// First call hits the backend.
	if _, err := c.LookupByFingerprint(context.Background(), "fp-abc"); err != nil {
		t.Fatalf("first lookup: %v", err)
	}
	if f.calls != 1 {
		t.Fatalf("calls = %d, want 1", f.calls)
	}
	// Second call within TTL is served from cache.
	if _, err := c.LookupByFingerprint(context.Background(), "fp-abc"); err != nil {
		t.Fatalf("cached lookup: %v", err)
	}
	if f.calls != 1 {
		t.Fatalf("calls after cache hit = %d, want 1", f.calls)
	}
	// After TTL, the backend is hit again.
	clock.now = clock.now.Add(31 * time.Second)
	if _, err := c.LookupByFingerprint(context.Background(), "fp-abc"); err != nil {
		t.Fatalf("post-ttl lookup: %v", err)
	}
	if f.calls != 2 {
		t.Fatalf("calls after ttl = %d, want 2", f.calls)
	}
	// Explicit invalidation (block-invalidation hook) forces a refetch.
	c.Invalidate("fp-abc")
	if _, err := c.LookupByFingerprint(context.Background(), "fp-abc"); err != nil {
		t.Fatalf("post-invalidate lookup: %v", err)
	}
	if f.calls != 3 {
		t.Fatalf("calls after invalidate = %d, want 3", f.calls)
	}
}

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

// stubAgentSrv implements herald's gRPC AgentService for the client test.
type stubAgentSrv struct {
	heraldv1.UnimplementedAgentServiceServer
}

func (stubAgentSrv) GetAgentByFingerprint(_ context.Context, r *heraldv1.GetAgentByFingerprintRequest) (*heraldv1.GetAgentByFingerprintResponse, error) {
	if r.Fingerprint != "fp-abc" {
		return nil, status.Error(codes.NotFound, "no agent for fingerprint")
	}
	return &heraldv1.GetAgentByFingerprintResponse{Agent: &heraldv1.Agent{
		Id: "agent-1", Org: "org-1", Active: true, Scopes: []string{"repo:read", "repo:write"},
	}}, nil
}

func TestGRPCClientLookup(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	g := grpc.NewServer()
	heraldv1.RegisterAgentServiceServer(g, stubAgentSrv{})
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	c := NewClientWithConn(conn)

	got, err := c.LookupByFingerprint(context.Background(), "fp-abc")
	if err != nil {
		t.Fatalf("LookupByFingerprint: %v", err)
	}
	if got.ID != "agent-1" || got.OrgID != "org-1" || !got.Active || !got.HasScope("repo:write") || got.Fingerprint != "fp-abc" {
		t.Fatalf("unexpected agent: %+v", got)
	}
	if _, err := c.LookupByFingerprint(context.Background(), "nope"); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("unknown fp: want ErrAgentNotFound, got %v", err)
	}
}
