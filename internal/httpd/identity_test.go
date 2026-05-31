package httpd

import (
	"net/http/httptest"
	"testing"
)

func TestIdentityFromHeaders(t *testing.T) {
	r := httptest.NewRequest("GET", "/org-1/widgets.git/info/refs?service=git-upload-pack", nil)
	r.Header.Set("X-CWB-Subject", "agent-7")
	r.Header.Set("X-CWB-Org", "org-1")
	r.Header.Set("X-CWB-Kind", "agent")
	r.Header.Set("X-CWB-Scopes", "repo:read repo:write")

	id, ok := identityFromHeaders(r)
	if !ok {
		t.Fatal("identityFromHeaders: want ok")
	}
	if id.Subject != "agent-7" || id.Org != "org-1" || id.Kind != "agent" {
		t.Fatalf("unexpected identity: %+v", id)
	}
	if !id.HasScope("repo:read") || !id.HasScope("repo:write") || id.HasScope("repo:admin") {
		t.Fatalf("scope parse wrong: %+v", id.Scopes)
	}
}

func TestIdentityMissingSubject(t *testing.T) {
	r := httptest.NewRequest("GET", "/x", nil)
	if _, ok := identityFromHeaders(r); ok {
		t.Fatal("missing X-CWB-Subject must yield !ok")
	}
}
