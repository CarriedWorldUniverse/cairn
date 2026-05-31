# cairn → ledger PR-as-issue Integration Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Opening a pull request in cairn (`POST .../pulls`) creates a linked tracking issue in ledger, attributed to the opener, with a first-class cairn PR record.

**Architecture:** A new `pull_request` table + repo-core methods; a new `internal/ledger` outbound client (mirrors `internal/herald`) that forwards the gateway-injected `X-CWB-*` identity; new `httpd` open/get handlers that validate, dedupe, call ledger on-behalf-of the opener, and persist the PR. No new service identity, no ledger change.

**Tech Stack:** Go 1.26, `net/http` (Go 1.22 method+wildcard routing), go-git (refs), modernc.org/sqlite, `testing` + `httptest`.

**Spec:** `docs/cairn/specs/2026-05-31-cairn-ledger-pr-integration-spec.md`

---

## File structure

- **Create** `internal/ledger/client.go` — the cairn→ledger HTTP client (`Client`, `IssueInput`, `ExternalRef`, `IssueResult`, `APIError`, `CreateIssue`). One responsibility: turn an issue-create call + a forwarded identity into a ledger REST request.
- **Create** `internal/ledger/client_test.go` — httptest-based tests of the client.
- **Modify** `internal/repo/schema.sql` — add the `pull_request` table + partial-unique index.
- **Modify** `internal/repo/service.go` — `Pull` type, `ErrPullNotFound`, `CreatePull`, `GetPull`, `FindOpenPull`.
- **Create** `internal/repo/pulls_test.go` — repo-core PR tests.
- **Create** `internal/httpd/pulls.go` — the `IssueCreator` interface, `handleOpenPull`, `handleGetPull`, `forwardCWB`.
- **Create** `internal/httpd/pulls_test.go` — handler tests with a fake `IssueCreator`.
- **Modify** `internal/httpd/server.go` — `Config` gains `Ledger IssueCreator` + `PublicBase string`; register the two `pulls` routes.
- **Modify** `cmd/cairn-server/main.go` — read `LEDGER_BASE_URL` (+ optional `CAIRN_PUBLIC_BASE`), construct the ledger client, inject into `httpd.Config`.

---

## Task 1: `pull_request` table + repo-core methods

**Files:**
- Modify: `internal/repo/schema.sql`
- Modify: `internal/repo/service.go`
- Test: `internal/repo/pulls_test.go`

- [ ] **Step 1: Add the schema**

Append to `internal/repo/schema.sql`:

```sql

CREATE TABLE IF NOT EXISTS pull_request (
  id               TEXT PRIMARY KEY,            -- 16-byte hex
  repo_id          TEXT NOT NULL REFERENCES repo(id) ON DELETE CASCADE,
  source_ref       TEXT NOT NULL,               -- branch name, e.g. "feature"
  target_ref       TEXT NOT NULL,               -- branch name, e.g. "main"
  title            TEXT NOT NULL,
  ledger_issue_key TEXT NOT NULL,               -- e.g. "ACME-7"
  state            TEXT NOT NULL DEFAULT 'open', -- 'open' | 'merged' | 'closed'
  opened_by        TEXT NOT NULL,               -- X-CWB-Subject of the opener
  created_at       TEXT NOT NULL                -- RFC3339
);

-- At most one OPEN pr per (repo, source, target).
CREATE UNIQUE INDEX IF NOT EXISTS pr_open_uniq
  ON pull_request(repo_id, source_ref, target_ref) WHERE state = 'open';
```

- [ ] **Step 2: Write the failing test**

Create `internal/repo/pulls_test.go`:

