package httpd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleOrgPurge(t *testing.T) {
	srv, core := newTestServer(t, nil)
	ctx := context.Background()

	// seed two repos in org "o1"
	if _, err := core.CreateRepo(ctx, "o1", "alpha"); err != nil {
		t.Fatalf("CreateRepo alpha: %v", err)
	}
	if _, err := core.CreateRepo(ctx, "o1", "beta"); err != nil {
		t.Fatalf("CreateRepo beta: %v", err)
	}

	do := func(scopes string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("DELETE", "/api/org", nil)
		req.Header.Set("X-CWB-Subject", "sys")
		req.Header.Set("X-CWB-Org", "o1")
		req.Header.Set("X-CWB-Kind", "agent")
		req.Header.Set("X-CWB-Scopes", scopes)
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, req)
		return rr
	}

	// no org:purge scope → 403
	if rr := do("repo:read"); rr.Code != http.StatusForbidden {
		t.Fatalf("no scope: got %d, want 403", rr.Code)
	}

	// with org:purge → 200 and repos gone
	if rr := do("org:purge"); rr.Code != http.StatusOK {
		t.Fatalf("purge: got %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	repos, err := core.ListRepos(ctx, "o1")
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 0 {
		t.Fatalf("repos should be gone, got %d", len(repos))
	}

	// idempotent: second purge on empty org → 200
	if rr := do("org:purge"); rr.Code != http.StatusOK {
		t.Fatalf("idempotent purge: got %d, want 200", rr.Code)
	}
}
