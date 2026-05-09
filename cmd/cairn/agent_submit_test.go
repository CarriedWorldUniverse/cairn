package cairn

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentSubmit_PostsRegistration(t *testing.T) {
	got := struct {
		ProposedOwner string `json:"proposed_owner"`
		Slug          string `json:"slug"`
		Domain        string `json:"domain"`
		PublicKeyHex  string `json:"public_key"`
	}{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"fingerprint": "cairn:test-fp",
			"slug":        "plumb",
			"status":      "active",
		})
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	paths, _ := ResolvePaths(srv.URL)
	writeTestSeed(t, paths)
	if err := paths.WriteToken("test-tok"); err != nil {
		t.Fatal(err)
	}

	out := &bytes.Buffer{}
	if err := AgentSubmit(srv.URL, "alice", "plumb", "darksoft.co.nz", out); err != nil {
		t.Fatal(err)
	}

	if got.ProposedOwner != "alice" || got.Slug != "plumb" || got.Domain != "darksoft.co.nz" {
		t.Errorf("posted body = %+v", got)
	}
	if len(got.PublicKeyHex) != 64 {
		t.Errorf("hex pubkey length = %d, want 64", len(got.PublicKeyHex))
	}
	if !strings.Contains(out.String(), "cairn:test-fp") {
		t.Errorf("output missing fingerprint:\n%s", out.String())
	}
}

func TestAgentSubmit_FailsWithoutToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	paths, _ := ResolvePaths("https://cairn.example.com")
	writeTestSeed(t, paths)

	out := &bytes.Buffer{}
	err := AgentSubmit("https://cairn.example.com", "alice", "plumb", "darksoft.co.nz", out)
	if err == nil {
		t.Error("expected error when token is missing")
	}
}

func TestAgentSubmit_AnonymousIsAllowed(t *testing.T) {
	gotAuth := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"fingerprint": "cairn:anon-fp",
			"status":      "pending",
		})
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	paths, _ := ResolvePaths(srv.URL)
	writeTestSeed(t, paths)
	// No WriteToken — anonymous mode.

	out := &bytes.Buffer{}
	err := AgentSubmitAnonymous(srv.URL, "alice", "plumb", "darksoft.co.nz", out)
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "" {
		t.Errorf("anonymous submit sent Authorization header: %q", gotAuth)
	}
	if !strings.Contains(out.String(), "pending") {
		t.Errorf("output missing pending status:\n%s", out.String())
	}
}