```go
package repo

import (
	"context"
	"errors"
	"testing"
)

func TestCreateGetFindOpenPull(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)

	r, err := svc.CreateRepo(ctx, "org-1", "widgets")
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	p := Pull{
		RepoID: r.ID, Source: "feature", Target: "main",
		Title: "Add X", LedgerIssueKey: "WID-1", OpenedBy: "agent-1",
	}
	if err := svc.CreatePull(ctx, &p); err != nil {
		t.Fatalf("CreatePull: %v", err)
	}
	if p.ID == "" || p.State != PullStateOpen {
		t.Fatalf("CreatePull did not populate id/state: %+v", p)
	}

	got, err := svc.GetPull(ctx, r.ID, p.ID)
	if err != nil {
		t.Fatalf("GetPull: %v", err)
	}
	if got.LedgerIssueKey != "WID-1" || got.Source != "feature" {
		t.Fatalf("GetPull mismatch: %+v", got)
	}

	open, err := svc.FindOpenPull(ctx, r.ID, "feature", "main")
	if err != nil {
		t.Fatalf("FindOpenPull: %v", err)
	}
	if open.ID != p.ID {
		t.Fatalf("FindOpenPull id = %s, want %s", open.ID, p.ID)
	}

	// No open PR for a different pair → ErrPullNotFound.
	if _, err := svc.FindOpenPull(ctx, r.ID, "other", "main"); !errors.Is(err, ErrPullNotFound) {
		t.Fatalf("FindOpenPull(other) err = %v, want ErrPullNotFound", err)
	}

	// A second open PR for the same (repo, source, target) is rejected by the index.
	dup := Pull{RepoID: r.ID, Source: "feature", Target: "main", Title: "dup", LedgerIssueKey: "WID-2", OpenedBy: "agent-1"}
	if err := svc.CreatePull(ctx, &dup); err == nil {
		t.Fatal("CreatePull duplicate open PR: want error, got nil")
	}
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `cd /Users/jacinta/Source/cairn && go test ./internal/repo/ -run TestCreateGetFindOpenPull`
Expected: FAIL — `undefined: Pull` / `CreatePull` / `PullStateOpen` / `ErrPullNotFound`.

- [ ] **Step 4: Implement the type + methods**

In `internal/repo/service.go`, add near the `Repo` type:

```go
// PullStateOpen is the only state this build writes; merged/closed are reserved.
const PullStateOpen = "open"

// ErrPullNotFound is returned when no pull request matches.
var ErrPullNotFound = errors.New("repo: pull request not found")

// Pull is a row in the pull_request catalogue: a source→target proposal bound
// to a ledger tracking issue.
type Pull struct {
	ID             string
	RepoID         string
	Source         string
	Target         string
	Title          string
	LedgerIssueKey string
	State          string
	OpenedBy       string
	CreatedAt      time.Time
}
```

Add the methods (anywhere after `CreateRepo`):

```go
// CreatePull inserts an open pull request. The partial-unique index rejects a
// second open PR for the same (repo, source, target). Populates p.ID/State.
func (s *Service) CreatePull(ctx context.Context, p *Pull) error {
	if p.RepoID == "" || p.Source == "" || p.Target == "" || p.Title == "" || p.LedgerIssueKey == "" {
		return errors.New("repo.CreatePull: repo, source, target, title, ledger_issue_key required")
	}
	p.ID = newID()
	p.State = PullStateOpen
	p.CreatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO pull_request(id, repo_id, source_ref, target_ref, title, ledger_issue_key, state, opened_by, created_at)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		p.ID, p.RepoID, p.Source, p.Target, p.Title, p.LedgerIssueKey, p.State, p.OpenedBy,
		p.CreatedAt.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("repo.CreatePull: %w", err)
	}
	return nil
}

const pullCols = `id, repo_id, source_ref, target_ref, title, ledger_issue_key, state, opened_by, created_at`

func scanPull(row interface{ Scan(...any) error }) (Pull, error) {
	var p Pull
	var created string
	if err := row.Scan(&p.ID, &p.RepoID, &p.Source, &p.Target, &p.Title,
		&p.LedgerIssueKey, &p.State, &p.OpenedBy, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Pull{}, ErrPullNotFound
		}
		return Pull{}, err
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return p, nil
}

// GetPull loads a pull request by (repo, id).
func (s *Service) GetPull(ctx context.Context, repoID, id string) (Pull, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+pullCols+` FROM pull_request WHERE repo_id=? AND id=?`, repoID, id)
	p, err := scanPull(row)
	if err != nil {
		return Pull{}, fmt.Errorf("repo.GetPull: %w", err)
	}
	return p, nil
}

