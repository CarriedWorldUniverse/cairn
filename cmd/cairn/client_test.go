package cairn

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient_PostAttachmentRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cairn/v1/agents/attachment-requests" {
			t.Errorf("path = %q, want /api/cairn/v1/agents/attachment-requests", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.Header.Get("Authorization") != "token test-tok" {
			t.Errorf("auth header = %q, want 'token test-tok'", r.Header.Get("Authorization"))
		}

		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["owner_username"] != "alice" {
			t.Errorf("owner_username = %v, want alice", body["owner_username"])
		}
		if body["slug"] != "plumb" {
			t.Errorf("slug = %v, want plumb", body["slug"])
		}
		if body["pubkey_content"] != "ssh-ed25519 AAAA comment" {
			t.Errorf("pubkey_content = %v", body["pubkey_content"])
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":          int64(99),
			"fingerprint": "cairn:test-fp",
			"status":      "pending",
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-tok")
	got, err := c.PostAttachmentRequest(context.Background(), PostAttachmentRequestInput{
		OwnerUsername: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PubkeyContent: "ssh-ed25519 AAAA comment",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 99 {
		t.Errorf("ID = %d, want 99", got.ID)
	}
	if got.Fingerprint != "cairn:test-fp" {
		t.Errorf("Fingerprint = %q", got.Fingerprint)
	}
	if got.Status != "pending" {
		t.Errorf("Status = %q, want pending", got.Status)
	}
}

func TestClient_PostAttachmentRequest_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":   "pubkey_already_claimed",
			"message": "duplicate",
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-tok")
	_, err := c.PostAttachmentRequest(context.Background(), PostAttachmentRequestInput{
		OwnerUsername: "alice", Slug: "plumb", Domain: "darksoft.co.nz",
		PubkeyContent: "ssh-ed25519 AAAA comment",
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
	if apiErr.ErrorCode != "pubkey_already_claimed" {
		t.Errorf("ErrorCode = %q, want pubkey_already_claimed", apiErr.ErrorCode)
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
}
