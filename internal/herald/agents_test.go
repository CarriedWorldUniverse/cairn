package herald

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestHeraldClientLookup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// NEX-412 contract: GET /api/agents/by-fingerprint/{fp}
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/api/agents/by-fingerprint/") {
			http.Error(w, "no", http.StatusNotFound)
			return
		}
		fp := strings.TrimPrefix(r.URL.Path, "/api/agents/by-fingerprint/")
		if fp != "fp-abc" {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"agent-1","org":"org-1","active":true,"scopes":["repo:read","repo:write"]}`))
	}))
	defer srv.Close()

	c := NewHeraldClient(srv.URL, srv.Client())
	got, err := c.LookupByFingerprint(context.Background(), "fp-abc")
	if err != nil {
		t.Fatalf("LookupByFingerprint: %v", err)
	}
	if got.ID != "agent-1" || got.OrgID != "org-1" || !got.Active || !got.HasScope("repo:write") {
		t.Fatalf("unexpected agent: %+v", got)
	}

	if _, err := c.LookupByFingerprint(context.Background(), "nope"); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("unknown fp: want ErrAgentNotFound, got %v", err)
	}
}