// FindOpenPull returns the open pull request for (repo, source, target), or
// ErrPullNotFound. Used for idempotent open.
func (s *Service) FindOpenPull(ctx context.Context, repoID, source, target string) (Pull, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+pullCols+` FROM pull_request
		 WHERE repo_id=? AND source_ref=? AND target_ref=? AND state='open'`,
		repoID, source, target)
	p, err := scanPull(row)
	if err != nil {
		return Pull{}, fmt.Errorf("repo.FindOpenPull: %w", err)
	}
	return p, nil
}
```

(`errors`, `fmt`, `sql`, `time` are already imported in `service.go`.)

- [ ] **Step 5: Run it to verify it passes**

Run: `cd /Users/jacinta/Source/cairn && go test ./internal/repo/ -run TestCreateGetFindOpenPull`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /Users/jacinta/Source/cairn
git add internal/repo/schema.sql internal/repo/service.go internal/repo/pulls_test.go
git commit -m "repo: pull_request table + CreatePull/GetPull/FindOpenPull"
```

---

## Task 2: cairn → ledger client (`internal/ledger`)

**Files:**
- Create: `internal/ledger/client.go`
- Test: `internal/ledger/client_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/ledger/client_test.go`:

```go
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
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd /Users/jacinta/Source/cairn && go test ./internal/ledger/`
Expected: FAIL — package `internal/ledger` does not exist / `undefined: NewClient`.

- [ ] **Step 3: Implement the client**

Create `internal/ledger/client.go`:

```go
// Package ledger is cairn's outbound client to the CWB issues/tracker service.
// cairn calls it in-cluster (ledger.cwb.svc) to open a tracking issue when a
// pull request is opened, FORWARDING the gateway-injected X-CWB-* identity of
// the opener so the issue is created on their behalf. Mirrors internal/herald.
package ledger

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Client calls ledger's REST API.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient builds a client against ledger's base URL (e.g.
// "http://ledger.cwb.svc:8081").
func NewClient(baseURL string, hc *http.Client) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: hc}
}

// ExternalRef links the issue back to the cairn branch/PR.
type ExternalRef struct {
	Tracker     string `json:"tracker"`
	Key         string `json:"key"`
	URL         string `json:"url,omitempty"`
	Description string `json:"description,omitempty"`
}

// IssueInput is the body of a create-issue request.
type IssueInput struct {
	Project          string        `json:"project"`
	Type             string        `json:"type"`
	Summary          string        `json:"summary"`
	Description      string        `json:"description,omitempty"`
	DefinitionOfDone string        `json:"definition_of_done,omitempty"`
	ExternalRefs     []ExternalRef `json:"external_refs,omitempty"`
}

// IssueResult is the decoded create-issue response (the parts cairn needs).
type IssueResult struct {
	Key string `json:"key"`
}

// APIError is a non-2xx ledger response. The handler mirrors Status back to the
// caller so a scope/validation failure surfaces unchanged.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("ledger: status %d: %s", e.Status, e.Body)
}

// cwbHeaders are the trusted identity headers cairn forwards to ledger.
var cwbHeaders = []string{"X-Cwb-Subject", "X-Cwb-Org", "X-Cwb-Kind", "X-Cwb-Scopes", "X-Cwb-Responsible-Human"}

// CreateIssue POSTs /api/issues with the forwarded identity. A non-2xx response
// is returned as *APIError; a transport failure as a plain wrapped error.
func (c *Client) CreateIssue(ctx context.Context, fwd http.Header, in IssueInput) (IssueResult, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return IssueResult{}, fmt.Errorf("ledger.CreateIssue: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/issues", bytes.NewReader(body))
	if err != nil {
		return IssueResult{}, fmt.Errorf("ledger.CreateIssue: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, h := range cwbHeaders {
		if v := fwd.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return IssueResult{}, fmt.Errorf("ledger.CreateIssue: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return IssueResult{}, &APIError{Status: resp.StatusCode, Body: string(raw)}
	}
	var out IssueResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return IssueResult{}, fmt.Errorf("ledger.CreateIssue: decode: %w", err)
	}
	if out.Key == "" {
		return IssueResult{}, fmt.Errorf("ledger.CreateIssue: empty key in response: %s", raw)
	}
	return out, nil
}
```

Note on header canonicalisation: `http.Header.Get`/`Set` canonicalise `X-CWB-Subject` → `X-Cwb-Subject`, so the constants above match regardless of the gateway's casing.

