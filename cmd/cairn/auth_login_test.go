package cairn

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthLogin_StoresToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/users/") {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %q", r.Method)
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "alice" || pass != "secret" {
			t.Errorf("basic auth = (%q, %q, %v)", user, pass, ok)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sha1": "fake-token-sha1",
		})
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	if err := AuthLogin(srv.URL, "alice", "secret", "cairn-cli"); err != nil {
		t.Fatal(err)
	}

	paths, _ := ResolvePaths(srv.URL)
	stored, err := paths.ReadToken()
	if err != nil {
		t.Fatal(err)
	}
	if stored != "fake-token-sha1" {
		t.Errorf("stored token = %q, want fake-token-sha1", stored)
	}

	info, _ := os.Stat(paths.TokenFile)
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("token mode = %#o, want 0600", perm)
	}
}

func TestAuthLogin_RejectsBadCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	err := AuthLogin(srv.URL, "alice", "wrong", "cairn-cli")
	if err == nil {
		t.Fatal("expected error on 401")
	}
}

func TestAuthLogin_DoesNotWriteTokenOnFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	_ = AuthLogin(srv.URL, "alice", "wrong", "cairn-cli")

	paths, _ := ResolvePaths(srv.URL)
	if _, err := os.Stat(paths.TokenFile); !os.IsNotExist(err) {
		t.Errorf("token file exists after failed login: stat err=%v", err)
	}
	_ = filepath.Join // keep import
}
