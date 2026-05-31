# cairn server-side merge op Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Merge a pull request server-side in cairn — fast-forward the target branch to the source tip, mark the PR `merged`, and best-effort comment the linked ledger issue.

**Architecture:** A new `POST .../pulls/{id}/merge` handler calls a new `repo.FastForward` (go-git in-process ancestor check + ref update — fast-forward only, so it's branch-protection-safe by construction), flips the `pull_request` state, then best-effort comments the linked ledger issue via a new `ledger.Client.CommentIssue` (forwarding the merger's `X-CWB-*`).

**Tech Stack:** Go 1.26, go-git v5 (`Commit.IsAncestor`, `Storer.SetReference`), `net/http`, modernc.org/sqlite, `testing` + `httptest`.

**Spec:** `docs/cairn/specs/2026-05-31-cairn-merge-op-spec.md`

---

## File structure

- **Modify** `internal/repo/service.go` — `ErrNotFastForward`, `ErrAlreadyUpToDate`, `FastForward`, `SetPullState`.
- **Create** `internal/repo/merge_test.go` — repo-core merge/state tests (+ a commit-writing helper).
- **Modify** `internal/ledger/client.go` — `CommentIssue`.
- **Modify** `internal/ledger/client_test.go` — `CommentIssue` test.
- **Modify** `internal/httpd/server.go` — extend the `IssueCreator` interface with `CommentIssue`; register the merge route.
- **Modify** `internal/httpd/pulls.go` — `handleMergePull` + `mergeResponse`.
- **Modify** `internal/httpd/pulls_test.go` — add `CommentIssue` to `fakeLedger`; merge handler tests (+ a ff-seeding helper).

No `cmd/cairn-server` change: the existing `*ledger.Client` gains the method and `Config.Ledger` is already injected.

---

## Task 1: repo-core `FastForward` + `SetPullState`

**Files:**
- Modify: `internal/repo/service.go`
- Test: `internal/repo/merge_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/repo/merge_test.go`:

```go
package repo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// writeCommit writes a commit object (zero tree — only history matters for
// ancestor checks) with the given parents and returns its hash.
func writeCommit(t *testing.T, g *git.Repository, msg string, parents ...plumbing.Hash) plumbing.Hash {
	t.Helper()
	c := &object.Commit{
		Author:       object.Signature{Name: "t", Email: "t@t", When: time.Unix(0, 0).UTC()},
		Committer:    object.Signature{Name: "t", Email: "t@t", When: time.Unix(0, 0).UTC()},
		Message:      msg,
		TreeHash:     plumbing.ZeroHash,
		ParentHashes: parents,
	}
	enc := g.Storer.NewEncodedObject()
	if err := c.Encode(enc); err != nil {
		t.Fatalf("encode commit: %v", err)
	}
	h, err := g.Storer.SetEncodedObject(enc)
	if err != nil {
		t.Fatalf("set object: %v", err)
	}
	return h
}

func setBranch(t *testing.T, g *git.Repository, name string, h plumbing.Hash) {
	t.Helper()
	if err := g.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName(name), h)); err != nil {
		t.Fatalf("set ref %s: %v", name, err)
	}
}

func TestFastForward(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	r, err := svc.CreateRepo(ctx, "org-1", "widgets")
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	g, err := git.PlainOpen(r.StoragePath)
	if err != nil {
		t.Fatalf("PlainOpen: %v", err)
	}

	// main = A; feature = A→B  →  main is ancestor of feature  →  ff.
	a := writeCommit(t, g, "A")
	b := writeCommit(t, g, "B", a)
	setBranch(t, g, "main", a)
	setBranch(t, g, "feature", b)

	sha, err := svc.FastForward(ctx, r.ID, "feature", "main")
	if err != nil {
		t.Fatalf("FastForward (ff case): %v", err)
	}
	if sha != b.String() {
		t.Fatalf("merged sha = %s, want %s", sha, b.String())
	}
	// main now points at B.
	ref, _ := svc.GetRef(ctx, r.ID, "refs/heads/main")
	if ref.Hash != b.String() {
		t.Fatalf("main = %s, want %s (advanced)", ref.Hash, b.String())
	}

	// Already up to date: merge feature(=A, ancestor of main=B) into main(=B).
	setBranch(t, g, "main", b)
	setBranch(t, g, "old", a)
	if _, err := svc.FastForward(ctx, r.ID, "old", "main"); !errors.Is(err, ErrAlreadyUpToDate) {
		t.Fatalf("up-to-date err = %v, want ErrAlreadyUpToDate", err)
	}

	// Diverged: two unrelated roots → not a fast-forward.
	c := writeCommit(t, g, "C") // independent root
	setBranch(t, g, "diverged", c)
	setBranch(t, g, "trunk", a)
	if _, err := svc.FastForward(ctx, r.ID, "diverged", "trunk"); !errors.Is(err, ErrNotFastForward) {
		t.Fatalf("diverged err = %v, want ErrNotFastForward", err)
	}
}

func TestSetPullState(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	r, _ := svc.CreateRepo(ctx, "org-1", "widgets")
	p := Pull{RepoID: r.ID, Source: "feature", Target: "main", Title: "x", LedgerIssueKey: "WID-1", OpenedBy: "a"}
	if err := svc.CreatePull(ctx, &p); err != nil {
		t.Fatalf("CreatePull: %v", err)
	}
	if err := svc.SetPullState(ctx, r.ID, p.ID, "merged"); err != nil {
		t.Fatalf("SetPullState: %v", err)
	}
	got, _ := svc.GetPull(ctx, r.ID, p.ID)
	if got.State != "merged" {
		t.Fatalf("state = %q, want merged", got.State)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd /Users/jacinta/Source/cairn && go test ./internal/repo/ -run 'TestFastForward|TestSetPullState'`