- [ ] **Step 4: Run it to verify it passes**

Run: `cd /Users/jacinta/Source/cairn && go test ./internal/ledger/`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
cd /Users/jacinta/Source/cairn
git add internal/ledger/client.go internal/ledger/client_test.go
git commit -m "ledger: cairn→ledger client (CreateIssue forwards X-CWB-* identity)"
```

---

## Task 3: `httpd` open/get PR handlers

**Files:**
- Create: `internal/httpd/pulls.go`
- Modify: `internal/httpd/server.go`
- Test: `internal/httpd/pulls_test.go`

- [ ] **Step 1: Wire the dependency into `Config` + register routes**

In `internal/httpd/server.go`, change `Config` and `Server`, and register the routes.

Replace the `Config` struct with:

```go
// IssueCreator is the slice of ledger cairn needs: open a tracking issue on
// behalf of a caller (identity forwarded via fwd). *ledger.Client satisfies it;
// tests use a fake.
type IssueCreator interface {
	CreateIssue(ctx context.Context, fwd http.Header, in ledgerclient.IssueInput) (ledgerclient.IssueResult, error)
}

// Config configures the HTTP ingress.
type Config struct {
	Core       *repo.Service
	GitPath    string       // path to the git binary; defaults to "git" on PATH
	Ledger     IssueCreator // outbound ledger client for PR-as-issue
	PublicBase string       // optional public base URL for building ExternalRef.url; "" omits url
}
```

Add the import (alias to avoid clashing with the package name `httpd`'s local vocabulary):

```go
	ledgerclient "github.com/CarriedWorldUniverse/cairn/internal/ledger"
```

and `"context"` if not already imported.

Store the new fields on `Server`:

```go
type Server struct {
	cfg     Config
	gitPath string
}
```

(unchanged — `Server` already holds `cfg`; the handlers read `s.cfg.Ledger` / `s.cfg.PublicBase`.)

In `Handler()`, register the PR routes BEFORE the `"/"` catch-all (Go 1.22 patterns are more specific than `/`, so order is not strictly required, but keep them grouped with the other `/api` routes):

```go
	mux.HandleFunc("POST /api/orgs/{org}/repos/{slug}/pulls", s.handleOpenPull)
	mux.HandleFunc("GET /api/orgs/{org}/repos/{slug}/pulls/{id}", s.handleGetPull)
```

- [ ] **Step 2: Write the failing test**

Create `internal/httpd/pulls_test.go`:

```go
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
	calls   int
	gotFwd  http.Header
	gotIn   ledgerclient.IssueInput
	result  ledgerclient.IssueResult
	err     error
}

