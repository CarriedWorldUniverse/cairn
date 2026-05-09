package cairn

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient_PostAgents(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cairn/v1/agents" {
			t.Errorf("path = %q, want /api/cairn/v1/agents", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.Header.Get("Authorization") != "token test-tok" {
			t.Errorf("auth header = %q, want 'token test-tok'", r.Header.Get("Authorization"))
		}

		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["proposed_owner"] != "alice" {
			t.Errorf("proposed_owner = %v, want alice", body["proposed_owner"])
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"fingerprint": "cairn:test-fp",
			"slug":        "plumb",
			"status":      "active",
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-tok")
	got, err := c.PostAgent(context.Background(), PostAgentRequest{
		ProposedOwner: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKey:     pub,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Fingerprint != "cairn:test-fp" {
		t.Errorf("Fingerprint = %q", got.Fingerprint)
	}
}

func TestClient_PostAgents_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":   "agent_exists",
			"message": "duplicate",
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-tok")
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, err := c.PostAgent(context.Background(), PostAgentRequest{
		ProposedOwner: "alice", Slug: "plumb", Domain: "darksoft.co.nz", PublicKey: pub,
	})
	if err == nil {
		t.Fatal("expected error on 409")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("err type = %T, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusConflict {
		t.Errorf("StatusCode = %d, want 409", apiErr.StatusCode)
	}
	if apiErr.ErrorCode != "agent_exists" {
		t.Errorf("ErrorCode = %q, want agent_exists", apiErr.ErrorCode)
	}
}

func TestClient_GetAgents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cairn/v1/agents" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("status") != "pending" {
			t.Errorf("status filter = %q, want pending", r.URL.Query().Get("status"))
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"fingerprint": "cairn:a", "slug": "plumb", "status": "pending"},
			{"fingerprint": "cairn:b", "slug": "anvil", "status": "pending"},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	got, err := c.ListAgents(context.Background(), "pending")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("len = %d, want 2", len(got))
	}
}

func TestClient_ApproveAndBlock(t *testing.T) {
	calls := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"fingerprint": "cairn:test"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")

	if err := c.Approve(context.Background(), "cairn:test"); err != nil {
		t.Fatal(err)
	}
	if err := c.Block(context.Background(), "cairn:test", "compromised"); err != nil {
		t.Fatal(err)
	}

	if len(calls) != 2 {
		t.Fatalf("calls = %v", calls)
	}
	if calls[0] != "POST /api/cairn/v1/agents/cairn:test/approve" {
		t.Errorf("approve call = %q", calls[0])
	}
	if calls[1] != "POST /api/cairn/v1/agents/cairn:test/block" {
		t.Errorf("block call = %q", calls[1])
	}

	_ = hex.EncodeToString // keep import
}
