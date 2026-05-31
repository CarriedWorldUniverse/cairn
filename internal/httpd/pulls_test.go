package httpd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	ledgerclient "github.com/CarriedWorldUniverse/cairn/internal/ledger"
	"github.com/CarriedWorldUniverse/cairn/internal/repo"
)

// fakeLedger records the last CreateIssue call and returns a scripted result.
type fakeLedger struct {
	calls         int
	gotFwd        http.Header
	gotIn         ledgerclient.IssueInput
	result        ledgerclient.IssueResult
	err           error
	commentCalls  int
	commentErr    error
	gotCommentKey string
}

func (f *fakeLedger) CreateIssue(_ context.Context, fwd http.Header, in ledgerclient.IssueInput) (ledgerclient.IssueResult, error) {
	f.calls++
	f.gotFwd, f.gotIn = fwd, in
	return f.result, f.err
}

func (f *fakeLedger) CommentIssue(_ context.Context, fwd http.Header, key, body string) error {
	f.commentCalls++
	f.gotCommentKey = key
	return f.commentErr
}

func newTestServer(t *testing.T, led IssueCreator) (*Server, *repo.Service) {
	t.Helper()
	dir := t.TempDir()
	core, err := repo.Open(filepath.Join(dir, "cairn.db"), filepath.Join(dir, "repos"))
	if err != nil {
		t.Fatalf("repo.Open: %v", err)
	}
	t.Cleanup(func() { _ = core.Close() })
	return New(Config{Core: core, Ledger: led}), core
}

// seedRepoWithBranch creates a repo and a branch ref so PR validation passes.
func seedRepoWithBranch(t *testing.T, core *repo.Service, org, slug, branch string) repo.Repo {
	t.Helper()
	r, err := core.CreateRepo(context.Background(), org, slug)
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	mustSeedRef(t, core, r.ID, "refs/heads/main")
	mustSeedRef(t, core, r.ID, "refs/heads/"+branch)
	return r
}

func mustSeedRef(t *testing.T, core *repo.Service, repoID, refName string) {
	t.Helper()
	path, err := core.StoragePathForID(context.Background(), repoID)
	if err != nil {
		t.Fatalf("StoragePathForID: %v", err)
	}
	g, err := git.PlainOpen(path)
	if err != nil {
		t.Fatalf("PlainOpen: %v", err)
	}
	st := g.Storer
	commit := &object.Commit{
		Author:    object.Signature{Name: "t", Email: "t@t", When: time.Unix(0, 0).UTC()},
		Committer: object.Signature{Name: "t", Email: "t@t", When: time.Unix(0, 0).UTC()},
		Message:   "seed " + refName,
		TreeHash:  plumbing.ZeroHash,
	}
	enc := st.NewEncodedObject()
	if err := commit.Encode(enc); err != nil {
		t.Fatalf("encode commit: %v", err)
	}
	h, err := st.SetEncodedObject(enc)
	if err != nil {
		t.Fatalf("set object: %v", err)
	}
	if err := st.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(refName), h)); err != nil {
		t.Fatalf("set ref: %v", err)
	}
}

func openPullReq(org, slug string, body map[string]any, scopes string) *http.Request {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/orgs/"+org+"/repos/"+slug+"/pulls", bytes.NewReader(b))
	req.Header.Set("X-CWB-Subject", "agent-1")
	req.Header.Set("X-CWB-Org", org)
	req.Header.Set("X-CWB-Scopes", scopes)
	req.SetPathValue("org", org)
	req.SetPathValue("slug", slug)
	return req
}