func (f *fakeLedger) CreateIssue(_ context.Context, fwd http.Header, in ledgerclient.IssueInput) (ledgerclient.IssueResult, error) {
	f.calls++
	f.gotFwd, f.gotIn = fwd, in
	return f.result, f.err
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

// seedRepoWithBranch creates a repo and a branch ref so the PR validation passes.
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
```

The test file also needs `mustSeedRef`, which writes a branch ref into the bare repo so `Core.GetRef` resolves it (the PR handler validates that `source`/`target` exist). The imports it needs (`bytes`, `time`, the three go-git packages) are already in the import block from Step 2.

- [ ] **Step 3: Add the `mustSeedRef` test helper**

Append to `internal/httpd/pulls_test.go`:

```go
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
	// A minimal commit object so the ref points at a real hash.
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
```

> `internal/repo/seed_test.go` has the same ref-seeding pattern (`store.SetReference(plumbing.NewHashReference(...))`); the above is the equivalent standalone form. `TreeHash: plumbing.ZeroHash` is fine here because the PR handler only reads the ref's hash, not the tree.

- [ ] **Step 4: Run it to verify it fails**

Run: `cd /Users/jacinta/Source/cairn && go test ./internal/httpd/ -run 'TestOpenPull|TestGetPull'`
Expected: FAIL — `undefined: (*Server).handleOpenPull` / `handleGetPull`.

- [ ] **Step 5: Implement the handlers**

Create `internal/httpd/pulls.go`:

```go
package httpd

import (
	"encoding/json"
	"errors"
	"net/http"

	ledgerclient "github.com/CarriedWorldUniverse/cairn/internal/ledger"
	"github.com/CarriedWorldUniverse/cairn/internal/repo"
)

type openPullBody struct {
	Source           string `json:"source"`
	Target           string `json:"target"`
	Title            string `json:"title"`
	Description      string `json:"description"`
	Project          string `json:"project"`
	DefinitionOfDone string `json:"definition_of_done"`
}

type pullResponse struct {
	ID             string `json:"id"`
	Repo           string `json:"repo"`
	Source         string `json:"source"`
	Target         string `json:"target"`
	Title          string `json:"title"`
	State          string `json:"state"`
	LedgerIssueKey string `json:"ledger_issue_key"`
	URL            string `json:"url,omitempty"`
}

func toPullResponse(p repo.Pull, slug, publicBase, org string) pullResponse {
	url := ""
	if publicBase != "" {
		url = publicBase + "/" + org + "/" + slug
	}
	return pullResponse{
		ID: p.ID, Repo: slug, Source: p.Source, Target: p.Target,
		Title: p.Title, State: p.State, LedgerIssueKey: p.LedgerIssueKey, URL: url,
	}
}

// handleOpenPull opens a PR: validate, dedupe, create the ledger issue on behalf
// of the opener, persist the PR. See the spec §7 flow.
func (s *Server) handleOpenPull(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFromHeaders(r)
	if !ok {
		httpErr(w, http.StatusUnauthorized, "missing identity")
		return
	}
	org := r.PathValue("org")
	slug := r.PathValue("slug")
	if id.Org != org {
		httpErr(w, http.StatusForbidden, "org mismatch")
		return
	}
	if !id.HasScope("repo:write") {
		httpErr(w, http.StatusForbidden, "missing scope repo:write")
		return
	}

	var body openPullBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.Source == "" || body.Target == "" || body.Title == "" || body.Project == "" {
		httpErr(w, http.StatusBadRequest, "source, target, title, project required")
		return
	}
	if body.Source == body.Target {
		httpErr(w, http.StatusBadRequest, "source and target must differ")
		return
	}

	rp, err := s.cfg.Core.GetRepo(r.Context(), org, slug)
	if err != nil {
		httpErr(w, http.StatusNotFound, "repo not found")
		return
	}
	srcRef, err := s.cfg.Core.GetRef(r.Context(), rp.ID, "refs/heads/"+body.Source)
	if err != nil {
		httpErr(w, http.StatusNotFound, "source branch not found")
		return
	}
	if _, err := s.cfg.Core.GetRef(r.Context(), rp.ID, "refs/heads/"+body.Target); err != nil {
		httpErr(w, http.StatusNotFound, "target branch not found")
		return
	}

	// Idempotency: an open PR for this (repo, source, target) already exists.
	if existing, err := s.cfg.Core.FindOpenPull(r.Context(), rp.ID, body.Source, body.Target); err == nil {
		writeJSON(w, http.StatusOK, toPullResponse(existing, slug, s.cfg.PublicBase, org))
		return
	} else if !errors.Is(err, repo.ErrPullNotFound) {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Create the ledger issue on behalf of the opener.
	headSHA := srcRef.Hash
	if len(headSHA) > 12 {
		headSHA = headSHA[:12]
	}
	ref := ledgerclient.ExternalRef{
		Tracker:     "cairn",
		Key:         org + "/" + slug + "@" + body.Source,
		Description: body.Source + "→" + body.Target + " @ " + headSHA,
	}
	if s.cfg.PublicBase != "" {
		ref.URL = s.cfg.PublicBase + "/" + org + "/" + slug
	}
	res, err := s.cfg.Ledger.CreateIssue(r.Context(), forwardCWB(r), ledgerclient.IssueInput{
		Project: body.Project, Type: "Story", Summary: body.Title,
		Description: body.Description, DefinitionOfDone: body.DefinitionOfDone,
		ExternalRefs: []ledgerclient.ExternalRef{ref},
	})
	if err != nil {
		var apiErr *ledgerclient.APIError
		if errors.As(err, &apiErr) {
			httpErr(w, apiErr.Status, "ledger rejected issue: "+apiErr.Body)
			return
		}
		httpErr(w, http.StatusBadGateway, "ledger unreachable: "+err.Error())
		return
	}

	p := repo.Pull{
		RepoID: rp.ID, Source: body.Source, Target: body.Target,
		Title: body.Title, LedgerIssueKey: res.Key, OpenedBy: id.Subject,
	}
	if err := s.cfg.Core.CreatePull(r.Context(), &p); err != nil {
		httpErr(w, http.StatusInternalServerError, "persist pull: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toPullResponse(p, slug, s.cfg.PublicBase, org))
}

// handleGetPull returns a PR by id.
func (s *Server) handleGetPull(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFromHeaders(r)
	if !ok {
		httpErr(w, http.StatusUnauthorized, "missing identity")
		return
	}
	org := r.PathValue("org")
	slug := r.PathValue("slug")
	if id.Org != org {
		httpErr(w, http.StatusForbidden, "org mismatch")
		return
	}
	if !id.HasScope("repo:read") {
		httpErr(w, http.StatusForbidden, "missing scope repo:read")
		return
	}
	rp, err := s.cfg.Core.GetRepo(r.Context(), org, slug)
	if err != nil {
		httpErr(w, http.StatusNotFound, "repo not found")
		return
	}
	p, err := s.cfg.Core.GetPull(r.Context(), rp.ID, r.PathValue("id"))
	if err != nil {
		httpErr(w, http.StatusNotFound, "pull not found")
		return
	}
	writeJSON(w, http.StatusOK, toPullResponse(p, slug, s.cfg.PublicBase, org))
}

// forwardCWB copies the trusted X-CWB-* identity headers from the inbound
// request so cairn can act on behalf of the caller against ledger.
func forwardCWB(r *http.Request) http.Header {
	out := http.Header{}
	for _, h := range []string{"X-Cwb-Subject", "X-Cwb-Org", "X-Cwb-Kind", "X-Cwb-Scopes", "X-Cwb-Responsible-Human"} {
		if v := r.Header.Get(h); v != "" {
			out.Set(h, v)
		}
	}
	return out
}

// writeJSON writes v as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
```

(`pulls.go` does not import `context` — the handlers use `r.Context()`. The `context` import lives in `server.go`, for the `IssueCreator` interface signature.)

- [ ] **Step 6: Run it to verify it passes**

Run: `cd /Users/jacinta/Source/cairn && go test ./internal/httpd/ -run 'TestOpenPull|TestGetPull'`
Expected: PASS (all subtests).

- [ ] **Step 7: Full package vet + build**

Run: `cd /Users/jacinta/Source/cairn && go vet ./... && go build ./...`
Expected: no output (success).

- [ ] **Step 8: Commit**

```bash
cd /Users/jacinta/Source/cairn
git add internal/httpd/server.go internal/httpd/pulls.go internal/httpd/pulls_test.go
git commit -m "httpd: open/get pull-request handlers (create ledger issue on behalf of opener)"
```

---

## Task 4: wire the ledger client in `cmd/cairn-server`

**Files:**
- Modify: `cmd/cairn-server/main.go`

- [ ] **Step 1: Read the env + construct the client + inject**

In `cmd/cairn-server/main.go`, add the imports:

```go
	ledgerclient "github.com/CarriedWorldUniverse/cairn/internal/ledger"
```

In `main()`, after the herald client is built and before `httpd.New(...)`, add:

```go
	// ledger client for PR-as-issue (cairn opens a tracking issue on PR open,
	// forwarding the opener's identity). In-cluster, like the herald client.
	ledgerBase := env("LEDGER_BASE_URL", "http://ledger.cwb.svc:8081")
	ledgerCli := ledgerclient.NewClient(ledgerBase, nil)
	publicBase := env("CAIRN_PUBLIC_BASE", "") // optional; "" omits ExternalRef.url
```

Change the `httpd.New` call from:

```go
	httpSrv := httpd.New(httpd.Config{Core: core})
```

to:

```go
	httpSrv := httpd.New(httpd.Config{Core: core, Ledger: ledgerCli, PublicBase: publicBase})
```

Update the config doc comment block at the top of the file to mention `LEDGER_BASE_URL` and `CAIRN_PUBLIC_BASE`.

- [ ] **Step 2: Build + run the full test suite**

Run: `cd /Users/jacinta/Source/cairn && go build ./... && go test ./...`
Expected: PASS across all packages.

- [ ] **Step 3: Commit**

```bash
cd /Users/jacinta/Source/cairn
git add cmd/cairn-server/main.go
git commit -m "cairn-server: wire ledger client (LEDGER_BASE_URL, CAIRN_PUBLIC_BASE)"
```

---

## Task 5: deploy + make the conformance journey assert it (cross-repo)

**Files:**
- Modify (cairn deploy): rebuild + redeploy on dMon (no manifest change needed — ledger is already deployed and `LEDGER_BASE_URL` defaults to `ledger.cwb.svc:8081`; set it explicitly in `deploy/k3s/20-deployment.yaml` for clarity).
- Modify (separate `cwb-conformance` change): `conformance/journey/journey_test.go`.

- [ ] **Step 1: Add `LEDGER_BASE_URL` to the cairn deployment (clarity)**

In `deploy/k3s/20-deployment.yaml`, add to the cairn container `env:` list:

```yaml
            - name: LEDGER_BASE_URL
              value: "http://ledger.cwb.svc:8081"
```

Commit:

```bash
cd /Users/jacinta/Source/cairn
git add deploy/k3s/20-deployment.yaml
git commit -m "cairn(k3s): set LEDGER_BASE_URL for PR-as-issue"
```

- [ ] **Step 2: Open the PR for this branch, merge after review**

This is the normal merge flow for the `feat/pr-as-ledger-issue` branch (cairn is branch-protected). After merge, rebuild + redeploy cairn on dMon:

```bash
ssh jacinta@100.91.185.71 'cd ~/src/cairn && git checkout cairn && git pull \
  && podman build -q -f cmd/cairn-server/Containerfile -t localhost/cairn:dev . \
  && podman save localhost/cairn:dev | sudo k3s ctr images import - \
  && sudo kubectl rollout restart deployment/cairn -n cwb \
  && sudo kubectl rollout status deployment/cairn -n cwb --timeout=120s'
```

- [ ] **Step 3: Flip the conformance journey step 3 from simulated to asserted**

In `cwb-conformance` `conformance/journey/journey_test.go`, replace the client-side issue creation (the "SIMULATED cairn→ledger" step) with: call cairn `POST /api/orgs/{org}/repos/{slug}/pulls {source:"feature", target:"main", title, project}`, assert `201`, then assert the returned `ledger_issue_key` resolves in ledger (GET the issue, reporter == builder, ExternalRef key == `<org>/<slug>@feature`). Remove the `(a) cairn→ledger PR-as-issue auto-integration` line from the SIMULATED log and add a log noting it is now asserted.

Run it against dMon:

```bash
ssh jacinta@100.91.185.71 'cd ~/src/cwb-conformance && git pull \
  && CIP=$(sudo kubectl get svc herald -n cwb -o jsonpath="{.spec.clusterIP}") \
  && ADMIN=$(sudo kubectl get secret herald-secrets -n cwb -o jsonpath="{.data.admin_token}" | base64 -d) \
  && CWB_ADMIN_TOKEN="$ADMIN" CWB_HERALD_ADMIN_URL="http://$CIP:8099" CWB_RUN_ID="pr$(date +%s)" \
     go run ./cmd/cwb-conform -target dmon -layers journey'
```
Expected: `PASS` — the journey now exercises the real integration.

- [ ] **Step 4: Commit the conformance change (in cwb-conformance) + PR**

```bash
cd /Users/jacinta/Source/cwb-conformance
git add conformance/journey/journey_test.go
git commit -m "journey: assert real cairn→ledger PR-as-issue (was client-simulated)"
```

---

## Notes for the implementer

- **TDD order matters:** Tasks 1–3 are red→green→commit. Task 3 depends on Tasks 1 & 2 (the `repo.Pull` type and `ledgerclient` package). Do them in order.
- **`GetRef` returns `repo.Ref{Name, Hash}`** — `Hash` is the 40-char hex string; the handler truncates to 12 for the human-readable ExternalRef description.
- **Header canonicalisation** is why the forwarded/constant header names use `X-Cwb-*` (Go's canonical form); `Get`/`Set` normalise, so inbound `X-CWB-Subject` is found.
- **YAGNI:** do not add merge/close/list endpoints, issue-update-on-push, or a repo→project map. They are named future work in the spec.
