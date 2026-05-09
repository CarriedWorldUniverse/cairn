package cairn

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentsList_PrintsTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"fingerprint": "cairn:fp1", "slug": "plumb", "domain": "darksoft.co.nz", "status": "active", "blocked": false},
			{"fingerprint": "cairn:fp2", "slug": "anvil", "domain": "darksoft.co.nz", "status": "pending", "blocked": false},
		})
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	paths, _ := ResolvePaths(srv.URL)
	_ = paths.WriteToken("tok")

	out := &bytes.Buffer{}
	if err := AgentsList(srv.URL, "", out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"plumb", "anvil", "active", "pending"} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q\noutput=%s", want, s)
		}
	}
}

func TestAgentsApprove_CallsEndpoint(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/approve") {
			called = true
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"fingerprint": "cairn:fp1", "status": "active"})
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	paths, _ := ResolvePaths(srv.URL)
	_ = paths.WriteToken("tok")

	out := &bytes.Buffer{}
	if err := AgentsApprove(srv.URL, "cairn:fp1", out); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("approve endpoint not called")
	}
}

func TestAgentsBlock_CallsEndpoint(t *testing.T) {
	body := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"fingerprint": "cairn:fp1", "status": "active"})
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	paths, _ := ResolvePaths(srv.URL)
	_ = paths.WriteToken("tok")

	out := &bytes.Buffer{}
	if err := AgentsBlock(srv.URL, "cairn:fp1", "key compromised", out); err != nil {
		t.Fatal(err)
	}
	if body["reason"] != "key compromised" {
		t.Errorf("reason = %q, want 'key compromised'", body["reason"])
	}
}