Expected: FAIL — `undefined: FastForward` / `SetPullState` / `ErrAlreadyUpToDate` / `ErrNotFastForward`.

- [ ] **Step 3: Implement the methods**

In `internal/repo/service.go`, add the sentinels near `ErrPullNotFound`:

```go
// ErrNotFastForward means source and target have diverged; a fast-forward merge
// is impossible (the caller should rebase). ErrAlreadyUpToDate means target
// already contains source (no ref change needed).
var (
	ErrNotFastForward   = errors.New("repo: not a fast-forward")
	ErrAlreadyUpToDate  = errors.New("repo: already up to date")
)
```

Add the methods (after `FindOpenPull`):

```go
// FastForward advances refs/heads/<target> to the tip of refs/heads/<source>
// when target is an ancestor of source. Returns the resulting target sha.
//   - target already contains source     → ErrAlreadyUpToDate (no change)
//   - target is a strict ancestor of src  → advance target, return src sha
//   - diverged                             → ErrNotFastForward
// A missing branch returns a wrapped ErrNotFound.
func (s *Service) FastForward(ctx context.Context, repoID, source, target string) (string, error) {
	g, err := s.openGit(ctx, repoID)
	if err != nil {
		return "", err
	}
	srcRef, err := g.Reference(plumbing.NewBranchReferenceName(source), true)
	if err != nil {
		return "", fmt.Errorf("repo.FastForward: source %q: %w: %w", source, ErrNotFound, err)
	}
	tgtRef, err := g.Reference(plumbing.NewBranchReferenceName(target), true)
	if err != nil {
		return "", fmt.Errorf("repo.FastForward: target %q: %w: %w", target, ErrNotFound, err)
	}
	if srcRef.Hash() == tgtRef.Hash() {
		return tgtRef.Hash().String(), ErrAlreadyUpToDate
	}
	srcCommit, err := g.CommitObject(srcRef.Hash())
	if err != nil {
		return "", fmt.Errorf("repo.FastForward: load source commit: %w", err)
	}
	tgtCommit, err := g.CommitObject(tgtRef.Hash())
	if err != nil {
		return "", fmt.Errorf("repo.FastForward: load target commit: %w", err)
	}
	// target already contains source → nothing to do.
	if ok, _ := srcCommit.IsAncestor(tgtCommit); ok {
		return tgtRef.Hash().String(), ErrAlreadyUpToDate
	}
	// target is an ancestor of source → fast-forward.
	if ok, _ := tgtCommit.IsAncestor(srcCommit); ok {
		newRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(target), srcRef.Hash())
		if err := g.Storer.SetReference(newRef); err != nil {
			return "", fmt.Errorf("repo.FastForward: advance target: %w", err)
		}
		return srcRef.Hash().String(), nil
	}
	return "", ErrNotFastForward
}

// SetPullState updates a pull request's state (e.g. to "merged").
func (s *Service) SetPullState(ctx context.Context, repoID, id, state string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE pull_request SET state=? WHERE repo_id=? AND id=?`, state, repoID, id)
	if err != nil {
		return fmt.Errorf("repo.SetPullState: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrPullNotFound
	}
	return nil
}
```

`ErrNotFound` already exists in `service.go` (used by `GetRepo`); `plumbing`, `fmt`, `errors` are already imported.

- [ ] **Step 4: Run it to verify it passes**

Run: `cd /Users/jacinta/Source/cairn && go test ./internal/repo/ -run 'TestFastForward|TestSetPullState'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jacinta/Source/cairn
git add internal/repo/service.go internal/repo/merge_test.go
git commit -m "repo: FastForward (ff-only merge) + SetPullState"
```

---

## Task 2: ledger-client `CommentIssue`

**Files:**
- Modify: `internal/ledger/client.go`
- Test: `internal/ledger/client_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/ledger/client_test.go`:

```go
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
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd /Users/jacinta/Source/cairn && go test ./internal/ledger/ -run TestCommentIssue`
Expected: FAIL — `c.CommentIssue undefined`.

- [ ] **Step 3: Implement `CommentIssue`**

Append to `internal/ledger/client.go` (uses the existing `cwbHeaders`, `APIError`, imports):

```go
// CommentIssue POSTs a comment to /api/issues/{key}/comments with the forwarded
// identity. A non-2xx response is *APIError; a transport failure a wrapped error.
// Callers (the merge handler) treat both as best-effort.
func (c *Client) CommentIssue(ctx context.Context, fwd http.Header, key, body string) error {
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return fmt.Errorf("ledger.CommentIssue: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/issues/"+key+"/comments", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("ledger.CommentIssue: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, h := range cwbHeaders {
		if v := fwd.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("ledger.CommentIssue: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return &APIError{Status: resp.StatusCode, Body: string(raw)}
	}
	return nil
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `cd /Users/jacinta/Source/cairn && go test ./internal/ledger/`
Expected: PASS (all tests).

- [ ] **Step 5: Commit**

```bash
cd /Users/jacinta/Source/cairn
git add internal/ledger/client.go internal/ledger/client_test.go
git commit -m "ledger: CommentIssue (post a comment, forwards X-CWB-*)"
```

---

## Task 3: httpd `handleMergePull`

**Files:**
- Modify: `internal/httpd/server.go`
- Modify: `internal/httpd/pulls.go`
- Test: `internal/httpd/pulls_test.go`

- [ ] **Step 1: Extend the interface + register the route**

In `internal/httpd/server.go`, extend `IssueCreator`:

```go
type IssueCreator interface {
	CreateIssue(ctx context.Context, fwd http.Header, in ledgerclient.IssueInput) (ledgerclient.IssueResult, error)
	CommentIssue(ctx context.Context, fwd http.Header, key, body string) error
}
```

Register the route (with the other pulls routes, before the `/` catch-all):

```go
	mux.HandleFunc("POST /api/orgs/{org}/repos/{slug}/pulls/{id}/merge", s.handleMergePull)
```

- [ ] **Step 2: Add `CommentIssue` to the test fake + write the failing tests**

In `internal/httpd/pulls_test.go`, extend the `fakeLedger` struct with three comment-recording fields, and add the `CommentIssue` method so the fake satisfies the extended `IssueCreator` interface:

```go
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

func (f *fakeLedger) CommentIssue(_ context.Context, fwd http.Header, key, body string) error {
	f.commentCalls++
	f.gotCommentKey = key
	return f.commentErr
}
```

Add a fast-forwardable seeding helper + the merge tests:

```go
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
	// main advanced to feature tip.
	ref, _ := core.GetRef(context.Background(), r.ID, "refs/heads/main")
	if ref.Hash != featSHA {
		t.Fatalf("main = %s, want %s", ref.Hash, featSHA)
	}
	// PR is merged.
	got, _ := core.GetPull(context.Background(), r.ID, p.ID)
	if got.State != "merged" {
		t.Fatalf("state = %q, want merged", got.State)
	}
	// linked issue was commented, on behalf of the merger.
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
```

Add the new imports to `internal/httpd/pulls_test.go` (`time`, the three go-git packages are already imported from the PR-integration tests; `repo`, `ledgerclient` already imported).

- [ ] **Step 3: Run it to verify it fails**

Run: `cd /Users/jacinta/Source/cairn && go test ./internal/httpd/ -run TestMergePull`
Expected: FAIL — `s.handleMergePull undefined`.

- [ ] **Step 4: Implement `handleMergePull`**

Append to `internal/httpd/pulls.go`:

```go
type mergeResponse struct {
	ID                 string `json:"id"`
	State              string `json:"state"`
	Target             string `json:"target"`
	MergedSHA          string `json:"merged_sha"`
	LedgerCommentError string `json:"ledger_comment_error,omitempty"`
}

// handleMergePull fast-forward-merges an open PR's source into its target, marks
// the PR merged, and best-effort comments the linked ledger issue. See spec §4.
func (s *Server) handleMergePull(w http.ResponseWriter, r *http.Request) {
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

	rp, err := s.cfg.Core.GetRepo(r.Context(), org, slug)
	if err != nil {
		httpErr(w, http.StatusNotFound, "repo not found")
		return
	}
	pull, err := s.cfg.Core.GetPull(r.Context(), rp.ID, r.PathValue("id"))
	if err != nil {
		httpErr(w, http.StatusNotFound, "pull not found")
		return
	}
	if pull.State != repo.PullStateOpen {
		httpErr(w, http.StatusConflict, "pull is not open")
		return
	}

	mergedSHA, ffErr := s.cfg.Core.FastForward(r.Context(), rp.ID, pull.Source, pull.Target)
	switch {
	case errors.Is(ffErr, repo.ErrAlreadyUpToDate):
		// already merged content; fall through to mark merged.
	case errors.Is(ffErr, repo.ErrNotFastForward):
		httpErr(w, http.StatusConflict, "not fast-forwardable; rebase "+pull.Source+" onto "+pull.Target)
		return
	case errors.Is(ffErr, repo.ErrNotFound):
		httpErr(w, http.StatusConflict, "source or target branch missing")
		return
	case ffErr != nil:
		httpErr(w, http.StatusInternalServerError, "merge: "+ffErr.Error())
		return
	}

	if err := s.cfg.Core.SetPullState(r.Context(), rp.ID, pull.ID, "merged"); err != nil {
		httpErr(w, http.StatusInternalServerError, "set state: "+err.Error())
		return
	}

	// Best-effort: tell the linked ledger issue. A failure here does NOT undo the
	// merge — the ref update is the source of truth.
	resp := mergeResponse{ID: pull.ID, State: "merged", Target: pull.Target, MergedSHA: mergedSHA}
	sha12 := mergedSHA
	if len(sha12) > 12 {
		sha12 = sha12[:12]
	}
	body := "merged " + pull.Source + " into " + pull.Target + " @ " + sha12
	if cErr := s.cfg.Ledger.CommentIssue(r.Context(), forwardCWB(r), pull.LedgerIssueKey, body); cErr != nil {
		resp.LedgerCommentError = cErr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}
```

(`errors`, `repo`, `net/http` already imported in `pulls.go`.)

- [ ] **Step 5: Run it to verify it passes**

Run: `cd /Users/jacinta/Source/cairn && go test ./internal/httpd/ -run TestMergePull`
Expected: PASS (all subtests).

- [ ] **Step 6: Full vet + build + test**

Run: `cd /Users/jacinta/Source/cairn && go vet ./... && go build ./... && go test ./...`
Expected: PASS across all packages.

- [ ] **Step 7: Commit**

```bash
cd /Users/jacinta/Source/cairn
git add internal/httpd/server.go internal/httpd/pulls.go internal/httpd/pulls_test.go
git commit -m "httpd: handleMergePull (ff merge + mark merged + best-effort ledger comment)"
```

---

## Task 4: deploy + flip the conformance journey merge step

**Files:**
- (cairn) PR + merge `feat/cairn-merge-op`, redeploy on dMon (no manifest change).
- (separate `cwb-conformance` change) `conformance/journey/journey_test.go`.

- [ ] **Step 1: Open + merge the cairn PR; redeploy**

```bash
cd /Users/jacinta/Source/cairn && git push -u origin feat/cairn-merge-op
gh pr create --base cairn --title "cairn: server-side merge op (ff-only) on pull requests" --body "Implements docs/cairn/specs/2026-05-31-cairn-merge-op-spec.md."
# after merge:
ssh jacinta@100.91.185.71 'cd ~/src/cairn && git checkout cairn && git pull \
  && podman build -q -f cmd/cairn-server/Containerfile -t localhost/cairn:dev . \
  && podman save localhost/cairn:dev | sudo k3s ctr images import - \
  && sudo kubectl rollout restart deployment/cairn -n cwb \
  && sudo kubectl rollout status deployment/cairn -n cwb --timeout=120s'
```

- [ ] **Step 2: Flip the journey merge step (in `cwb-conformance`)**

In `conformance/journey/journey_test.go`, replace the client-side fast-forward block (the `=== 5. merge ===` step that does `git checkout main` / `git merge --ff-only feature` / `git push origin main`) with a call to the cairn merge endpoint:

```go
	// === 5. merge: cairn server-side fast-forward merge (real op) ===
	mergeURL := tgt.GatewayURL + tgt.CairnPath + "/api/orgs/" + org.OrgID + "/repos/" + repo + "/pulls/" + prID + "/merge"
	if resp, raw := lpost(t, mergeURL, builder.Token, map[string]any{}); resp.StatusCode != http.StatusOK {
		t.Fatalf("merge = %d, want 200: %s", resp.StatusCode, raw)
	}
	t.Log("cairn: PR fast-forward-merged server-side")
```

This needs the PR id, so `openPull` (step 2 of the journey) must return it. Change `openPull` to decode and return both the issue key and the PR id (`{ "id", "ledger_issue_key" }`), and capture `prID` at the call site. Update the SIMULATED log to drop the server-side-merge item (only human-review remains simulated). The verify step (step 6 — fresh clone HEAD == featSHA) is unchanged and now confirms the *server-side* merge landed.

Run it:

```bash
ssh jacinta@100.91.185.71 'cd ~/src/cwb-conformance && git pull \
  && CIP=$(sudo kubectl get svc herald -n cwb -o jsonpath="{.spec.clusterIP}") \
  && ADMIN=$(sudo kubectl get secret herald-secrets -n cwb -o jsonpath="{.data.admin_token}" | base64 -d) \
  && CWB_ADMIN_TOKEN="$ADMIN" CWB_HERALD_ADMIN_URL="http://$CIP:8099" CWB_RUN_ID="mrg$(date +%s)" \
     go run ./cmd/cwb-conform -target dmon -layers journey'
```
Expected: `PASS` — the merge step now exercises the real cairn op.

- [ ] **Step 3: Commit the conformance change (in cwb-conformance) + PR**

```bash
cd /Users/jacinta/Source/cwb-conformance
git add conformance/journey/journey_test.go
git commit -m "journey: assert real cairn server-side merge (was client ff-merge)"
```

---

## Notes for the implementer

- **`FastForward` semantics:** the order of checks matters — equal-hash and source-ancestor-of-target both mean *already up to date* (no ref change); only target-ancestor-of-source advances the ref. Anything else is `ErrNotFastForward`.
- **Protection is not invoked** by the merge op — that's intentional and safe because a fast-forward is exactly what the protection hook would allow; a future merge-commit strategy must re-check `protect.Allow`.
- **Best-effort comment:** never return non-200 because the ledger comment failed; surface it in `ledger_comment_error` instead. The merge (ref + state) already happened.
- **Zero-tree commits** in tests are fine: `IsAncestor` walks parent history, not trees.
- **YAGNI:** no merge-commit/squash, no conflict handling, no issue transition, no review gating — all named future work in the spec.