func TestOpenPull_CreatesIssueAndPR(t *testing.T) {
	led := &fakeLedger{result: ledgerclient.IssueResult{Key: "WID-1"}}
	s, core := newTestServer(t, led)
	seedRepoWithBranch(t, core, "org-1", "widgets", "feature")

	req := openPullReq("org-1", "widgets",
		map[string]any{"source": "feature", "target": "main", "title": "Add X", "project": "WID"},
		"repo:write issue:write")
	rec := httptest.NewRecorder()
	s.handleOpenPull(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if led.calls != 1 {
		t.Fatalf("ledger calls = %d, want 1", led.calls)
	}
	if led.gotFwd.Get("X-CWB-Subject") != "agent-1" {
		t.Errorf("identity not forwarded: %v", led.gotFwd)
	}
	if led.gotIn.Project != "WID" || led.gotIn.Summary != "Add X" {
		t.Errorf("issue input = %+v", led.gotIn)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["ledger_issue_key"] != "WID-1" || out["state"] != "open" {
		t.Errorf("response = %v", out)
	}
}

func TestOpenPull_IdempotentReturnsExisting(t *testing.T) {
	led := &fakeLedger{result: ledgerclient.IssueResult{Key: "WID-1"}}
	s, core := newTestServer(t, led)
	seedRepoWithBranch(t, core, "org-1", "widgets", "feature")
	body := map[string]any{"source": "feature", "target": "main", "title": "Add X", "project": "WID"}

	rec1 := httptest.NewRecorder()
	s.handleOpenPull(rec1, openPullReq("org-1", "widgets", body, "repo:write issue:write"))
	rec2 := httptest.NewRecorder()
	s.handleOpenPull(rec2, openPullReq("org-1", "widgets", body, "repo:write issue:write"))

	if rec2.Code != http.StatusOK {
		t.Fatalf("second open status = %d, want 200: %s", rec2.Code, rec2.Body.String())
	}
	if led.calls != 1 {
		t.Fatalf("ledger calls = %d, want 1 (second open must not create a new issue)", led.calls)
	}
}

func TestOpenPull_Validation(t *testing.T) {
	led := &fakeLedger{result: ledgerclient.IssueResult{Key: "WID-1"}}
	s, core := newTestServer(t, led)
	seedRepoWithBranch(t, core, "org-1", "widgets", "feature")

	cases := []struct {
		name   string
		body   map[string]any
		scopes string
		want   int
	}{
		{"missing-title", map[string]any{"source": "feature", "target": "main", "project": "WID"}, "repo:write issue:write", http.StatusBadRequest},
		{"source-eq-target", map[string]any{"source": "main", "target": "main", "title": "x", "project": "WID"}, "repo:write issue:write", http.StatusBadRequest},
		{"unknown-source", map[string]any{"source": "nope", "target": "main", "title": "x", "project": "WID"}, "repo:write issue:write", http.StatusNotFound},
		{"no-repo-write", map[string]any{"source": "feature", "target": "main", "title": "x", "project": "WID"}, "issue:write", http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.handleOpenPull(rec, openPullReq("org-1", "widgets", c.body, c.scopes))
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, c.want, rec.Body.String())
			}
		})
	}
	if led.calls != 0 {
		t.Fatalf("ledger called %d times on validation failures, want 0", led.calls)
	}
}

func TestOpenPull_LedgerErrorMirroredNoPR(t *testing.T) {
	led := &fakeLedger{err: &ledgerclient.APIError{Status: http.StatusForbidden, Body: `{"error":"insufficient_scope"}`}}
	s, core := newTestServer(t, led)
	r := seedRepoWithBranch(t, core, "org-1", "widgets", "feature")

	rec := httptest.NewRecorder()
	s.handleOpenPull(rec, openPullReq("org-1", "widgets",
		map[string]any{"source": "feature", "target": "main", "title": "x", "project": "WID"},
		"repo:write issue:write"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (mirrored): %s", rec.Code, rec.Body.String())
	}
	// No PR row persisted.
	if _, err := core.FindOpenPull(context.Background(), r.ID, "feature", "main"); err == nil {
		t.Fatal("a PR row was persisted despite the ledger error")
	}
}

func TestGetPull(t *testing.T) {
	led := &fakeLedger{result: ledgerclient.IssueResult{Key: "WID-1"}}
	s, core := newTestServer(t, led)
	seedRepoWithBranch(t, core, "org-1", "widgets", "feature")
	rec := httptest.NewRecorder()
	s.handleOpenPull(rec, openPullReq("org-1", "widgets",
		map[string]any{"source": "feature", "target": "main", "title": "Add X", "project": "WID"}, "repo:write issue:write"))
	var opened map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &opened)
	id, _ := opened["id"].(string)

	greq := httptest.NewRequest(http.MethodGet, "/api/orgs/org-1/repos/widgets/pulls/"+id, nil)
	greq.Header.Set("X-CWB-Subject", "agent-1")
	greq.Header.Set("X-CWB-Org", "org-1")
	greq.Header.Set("X-CWB-Scopes", "repo:read")
	greq.SetPathValue("org", "org-1")
	greq.SetPathValue("slug", "widgets")
	greq.SetPathValue("id", id)
	grec := httptest.NewRecorder()
	s.handleGetPull(grec, greq)
	if grec.Code != http.StatusOK {
		t.Fatalf("GET pull = %d, want 200: %s", grec.Code, grec.Body.String())
	}
}

// seedFFRepo creates a repo where feature descends from main (a ff is possible)
// and returns the repo + the feature head sha.
func seedFFRepo(t *testing.T, core *repo.Service, org, slug string) (repo.Repo, string) {
	t.Helper()
	r, err := core.CreateRepo(context.Background(), org, slug)
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	g, err := git.PlainOpen(r.StoragePath)
	if err != nil {
		t.Fatalf("PlainOpen: %v", err)
	}
	a := writeHTTPDCommit(t, g, "A")
	b := writeHTTPDCommit(t, g, "B", a)
	setHTTPDBranch(t, g, "main", a)
	setHTTPDBranch(t, g, "feature", b)
	return r, b.String()
}

