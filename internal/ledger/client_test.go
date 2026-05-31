package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateIssue_ForwardsIdentityAndReturnsKey(t *testing.T) {
	var gotSub, gotScopes, gotCT string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSub = r.Header.Get("X-CWB-Subject")
		gotScopes = r.Header.Get("X-CWB-Scopes")
		gotCT = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"key":"WID-7"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, nil)
	fwd := http.Header{"X-Cwb-Subject": {"agent-1"}, "X-Cwb-Org": {"org-1"}, "X-Cwb-Scopes": {"repo:write issue:write"}}
	res, err := c.CreateIssue(context.Background(), fwd, IssueInput{
		Project: "WID", Type: "Story", Summary: "Add X",
		ExternalRefs: []ExternalRef{{Tracker: "cairn", Key: "org-1/widgets@feature"}},
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if res.Key != "WID-7" {
		t.Fatalf("key = %q, want WID-7", res.Key)
	}
	if gotSub != "agent-1" || gotScopes != "repo:write issue:write" {
		t.Fatalf("identity not forwarded: sub=%q scopes=%q", gotSub, gotScopes)
	}
	if gotCT != "application/json" {
		t.Fatalf("content-type = %q", gotCT)
	}
	if gotBody["project"] != "WID" || gotBody["summary"] != "Add X" {
		t.Fatalf("body = %v", gotBody)
	}
}

func TestCreateIssue_Non2xxIsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"insufficient_scope"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, nil)
	_, err := c.CreateIssue(context.Background(), http.Header{}, IssueInput{Project: "WID", Summary: "x"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.Status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", apiErr.Status)
	}
}

func TestCommentIssue_ForwardsIdentityAndPostsBody(t *testing.T) {
	var gotPath, gotSub string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSub = r.Header.Get("X-CWB-Subject")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, nil)
	fwd := http.Header{"X-Cwb-Subject": {"agent-1"}}
	if err := c.CommentIssue(context.Background(), fwd, "WID-1", "merged feature into main"); err != nil {
		t.Fatalf("CommentIssue: %v", err)
	}
	if gotPath != "/api/issues/WID-1/comments" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotSub != "agent-1" {
		t.Fatalf("subject not forwarded: %q", gotSub)
	}
	if gotBody["body"] != "merged feature into main" {
		t.Fatalf("body = %v", gotBody)
	}
}

func TestCommentIssue_Non2xxIsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, nil)
	var apiErr *APIError
	if err := c.CommentIssue(context.Background(), http.Header{}, "WID-1", "x"); !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
}
