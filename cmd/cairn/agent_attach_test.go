//
// Cairn-specific code; AGPLv3. See LICENSING.md.

package cairn

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testPubkeyLine = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIH9Ovsx7r5lT4kHj4P+jZmRkBeJ4UPP1rA4XkD0o3w0Z plumb@darksoft.co.nz"

func writeTestPubkey(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "plumb.key.pub")
	if err := os.WriteFile(path, []byte(testPubkeyLine+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAgentAttach_PostsAttachmentRequest(t *testing.T) {
	got := struct {
		OwnerUsername string `json:"owner_username"`
		Slug          string `json:"slug"`
		Domain        string `json:"domain"`
		PubkeyContent string `json:"pubkey_content"`
	}{}
	gotPath := ""
	gotAuth := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":          int64(42),
			"fingerprint": "cairn:test-fp",
			"status":      "active",
		})
	}))
	defer srv.Close()

	dir := t.TempDir()
	pubFile := writeTestPubkey(t, dir)

	out := &bytes.Buffer{}
	if err := AgentAttach(srv.URL, "alice", "plumb", "darksoft.co.nz", pubFile, "test-tok", out); err != nil {
		t.Fatal(err)
	}

	if gotPath != "/api/cairn/v1/agents/attachment-requests" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "token test-tok" {
		t.Errorf("auth header = %q, want 'token test-tok'", gotAuth)
	}
	if got.OwnerUsername != "alice" || got.Slug != "plumb" || got.Domain != "darksoft.co.nz" {
		t.Errorf("posted body = %+v", got)
	}
	if got.PubkeyContent != testPubkeyLine {
		t.Errorf("pubkey_content = %q, want %q", got.PubkeyContent, testPubkeyLine)
	}

	s := out.String()
	for _, want := range []string{"id: 42", "cairn:test-fp", "status: active"} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q\noutput=%s", want, s)
		}
	}
}

func TestAgentAttach_AnonymousIsAllowed(t *testing.T) {
	gotAuth := "sentinel"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":          int64(7),
			"fingerprint": "cairn:anon-fp",
			"status":      "pending",
		})
	}))
	defer srv.Close()

	dir := t.TempDir()
	pubFile := writeTestPubkey(t, dir)

	out := &bytes.Buffer{}
	if err := AgentAttach(srv.URL, "alice", "plumb", "darksoft.co.nz", pubFile, "", out); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "" {
		t.Errorf("anonymous attach sent Authorization header: %q", gotAuth)
	}

	s := out.String()
	if !strings.Contains(s, "pending") {
		t.Errorf("output missing pending status:\n%s", s)
	}
	if !strings.Contains(s, "awaiting alice's approval") {
		t.Errorf("output missing next-step hint:\n%s", s)
	}
}

func TestAgentAttach_RequiresPubkeyFile(t *testing.T) {
	out := &bytes.Buffer{}
	if err := AgentAttach("https://cairn.example.com", "alice", "plumb", "darksoft.co.nz", "", "", out); err == nil {
		t.Error("expected error when --pubkey is empty")
	}
}

func TestAgentAttach_MissingFileErrors(t *testing.T) {
	dir := t.TempDir()
	out := &bytes.Buffer{}
	err := AgentAttach("https://cairn.example.com", "alice", "plumb", "darksoft.co.nz",
		filepath.Join(dir, "nope.pub"), "", out)
	if err == nil {
		t.Error("expected error for missing pubkey file")
	}
}