func writeHTTPDCommit(t *testing.T, g *git.Repository, msg string, parents ...plumbing.Hash) plumbing.Hash {
	t.Helper()
	c := &object.Commit{
		Author:       object.Signature{Name: "t", Email: "t@t", When: time.Unix(0, 0).UTC()},
		Committer:    object.Signature{Name: "t", Email: "t@t", When: time.Unix(0, 0).UTC()},
		Message:      msg, TreeHash: plumbing.ZeroHash, ParentHashes: parents,
	}
	enc := g.Storer.NewEncodedObject()
	if err := c.Encode(enc); err != nil {
		t.Fatalf("encode: %v", err)
	}
	h, err := g.Storer.SetEncodedObject(enc)
	if err != nil {
		t.Fatalf("set object: %v", err)
	}
	return h
}

func setHTTPDBranch(t *testing.T, g *git.Repository, name string, h plumbing.Hash) {
	t.Helper()
	if err := g.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName(name), h)); err != nil {
		t.Fatalf("set ref: %v", err)
	}
}

func mergeReq(org, slug, id, scopes string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/orgs/"+org+"/repos/"+slug+"/pulls/"+id+"/merge", nil)
	req.Header.Set("X-CWB-Subject", "agent-1")
	req.Header.Set("X-CWB-Org", org)
	req.Header.Set("X-CWB-Scopes", scopes)
	req.SetPathValue("org", org)
	req.SetPathValue("slug", slug)
	req.SetPathValue("id", id)
	return req
}

func seedOpenPull(t *testing.T, core *repo.Service, repoID string) repo.Pull {
	t.Helper()
	p := repo.Pull{RepoID: repoID, Source: "feature", Target: "main", Title: "Add X", LedgerIssueKey: "WID-1", OpenedBy: "agent-1"}
	if err := core.CreatePull(context.Background(), &p); err != nil {
		t.Fatalf("CreatePull: %v", err)
	}
	return p
}

func TestMergePull_FastForward(t *testing.T) {
	led := &fakeLedger{}
	s, core := newTestServer(t, led)
	r, featSHA := seedFFRepo(t, core, "org-1", "widgets")
	p := seedOpenPull(t, core, r.ID)

	rec := httptest.NewRecorder()
	s.handleMergePull(rec, mergeReq("org-1", "widgets", p.ID, "repo:write"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	ref, _ := core.GetRef(context.Background(), r.ID, "refs/heads/main")
	if ref.Hash != featSHA {
		t.Fatalf("main = %s, want %s", ref.Hash, featSHA)
	}
	got, _ := core.GetPull(context.Background(), r.ID, p.ID)
	if got.State != "merged" {
		t.Fatalf("state = %q, want merged", got.State)
	}
	if led.commentCalls != 1 || led.gotCommentKey != "WID-1" {
		t.Fatalf("comment calls=%d key=%q", led.commentCalls, led.gotCommentKey)
	}
}

func TestMergePull_NotOpen(t *testing.T) {
	led := &fakeLedger{}
	s, core := newTestServer(t, led)
	r, _ := seedFFRepo(t, core, "org-1", "widgets")
	p := seedOpenPull(t, core, r.ID)
	_ = core.SetPullState(context.Background(), r.ID, p.ID, "merged")

	rec := httptest.NewRecorder()
	s.handleMergePull(rec, mergeReq("org-1", "widgets", p.ID, "repo:write"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestMergePull_Diverged(t *testing.T) {
	led := &fakeLedger{}
	s, core := newTestServer(t, led)
	r, err := core.CreateRepo(context.Background(), "org-1", "widgets")
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	g, _ := git.PlainOpen(r.StoragePath)
	main := writeHTTPDCommit(t, g, "main-root")
	feat := writeHTTPDCommit(t, g, "feat-root") // independent root
	setHTTPDBranch(t, g, "main", main)
	setHTTPDBranch(t, g, "feature", feat)
	p := seedOpenPull(t, core, r.ID)

	rec := httptest.NewRecorder()
	s.handleMergePull(rec, mergeReq("org-1", "widgets", p.ID, "repo:write"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("diverged status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

func TestMergePull_NoRepoWrite(t *testing.T) {
	led := &fakeLedger{}
	s, core := newTestServer(t, led)
	r, _ := seedFFRepo(t, core, "org-1", "widgets")
	p := seedOpenPull(t, core, r.ID)
	rec := httptest.NewRecorder()
	s.handleMergePull(rec, mergeReq("org-1", "widgets", p.ID, "repo:read"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestMergePull_LedgerCommentFailureStillMerges(t *testing.T) {
	led := &fakeLedger{commentErr: &ledgerclient.APIError{Status: 500, Body: "boom"}}
	s, core := newTestServer(t, led)
	r, featSHA := seedFFRepo(t, core, "org-1", "widgets")
	p := seedOpenPull(t, core, r.ID)

	rec := httptest.NewRecorder()
	s.handleMergePull(rec, mergeReq("org-1", "widgets", p.ID, "repo:write"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (merge stands): %s", rec.Code, rec.Body.String())
	}
	ref, _ := core.GetRef(context.Background(), r.ID, "refs/heads/main")
	if ref.Hash != featSHA {
		t.Fatalf("main not advanced despite comment failure")
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["ledger_comment_error"] == "" {
		t.Fatalf("expected ledger_comment_error to be populated: %v", out)
	}
}
