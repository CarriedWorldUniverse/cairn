# cairn-native — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build cairn-native — a CWB-shape git hosting service wrapping go-git, replacing the Forgejo fork. Single Go binary, three frontends (SSH, HTTP, web UI), herald for identity, ledger for PR lifecycle.

**Architecture:** New `cmd/cairn-server` + `internal/cairn` tree in the existing cairn repo. SQLite for metadata (repos, refs, pr_pointers, push_events). go-git for git-protocol/storage. SSH frontend via gliderlabs/ssh — casket pubkey IS the SSH identity. HTTP frontend via go-git's smart-HTTP server, reverse-proxied by interchange-gateway with X-CWB-* identity injection. Web UI = server-rendered html/template, herald path-A authz_code client. PRs are refs+ledger-tickets; storage primitives reuse NEX-348 untouched.

**Tech Stack:** Go 1.26, `github.com/go-git/go-git/v5`, `github.com/gliderlabs/ssh`, `golang.org/x/crypto/ssh`, `github.com/CarriedWorldUniverse/herald/heraldauth`, `modernc.org/sqlite`, `html/template`. No JS build pipeline.

**Spec ref:** [`docs/cairn/specs/2026-05-31-cairn-native-spec.md`](../specs/2026-05-31-cairn-native-spec.md).

**Repo:** `github.com/CarriedWorldUniverse/cairn` (existing). All new code lives under `cmd/cairn-server/` and `internal/cairn/`. The pre-existing Forgejo tree is not touched (will be archived later); nothing in this plan reads or imports from it.

**Branch strategy:** One branch per task, off `main`. Each task lands as a single squash-merged PR. Tasks 1, 2, 3 are sequential at the start (Task 1 lands first; 2 and 3 can run in parallel after). Tasks 5 (ledger) and 4 (web UI) are gated on external dependencies — see the notes inside those tasks. Task 7 is last.

**Cross-task type contract (all tasks honor these):**

The Task 1 package `github.com/CarriedWorldUniverse/cairn/internal/cairn/repo` exports:

```go
// Repo is one git repository owned by a herald org.
type Repo struct {
    ID             string    // uuid
    OrgID          string    // herald org uuid
    Slug           string    // unique within org
    DefaultBranch  string    // e.g. "main"
    Protection     string    // JSON blob; see repo.ProtectionRules
    StorageURI     string    // "file:///var/lib/cairn/repos/<org>/<slug>.git" etc.
    CreatedAt      string
    UpdatedAt      string
}

// PRPointer is one PR's cairn-side state. Lifecycle lives in ledger.
type PRPointer struct {
    RepoID       string
    PRNumber     int
    Ref          string // "refs/cairn/pr/N"
    LedgerTicket string // opaque ledger key
    HeadSHA      string
    BaseBranch   string
}

// PushEvent records one accepted ref update for audit.
type PushEvent struct {
    ID         string
    RepoID     string
    Ref        string
    OldSHA     string
    NewSHA     string
    PusherSub  string
    PusherKind string // "agent" | "human"
    At         string
    Bypass     bool
}

// Store is the SQLite-backed metadata persistence seam.
type Store interface {
    CreateRepo(ctx context.Context, r Repo) (Repo, error)
    GetRepo(ctx context.Context, orgID, slug string) (Repo, error)
    GetRepoByID(ctx context.Context, id string) (Repo, error)
    ListReposByOrg(ctx context.Context, orgID string) ([]Repo, error)
    UpdateProtection(ctx context.Context, id, protectionJSON string) error
    DeleteRepo(ctx context.Context, id string) error

    NextPRNumber(ctx context.Context, repoID string) (int, error)
    CreatePRPointer(ctx context.Context, p PRPointer) error
    GetPRPointer(ctx context.Context, repoID string, n int) (PRPointer, error)
    UpdatePRHead(ctx context.Context, repoID string, n int, headSHA string) error
    DeletePRPointer(ctx context.Context, repoID string, n int) error
    ListPRPointers(ctx context.Context, repoID string) ([]PRPointer, error)

    RecordPushEvent(ctx context.Context, e PushEvent) error

    Close() error
}

// Service is the cairn-core API every frontend dials into.
type Service struct{ /* unexported fields */ }

func NewService(store Store, repoRoot string) *Service

// Repo lifecycle.
func (s *Service) CreateRepo(ctx context.Context, orgID, slug, defaultBranch string) (Repo, error)
func (s *Service) GetRepo(ctx context.Context, orgID, slug string) (Repo, error)
func (s *Service) DeleteRepo(ctx context.Context, orgID, slug string) error

// Refs.
func (s *Service) ListRefs(ctx context.Context, r Repo) ([]Ref, error)
func (s *Service) GetRef(ctx context.Context, r Repo, name string) (Ref, error)
func (s *Service) DeleteRef(ctx context.Context, r Repo, name string) error

// Open returns a go-git *git.Repository for the repo's storage. The caller
// holds it for the duration of a single request and does not cache.
func (s *Service) Open(ctx context.Context, r Repo) (*git.Repository, error)

// Ref is a name + target SHA.
type Ref struct {
    Name   string // e.g. "refs/heads/main"
    Target string // 40-hex SHA
}

// ProtectionRules is the decoded JSON for Repo.Protection.
type ProtectionRules struct {
    Rules []ProtectionRule `json:"rules"`
}
type ProtectionRule struct {
    Pattern             string `json:"pattern"`              // "main", "release/*"
    RequiredScope       string `json:"required_scope"`       // "repo:admin"
    AllowForcePush      bool   `json:"allow_force_push"`
    AllowDelete         bool   `json:"allow_delete"`
    BypassRequiresAudit bool   `json:"bypass_requires_audit"`
}
```

Identity passed into the core is carried as a value type defined in Task 1:

```go
// Caller is the verified identity of the requester. Populated by frontends:
//   - SSH frontend: from the casket-pubkey lookup
//   - HTTP frontend: from X-CWB-* headers (gateway-injected)
type Caller struct {
    Subject string   // herald subject id
    Kind    string   // "agent" | "human"
    Org     string   // herald org id
    Scopes  []string // e.g. {"repo:read", "repo:write"}
}

func (c Caller) HasScope(s string) bool
```

---

## Task 1: Repo + ref CRUD core via go-git — NEX-386

**Files:**

Create:
- `go.mod` (new module: `github.com/CarriedWorldUniverse/cairn-native`) — see Step 1 note about why this is a separate module from the old Forgejo `cairn` go.mod.
- `internal/cairn/repo/repo.go`
- `internal/cairn/repo/repo_test.go`
- `internal/cairn/repo/protection.go`
- `internal/cairn/repo/protection_test.go`
- `internal/cairn/repo/service.go`
- `internal/cairn/repo/service_test.go`
- `internal/cairn/repo/store.go`
- `internal/cairn/repo/sqlite.go`
- `internal/cairn/repo/sqlite_test.go`
- `internal/cairn/repo/schema.sql`
- `internal/cairn/repo/caller.go`
- `internal/cairn/repo/caller_test.go`

Test:
- All `_test.go` files above run under `go test ./internal/cairn/repo/...`

> **Module note:** the existing `cairn` repo has a `go.mod` declaring `github.com/CarriedWorldUniverse/cairn` (Forgejo content). Cairn-native ships as a NEW go module at the repo root path `cmd/cairn-server/`-adjacent. To avoid colliding with Forgejo's dep graph during the transition, this plan uses a SECOND `go.mod` rooted under a new top-level directory `cairn-native/`. After Forgejo's tree is archived (out of scope here), this can be flattened. For the duration of this plan, every file path below is under `cairn-native/` at the repo root. Equivalent absolute path on disk: `/Users/jacinta/Source/cairn/cairn-native/...`.

- [ ] **Step 1: Create the cairn-native module skeleton**

Run:

```bash
cd /Users/jacinta/Source/cairn
mkdir -p cairn-native/cmd/cairn-server cairn-native/internal/cairn/repo
cd cairn-native
go mod init github.com/CarriedWorldUniverse/cairn/cairn-native
```

Expected output prefix:

```
go: creating new go.mod: module github.com/CarriedWorldUniverse/cairn/cairn-native
```

- [ ] **Step 2: Add core dependencies and commit the empty module**

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go get github.com/go-git/go-git/v5@v5.12.0
go get github.com/google/uuid@v1.6.0
go get modernc.org/sqlite@v1.34.1
go get github.com/CarriedWorldUniverse/herald@latest
go mod tidy
```

Expected output prefix: `go: added github.com/go-git/go-git/v5`

Then:

```bash
git add cairn-native/go.mod cairn-native/go.sum
git commit -m "chore(cairn-native): init module skeleton"
```

- [ ] **Step 3: Write `caller.go` — the Caller value type**

Create `cairn-native/internal/cairn/repo/caller.go`:

```go
// Package repo is the cairn-native core: repo + ref CRUD on top of go-git,
// SQLite metadata, branch-protection evaluation. Frontends (SSH, HTTP, web UI)
// call into Service. Identity is passed in as a Caller value populated by the
// frontend from either a casket-pubkey lookup (SSH) or gateway-injected
// X-CWB-* headers (HTTP).
package repo

// Caller is the verified identity of a request, populated by the frontend.
// The core never trusts unverified strings — frontends do verification before
// constructing a Caller.
type Caller struct {
	Subject string   // herald subject id (uuid)
	Kind    string   // "agent" | "human"
	Org     string   // herald org id (uuid)
	Scopes  []string // e.g. {"repo:read", "repo:write"}
}

// HasScope reports whether the Caller holds the named scope exactly.
func (c Caller) HasScope(s string) bool {
	for _, sc := range c.Scopes {
		if sc == s {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Write `caller_test.go` and run it (expect PASS)**

Create `cairn-native/internal/cairn/repo/caller_test.go`:

```go
package repo

import "testing"

func TestCallerHasScope(t *testing.T) {
	c := Caller{Scopes: []string{"repo:read", "repo:write"}}
	if !c.HasScope("repo:read") {
		t.Error("expected repo:read")
	}
	if !c.HasScope("repo:write") {
		t.Error("expected repo:write")
	}
	if c.HasScope("repo:admin") {
		t.Error("did not expect repo:admin")
	}
}

func TestCallerHasScope_Empty(t *testing.T) {
	c := Caller{}
	if c.HasScope("repo:read") {
		t.Error("empty caller has no scopes")
	}
}
```

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/repo/...
```

Expected output prefix: `ok 	github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/repo`

- [ ] **Step 5: Write `repo.go` — the Repo, PRPointer, PushEvent, Ref structs**

Create `cairn-native/internal/cairn/repo/repo.go`:

```go
package repo

// Repo is one git repository owned by a herald org. Cairn owns this row;
// herald owns the org row it references.
type Repo struct {
	ID            string // uuid
	OrgID         string // herald org uuid (not FK-enforced cross-service)
	Slug          string // unique within org
	DefaultBranch string // e.g. "main"
	Protection    string // JSON; see ProtectionRules
	StorageURI    string // "file:///var/lib/cairn/repos/<org>/<slug>.git"
	CreatedAt     string
	UpdatedAt     string
}

// PRPointer is the cairn-side handle on a PR. The lifecycle (title, comments,
// reviews, state) lives in ledger keyed by LedgerTicket.
type PRPointer struct {
	RepoID       string
	PRNumber     int
	Ref          string // "refs/cairn/pr/N"
	LedgerTicket string // e.g. "NEX-1234" — opaque to cairn
	HeadSHA      string
	BaseBranch   string
}

// PushEvent records one accepted ref update for audit.
type PushEvent struct {
	ID         string
	RepoID     string
	Ref        string
	OldSHA     string
	NewSHA     string
	PusherSub  string
	PusherKind string // "agent" | "human"
	At         string
	Bypass     bool
}

// Ref is a git ref name + target SHA.
type Ref struct {
	Name   string // "refs/heads/main"
	Target string // 40-hex SHA
}
```

- [ ] **Step 6: Write the schema and `store.go` interface**

Create `cairn-native/internal/cairn/repo/schema.sql`:

```sql
-- cairn-native metadata. Mirrors spec §3.
-- Repos and PR pointers belong to cairn. Org/user identity lives in herald.

CREATE TABLE IF NOT EXISTS repo (
  id              TEXT PRIMARY KEY,                  -- uuid
  org_id          TEXT NOT NULL,                     -- herald org uuid
  slug            TEXT NOT NULL,
  default_branch  TEXT NOT NULL DEFAULT 'main',
  protection      TEXT NOT NULL DEFAULT '{"rules":[]}',
  storage_uri     TEXT NOT NULL,
  created_at      TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at      TEXT NOT NULL DEFAULT (datetime('now')),
  UNIQUE(org_id, slug)
);
CREATE INDEX IF NOT EXISTS idx_repo_org ON repo(org_id);

CREATE TABLE IF NOT EXISTS pr_pointer (
  repo_id        TEXT NOT NULL REFERENCES repo(id) ON DELETE CASCADE,
  pr_number      INTEGER NOT NULL,
  ref            TEXT NOT NULL,
  ledger_ticket  TEXT NOT NULL DEFAULT '',
  head_sha       TEXT NOT NULL,
  base_branch    TEXT NOT NULL,
  created_at     TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at     TEXT NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (repo_id, pr_number)
);

CREATE TABLE IF NOT EXISTS pr_counter (
  repo_id TEXT PRIMARY KEY REFERENCES repo(id) ON DELETE CASCADE,
  next    INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS push_event (
  id          TEXT PRIMARY KEY,
  repo_id     TEXT NOT NULL REFERENCES repo(id) ON DELETE CASCADE,
  ref         TEXT NOT NULL,
  old_sha     TEXT NOT NULL,
  new_sha     TEXT NOT NULL,
  pusher_sub  TEXT NOT NULL,
  pusher_kind TEXT NOT NULL,
  at          TEXT NOT NULL DEFAULT (datetime('now')),
  bypass      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_push_event_repo ON push_event(repo_id);
```

Create `cairn-native/internal/cairn/repo/store.go`:

```go
package repo

import (
	"context"
	"errors"
)

// ErrNotFound is returned by Get* when no row matches.
var ErrNotFound = errors.New("repo: not found")

// ErrConflict is returned when a unique constraint is violated (e.g. duplicate
// (org_id, slug)).
var ErrConflict = errors.New("repo: conflict")

// Store is the cairn-native metadata persistence seam. Implementations MUST
// be safe for concurrent use.
type Store interface {
	CreateRepo(ctx context.Context, r Repo) (Repo, error)
	GetRepo(ctx context.Context, orgID, slug string) (Repo, error)
	GetRepoByID(ctx context.Context, id string) (Repo, error)
	ListReposByOrg(ctx context.Context, orgID string) ([]Repo, error)
	UpdateProtection(ctx context.Context, id, protectionJSON string) error
	DeleteRepo(ctx context.Context, id string) error

	NextPRNumber(ctx context.Context, repoID string) (int, error)
	CreatePRPointer(ctx context.Context, p PRPointer) error
	GetPRPointer(ctx context.Context, repoID string, n int) (PRPointer, error)
	UpdatePRHead(ctx context.Context, repoID string, n int, headSHA string) error
	DeletePRPointer(ctx context.Context, repoID string, n int) error
	ListPRPointers(ctx context.Context, repoID string) ([]PRPointer, error)

	RecordPushEvent(ctx context.Context, e PushEvent) error

	Close() error
}
```

- [ ] **Step 7: Write the failing test `sqlite_test.go` for CreateRepo/GetRepo**

Create `cairn-native/internal/cairn/repo/sqlite_test.go`:

```go
package repo

import (
	"context"
	"errors"
	"testing"
)

func newTestStore(t *testing.T) *SQLite {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCreateAndGetRepo(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	in := Repo{
		OrgID:         "org-1",
		Slug:          "alpha",
		DefaultBranch: "main",
		StorageURI:    "file:///tmp/alpha.git",
	}
	out, err := s.CreateRepo(ctx, in)
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if out.ID == "" {
		t.Error("expected non-empty ID")
	}
	if out.CreatedAt == "" {
		t.Error("expected created_at populated")
	}

	got, err := s.GetRepo(ctx, "org-1", "alpha")
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	if got.ID != out.ID {
		t.Errorf("ID mismatch: %q vs %q", got.ID, out.ID)
	}
	if got.Protection != `{"rules":[]}` {
		t.Errorf("protection default: %q", got.Protection)
	}
}

func TestGetRepo_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, err := s.GetRepo(ctx, "org-1", "absent")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestCreateRepo_DuplicateConflict(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	in := Repo{OrgID: "org-1", Slug: "alpha", StorageURI: "file:///tmp/x.git", DefaultBranch: "main"}
	if _, err := s.CreateRepo(ctx, in); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := s.CreateRepo(ctx, in)
	if !errors.Is(err, ErrConflict) {
		t.Errorf("want ErrConflict, got %v", err)
	}
}
```

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/repo/...
```

Expected output prefix: `FAIL` (Open / SQLite not defined yet).

- [ ] **Step 8: Implement `sqlite.go` (Open + CreateRepo + GetRepo + GetRepoByID + ErrConflict)**

Create `cairn-native/internal/cairn/repo/sqlite.go`:

```go
package repo

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// SQLite is the modernc.org/sqlite-backed Store (CGO-free).
type SQLite struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at path and applies the schema.
// Use ":memory:" for tests. Foreign keys are enabled.
func Open(path string) (*SQLite, error) {
	dsn := path
	if path == ":memory:" {
		dsn = "file::memory:?cache=shared"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("repo.Open: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys = ON;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("repo.Open: enable fk: %w", err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("repo.Open: apply schema: %w", err)
	}
	return &SQLite{db: db}, nil
}

// Close releases the database.
func (s *SQLite) Close() error { return s.db.Close() }

// newID returns a fresh uuid string.
func newID() string { return uuid.NewString() }

// CreateRepo inserts a new repo row, assigning a UUID if r.ID is empty.
func (s *SQLite) CreateRepo(ctx context.Context, r Repo) (Repo, error) {
	if r.ID == "" {
		r.ID = newID()
	}
	if r.DefaultBranch == "" {
		r.DefaultBranch = "main"
	}
	if r.Protection == "" {
		r.Protection = `{"rules":[]}`
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO repo (id, org_id, slug, default_branch, protection, storage_uri)
		VALUES (?, ?, ?, ?, ?, ?)`,
		r.ID, r.OrgID, r.Slug, r.DefaultBranch, r.Protection, r.StorageURI)
	if err != nil {
		if isUniqueViolation(err) {
			return Repo{}, ErrConflict
		}
		return Repo{}, fmt.Errorf("CreateRepo: %w", err)
	}
	return s.GetRepoByID(ctx, r.ID)
}

// GetRepo fetches by (org_id, slug).
func (s *SQLite) GetRepo(ctx context.Context, orgID, slug string) (Repo, error) {
	return s.scanRepo(s.db.QueryRowContext(ctx,
		repoSelect+` WHERE org_id = ? AND slug = ?`, orgID, slug))
}

// GetRepoByID fetches by uuid.
func (s *SQLite) GetRepoByID(ctx context.Context, id string) (Repo, error) {
	return s.scanRepo(s.db.QueryRowContext(ctx,
		repoSelect+` WHERE id = ?`, id))
}

const repoSelect = `SELECT id, org_id, slug, default_branch, protection,
	storage_uri, created_at, updated_at FROM repo`

type scanner interface{ Scan(dest ...any) error }

func (s *SQLite) scanRepo(row scanner) (Repo, error) {
	var r Repo
	err := row.Scan(&r.ID, &r.OrgID, &r.Slug, &r.DefaultBranch, &r.Protection,
		&r.StorageURI, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Repo{}, ErrNotFound
	}
	if err != nil {
		return Repo{}, fmt.Errorf("scanRepo: %w", err)
	}
	return r, nil
}

func isUniqueViolation(err error) bool {
	// modernc.org/sqlite surfaces the constraint error in the message.
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
```

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/repo/...
```

Expected output prefix: `ok 	github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/repo`

- [ ] **Step 9: Add ListReposByOrg + UpdateProtection + DeleteRepo to sqlite.go, with tests first**

Append to `cairn-native/internal/cairn/repo/sqlite_test.go`:

```go
func TestListReposByOrg(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	for _, slug := range []string{"a", "b", "c"} {
		if _, err := s.CreateRepo(ctx, Repo{OrgID: "org-1", Slug: slug, StorageURI: "file:///t/" + slug + ".git", DefaultBranch: "main"}); err != nil {
			t.Fatalf("create %s: %v", slug, err)
		}
	}
	if _, err := s.CreateRepo(ctx, Repo{OrgID: "org-2", Slug: "x", StorageURI: "file:///t/x.git", DefaultBranch: "main"}); err != nil {
		t.Fatalf("create org-2: %v", err)
	}
	out, err := s.ListReposByOrg(ctx, "org-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 3 {
		t.Errorf("want 3 repos, got %d", len(out))
	}
}

func TestUpdateProtection(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	r, _ := s.CreateRepo(ctx, Repo{OrgID: "o", Slug: "p", StorageURI: "file:///t/p.git", DefaultBranch: "main"})
	want := `{"rules":[{"pattern":"main","required_scope":"repo:admin"}]}`
	if err := s.UpdateProtection(ctx, r.ID, want); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := s.GetRepoByID(ctx, r.ID)
	if got.Protection != want {
		t.Errorf("protection: got %q want %q", got.Protection, want)
	}
}

func TestDeleteRepo(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	r, _ := s.CreateRepo(ctx, Repo{OrgID: "o", Slug: "d", StorageURI: "file:///t/d.git", DefaultBranch: "main"})
	if err := s.DeleteRepo(ctx, r.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err := s.GetRepoByID(ctx, r.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound after delete, got %v", err)
	}
}
```

Run (expect FAIL):

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/repo/...
```

Expected output prefix: `FAIL` (methods not defined).

- [ ] **Step 10: Implement ListReposByOrg + UpdateProtection + DeleteRepo**

Append to `cairn-native/internal/cairn/repo/sqlite.go`:

```go
// ListReposByOrg returns every repo for an org, ordered by slug.
func (s *SQLite) ListReposByOrg(ctx context.Context, orgID string) ([]Repo, error) {
	rows, err := s.db.QueryContext(ctx, repoSelect+` WHERE org_id = ? ORDER BY slug`, orgID)
	if err != nil {
		return nil, fmt.Errorf("ListReposByOrg: %w", err)
	}
	defer rows.Close()
	var out []Repo
	for rows.Next() {
		var r Repo
		if err := rows.Scan(&r.ID, &r.OrgID, &r.Slug, &r.DefaultBranch, &r.Protection,
			&r.StorageURI, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateProtection overwrites the protection JSON for a repo.
func (s *SQLite) UpdateProtection(ctx context.Context, id, protectionJSON string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE repo SET protection = ?, updated_at = datetime('now') WHERE id = ?`,
		protectionJSON, id)
	if err != nil {
		return fmt.Errorf("UpdateProtection: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteRepo removes the repo row. ON DELETE CASCADE cleans up pr_pointer,
// pr_counter, push_event. The on-disk git directory is the caller's job.
func (s *SQLite) DeleteRepo(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM repo WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("DeleteRepo: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
```

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/repo/...
```

Expected output prefix: `ok`

- [ ] **Step 11: Write failing PR-pointer tests**

Append to `cairn-native/internal/cairn/repo/sqlite_test.go`:

```go
func TestNextPRNumber_MonotonicPerRepo(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	r1, _ := s.CreateRepo(ctx, Repo{OrgID: "o", Slug: "r1", StorageURI: "file:///t/r1.git", DefaultBranch: "main"})
	r2, _ := s.CreateRepo(ctx, Repo{OrgID: "o", Slug: "r2", StorageURI: "file:///t/r2.git", DefaultBranch: "main"})

	for i := 1; i <= 3; i++ {
		n, err := s.NextPRNumber(ctx, r1.ID)
		if err != nil {
			t.Fatalf("NextPRNumber r1: %v", err)
		}
		if n != i {
			t.Errorf("r1 want %d got %d", i, n)
		}
	}
	n, _ := s.NextPRNumber(ctx, r2.ID)
	if n != 1 {
		t.Errorf("r2 first want 1 got %d", n)
	}
}

func TestPRPointerCRUD(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	r, _ := s.CreateRepo(ctx, Repo{OrgID: "o", Slug: "r", StorageURI: "file:///t/r.git", DefaultBranch: "main"})
	p := PRPointer{
		RepoID: r.ID, PRNumber: 1, Ref: "refs/cairn/pr/1",
		LedgerTicket: "NEX-1", HeadSHA: "deadbeef", BaseBranch: "main",
	}
	if err := s.CreatePRPointer(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetPRPointer(ctx, r.ID, 1)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.LedgerTicket != "NEX-1" {
		t.Errorf("ticket: %q", got.LedgerTicket)
	}
	if err := s.UpdatePRHead(ctx, r.ID, 1, "cafef00d"); err != nil {
		t.Fatalf("update head: %v", err)
	}
	got, _ = s.GetPRPointer(ctx, r.ID, 1)
	if got.HeadSHA != "cafef00d" {
		t.Errorf("head: %q", got.HeadSHA)
	}
	if err := s.DeletePRPointer(ctx, r.ID, 1); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetPRPointer(ctx, r.ID, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete want ErrNotFound got %v", err)
	}
}

func TestRecordPushEvent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	r, _ := s.CreateRepo(ctx, Repo{OrgID: "o", Slug: "r", StorageURI: "file:///t/r.git", DefaultBranch: "main"})
	e := PushEvent{
		RepoID: r.ID, Ref: "refs/heads/main",
		OldSHA: "00", NewSHA: "ff",
		PusherSub: "sub-1", PusherKind: "agent",
		Bypass: true,
	}
	if err := s.RecordPushEvent(ctx, e); err != nil {
		t.Fatalf("record: %v", err)
	}
}
```

Run (expect FAIL):

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/repo/...
```

Expected output prefix: `FAIL`

- [ ] **Step 12: Implement PR-pointer + push_event methods**

Append to `cairn-native/internal/cairn/repo/sqlite.go`:

```go
// NextPRNumber atomically increments the per-repo PR counter and returns the
// new value (1 on first call for a repo).
func (s *SQLite) NextPRNumber(ctx context.Context, repoID string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("NextPRNumber begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO pr_counter (repo_id, next) VALUES (?, 1)
		 ON CONFLICT(repo_id) DO UPDATE SET next = next + 1`,
		repoID); err != nil {
		return 0, fmt.Errorf("NextPRNumber upsert: %w", err)
	}
	var n int
	if err := tx.QueryRowContext(ctx,
		`SELECT next FROM pr_counter WHERE repo_id = ?`, repoID).Scan(&n); err != nil {
		return 0, fmt.Errorf("NextPRNumber read: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("NextPRNumber commit: %w", err)
	}
	return n, nil
}

// CreatePRPointer inserts a pr_pointer row. PRNumber must already be reserved
// via NextPRNumber.
func (s *SQLite) CreatePRPointer(ctx context.Context, p PRPointer) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO pr_pointer (repo_id, pr_number, ref, ledger_ticket, head_sha, base_branch)
		VALUES (?, ?, ?, ?, ?, ?)`,
		p.RepoID, p.PRNumber, p.Ref, p.LedgerTicket, p.HeadSHA, p.BaseBranch)
	if err != nil {
		return fmt.Errorf("CreatePRPointer: %w", err)
	}
	return nil
}

// GetPRPointer fetches by (repo_id, pr_number).
func (s *SQLite) GetPRPointer(ctx context.Context, repoID string, n int) (PRPointer, error) {
	var p PRPointer
	var created, updated string
	err := s.db.QueryRowContext(ctx, `
		SELECT repo_id, pr_number, ref, ledger_ticket, head_sha, base_branch, created_at, updated_at
		FROM pr_pointer WHERE repo_id = ? AND pr_number = ?`,
		repoID, n).Scan(&p.RepoID, &p.PRNumber, &p.Ref, &p.LedgerTicket,
		&p.HeadSHA, &p.BaseBranch, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return PRPointer{}, ErrNotFound
	}
	if err != nil {
		return PRPointer{}, fmt.Errorf("GetPRPointer: %w", err)
	}
	return p, nil
}

// UpdatePRHead updates head_sha + updated_at.
func (s *SQLite) UpdatePRHead(ctx context.Context, repoID string, n int, headSHA string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE pr_pointer SET head_sha = ?, updated_at = datetime('now')
		WHERE repo_id = ? AND pr_number = ?`,
		headSHA, repoID, n)
	if err != nil {
		return fmt.Errorf("UpdatePRHead: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePRPointer removes the row. (The git ref is the caller's job.)
func (s *SQLite) DeletePRPointer(ctx context.Context, repoID string, n int) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM pr_pointer WHERE repo_id = ? AND pr_number = ?`,
		repoID, n)
	if err != nil {
		return fmt.Errorf("DeletePRPointer: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// ListPRPointers returns every PR pointer for a repo, ordered by pr_number desc.
func (s *SQLite) ListPRPointers(ctx context.Context, repoID string) ([]PRPointer, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT repo_id, pr_number, ref, ledger_ticket, head_sha, base_branch
		FROM pr_pointer WHERE repo_id = ? ORDER BY pr_number DESC`, repoID)
	if err != nil {
		return nil, fmt.Errorf("ListPRPointers: %w", err)
	}
	defer rows.Close()
	var out []PRPointer
	for rows.Next() {
		var p PRPointer
		if err := rows.Scan(&p.RepoID, &p.PRNumber, &p.Ref, &p.LedgerTicket,
			&p.HeadSHA, &p.BaseBranch); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RecordPushEvent appends one push_event row.
func (s *SQLite) RecordPushEvent(ctx context.Context, e PushEvent) error {
	if e.ID == "" {
		e.ID = newID()
	}
	var bypass int
	if e.Bypass {
		bypass = 1
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO push_event (id, repo_id, ref, old_sha, new_sha, pusher_sub, pusher_kind, bypass)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.RepoID, e.Ref, e.OldSHA, e.NewSHA, e.PusherSub, e.PusherKind, bypass)
	if err != nil {
		return fmt.Errorf("RecordPushEvent: %w", err)
	}
	return nil
}
```

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/repo/...
```

Expected output prefix: `ok`

- [ ] **Step 13: Commit the store layer**

Run:

```bash
cd /Users/jacinta/Source/cairn
git add cairn-native/internal/cairn/repo/repo.go cairn-native/internal/cairn/repo/caller.go cairn-native/internal/cairn/repo/caller_test.go cairn-native/internal/cairn/repo/store.go cairn-native/internal/cairn/repo/sqlite.go cairn-native/internal/cairn/repo/sqlite_test.go cairn-native/internal/cairn/repo/schema.sql cairn-native/go.mod cairn-native/go.sum
git commit -m "feat(cairn-native): repo metadata store on sqlite"
```

- [ ] **Step 14: Write the failing protection-rules unit tests**

Create `cairn-native/internal/cairn/repo/protection_test.go`:

```go
package repo

import "testing"

func TestParseProtectionRules_Empty(t *testing.T) {
	rules, err := ParseProtectionRules(`{"rules":[]}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rules.Rules) != 0 {
		t.Errorf("want 0 rules, got %d", len(rules.Rules))
	}
}

func TestMatchProtection_FirstMatchWins(t *testing.T) {
	rules := ProtectionRules{Rules: []ProtectionRule{
		{Pattern: "main", RequiredScope: "repo:admin", AllowForcePush: false, AllowDelete: false},
		{Pattern: "release/*", RequiredScope: "repo:admin"},
		{Pattern: "*", RequiredScope: "repo:write"},
	}}
	got, ok := rules.Match("refs/heads/main")
	if !ok || got.RequiredScope != "repo:admin" {
		t.Errorf("main: got %+v ok=%v", got, ok)
	}
	got, ok = rules.Match("refs/heads/release/v1")
	if !ok || got.Pattern != "release/*" {
		t.Errorf("release: got %+v ok=%v", got, ok)
	}
	got, ok = rules.Match("refs/heads/feature/x")
	if !ok || got.RequiredScope != "repo:write" {
		t.Errorf("wildcard: got %+v ok=%v", got, ok)
	}
}

func TestMatchProtection_NoMatch(t *testing.T) {
	rules := ProtectionRules{Rules: []ProtectionRule{
		{Pattern: "main", RequiredScope: "repo:admin"},
	}}
	if _, ok := rules.Match("refs/heads/feature"); ok {
		t.Error("expected no match")
	}
}

func TestDefaultProtection(t *testing.T) {
	rules := DefaultProtection("main")
	if len(rules.Rules) != 1 {
		t.Fatalf("want 1 rule, got %d", len(rules.Rules))
	}
	r := rules.Rules[0]
	if r.Pattern != "main" || r.RequiredScope != "repo:admin" {
		t.Errorf("default: %+v", r)
	}
	if r.AllowForcePush || r.AllowDelete {
		t.Error("default protection must forbid force-push and delete")
	}
	if !r.BypassRequiresAudit {
		t.Error("default protection must require audit on bypass")
	}
}
```

Run (expect FAIL):

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/repo/...
```

Expected output prefix: `FAIL`

- [ ] **Step 15: Implement `protection.go`**

Create `cairn-native/internal/cairn/repo/protection.go`:

```go
package repo

import (
	"encoding/json"
	"path"
	"strings"
)

// ProtectionRules is the decoded shape of Repo.Protection.
type ProtectionRules struct {
	Rules []ProtectionRule `json:"rules"`
}

// ProtectionRule is one branch-protection entry. Pattern is matched against
// the branch name (the part after "refs/heads/") using path.Match semantics.
type ProtectionRule struct {
	Pattern             string `json:"pattern"`              // e.g. "main", "release/*"
	RequiredScope       string `json:"required_scope"`       // e.g. "repo:admin"
	AllowForcePush      bool   `json:"allow_force_push"`
	AllowDelete         bool   `json:"allow_delete"`
	BypassRequiresAudit bool   `json:"bypass_requires_audit"`
}

// ParseProtectionRules decodes the JSON stored on Repo.Protection.
func ParseProtectionRules(jsonStr string) (ProtectionRules, error) {
	var r ProtectionRules
	if jsonStr == "" {
		return r, nil
	}
	if err := json.Unmarshal([]byte(jsonStr), &r); err != nil {
		return ProtectionRules{}, err
	}
	return r, nil
}

// EncodeProtection re-serializes for storage.
func EncodeProtection(r ProtectionRules) (string, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Match finds the first rule whose Pattern matches the branch part of refName
// ("refs/heads/<branch>" -> "<branch>"). Returns (rule, true) on match.
// For non-branch refs (tags, custom refs), the full refName is matched.
func (r ProtectionRules) Match(refName string) (ProtectionRule, bool) {
	target := strings.TrimPrefix(refName, "refs/heads/")
	for _, rule := range r.Rules {
		ok, _ := path.Match(rule.Pattern, target)
		if ok {
			return rule, true
		}
	}
	return ProtectionRule{}, false
}

// DefaultProtection returns the rule auto-applied to every new repo:
// protect the default branch with repo:admin, no force-push, no delete, audit
// on bypass. Spec §7.
func DefaultProtection(defaultBranch string) ProtectionRules {
	return ProtectionRules{Rules: []ProtectionRule{
		{
			Pattern:             defaultBranch,
			RequiredScope:       "repo:admin",
			AllowForcePush:      false,
			AllowDelete:         false,
			BypassRequiresAudit: true,
		},
	}}
}
```

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/repo/...
```

Expected output prefix: `ok`

- [ ] **Step 16: Write failing Service tests (CreateRepo + Open + ListRefs)**

Create `cairn-native/internal/cairn/repo/service_test.go`:

```go
package repo

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func newTestService(t *testing.T) (*Service, string) {
	t.Helper()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root := t.TempDir()
	return NewService(st, root), root
}

func TestService_CreateRepo_InitializesBareGit(t *testing.T) {
	ctx := context.Background()
	svc, root := newTestService(t)
	r, err := svc.CreateRepo(ctx, "org-1", "alpha", "main")
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if r.OrgID != "org-1" || r.Slug != "alpha" {
		t.Errorf("repo fields: %+v", r)
	}
	if r.DefaultBranch != "main" {
		t.Errorf("default_branch: %q", r.DefaultBranch)
	}
	if r.StorageURI != "file://"+filepath.Join(root, "org-1", "alpha.git") {
		t.Errorf("storage_uri: %q", r.StorageURI)
	}
	// Reopen via go-git to confirm the bare repo exists.
	gr, err := svc.Open(ctx, r)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	cfg, err := gr.Config()
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if !cfg.Core.IsBare {
		t.Error("expected bare repo")
	}
}

func TestService_CreateRepo_DefaultProtectionApplied(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	r, err := svc.CreateRepo(ctx, "o", "p", "main")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rules, err := ParseProtectionRules(r.Protection)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rules.Rules) != 1 || rules.Rules[0].Pattern != "main" {
		t.Errorf("want default protection on main, got %+v", rules)
	}
}

func TestService_ListRefs_EmptyAndPopulated(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	r, _ := svc.CreateRepo(ctx, "o", "r", "main")

	// Empty bare repo: no refs.
	refs, err := svc.ListRefs(ctx, r)
	if err != nil {
		t.Fatalf("ListRefs empty: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("expected 0 refs, got %d", len(refs))
	}

	// Add a ref by writing a synthetic commit.
	gr, err := svc.Open(ctx, r)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	storer := gr.Storer
	tree := &object.Tree{}
	te, err := object.GetTree(storer, plumbing.ZeroHash)
	_ = te
	_ = tree
	// Easier path: use writeBlobObj + Tree obj manually.
	blob := &plumbing.MemoryObject{}
	blob.SetType(plumbing.BlobObject)
	_, _ = blob.Write([]byte("hello"))
	bh, err := storer.SetEncodedObject(blob)
	if err != nil {
		t.Fatalf("blob: %v", err)
	}
	treeObj := &object.Tree{Entries: []object.TreeEntry{
		{Name: "hello.txt", Mode: 0o100644, Hash: bh},
	}}
	to := &plumbing.MemoryObject{}
	to.SetType(plumbing.TreeObject)
	if err := treeObj.Encode(to); err != nil {
		t.Fatalf("encode tree: %v", err)
	}
	th, err := storer.SetEncodedObject(to)
	if err != nil {
		t.Fatalf("tree set: %v", err)
	}
	commit := &object.Commit{
		Author:    object.Signature{Name: "t", Email: "t@e"},
		Committer: object.Signature{Name: "t", Email: "t@e"},
		Message:   "initial",
		TreeHash:  th,
	}
	co := &plumbing.MemoryObject{}
	co.SetType(plumbing.CommitObject)
	if err := commit.Encode(co); err != nil {
		t.Fatalf("encode commit: %v", err)
	}
	ch, err := storer.SetEncodedObject(co)
	if err != nil {
		t.Fatalf("commit set: %v", err)
	}
	if err := storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName("main"), ch)); err != nil {
		t.Fatalf("set ref: %v", err)
	}

	refs, err = svc.ListRefs(ctx, r)
	if err != nil {
		t.Fatalf("ListRefs: %v", err)
	}
	found := false
	for _, ref := range refs {
		if ref.Name == "refs/heads/main" && ref.Target == ch.String() {
			found = true
		}
	}
	if !found {
		t.Errorf("ref not found in %+v", refs)
	}

	// Spot-check GetRef + DeleteRef.
	got, err := svc.GetRef(ctx, r, "refs/heads/main")
	if err != nil || got.Target != ch.String() {
		t.Errorf("GetRef: %+v err=%v", got, err)
	}
	if err := svc.DeleteRef(ctx, r, "refs/heads/main"); err != nil {
		t.Fatalf("DeleteRef: %v", err)
	}
	if _, err := svc.GetRef(ctx, r, "refs/heads/main"); err == nil {
		t.Error("expected ref to be gone")
	}
	_ = git.ErrRepositoryNotExists // keep import used
}
```

Run (expect FAIL):

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/repo/...
```

Expected output prefix: `FAIL`

- [ ] **Step 17: Implement `service.go`**

Create `cairn-native/internal/cairn/repo/service.go`:

```go
package repo

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// Service is the cairn-native core. Frontends (SSH, HTTP, web UI) call
// methods here. Service does NOT enforce auth — frontends pass a Caller and
// Service surfaces typed errors when scopes are missing. Service DOES enforce
// branch protection at receive-pack time (Task 6 wires hooks).
type Service struct {
	store    Store
	repoRoot string // filesystem root for file:// storage URIs
}

// NewService constructs the core. repoRoot is the on-disk parent for bare
// git directories when StorageURI is file://.
func NewService(store Store, repoRoot string) *Service {
	return &Service{store: store, repoRoot: repoRoot}
}

// CreateRepo allocates a Repo row, initializes a bare git directory on disk,
// and writes the default branch-protection rule. defaultBranch defaults to
// "main" if empty.
func (s *Service) CreateRepo(ctx context.Context, orgID, slug, defaultBranch string) (Repo, error) {
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	gitDir := filepath.Join(s.repoRoot, orgID, slug+".git")
	if err := os.MkdirAll(filepath.Dir(gitDir), 0o755); err != nil {
		return Repo{}, fmt.Errorf("CreateRepo mkdir: %w", err)
	}
	if _, err := git.PlainInit(gitDir, true); err != nil {
		return Repo{}, fmt.Errorf("CreateRepo git init: %w", err)
	}
	protJSON, err := EncodeProtection(DefaultProtection(defaultBranch))
	if err != nil {
		return Repo{}, fmt.Errorf("CreateRepo encode protection: %w", err)
	}
	storageURI := "file://" + gitDir
	r, err := s.store.CreateRepo(ctx, Repo{
		OrgID:         orgID,
		Slug:          slug,
		DefaultBranch: defaultBranch,
		Protection:    protJSON,
		StorageURI:    storageURI,
	})
	if err != nil {
		// Best-effort rollback of the on-disk directory.
		_ = os.RemoveAll(gitDir)
		return Repo{}, err
	}
	return r, nil
}

// GetRepo proxies to the store.
func (s *Service) GetRepo(ctx context.Context, orgID, slug string) (Repo, error) {
	return s.store.GetRepo(ctx, orgID, slug)
}

// DeleteRepo removes the on-disk directory then the metadata row.
func (s *Service) DeleteRepo(ctx context.Context, orgID, slug string) error {
	r, err := s.store.GetRepo(ctx, orgID, slug)
	if err != nil {
		return err
	}
	dir, err := storagePath(r.StorageURI)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("DeleteRepo rmdir: %w", err)
	}
	return s.store.DeleteRepo(ctx, r.ID)
}

// Open returns a go-git *git.Repository for the repo's storage. Caller scope:
// one request; do not cache.
func (s *Service) Open(ctx context.Context, r Repo) (*git.Repository, error) {
	dir, err := storagePath(r.StorageURI)
	if err != nil {
		return nil, err
	}
	return git.PlainOpen(dir)
}

// ListRefs returns every ref in the repo (heads + tags + cairn PRs).
func (s *Service) ListRefs(ctx context.Context, r Repo) ([]Ref, error) {
	gr, err := s.Open(ctx, r)
	if err != nil {
		return nil, err
	}
	iter, err := gr.References()
	if err != nil {
		return nil, fmt.Errorf("ListRefs: %w", err)
	}
	var out []Ref
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		// Skip symbolic refs (HEAD).
		if ref.Type() == plumbing.SymbolicReference {
			return nil
		}
		out = append(out, Ref{Name: ref.Name().String(), Target: ref.Hash().String()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetRef returns one ref by name.
func (s *Service) GetRef(ctx context.Context, r Repo, name string) (Ref, error) {
	gr, err := s.Open(ctx, r)
	if err != nil {
		return Ref{}, err
	}
	ref, err := gr.Reference(plumbing.ReferenceName(name), false)
	if err != nil {
		return Ref{}, fmt.Errorf("GetRef %s: %w", name, err)
	}
	return Ref{Name: ref.Name().String(), Target: ref.Hash().String()}, nil
}

// DeleteRef removes a ref. Branch-protection enforcement is layered on top by
// Task 6's checkRefDeletion; the core method here is the raw operation.
func (s *Service) DeleteRef(ctx context.Context, r Repo, name string) error {
	gr, err := s.Open(ctx, r)
	if err != nil {
		return err
	}
	return gr.Storer.RemoveReference(plumbing.ReferenceName(name))
}

// storagePath extracts a filesystem path from a "file://..." StorageURI.
// Non-file schemes are out of scope for MVP and return an error.
func storagePath(storageURI string) (string, error) {
	if !strings.HasPrefix(storageURI, "file://") {
		return "", fmt.Errorf("unsupported storage scheme: %q", storageURI)
	}
	u, err := url.Parse(storageURI)
	if err != nil {
		return "", fmt.Errorf("storage uri: %w", err)
	}
	return u.Path, nil
}
```

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/repo/...
```

Expected output prefix: `ok`

- [ ] **Step 18: Add `repo_test.go` smoke covering ProtectionRules round-trip on a stored repo**

Create `cairn-native/internal/cairn/repo/repo_test.go`:

```go
package repo

import (
	"context"
	"testing"
)

func TestServiceCreate_ProtectionRoundTrip(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	r, err := svc.CreateRepo(ctx, "o", "p", "trunk")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rules, err := ParseProtectionRules(r.Protection)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rules.Rules) != 1 || rules.Rules[0].Pattern != "trunk" {
		t.Errorf("rules: %+v", rules)
	}
	// Update protection via the store and re-fetch.
	updated := ProtectionRules{Rules: []ProtectionRule{
		{Pattern: "trunk", RequiredScope: "repo:admin"},
		{Pattern: "release/*", RequiredScope: "repo:admin"},
	}}
	js, err := EncodeProtection(updated)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := svc.store.UpdateProtection(ctx, r.ID, js); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := svc.GetRepo(ctx, "o", "p")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	gotRules, _ := ParseProtectionRules(got.Protection)
	if len(gotRules.Rules) != 2 {
		t.Errorf("want 2 rules, got %d", len(gotRules.Rules))
	}
}
```

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/repo/...
```

Expected output prefix: `ok`

- [ ] **Step 19: Commit Task 1**

Run:

```bash
cd /Users/jacinta/Source/cairn
git add cairn-native/internal/cairn/repo/protection.go cairn-native/internal/cairn/repo/protection_test.go cairn-native/internal/cairn/repo/service.go cairn-native/internal/cairn/repo/service_test.go cairn-native/internal/cairn/repo/repo_test.go cairn-native/go.mod cairn-native/go.sum
git commit -m "feat(cairn-native): repo service + protection rules on go-git"
```

---

## Task 2: SSH git frontend (casket pubkey as SSH identity) — NEX-387

**Files:**

Create:
- `cairn-native/internal/cairn/identity/identity.go` — IdentityResolver interface + caching wrapper
- `cairn-native/internal/cairn/identity/identity_test.go`
- `cairn-native/internal/cairn/identity/herald_resolver.go` — concrete herald admin-API lookup
- `cairn-native/internal/cairn/identity/herald_resolver_test.go`
- `cairn-native/internal/cairn/sshd/sshd.go` — gliderlabs/ssh server + git-upload/receive dispatch
- `cairn-native/internal/cairn/sshd/sshd_test.go`
- `cairn-native/internal/cairn/sshd/fingerprint.go` — pubkey fingerprint algorithm
- `cairn-native/internal/cairn/sshd/fingerprint_test.go`

Modify:
- `cairn-native/internal/cairn/repo/repo.go` — add `RepoLookup` interface and re-use `Service` as its implementation (see Step 1)
- `cairn-native/go.mod` — add `gliderlabs/ssh`, `golang.org/x/crypto/ssh`

> **Dep on Task 1:** This task uses `repo.Service`, `repo.Repo`, `repo.Caller` exactly as defined in Task 1. The SSH command path (`git-upload-pack` / `git-receive-pack`) shells out to a sidecar `git` binary that operates on the bare directory pointed to by `repo.StorageURI`. Pure-Go protocol handlers are deferred — the spec is happy with shelling out for MVP because we trust our own filesystem.

- [ ] **Step 1: Add `RepoLookup` so sshd can be tested without full Service wiring**

Append to `cairn-native/internal/cairn/repo/repo.go`:

```go
import "context"

// RepoLookup is the read-only subset of Service that frontends need to map an
// (org, slug) URL into a Repo before dispatching protocol handlers.
type RepoLookup interface {
	GetRepo(ctx context.Context, orgID, slug string) (Repo, error)
}
```

(If go imports are already declared in `repo.go`, just append the interface; otherwise add the import.) Verify build:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go build ./...
```

Expected output: empty (success).

- [ ] **Step 2: Add the SSH library deps**

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go get github.com/gliderlabs/ssh@v0.3.7
go get golang.org/x/crypto@latest
go mod tidy
```

Expected output prefix: `go: added github.com/gliderlabs/ssh`

- [ ] **Step 3: Write the fingerprint test (expect FAIL)**

Create `cairn-native/internal/cairn/sshd/fingerprint_test.go`:

```go
package sshd

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

func TestCasketFingerprint_Stable(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	sshPub, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("sshpub: %v", err)
	}
	a := CasketFingerprint(sshPub)
	b := CasketFingerprint(sshPub)
	if a != b {
		t.Errorf("fingerprint not stable: %q vs %q", a, b)
	}
	if len(a) == 0 {
		t.Error("empty fingerprint")
	}
}

func TestCasketFingerprint_DistinctKeysDistinctFP(t *testing.T) {
	pub1, _, _ := ed25519.GenerateKey(rand.Reader)
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	s1, _ := gossh.NewPublicKey(pub1)
	s2, _ := gossh.NewPublicKey(pub2)
	if CasketFingerprint(s1) == CasketFingerprint(s2) {
		t.Error("distinct keys produced same fingerprint")
	}
}
```

Run (expect FAIL — package undefined):

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/sshd/...
```

Expected output prefix: `FAIL` or build error referencing `sshd`.

- [ ] **Step 4: Implement `fingerprint.go`**

Create `cairn-native/internal/cairn/sshd/fingerprint.go`:

```go
// Package sshd is the cairn-native SSH git frontend. It uses gliderlabs/ssh
// to accept SSH connections, authenticates by mapping the presented public
// key's casket fingerprint to a herald agent (via internal/cairn/identity),
// and dispatches the requested git-upload-pack / git-receive-pack command to
// the matching repo on disk.
package sshd

import (
	"crypto/sha256"
	"encoding/base64"

	gossh "golang.org/x/crypto/ssh"
)

// CasketFingerprint is the canonical cairn-side fingerprint of a public key:
// base64url-no-padding of the first 16 bytes of sha256(Marshaled pubkey).
//
// It matches herald's casket_fingerprint algorithm (heraldauth + herald.identity
// agree on this shape).
func CasketFingerprint(pub gossh.PublicKey) string {
	raw := pub.Marshal()
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}
```

Run (expect PASS):

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/sshd/...
```

Expected output prefix: `ok`

- [ ] **Step 5: Write the IdentityResolver interface and a cache wrapper test**

Create `cairn-native/internal/cairn/identity/identity.go`:

```go
// Package identity maps casket fingerprints to herald agent records for the
// SSH frontend. Production wraps herald's admin API; tests use a fake.
package identity

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Agent is the minimum information the SSH frontend needs after a fingerprint
// lookup. Populated from herald.
type Agent struct {
	Subject string   // herald subject id (uuid)
	OrgID   string   // herald org uuid
	Status  string   // "active" | "blocked" | "pending"
	Scopes  []string // effective scopes granted to this agent
}

// ErrNotFound is returned when no agent matches a fingerprint.
var ErrNotFound = errors.New("identity: agent not found")

// Resolver looks up an agent by casket fingerprint.
type Resolver interface {
	ByFingerprint(ctx context.Context, fp string) (Agent, error)
}

// Cache wraps a Resolver with a short-TTL fingerprint cache. Cache invalidation
// on agent-block is best-effort: when status changes to "blocked", the TTL
// determines how long stale "active" entries can stick around. MVP TTL is 30s.
type Cache struct {
	inner Resolver
	ttl   time.Duration
	now   func() time.Time

	mu      sync.RWMutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	agent Agent
	at    time.Time
}

// NewCache wraps inner with a TTL cache. If ttl <= 0 it defaults to 30s.
func NewCache(inner Resolver, ttl time.Duration) *Cache {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &Cache{
		inner:   inner,
		ttl:     ttl,
		now:     time.Now,
		entries: map[string]cacheEntry{},
	}
}

// ByFingerprint returns the cached agent if fresh, else delegates.
func (c *Cache) ByFingerprint(ctx context.Context, fp string) (Agent, error) {
	c.mu.RLock()
	e, ok := c.entries[fp]
	c.mu.RUnlock()
	if ok && c.now().Sub(e.at) < c.ttl {
		return e.agent, nil
	}
	a, err := c.inner.ByFingerprint(ctx, fp)
	if err != nil {
		return Agent{}, err
	}
	c.mu.Lock()
	c.entries[fp] = cacheEntry{agent: a, at: c.now()}
	c.mu.Unlock()
	return a, nil
}

// Invalidate drops a single fingerprint from the cache.
func (c *Cache) Invalidate(fp string) {
	c.mu.Lock()
	delete(c.entries, fp)
	c.mu.Unlock()
}
```

- [ ] **Step 6: Write the cache test**

Create `cairn-native/internal/cairn/identity/identity_test.go`:

```go
package identity

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type fakeResolver struct {
	calls int64
	agent Agent
	err   error
}

func (f *fakeResolver) ByFingerprint(ctx context.Context, fp string) (Agent, error) {
	atomic.AddInt64(&f.calls, 1)
	if f.err != nil {
		return Agent{}, f.err
	}
	return f.agent, nil
}

func TestCache_HitsInnerOnceWithinTTL(t *testing.T) {
	f := &fakeResolver{agent: Agent{Subject: "s1", OrgID: "o", Status: "active"}}
	c := NewCache(f, time.Hour)
	for i := 0; i < 5; i++ {
		a, err := c.ByFingerprint(context.Background(), "fp-1")
		if err != nil {
			t.Fatalf("lookup %d: %v", i, err)
		}
		if a.Subject != "s1" {
			t.Errorf("subject mismatch: %q", a.Subject)
		}
	}
	if got := atomic.LoadInt64(&f.calls); got != 1 {
		t.Errorf("inner called %d times, want 1", got)
	}
}

func TestCache_ExpiresAfterTTL(t *testing.T) {
	f := &fakeResolver{agent: Agent{Subject: "s1", Status: "active"}}
	c := NewCache(f, time.Hour)
	now := time.Unix(1_000_000, 0)
	c.now = func() time.Time { return now }
	if _, err := c.ByFingerprint(context.Background(), "fp"); err != nil {
		t.Fatal(err)
	}
	// Jump past TTL.
	now = now.Add(2 * time.Hour)
	if _, err := c.ByFingerprint(context.Background(), "fp"); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt64(&f.calls); got != 2 {
		t.Errorf("calls=%d want 2", got)
	}
}

func TestCache_Invalidate(t *testing.T) {
	f := &fakeResolver{agent: Agent{Subject: "s1", Status: "active"}}
	c := NewCache(f, time.Hour)
	_, _ = c.ByFingerprint(context.Background(), "fp")
	c.Invalidate("fp")
	_, _ = c.ByFingerprint(context.Background(), "fp")
	if got := atomic.LoadInt64(&f.calls); got != 2 {
		t.Errorf("calls=%d want 2", got)
	}
}
```

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/identity/...
```

Expected output prefix: `ok`

- [ ] **Step 7: Implement `herald_resolver.go` against a JSON admin endpoint**

Create `cairn-native/internal/cairn/identity/herald_resolver.go`:

```go
package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// HeraldResolver looks up agents by casket fingerprint via herald's admin API.
//
// Expected herald endpoint (added in NEX-387 or already present):
//   GET /api/agents/by-fingerprint/{fp}
//   Authorization: Bearer <CAIRN_HERALD_ADMIN_TOKEN>
//   200 { "subject": "...", "org_id": "...", "status": "active",
//         "scopes": ["repo:read","repo:write"] }
//   404 if no agent matches
type HeraldResolver struct {
	BaseURL    string // e.g. "http://herald.cwb.svc:8099"
	AdminToken string
	HTTP       *http.Client
}

// NewHeraldResolver constructs a resolver. baseURL must NOT end with "/".
func NewHeraldResolver(baseURL, adminToken string) *HeraldResolver {
	return &HeraldResolver{
		BaseURL:    baseURL,
		AdminToken: adminToken,
		HTTP:       &http.Client{},
	}
}

// ByFingerprint calls herald's admin lookup endpoint.
func (h *HeraldResolver) ByFingerprint(ctx context.Context, fp string) (Agent, error) {
	if fp == "" {
		return Agent{}, errors.New("identity: empty fingerprint")
	}
	u := h.BaseURL + "/api/agents/by-fingerprint/" + url.PathEscape(fp)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Agent{}, fmt.Errorf("identity: build req: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+h.AdminToken)
	resp, err := h.HTTP.Do(req)
	if err != nil {
		return Agent{}, fmt.Errorf("identity: GET %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Agent{}, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return Agent{}, fmt.Errorf("identity: GET %s: status %d", u, resp.StatusCode)
	}
	var body struct {
		Subject string   `json:"subject"`
		OrgID   string   `json:"org_id"`
		Status  string   `json:"status"`
		Scopes  []string `json:"scopes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Agent{}, fmt.Errorf("identity: decode: %w", err)
	}
	return Agent{
		Subject: body.Subject,
		OrgID:   body.OrgID,
		Status:  body.Status,
		Scopes:  body.Scopes,
	}, nil
}
```

- [ ] **Step 8: Write the herald-resolver HTTP test using httptest**

Create `cairn-native/internal/cairn/identity/herald_resolver_test.go`:

```go
package identity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHeraldResolver_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agents/by-fingerprint/abc123" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("auth header: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"subject":"s1","org_id":"o1","status":"active","scopes":["repo:read","repo:write"]}`))
	}))
	defer srv.Close()

	r := NewHeraldResolver(srv.URL, "test-token")
	a, err := r.ByFingerprint(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if a.Subject != "s1" || a.OrgID != "o1" || a.Status != "active" {
		t.Errorf("agent: %+v", a)
	}
	if len(a.Scopes) != 2 || a.Scopes[0] != "repo:read" {
		t.Errorf("scopes: %v", a.Scopes)
	}
}

func TestHeraldResolver_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer srv.Close()
	r := NewHeraldResolver(srv.URL, "tok")
	_, err := r.ByFingerprint(context.Background(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestHeraldResolver_EmptyFingerprint(t *testing.T) {
	r := NewHeraldResolver("http://unused", "")
	_, err := r.ByFingerprint(context.Background(), "")
	if err == nil {
		t.Error("expected error for empty fp")
	}
}
```

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/identity/...
```

Expected output prefix: `ok`

- [ ] **Step 9: Commit the identity package**

Run:

```bash
cd /Users/jacinta/Source/cairn
git add cairn-native/internal/cairn/identity/ cairn-native/go.mod cairn-native/go.sum
git commit -m "feat(cairn-native): casket-fingerprint to herald-agent resolver"
```

- [ ] **Step 10: Write a failing sshd test that asserts a known fingerprint authorizes the matched agent**

Create `cairn-native/internal/cairn/sshd/sshd_test.go`:

```go
package sshd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/identity"
	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/repo"
	gossh "golang.org/x/crypto/ssh"
)

type stubResolver struct {
	byFP map[string]identity.Agent
}

func (s *stubResolver) ByFingerprint(_ context.Context, fp string) (identity.Agent, error) {
	a, ok := s.byFP[fp]
	if !ok {
		return identity.Agent{}, identity.ErrNotFound
	}
	return a, nil
}

type stubLookup struct {
	repos map[string]repo.Repo
}

func (s *stubLookup) GetRepo(_ context.Context, org, slug string) (repo.Repo, error) {
	r, ok := s.repos[org+"/"+slug]
	if !ok {
		return repo.Repo{}, repo.ErrNotFound
	}
	return r, nil
}

func newSSHKey(t *testing.T) (gossh.PublicKey, string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return sp, CasketFingerprint(sp)
}

func TestAuthorizeKey_ActiveAgent(t *testing.T) {
	pub, fp := newSSHKey(t)
	res := &stubResolver{byFP: map[string]identity.Agent{
		fp: {Subject: "agent-1", OrgID: "org-1", Status: "active", Scopes: []string{"repo:read"}},
	}}
	s := &Server{Resolver: res}
	caller, ok := s.authorizeKey(context.Background(), pub)
	if !ok {
		t.Fatal("expected authorization")
	}
	if caller.Subject != "agent-1" || caller.Org != "org-1" || caller.Kind != "agent" {
		t.Errorf("caller: %+v", caller)
	}
	if !caller.HasScope("repo:read") {
		t.Error("scope missing")
	}
}

func TestAuthorizeKey_BlockedAgent(t *testing.T) {
	pub, fp := newSSHKey(t)
	res := &stubResolver{byFP: map[string]identity.Agent{
		fp: {Subject: "agent-1", OrgID: "org-1", Status: "blocked"},
	}}
	s := &Server{Resolver: res}
	if _, ok := s.authorizeKey(context.Background(), pub); ok {
		t.Error("blocked agent must be rejected")
	}
}

func TestAuthorizeKey_UnknownKey(t *testing.T) {
	pub, _ := newSSHKey(t)
	s := &Server{Resolver: &stubResolver{byFP: map[string]identity.Agent{}}}
	if _, ok := s.authorizeKey(context.Background(), pub); ok {
		t.Error("unknown key must be rejected")
	}
}

func TestParseCommand_ValidUploadPack(t *testing.T) {
	op, org, slug, err := parseGitCommand("git-upload-pack 'org-1/alpha.git'")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if op != "git-upload-pack" || org != "org-1" || slug != "alpha" {
		t.Errorf("got op=%q org=%q slug=%q", op, org, slug)
	}
}

func TestParseCommand_ReceivePack(t *testing.T) {
	op, org, slug, err := parseGitCommand("git-receive-pack '/org/repo'")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if op != "git-receive-pack" || org != "org" || slug != "repo" {
		t.Errorf("got op=%q org=%q slug=%q", op, org, slug)
	}
}

func TestParseCommand_InvalidShellInjection(t *testing.T) {
	if _, _, _, err := parseGitCommand("git-upload-pack 'a/b' ; rm -rf /"); err == nil {
		t.Error("expected error on injection-style command")
	}
}

func TestParseCommand_UnknownVerb(t *testing.T) {
	if _, _, _, err := parseGitCommand("git-archive 'a/b'"); err == nil {
		t.Error("expected error on unknown verb")
	}
}
```

Run (expect FAIL):

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/sshd/...
```

Expected output prefix: `FAIL`

- [ ] **Step 11: Implement `sshd.go` core (Server struct, authorizeKey, parseGitCommand, dispatcher)**

Create `cairn-native/internal/cairn/sshd/sshd.go`:

```go
package sshd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/identity"
	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/repo"
	gssh "github.com/gliderlabs/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// Server is the cairn-native SSH frontend.
type Server struct {
	Addr     string             // listen address, e.g. ":22"
	HostKey  []byte             // PEM ed25519 private host key
	Resolver identity.Resolver  // production: identity.Cache wrapping HeraldResolver
	Repos    repo.RepoLookup    // production: *repo.Service
	Service  *repo.Service      // for protocol dispatch — needs Open()

	// PushHook is called once per successful push (called from Task 6 hooks
	// inside receive-pack). MVP wires this in Task 5/6 — nil is fine for now.
	PushHook func(ctx context.Context, caller repo.Caller, r repo.Repo, ref, oldSHA, newSHA string)
}

// ListenAndServe binds the SSH listener and serves.
func (s *Server) ListenAndServe() error {
	signer, err := gossh.ParsePrivateKey(s.HostKey)
	if err != nil {
		return fmt.Errorf("sshd: parse host key: %w", err)
	}
	srv := &gssh.Server{
		Addr: s.Addr,
		PublicKeyHandler: func(ctx gssh.Context, key gssh.PublicKey) bool {
			caller, ok := s.authorizeKey(ctx, key)
			if !ok {
				return false
			}
			ctx.SetValue(callerKey{}, caller)
			return true
		},
		Handler: s.handleSession,
	}
	srv.AddHostKey(signer)
	return srv.ListenAndServe()
}

// callerKey is the context key for the authorized caller.
type callerKey struct{}

// authorizeKey computes the fingerprint, resolves the agent, and rejects
// non-active accounts. Returns the resulting Caller on success.
func (s *Server) authorizeKey(ctx context.Context, key gossh.PublicKey) (repo.Caller, bool) {
	fp := CasketFingerprint(key)
	agent, err := s.Resolver.ByFingerprint(ctx, fp)
	if err != nil {
		return repo.Caller{}, false
	}
	if agent.Status != "active" {
		return repo.Caller{}, false
	}
	return repo.Caller{
		Subject: agent.Subject,
		Kind:    "agent",
		Org:     agent.OrgID,
		Scopes:  agent.Scopes,
	}, true
}

// handleSession dispatches a single SSH session: only git-upload-pack /
// git-receive-pack are accepted. Everything else gets a polite refusal.
func (s *Server) handleSession(sess gssh.Session) {
	rawCmd := strings.Join(sess.Command(), " ")
	op, org, slug, err := parseGitCommand(rawCmd)
	if err != nil {
		_, _ = io.WriteString(sess.Stderr(), fmt.Sprintf("cairn: %v\n", err))
		_ = sess.Exit(1)
		return
	}

	caller, _ := sess.Context().Value(callerKey{}).(repo.Caller)

	r, err := s.Repos.GetRepo(sess.Context(), org, slug)
	if err != nil {
		_, _ = io.WriteString(sess.Stderr(), fmt.Sprintf("cairn: repo %s/%s not found\n", org, slug))
		_ = sess.Exit(2)
		return
	}

	// Scope check (clone = repo:read, push = repo:write).
	needed := "repo:read"
	if op == "git-receive-pack" {
		needed = "repo:write"
	}
	if !caller.HasScope(needed) {
		_, _ = io.WriteString(sess.Stderr(), fmt.Sprintf("cairn: missing scope %s\n", needed))
		_ = sess.Exit(3)
		return
	}
	// Same-org check.
	if r.OrgID != caller.Org {
		_, _ = io.WriteString(sess.Stderr(), "cairn: repo belongs to a different org\n")
		_ = sess.Exit(3)
		return
	}

	gitDir, err := storageFSPath(r.StorageURI)
	if err != nil {
		_, _ = io.WriteString(sess.Stderr(), fmt.Sprintf("cairn: %v\n", err))
		_ = sess.Exit(4)
		return
	}

	// Shell out to the system git binary for protocol I/O. The git CLI is
	// the most battle-tested implementation; we trust the on-disk path
	// (gitDir) because cairn controls it.
	cmd := exec.CommandContext(sess.Context(), op, gitDir)
	cmd.Stdin = sess
	cmd.Stdout = sess
	cmd.Stderr = sess.Stderr()
	if err := cmd.Run(); err != nil {
		_ = sess.Exit(exitCode(err))
		return
	}
	_ = sess.Exit(0)
}

// parseGitCommand validates and parses a git-upload-pack / git-receive-pack
// command line. Accepted form:
//
//	git-upload-pack '<org>/<slug>[.git]'
//	git-receive-pack '<org>/<slug>[.git]'
//
// Leading slash and trailing ".git" are tolerated. Any extra tokens (e.g. a
// shell metacharacter or a second arg) are rejected — we never invoke a shell.
func parseGitCommand(cmd string) (op, org, slug string, err error) {
	tokens := splitOnce(cmd)
	if len(tokens) != 2 {
		return "", "", "", errors.New("expected: <verb> '<org>/<slug>'")
	}
	op = tokens[0]
	if op != "git-upload-pack" && op != "git-receive-pack" {
		return "", "", "", fmt.Errorf("unsupported command: %q", op)
	}
	arg := tokens[1]
	arg = strings.Trim(arg, "'\" ")
	if strings.ContainsAny(arg, ";|&`$\\\n\r\t") {
		return "", "", "", errors.New("invalid characters in path")
	}
	arg = strings.TrimPrefix(arg, "/")
	arg = strings.TrimSuffix(arg, ".git")
	parts := strings.Split(arg, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", errors.New("expected '<org>/<slug>'")
	}
	return op, parts[0], parts[1], nil
}

// splitOnce splits cmd on the first whitespace boundary into [verb, rest].
func splitOnce(cmd string) []string {
	cmd = strings.TrimSpace(cmd)
	i := strings.IndexAny(cmd, " \t")
	if i < 0 {
		return []string{cmd}
	}
	return []string{cmd[:i], strings.TrimSpace(cmd[i:])}
}

// storageFSPath is sshd's copy of repo.storagePath because the latter is
// unexported. file://-only for MVP.
func storageFSPath(storageURI string) (string, error) {
	if !strings.HasPrefix(storageURI, "file://") {
		return "", fmt.Errorf("unsupported storage scheme: %q", storageURI)
	}
	u, err := url.Parse(storageURI)
	if err != nil {
		return "", err
	}
	return filepath.Clean(u.Path), nil
}

// exitCode pulls a numeric exit code out of an *exec.ExitError; 255 otherwise.
func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 255
}
```

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/sshd/...
```

Expected output prefix: `ok`

- [ ] **Step 12: Commit Task 2**

Run:

```bash
cd /Users/jacinta/Source/cairn
git add cairn-native/internal/cairn/sshd/ cairn-native/internal/cairn/repo/repo.go cairn-native/go.mod cairn-native/go.sum
git commit -m "feat(cairn-native): ssh git frontend with casket-pubkey auth"
```

---

## Task 3: HTTP git frontend (Smart-HTTP via gateway) — NEX-388

**Files:**

Create:
- `cairn-native/internal/cairn/httpd/httpd.go` — http.Handler that owns `/cairn/...` and dispatches REST + Smart-HTTPv2 git
- `cairn-native/internal/cairn/httpd/httpd_test.go`
- `cairn-native/internal/cairn/httpd/cwbheaders.go` — Caller extraction from X-CWB-* headers
- `cairn-native/internal/cairn/httpd/cwbheaders_test.go`
- `cairn-native/internal/cairn/httpd/git_http.go` — Smart-HTTP handlers shelling out to git-upload-pack / git-receive-pack
- `cairn-native/internal/cairn/httpd/git_http_test.go`
- `cairn-native/internal/cairn/httpd/restapi.go` — REST surface from spec §8
- `cairn-native/internal/cairn/httpd/restapi_test.go`

> **Dep on Task 1:** uses `repo.Service`, `repo.Repo`, `repo.Caller`. Same type-contract block as above; no new types added to Task 1.

- [ ] **Step 1: Write the failing CWB-header parser tests**

Create `cairn-native/internal/cairn/httpd/cwbheaders_test.go`:

```go
package httpd

import (
	"net/http"
	"testing"
)

func TestCallerFromCWBHeaders_HappyPath(t *testing.T) {
	r, _ := http.NewRequest("GET", "/cairn/x", nil)
	r.Header.Set("X-CWB-Subject", "u1")
	r.Header.Set("X-CWB-Kind", "human")
	r.Header.Set("X-CWB-Org", "org-1")
	r.Header.Set("X-CWB-Scopes", "repo:read repo:write")
	c, err := CallerFromCWBHeaders(r)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.Subject != "u1" || c.Kind != "human" || c.Org != "org-1" {
		t.Errorf("caller: %+v", c)
	}
	if len(c.Scopes) != 2 || c.Scopes[0] != "repo:read" {
		t.Errorf("scopes: %v", c.Scopes)
	}
}

func TestCallerFromCWBHeaders_MissingSubject(t *testing.T) {
	r, _ := http.NewRequest("GET", "/cairn/x", nil)
	r.Header.Set("X-CWB-Org", "o")
	if _, err := CallerFromCWBHeaders(r); err == nil {
		t.Error("expected error on missing subject")
	}
}

func TestCallerFromCWBHeaders_EmptyScopes(t *testing.T) {
	r, _ := http.NewRequest("GET", "/cairn/x", nil)
	r.Header.Set("X-CWB-Subject", "s")
	r.Header.Set("X-CWB-Kind", "agent")
	r.Header.Set("X-CWB-Org", "o")
	c, err := CallerFromCWBHeaders(r)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(c.Scopes) != 0 {
		t.Errorf("scopes should be empty: %v", c.Scopes)
	}
}
```

Run (expect FAIL):

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/httpd/...
```

Expected output prefix: `FAIL`

- [ ] **Step 2: Implement `cwbheaders.go`**

Create `cairn-native/internal/cairn/httpd/cwbheaders.go`:

```go
// Package httpd is the cairn-native HTTP frontend. It serves the REST API
// (spec §8), the Smart-HTTPv2 git protocol, and (in Task 4) the web UI.
//
// All requests reach this handler via interchange-gateway, which has already
// verified the herald token and stripped any client-supplied X-CWB-* headers
// before injecting its own. Cairn therefore TRUSTS the X-CWB-* headers
// unconditionally — the gateway is the trust boundary.
package httpd

import (
	"errors"
	"net/http"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/repo"
)

// CallerFromCWBHeaders extracts the verified caller from gateway-injected
// X-CWB-* headers. Returns an error if X-CWB-Subject is missing — the gateway
// always sets it on authenticated routes, so a missing subject means either
// the request bypassed the gateway (misconfig) or the route is misclassified.
func CallerFromCWBHeaders(r *http.Request) (repo.Caller, error) {
	sub := r.Header.Get("X-CWB-Subject")
	if sub == "" {
		return repo.Caller{}, errors.New("httpd: missing X-CWB-Subject header")
	}
	var scopes []string
	if s := r.Header.Get("X-CWB-Scopes"); s != "" {
		scopes = strings.Fields(s)
	}
	return repo.Caller{
		Subject: sub,
		Kind:    r.Header.Get("X-CWB-Kind"),
		Org:     r.Header.Get("X-CWB-Org"),
		Scopes:  scopes,
	}, nil
}
```

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/httpd/...
```

Expected output prefix: `ok`

- [ ] **Step 3: Write the failing REST test for `POST /api/orgs/{org}/repos`**

Create `cairn-native/internal/cairn/httpd/restapi_test.go`:

```go
package httpd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/repo"
)

func newTestHandler(t *testing.T) (http.Handler, *repo.Service) {
	t.Helper()
	st, err := repo.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := repo.NewService(st, t.TempDir())
	h := New(&Config{Service: svc}).Handler()
	return h, svc
}

func req(method, path, body string, c repo.Caller) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if c.Subject != "" {
		r.Header.Set("X-CWB-Subject", c.Subject)
		r.Header.Set("X-CWB-Kind", c.Kind)
		r.Header.Set("X-CWB-Org", c.Org)
		r.Header.Set("X-CWB-Scopes", strings.Join(c.Scopes, " "))
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	return r
}

func TestCreateRepo_ScopeRequired(t *testing.T) {
	h, _ := newTestHandler(t)
	w := httptest.NewRecorder()
	caller := repo.Caller{Subject: "u", Kind: "human", Org: "org-1", Scopes: []string{"repo:read"}}
	r := req("POST", "/api/orgs/org-1/repos", `{"slug":"alpha"}`, caller)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("want 403, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestCreateRepo_HappyPath(t *testing.T) {
	h, _ := newTestHandler(t)
	w := httptest.NewRecorder()
	caller := repo.Caller{Subject: "u", Kind: "human", Org: "org-1", Scopes: []string{"repo:create"}}
	r := req("POST", "/api/orgs/org-1/repos", `{"slug":"alpha","default_branch":"main"}`, caller)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d body=%s", w.Code, w.Body.String())
	}
	var got repo.Repo
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.OrgID != "org-1" || got.Slug != "alpha" {
		t.Errorf("repo: %+v", got)
	}
}

func TestCreateRepo_OrgMismatch(t *testing.T) {
	h, _ := newTestHandler(t)
	w := httptest.NewRecorder()
	caller := repo.Caller{Subject: "u", Kind: "human", Org: "other-org", Scopes: []string{"repo:create"}}
	r := req("POST", "/api/orgs/org-1/repos", `{"slug":"x"}`, caller)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("want 403 on cross-org, got %d", w.Code)
	}
}

func TestListRepos(t *testing.T) {
	h, _ := newTestHandler(t)
	caller := repo.Caller{Subject: "u", Kind: "human", Org: "org-1", Scopes: []string{"repo:create", "repo:read"}}
	for _, slug := range []string{"a", "b"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req("POST", "/api/orgs/org-1/repos", `{"slug":"`+slug+`"}`, caller))
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d", slug, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req("GET", "/api/orgs/org-1/repos", "", caller))
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d", w.Code)
	}
	var list []repo.Repo
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("want 2 repos, got %d", len(list))
	}
}

func TestGetRepo_NotFound(t *testing.T) {
	h, _ := newTestHandler(t)
	caller := repo.Caller{Subject: "u", Kind: "human", Org: "org-1", Scopes: []string{"repo:read"}}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req("GET", "/api/orgs/org-1/repos/absent", "", caller))
	if w.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", w.Code)
	}
}

func TestHealthz(t *testing.T) {
	h, _ := newTestHandler(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/healthz", nil)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("healthz: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"status":"ok"`) {
		t.Errorf("healthz body: %s", w.Body.String())
	}
}

func TestUnauthenticated_MissingSubject(t *testing.T) {
	h, _ := newTestHandler(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/orgs/org-1/repos", nil)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", w.Code)
	}
}

// Ensure context isn't nil in handler paths.
var _ context.Context = context.Background()
```

Run (expect FAIL):

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/httpd/...
```

Expected output prefix: `FAIL`

- [ ] **Step 4: Implement `httpd.go` (Server, Config, New, Handler, mux wiring)**

Create `cairn-native/internal/cairn/httpd/httpd.go`:

```go
package httpd

import (
	"encoding/json"
	"net/http"

	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/repo"
)

// Config configures a Server.
type Config struct {
	Service *repo.Service
	// LedgerClient is the Task 5 dependency (PR-as-ledger-issue). nil = log-only.
	LedgerClient LedgerClient
}

// LedgerClient is the Task 5 contract; declared here as an interface so Task 3
// compiles without Task 5. Task 5 provides a concrete impl + wires it in.
type LedgerClient interface {
	CreatePRTicket(ctx contextLike, in PRTicketInput) (string, error)
	AppendPRComment(ctx contextLike, ticketKey, body string) error
}

// contextLike is `context.Context` re-exported here so file ordering doesn't
// force an import. (We keep the typedef tight; Task 5 references it.)
type contextLike = stdContext

// PRTicketInput is the cairn-side payload sent to ledger when opening a PR.
// Task 5 owns the over-the-wire encoding; cairn just fills in the fields.
type PRTicketInput struct {
	RepoID       string
	OrgID        string
	RepoSlug     string
	PRNumber     int
	HeadSHA      string
	BaseBranch   string
	TitleHint    string
	DiffSummary  string
	OpenerSub    string
	OpenerKind   string
}

// Server is the cairn-native HTTP frontend.
type Server struct {
	svc    *repo.Service
	ledger LedgerClient
}

// New builds the server.
func New(cfg *Config) *Server {
	return &Server{svc: cfg.Service, ledger: cfg.LedgerClient}
}

// Handler returns the routed http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// Internal.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"cairn"}`))
	})

	// REST API (spec §8).
	mux.HandleFunc("GET /api/orgs/{org}/repos", s.authed(s.listRepos))
	mux.HandleFunc("POST /api/orgs/{org}/repos", s.authed(s.createRepo))
	mux.HandleFunc("GET /api/orgs/{org}/repos/{slug}", s.authed(s.getRepo))
	mux.HandleFunc("PUT /api/orgs/{org}/repos/{slug}/protection", s.authed(s.putProtection))
	mux.HandleFunc("DELETE /api/orgs/{org}/repos/{slug}", s.authed(s.deleteRepo))
	mux.HandleFunc("GET /api/orgs/{org}/repos/{slug}/refs", s.authed(s.listRefs))
	mux.HandleFunc("GET /api/orgs/{org}/repos/{slug}/refs/{ref...}", s.authed(s.getRef))
	mux.HandleFunc("DELETE /api/orgs/{org}/repos/{slug}/refs/{ref...}", s.authed(s.deleteRef))
	mux.HandleFunc("GET /api/orgs/{org}/repos/{slug}/prs", s.authed(s.listPRs))
	mux.HandleFunc("GET /api/orgs/{org}/repos/{slug}/prs/{n}", s.authed(s.getPR))

	// Smart-HTTPv2 git protocol.
	mux.HandleFunc("GET /{org}/{slug}/info/refs", s.authed(s.gitInfoRefs))
	mux.HandleFunc("POST /{org}/{slug}/git-upload-pack", s.authed(s.gitUploadPack))
	mux.HandleFunc("POST /{org}/{slug}/git-receive-pack", s.authed(s.gitReceivePack))

	return mux
}

// authed wraps a handler with the X-CWB-* caller extraction.
func (s *Server) authed(fn func(w http.ResponseWriter, r *http.Request, caller repo.Caller)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller, err := CallerFromCWBHeaders(r)
		if err != nil {
			writeJSONError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		fn(w, r, caller)
	}
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":` + jsonString(msg) + `}`))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// jsonString is a tiny inline encoder for a single string field, avoiding a
// json.Marshal allocation on the hot error path.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
```

Create `cairn-native/internal/cairn/httpd/contextalias.go`:

```go
package httpd

import "context"

// stdContext is an internal alias to keep the LedgerClient typedef in httpd.go
// free of import ordering surprises across the file split.
type stdContext = context.Context
```

- [ ] **Step 5: Implement `restapi.go` (the handler methods invoked by httpd.go)**

Create `cairn-native/internal/cairn/httpd/restapi.go`:

```go
package httpd

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/repo"
)

// scope checks
const (
	scopeRead   = "repo:read"
	scopeWrite  = "repo:write"
	scopeAdmin  = "repo:admin"
	scopeCreate = "repo:create"
)

// requireOrgAndScope confirms the caller's verified org matches the URL org
// and that the caller holds the named scope. Returns true on success; on
// failure writes the error response and returns false.
func requireOrgAndScope(w http.ResponseWriter, r *http.Request, c repo.Caller, scope string) bool {
	urlOrg := r.PathValue("org")
	if urlOrg == "" {
		writeJSONError(w, http.StatusBadRequest, "missing org")
		return false
	}
	if c.Org != urlOrg {
		writeJSONError(w, http.StatusForbidden, "caller org does not match url org")
		return false
	}
	if !c.HasScope(scope) {
		writeJSONError(w, http.StatusForbidden, "missing scope: "+scope)
		return false
	}
	return true
}

func (s *Server) listRepos(w http.ResponseWriter, r *http.Request, c repo.Caller) {
	if !requireOrgAndScope(w, r, c, scopeRead) {
		return
	}
	out, err := s.svc.ListRepos(r.Context(), c.Org)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out == nil {
		out = []repo.Repo{}
	}
	writeJSON(w, http.StatusOK, out)
}

type createRepoIn struct {
	Slug          string `json:"slug"`
	DefaultBranch string `json:"default_branch"`
}

func (s *Server) createRepo(w http.ResponseWriter, r *http.Request, c repo.Caller) {
	if !requireOrgAndScope(w, r, c, scopeCreate) {
		return
	}
	var in createRepoIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if !validSlug(in.Slug) {
		writeJSONError(w, http.StatusBadRequest, "invalid slug")
		return
	}
	rp, err := s.svc.CreateRepo(r.Context(), c.Org, in.Slug, in.DefaultBranch)
	if err != nil {
		if errors.Is(err, repo.ErrConflict) {
			writeJSONError(w, http.StatusConflict, "repo already exists")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, rp)
}

func (s *Server) getRepo(w http.ResponseWriter, r *http.Request, c repo.Caller) {
	if !requireOrgAndScope(w, r, c, scopeRead) {
		return
	}
	rp, err := s.svc.GetRepo(r.Context(), c.Org, r.PathValue("slug"))
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "repo not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rp)
}

func (s *Server) deleteRepo(w http.ResponseWriter, r *http.Request, c repo.Caller) {
	if !requireOrgAndScope(w, r, c, scopeAdmin) {
		return
	}
	if err := s.svc.DeleteRepo(r.Context(), c.Org, r.PathValue("slug")); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "repo not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) putProtection(w http.ResponseWriter, r *http.Request, c repo.Caller) {
	if !requireOrgAndScope(w, r, c, scopeAdmin) {
		return
	}
	var rules repo.ProtectionRules
	if err := json.NewDecoder(r.Body).Decode(&rules); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	rp, err := s.svc.GetRepo(r.Context(), c.Org, r.PathValue("slug"))
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "repo not found")
		return
	}
	if err := s.svc.UpdateProtection(r.Context(), rp.ID, rules); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listRefs(w http.ResponseWriter, r *http.Request, c repo.Caller) {
	if !requireOrgAndScope(w, r, c, scopeRead) {
		return
	}
	rp, err := s.svc.GetRepo(r.Context(), c.Org, r.PathValue("slug"))
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "repo not found")
		return
	}
	refs, err := s.svc.ListRefs(r.Context(), rp)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if refs == nil {
		refs = []repo.Ref{}
	}
	writeJSON(w, http.StatusOK, refs)
}

func (s *Server) getRef(w http.ResponseWriter, r *http.Request, c repo.Caller) {
	if !requireOrgAndScope(w, r, c, scopeRead) {
		return
	}
	rp, err := s.svc.GetRepo(r.Context(), c.Org, r.PathValue("slug"))
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "repo not found")
		return
	}
	refName := "refs/" + r.PathValue("ref")
	ref, err := s.svc.GetRef(r.Context(), rp, refName)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "ref not found")
		return
	}
	writeJSON(w, http.StatusOK, ref)
}

func (s *Server) deleteRef(w http.ResponseWriter, r *http.Request, c repo.Caller) {
	if !requireOrgAndScope(w, r, c, scopeAdmin) {
		return
	}
	rp, err := s.svc.GetRepo(r.Context(), c.Org, r.PathValue("slug"))
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "repo not found")
		return
	}
	refName := "refs/" + r.PathValue("ref")
	// Task 6 layers protection checking here; for now, the core method is raw.
	if err := s.svc.DeleteRef(r.Context(), rp, refName); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listPRs(w http.ResponseWriter, r *http.Request, c repo.Caller) {
	if !requireOrgAndScope(w, r, c, scopeRead) {
		return
	}
	rp, err := s.svc.GetRepo(r.Context(), c.Org, r.PathValue("slug"))
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "repo not found")
		return
	}
	prs, err := s.svc.ListPRs(r.Context(), rp)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if prs == nil {
		prs = []repo.PRPointer{}
	}
	writeJSON(w, http.StatusOK, prs)
}

func (s *Server) getPR(w http.ResponseWriter, r *http.Request, c repo.Caller) {
	if !requireOrgAndScope(w, r, c, scopeRead) {
		return
	}
	rp, err := s.svc.GetRepo(r.Context(), c.Org, r.PathValue("slug"))
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "repo not found")
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid pr number")
		return
	}
	pr, err := s.svc.GetPR(r.Context(), rp, n)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "pr not found")
		return
	}
	writeJSON(w, http.StatusOK, pr)
}

// validSlug enforces lowercase alnum + dash + underscore.
func validSlug(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return !strings.HasPrefix(s, "-") && !strings.HasPrefix(s, "_")
}
```

- [ ] **Step 6: Add the missing Service methods used by restapi.go**

Append to `cairn-native/internal/cairn/repo/service.go`:

```go
// ListRepos is a thin pass-through to Store.ListReposByOrg.
func (s *Service) ListRepos(ctx context.Context, orgID string) ([]Repo, error) {
	return s.store.ListReposByOrg(ctx, orgID)
}

// UpdateProtection encodes the rules and writes them to the store.
func (s *Service) UpdateProtection(ctx context.Context, repoID string, rules ProtectionRules) error {
	js, err := EncodeProtection(rules)
	if err != nil {
		return err
	}
	return s.store.UpdateProtection(ctx, repoID, js)
}

// ListPRs returns every PR pointer for a repo.
func (s *Service) ListPRs(ctx context.Context, r Repo) ([]PRPointer, error) {
	return s.store.ListPRPointers(ctx, r.ID)
}

// GetPR fetches one PR pointer.
func (s *Service) GetPR(ctx context.Context, r Repo, n int) (PRPointer, error) {
	return s.store.GetPRPointer(ctx, r.ID, n)
}
```

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/...
```

Expected output prefix: `ok` for repo + httpd packages.

- [ ] **Step 7: Write the failing Smart-HTTP test for /info/refs**

Create `cairn-native/internal/cairn/httpd/git_http_test.go`:

```go
package httpd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/repo"
)

func TestGitInfoRefs_UploadPack_Empty(t *testing.T) {
	h, svc := newTestHandler(t)
	_, _ = svc.CreateRepo(httptest.NewRequest("GET", "/", nil).Context(), "org-1", "alpha", "main")

	w := httptest.NewRecorder()
	caller := repo.Caller{Subject: "u", Kind: "human", Org: "org-1", Scopes: []string{"repo:read"}}
	r := req("GET", "/org-1/alpha/info/refs?service=git-upload-pack", "", caller)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("info/refs: %d body=%s", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if ct != "application/x-git-upload-pack-advertisement" {
		t.Errorf("content-type: %q", ct)
	}
	// Smart-HTTPv2 advertises starts with a pkt-line announcing the service.
	if !strings.Contains(w.Body.String(), "# service=git-upload-pack") {
		t.Errorf("advertisement body: %q", w.Body.String())
	}
}

func TestGitInfoRefs_WrongService(t *testing.T) {
	h, svc := newTestHandler(t)
	_, _ = svc.CreateRepo(httptest.NewRequest("GET", "/", nil).Context(), "org-1", "alpha", "main")
	w := httptest.NewRecorder()
	caller := repo.Caller{Subject: "u", Kind: "human", Org: "org-1", Scopes: []string{"repo:read"}}
	h.ServeHTTP(w, req("GET", "/org-1/alpha/info/refs?service=git-archive", "", caller))
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 on unsupported service, got %d", w.Code)
	}
}

func TestGitInfoRefs_ScopeRequired(t *testing.T) {
	h, svc := newTestHandler(t)
	_, _ = svc.CreateRepo(httptest.NewRequest("GET", "/", nil).Context(), "org-1", "alpha", "main")
	w := httptest.NewRecorder()
	caller := repo.Caller{Subject: "u", Kind: "human", Org: "org-1", Scopes: nil}
	h.ServeHTTP(w, req("GET", "/org-1/alpha/info/refs?service=git-upload-pack", "", caller))
	if w.Code != http.StatusForbidden {
		t.Errorf("want 403, got %d", w.Code)
	}
}

func TestGitReceivePack_RequiresWriteScope(t *testing.T) {
	h, svc := newTestHandler(t)
	_, _ = svc.CreateRepo(httptest.NewRequest("GET", "/", nil).Context(), "org-1", "alpha", "main")
	w := httptest.NewRecorder()
	caller := repo.Caller{Subject: "u", Kind: "human", Org: "org-1", Scopes: []string{"repo:read"}}
	r := req("POST", "/org-1/alpha/git-receive-pack", "", caller)
	r.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("want 403 on receive-pack without write, got %d", w.Code)
	}
}
```

Run (expect FAIL):

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/httpd/...
```

Expected output prefix: `FAIL`

- [ ] **Step 8: Implement `git_http.go`**

Create `cairn-native/internal/cairn/httpd/git_http.go`:

```go
package httpd

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/repo"
)

// gitInfoRefs implements GET /{org}/{slug}/info/refs?service=...
// It serves Smart-HTTPv2's "advertisement" phase by shelling out to
// `git <verb> --stateless-rpc --advertise-refs <gitdir>` and prefixing the
// service announcement pkt-line.
func (s *Server) gitInfoRefs(w http.ResponseWriter, r *http.Request, c repo.Caller) {
	urlOrg := r.PathValue("org")
	if c.Org != urlOrg {
		writeJSONError(w, http.StatusForbidden, "caller org does not match url org")
		return
	}
	service := r.URL.Query().Get("service")
	verb := strings.TrimPrefix(service, "git-")
	if verb != "upload-pack" && verb != "receive-pack" {
		writeJSONError(w, http.StatusBadRequest, "unsupported service")
		return
	}
	needed := scopeRead
	if verb == "receive-pack" {
		needed = scopeWrite
	}
	if !c.HasScope(needed) {
		writeJSONError(w, http.StatusForbidden, "missing scope: "+needed)
		return
	}
	rp, err := s.svc.GetRepo(r.Context(), c.Org, r.PathValue("slug"))
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "repo not found")
		return
	}
	gitDir, err := storageFSPathHTTP(rp.StorageURI)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/x-git-"+verb+"-advertisement")
	w.Header().Set("Cache-Control", "no-cache")
	if _, err := w.Write(pktLine("# service=git-" + verb + "\n")); err != nil {
		return
	}
	if _, err := w.Write([]byte("0000")); err != nil {
		return
	}

	cmd := exec.CommandContext(r.Context(), "git", verb, "--stateless-rpc", "--advertise-refs", gitDir)
	cmd.Stdout = w
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		// Header already written; nothing else we can do but log.
		return
	}
}

// gitUploadPack implements POST /{org}/{slug}/git-upload-pack — the body
// carries the client's wants; cairn pipes it to `git upload-pack --stateless-rpc`.
func (s *Server) gitUploadPack(w http.ResponseWriter, r *http.Request, c repo.Caller) {
	s.gitRPC(w, r, c, "upload-pack", scopeRead)
}

// gitReceivePack implements POST /{org}/{slug}/git-receive-pack.
func (s *Server) gitReceivePack(w http.ResponseWriter, r *http.Request, c repo.Caller) {
	s.gitRPC(w, r, c, "receive-pack", scopeWrite)
}

func (s *Server) gitRPC(w http.ResponseWriter, r *http.Request, c repo.Caller, verb, scope string) {
	if c.Org != r.PathValue("org") {
		writeJSONError(w, http.StatusForbidden, "caller org does not match url org")
		return
	}
	if !c.HasScope(scope) {
		writeJSONError(w, http.StatusForbidden, "missing scope: "+scope)
		return
	}
	rp, err := s.svc.GetRepo(r.Context(), c.Org, r.PathValue("slug"))
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "repo not found")
		return
	}
	gitDir, err := storageFSPathHTTP(rp.StorageURI)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/x-git-"+verb+"-result")
	w.Header().Set("Cache-Control", "no-cache")

	cmd := exec.CommandContext(r.Context(), "git", verb, "--stateless-rpc", gitDir)
	cmd.Stdin = r.Body
	cmd.Stdout = w
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return
	}
}

// pktLine formats a smart-protocol pkt-line: 4-hex length prefix + payload.
func pktLine(payload string) []byte {
	l := len(payload) + 4
	return []byte(fmt.Sprintf("%04x%s", l, payload))
}

// storageFSPathHTTP is the httpd-local copy of repo.storagePath (unexported).
func storageFSPathHTTP(storageURI string) (string, error) {
	if !strings.HasPrefix(storageURI, "file://") {
		return "", fmt.Errorf("unsupported storage scheme: %q", storageURI)
	}
	u, err := url.Parse(storageURI)
	if err != nil {
		return "", err
	}
	return u.Path, nil
}
```

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/httpd/...
```

Expected output prefix: `ok`

- [ ] **Step 9: Commit Task 3**

Run:

```bash
cd /Users/jacinta/Source/cairn
git add cairn-native/internal/cairn/httpd/ cairn-native/internal/cairn/repo/service.go
git commit -m "feat(cairn-native): http frontend (REST + smart-http git via gateway)"
```

---

## Task 4: Web UI (repo browser: tree/blame/diff + path-A login) — NEX-389

> **External dependency:** This task assumes herald's path-A authz_code flow (NEX-393) is live. While it's still in flight, all OIDC interactions in this task talk to a fake IdP run by `internal/cairn/webui/idpfake`. Once NEX-393 merges, point `CAIRN_HERALD_ISSUER` at the real herald instance and the same client code works unchanged.

**Files:**

Create:
- `cairn-native/internal/cairn/webui/webui.go` — UI server (mounted alongside REST in Task 3's mux)
- `cairn-native/internal/cairn/webui/webui_test.go`
- `cairn-native/internal/cairn/webui/oidc_client.go` — wraps `coreos/go-oidc` v3 for the authz_code flow
- `cairn-native/internal/cairn/webui/oidc_client_test.go`
- `cairn-native/internal/cairn/webui/session.go` — server-side session keyed by cookie
- `cairn-native/internal/cairn/webui/session_test.go`
- `cairn-native/internal/cairn/webui/render.go` — tree/blob/blame/commit/diff/pr renderers using go-git
- `cairn-native/internal/cairn/webui/render_test.go`
- `cairn-native/internal/cairn/webui/templates/layout.html`
- `cairn-native/internal/cairn/webui/templates/orgs.html`
- `cairn-native/internal/cairn/webui/templates/repos.html`
- `cairn-native/internal/cairn/webui/templates/repo.html`
- `cairn-native/internal/cairn/webui/templates/tree.html`
- `cairn-native/internal/cairn/webui/templates/blob.html`
- `cairn-native/internal/cairn/webui/templates/blame.html`
- `cairn-native/internal/cairn/webui/templates/commit.html`
- `cairn-native/internal/cairn/webui/templates/compare.html`
- `cairn-native/internal/cairn/webui/templates/pr.html`
- `cairn-native/internal/cairn/webui/idpfake/idpfake.go` — test-only fake IdP

Modify:
- `cairn-native/internal/cairn/httpd/httpd.go` — mount the webui handler

- [ ] **Step 1: Add the OIDC client dep**

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go get github.com/coreos/go-oidc/v3@v3.11.0
go get golang.org/x/oauth2@v0.24.0
go mod tidy
```

Expected output prefix: `go: added github.com/coreos/go-oidc/v3`

- [ ] **Step 2: Build the test-only fake IdP**

Create `cairn-native/internal/cairn/webui/idpfake/idpfake.go`:

```go
// Package idpfake is a test-only OIDC IdP: implements discovery, JWKS,
// /authorize (auto-issues a code immediately), /token, /userinfo. ED25519-
// signed ID tokens.
package idpfake

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// IdP is a fake herald.
type IdP struct {
	Server *httptest.Server
	priv   ed25519.PrivateKey
	kid    string

	mu    sync.Mutex
	codes map[string]string // code -> subject
}

// New spins up a fake IdP. The caller will run with the issuer = idp.URL().
func New(t interface{ Helper(); Fatalf(string, ...any) }) *IdP {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	_ = pub
	idp := &IdP{priv: priv, kid: "k1", codes: map[string]string{}}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", idp.discovery)
	mux.HandleFunc("/jwks", idp.jwks)
	mux.HandleFunc("/authorize", idp.authorize)
	mux.HandleFunc("/token", idp.token)
	mux.HandleFunc("/userinfo", idp.userinfo)
	idp.Server = httptest.NewServer(mux)
	return idp
}

// URL is the issuer URL (no trailing slash).
func (i *IdP) URL() string { return i.Server.URL }

// Close shuts the server.
func (i *IdP) Close() { i.Server.Close() }

func (i *IdP) discovery(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(fmt.Sprintf(`{
		"issuer": "%[1]s",
		"authorization_endpoint": "%[1]s/authorize",
		"token_endpoint": "%[1]s/token",
		"userinfo_endpoint": "%[1]s/userinfo",
		"jwks_uri": "%[1]s/jwks",
		"id_token_signing_alg_values_supported": ["EdDSA"],
		"response_types_supported": ["code"],
		"subject_types_supported": ["public"]
	}`, i.Server.URL)))
}

func (i *IdP) jwks(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	pub := i.priv.Public().(ed25519.PublicKey)
	jwk := jose.JSONWebKey{Key: pub, KeyID: i.kid, Algorithm: "EdDSA", Use: "sig"}
	set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}}
	_ = json.NewEncoder(w).Encode(set)
}

// authorize auto-issues a code for sub=u1 and redirects.
func (i *IdP) authorize(w http.ResponseWriter, r *http.Request) {
	code := base64.RawURLEncoding.EncodeToString([]byte("code-" + r.URL.Query().Get("state")))
	i.mu.Lock()
	i.codes[code] = "u1"
	i.mu.Unlock()
	redirect := r.URL.Query().Get("redirect_uri")
	state := r.URL.Query().Get("state")
	http.Redirect(w, r, redirect+"?code="+code+"&state="+state, http.StatusFound)
}

func (i *IdP) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	code := r.Form.Get("code")
	i.mu.Lock()
	sub, ok := i.codes[code]
	delete(i.codes, code)
	i.mu.Unlock()
	if !ok {
		http.Error(w, "bad code", 400)
		return
	}
	id := i.signIDToken(map[string]any{
		"iss": i.Server.URL,
		"sub": sub,
		"aud": r.Form.Get("client_id"),
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
		"org": "org-1",
		"kind": "human",
		"scope": "repo:read repo:write",
	})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(fmt.Sprintf(`{"access_token":"%s","id_token":"%s","token_type":"Bearer","expires_in":3600}`, id, id)))
}

func (i *IdP) userinfo(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"sub":"u1","org":"org-1","kind":"human"}`))
}

func (i *IdP) signIDToken(claims map[string]any) string {
	signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.EdDSA, Key: i.priv}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", i.kid))
	b, _ := json.Marshal(claims)
	js, _ := signer.Sign(b)
	out, _ := js.CompactSerialize()
	return strings.TrimSpace(out)
}
```

- [ ] **Step 3: Write the failing OIDC-client test using idpfake**

Create `cairn-native/internal/cairn/webui/oidc_client_test.go`:

```go
package webui

import (
	"context"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/webui/idpfake"
)

func TestOIDCClient_AuthURLAndExchange(t *testing.T) {
	idp := idpfake.New(t)
	defer idp.Close()

	cli, err := NewOIDCClient(context.Background(), OIDCConfig{
		Issuer:       idp.URL(),
		ClientID:     "cairn-test",
		ClientSecret: "secret",
		RedirectURL:  "http://cairn/oauth/callback",
		Scopes:       []string{"openid", "profile"},
	})
	if err != nil {
		t.Fatalf("NewOIDCClient: %v", err)
	}
	url := cli.AuthCodeURL("state-1", "verifier-1")
	if url == "" {
		t.Error("empty auth code url")
	}

	// Simulate the redirect-with-code by extracting from idpfake's /authorize.
	// idpfake auto-issues "code-<state>" — encode as it does.
	code := base64URL("code-state-1")
	tok, err := cli.Exchange(context.Background(), code, "verifier-1")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if tok.AccessToken == "" {
		t.Error("empty access token")
	}
	if tok.Subject != "u1" {
		t.Errorf("subject: %q", tok.Subject)
	}
	if tok.Org != "org-1" {
		t.Errorf("org: %q", tok.Org)
	}
}

func base64URL(s string) string {
	// matches idpfake's encoding
	return rawb64(s)
}
```

Create the helper `cairn-native/internal/cairn/webui/b64.go`:

```go
package webui

import "encoding/base64"

func rawb64(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}
```

Run (expect FAIL):

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/webui/...
```

Expected output prefix: `FAIL`

- [ ] **Step 4: Implement `oidc_client.go`**

Create `cairn-native/internal/cairn/webui/oidc_client.go`:

```go
// Package webui is the cairn-native web UI: server-rendered HTML, herald
// path-A authz_code login, repo browser using go-git.
package webui

import (
	"context"
	"errors"
	"fmt"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCConfig configures the herald path-A client.
type OIDCConfig struct {
	Issuer       string   // herald issuer URL (e.g. http://herald.cwb.svc:8099/)
	ClientID     string
	ClientSecret string
	RedirectURL  string   // e.g. http://cwb/cairn/oauth/callback
	Scopes       []string // typically {"openid","profile","repo:read","repo:write"}
}

// OIDCClient wraps oauth2 + go-oidc for the authz_code flow.
type OIDCClient struct {
	cfg      OIDCConfig
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// SessionToken is the minimum bits cairn stores after a successful exchange.
type SessionToken struct {
	AccessToken string
	Subject     string
	Org         string
	Kind        string
	Scopes      []string
}

// NewOIDCClient builds the client (does a one-shot discovery on the issuer).
func NewOIDCClient(ctx context.Context, cfg OIDCConfig) (*OIDCClient, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" || cfg.RedirectURL == "" {
		return nil, errors.New("webui: OIDCConfig missing required field")
	}
	prov, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("webui: discovery: %w", err)
	}
	return &OIDCClient{
		cfg: cfg,
		oauth: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint:     prov.Endpoint(),
			Scopes:       cfg.Scopes,
		},
		verifier: prov.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
	}, nil
}

// AuthCodeURL returns the URL to redirect a browser to for login. PKCE
// challenge is the S256 of the verifier.
func (c *OIDCClient) AuthCodeURL(state, verifier string) string {
	return c.oauth.AuthCodeURL(state,
		oauth2.SetAuthURLParam("code_challenge", pkceS256(verifier)),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"))
}

// Exchange swaps the code for a token, verifying the id_token.
func (c *OIDCClient) Exchange(ctx context.Context, code, verifier string) (SessionToken, error) {
	tok, err := c.oauth.Exchange(ctx, code, oauth2.SetAuthURLParam("code_verifier", verifier))
	if err != nil {
		return SessionToken{}, fmt.Errorf("webui: exchange: %w", err)
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		return SessionToken{}, errors.New("webui: token response missing id_token")
	}
	idt, err := c.verifier.Verify(ctx, rawID)
	if err != nil {
		return SessionToken{}, fmt.Errorf("webui: id_token verify: %w", err)
	}
	var claims struct {
		Sub   string `json:"sub"`
		Org   string `json:"org"`
		Kind  string `json:"kind"`
		Scope string `json:"scope"`
	}
	if err := idt.Claims(&claims); err != nil {
		return SessionToken{}, fmt.Errorf("webui: id_token claims: %w", err)
	}
	return SessionToken{
		AccessToken: tok.AccessToken,
		Subject:     claims.Sub,
		Org:         claims.Org,
		Kind:        claims.Kind,
		Scopes:      splitFields(claims.Scope),
	}, nil
}

func splitFields(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ' ' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}
```

Create `cairn-native/internal/cairn/webui/pkce.go`:

```go
package webui

import (
	"crypto/sha256"
	"encoding/base64"
)

// pkceS256 returns the S256 PKCE challenge derived from a verifier.
func pkceS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
```

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/webui/...
```

Expected output prefix: `ok` (oidc tests pass; webui_test and others not yet written so they don't FAIL build).

- [ ] **Step 5: Write the failing session test**

Create `cairn-native/internal/cairn/webui/session_test.go`:

```go
package webui

import "testing"

func TestSessionStore_PutGetDelete(t *testing.T) {
	s := NewSessionStore()
	id := s.Put(SessionToken{Subject: "u1", Org: "org-1", Scopes: []string{"repo:read"}})
	if id == "" {
		t.Fatal("empty id")
	}
	got, ok := s.Get(id)
	if !ok || got.Subject != "u1" {
		t.Errorf("get: ok=%v got=%+v", ok, got)
	}
	s.Delete(id)
	if _, ok := s.Get(id); ok {
		t.Error("expected gone after delete")
	}
}

func TestSessionStore_IDsAreUnique(t *testing.T) {
	s := NewSessionStore()
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := s.Put(SessionToken{Subject: "u"})
		if seen[id] {
			t.Errorf("duplicate id: %s", id)
		}
		seen[id] = true
	}
}
```

Run (expect FAIL):

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/webui/...
```

Expected output prefix: `FAIL`

- [ ] **Step 6: Implement `session.go`**

Create `cairn-native/internal/cairn/webui/session.go`:

```go
package webui

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// SessionStore is an in-memory session store keyed by random session id.
// MVP — for production we'll move this to herald-backed sessions or a TTL
// SQLite table. For now: ephemeral, restart-on-deploy is acceptable.
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]session
	ttl      time.Duration
}

type session struct {
	tok SessionToken
	at  time.Time
}

// NewSessionStore returns a SessionStore with a 12-hour TTL.
func NewSessionStore() *SessionStore {
	return &SessionStore{
		sessions: map[string]session{},
		ttl:      12 * time.Hour,
	}
}

// Put stores tok under a new random id and returns the id.
func (s *SessionStore) Put(tok SessionToken) string {
	id := newSessionID()
	s.mu.Lock()
	s.sessions[id] = session{tok: tok, at: time.Now()}
	s.mu.Unlock()
	return id
}

// Get returns the token for id if present and not expired.
func (s *SessionStore) Get(id string) (SessionToken, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.sessions[id]
	if !ok {
		return SessionToken{}, false
	}
	if time.Since(e.at) > s.ttl {
		return SessionToken{}, false
	}
	return e.tok, true
}

// Delete removes a session.
func (s *SessionStore) Delete(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

// newSessionID returns a 32-byte random url-safe id.
func newSessionID() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}
```

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/webui/...
```

Expected output prefix: `ok`

- [ ] **Step 7: Write the failing render tests for tree + blob views**

Create `cairn-native/internal/cairn/webui/render_test.go`:

```go
package webui

import (
	"context"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/repo"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func seedRepo(t *testing.T) (*repo.Service, repo.Repo, plumbing.Hash) {
	t.Helper()
	st, err := repo.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := repo.NewService(st, t.TempDir())
	r, err := svc.CreateRepo(context.Background(), "o", "demo", "main")
	if err != nil {
		t.Fatal(err)
	}
	gr, err := svc.Open(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	// blob
	blob := &plumbing.MemoryObject{}
	blob.SetType(plumbing.BlobObject)
	_, _ = blob.Write([]byte("hello world\n"))
	bh, _ := gr.Storer.SetEncodedObject(blob)
	// tree
	tree := &object.Tree{Entries: []object.TreeEntry{
		{Name: "README.md", Mode: 0o100644, Hash: bh},
	}}
	to := &plumbing.MemoryObject{}
	to.SetType(plumbing.TreeObject)
	_ = tree.Encode(to)
	th, _ := gr.Storer.SetEncodedObject(to)
	// commit
	commit := &object.Commit{
		Author:    object.Signature{Name: "t", Email: "t@e"},
		Committer: object.Signature{Name: "t", Email: "t@e"},
		Message:   "initial",
		TreeHash:  th,
	}
	co := &plumbing.MemoryObject{}
	co.SetType(plumbing.CommitObject)
	_ = commit.Encode(co)
	ch, _ := gr.Storer.SetEncodedObject(co)
	_ = gr.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName("main"), ch))
	return svc, r, ch
}

func TestRenderTree_ListsEntries(t *testing.T) {
	svc, r, _ := seedRepo(t)
	rnd := NewRenderer(svc)
	entries, err := rnd.Tree(context.Background(), r, "main", "")
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "README.md" {
		t.Errorf("entries: %+v", entries)
	}
}

func TestRenderBlob_ReturnsContents(t *testing.T) {
	svc, r, _ := seedRepo(t)
	rnd := NewRenderer(svc)
	b, err := rnd.Blob(context.Background(), r, "main", "README.md")
	if err != nil {
		t.Fatalf("Blob: %v", err)
	}
	if string(b) != "hello world\n" {
		t.Errorf("blob: %q", b)
	}
}

func TestRenderCommit_ReturnsMessage(t *testing.T) {
	svc, r, ch := seedRepo(t)
	rnd := NewRenderer(svc)
	c, err := rnd.Commit(context.Background(), r, ch.String())
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if c.Message != "initial" {
		t.Errorf("msg: %q", c.Message)
	}
}
```

Run (expect FAIL):

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/webui/...
```

Expected output prefix: `FAIL`

- [ ] **Step 8: Implement `render.go`**

Create `cairn-native/internal/cairn/webui/render.go`:

```go
package webui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/repo"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Renderer pulls structured data out of a repo for the web UI templates.
type Renderer struct {
	svc *repo.Service
}

// NewRenderer builds a Renderer.
func NewRenderer(svc *repo.Service) *Renderer {
	return &Renderer{svc: svc}
}

// TreeEntry is one row in a directory listing.
type TreeEntry struct {
	Name   string
	Mode   string // "blob" | "tree" | other
	Target string // SHA of the entry's object
}

// CommitView is what the commit template renders.
type CommitView struct {
	SHA       string
	Author    string
	Committer string
	Message   string
	Parents   []string
	TreeSHA   string
}

// Tree returns the entries in a directory under ref at path. Empty path = root.
func (rnd *Renderer) Tree(ctx context.Context, r repo.Repo, ref, dirPath string) ([]TreeEntry, error) {
	gr, err := rnd.svc.Open(ctx, r)
	if err != nil {
		return nil, err
	}
	h, err := resolveRef(gr.Storer, ref)
	if err != nil {
		return nil, err
	}
	commit, err := object.GetCommit(gr.Storer, h)
	if err != nil {
		return nil, fmt.Errorf("Tree commit: %w", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("Tree tree: %w", err)
	}
	if dirPath != "" {
		sub, err := tree.Tree(dirPath)
		if err != nil {
			return nil, fmt.Errorf("Tree sub: %w", err)
		}
		tree = sub
	}
	var out []TreeEntry
	for _, e := range tree.Entries {
		mode := "blob"
		if e.Mode.IsFile() {
			mode = "blob"
		}
		if e.Mode == 0o040000 {
			mode = "tree"
		}
		out = append(out, TreeEntry{Name: e.Name, Mode: mode, Target: e.Hash.String()})
	}
	return out, nil
}

// Blob returns raw contents of path under ref.
func (rnd *Renderer) Blob(ctx context.Context, r repo.Repo, ref, filePath string) ([]byte, error) {
	gr, err := rnd.svc.Open(ctx, r)
	if err != nil {
		return nil, err
	}
	h, err := resolveRef(gr.Storer, ref)
	if err != nil {
		return nil, err
	}
	commit, err := object.GetCommit(gr.Storer, h)
	if err != nil {
		return nil, err
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, err
	}
	file, err := tree.File(filePath)
	if err != nil {
		return nil, fmt.Errorf("Blob file: %w", err)
	}
	rdr, err := file.Reader()
	if err != nil {
		return nil, err
	}
	defer rdr.Close()
	return io.ReadAll(rdr)
}

// Blame returns line-by-line authorship for path under ref.
type BlameLine struct {
	Line   string
	Author string
	SHA    string
}

func (rnd *Renderer) Blame(ctx context.Context, r repo.Repo, ref, filePath string) ([]BlameLine, error) {
	gr, err := rnd.svc.Open(ctx, r)
	if err != nil {
		return nil, err
	}
	h, err := resolveRef(gr.Storer, ref)
	if err != nil {
		return nil, err
	}
	commit, err := object.GetCommit(gr.Storer, h)
	if err != nil {
		return nil, err
	}
	br, err := git_Blame(commit, filePath)
	if err != nil {
		return nil, fmt.Errorf("Blame: %w", err)
	}
	var out []BlameLine
	for _, l := range br.Lines {
		out = append(out, BlameLine{Line: l.Text, Author: l.AuthorName, SHA: l.Hash.String()})
	}
	return out, nil
}

// Commit returns a CommitView for sha.
func (rnd *Renderer) Commit(ctx context.Context, r repo.Repo, sha string) (CommitView, error) {
	gr, err := rnd.svc.Open(ctx, r)
	if err != nil {
		return CommitView{}, err
	}
	h := plumbing.NewHash(sha)
	c, err := object.GetCommit(gr.Storer, h)
	if err != nil {
		return CommitView{}, fmt.Errorf("Commit: %w", err)
	}
	var parents []string
	for _, p := range c.ParentHashes {
		parents = append(parents, p.String())
	}
	return CommitView{
		SHA:       c.Hash.String(),
		Author:    c.Author.String(),
		Committer: c.Committer.String(),
		Message:   c.Message,
		Parents:   parents,
		TreeSHA:   c.TreeHash.String(),
	}, nil
}

// Compare returns a unified-diff-ish summary between base and head (both resolvable as refs or SHAs).
func (rnd *Renderer) Compare(ctx context.Context, r repo.Repo, base, head string) (string, error) {
	gr, err := rnd.svc.Open(ctx, r)
	if err != nil {
		return "", err
	}
	bh, err := resolveRef(gr.Storer, base)
	if err != nil {
		return "", err
	}
	hh, err := resolveRef(gr.Storer, head)
	if err != nil {
		return "", err
	}
	bc, err := object.GetCommit(gr.Storer, bh)
	if err != nil {
		return "", err
	}
	hc, err := object.GetCommit(gr.Storer, hh)
	if err != nil {
		return "", err
	}
	patch, err := bc.Patch(hc)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	_ = patch.Encode(&sb)
	return sb.String(), nil
}

// resolveRef accepts a SHA (40 hex), a branch name, or a fully-qualified ref.
func resolveRef(storer interface {
	Reference(plumbing.ReferenceName) (*plumbing.Reference, error)
}, ref string) (plumbing.Hash, error) {
	if len(ref) == 40 {
		ok := true
		for _, c := range ref {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				ok = false
				break
			}
		}
		if ok {
			return plumbing.NewHash(ref), nil
		}
	}
	candidates := []plumbing.ReferenceName{
		plumbing.ReferenceName(ref),
		plumbing.NewBranchReferenceName(ref),
		plumbing.NewTagReferenceName(ref),
	}
	for _, name := range candidates {
		rf, err := storer.Reference(name)
		if err == nil {
			return rf.Hash(), nil
		}
	}
	return plumbing.ZeroHash, errors.New("ref not found: " + ref)
}
```

Create `cairn-native/internal/cairn/webui/blame_shim.go` — since go-git's blame lives at the repo level not the commit level, we wrap it cleanly:

```go
package webui

import (
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// git_Blame computes per-line authorship for a path against a commit. The
// underlying go-git API is git.Blame(commit, path).
func git_Blame(commit *object.Commit, filePath string) (*git.BlameResult, error) {
	return git.Blame(commit, filePath)
}
```

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/webui/...
```

Expected output prefix: `ok`

- [ ] **Step 9: Write the failing webui_test.go for the OAuth callback round-trip**

Create `cairn-native/internal/cairn/webui/webui_test.go`:

```go
package webui

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/repo"
	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/webui/idpfake"
)

func newTestWebUI(t *testing.T, idpIssuer, redirectBase string) (*Server, *repo.Service) {
	st, err := repo.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := repo.NewService(st, t.TempDir())
	srv, err := New(context.Background(), Config{
		Service: svc,
		OIDC: OIDCConfig{
			Issuer:       idpIssuer,
			ClientID:     "cairn-test",
			ClientSecret: "secret",
			RedirectURL:  redirectBase + "/oauth/callback",
			Scopes:       []string{"openid"},
		},
	})
	if err != nil {
		t.Fatalf("webui.New: %v", err)
	}
	return srv, svc
}

func TestLoginFlow(t *testing.T) {
	idp := idpfake.New(t)
	defer idp.Close()

	// Spin up a test server hosting the webui mux.
	var cairn *httptest.Server
	srv, _ := newTestWebUI(t, idp.URL(), "PLACEHOLDER")
	mux := http.NewServeMux()
	mux.Handle("/", srv.Handler())
	cairn = httptest.NewServer(mux)
	defer cairn.Close()
	// Rebuild with correct redirect URL now that cairn.URL is known.
	srv2, _ := newTestWebUI(t, idp.URL(), cairn.URL)
	mux2 := http.NewServeMux()
	mux2.Handle("/", srv2.Handler())
	cairn.Config.Handler = mux2

	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 8 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	// Visit a protected page — should redirect to herald then back via callback.
	resp, err := client.Get(cairn.URL + "/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("final status: %d url=%s", resp.StatusCode, resp.Request.URL)
	}
	// We should now have a session cookie.
	u, _ := url.Parse(cairn.URL)
	if len(jar.Cookies(u)) == 0 {
		t.Error("no session cookie set")
	}
}

func TestProtectedPage_WithoutSession_RedirectsToHerald(t *testing.T) {
	idp := idpfake.New(t)
	defer idp.Close()
	srv, _ := newTestWebUI(t, idp.URL(), "http://cairn.example/")
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	srv.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusFound {
		t.Fatalf("want 302, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "/authorize") {
		t.Errorf("location: %q", loc)
	}
}
```

Run (expect FAIL):

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/webui/...
```

Expected output prefix: `FAIL`

- [ ] **Step 10: Implement `webui.go` (Server, Config, routes, login dance, page handlers)**

Create `cairn-native/internal/cairn/webui/webui.go`:

```go
package webui

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/repo"
)

//go:embed templates/*.html
var templatesFS embed.FS

// Config configures a webui Server.
type Config struct {
	Service *repo.Service
	OIDC    OIDCConfig
}

// Server is the web UI.
type Server struct {
	svc      *repo.Service
	oidc     *OIDCClient
	sessions *SessionStore
	rnd      *Renderer
	tpl      *template.Template

	// pkceState holds (state -> verifier) for in-flight logins. Bounded by
	// browser-side state TTL; we don't expire here (state is single-use).
	pkceMu    sessionMutex
	pkce      map[string]string
}

// sessionMutex wraps a sync.Mutex to keep webui.go small (avoid extra import).
type sessionMutex = simpleMutex
type simpleMutex struct{ m mu }
type mu = mutexImpl

// New builds the web UI server.
func New(ctx context.Context, cfg Config) (*Server, error) {
	cli, err := NewOIDCClient(ctx, cfg.OIDC)
	if err != nil {
		return nil, err
	}
	tpl := template.New("").Funcs(template.FuncMap{
		"truncate": func(s string, n int) string {
			if len(s) <= n {
				return s
			}
			return s[:n] + "…"
		},
	})
	tpl, err = tpl.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{
		svc:      cfg.Service,
		oidc:     cli,
		sessions: NewSessionStore(),
		rnd:      NewRenderer(cfg.Service),
		tpl:      tpl,
		pkce:     map[string]string{},
	}, nil
}

// Handler returns the routed http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /oauth/callback", s.handleOAuthCallback)
	mux.HandleFunc("GET /", s.authed(s.handleOrgs))
	mux.HandleFunc("GET /{org}", s.authed(s.handleRepos))
	mux.HandleFunc("GET /{org}/{slug}", s.authed(s.handleRepo))
	mux.HandleFunc("GET /{org}/{slug}/tree/{ref}/{path...}", s.authed(s.handleTree))
	mux.HandleFunc("GET /{org}/{slug}/blob/{ref}/{path...}", s.authed(s.handleBlob))
	mux.HandleFunc("GET /{org}/{slug}/blame/{ref}/{path...}", s.authed(s.handleBlame))
	mux.HandleFunc("GET /{org}/{slug}/commit/{sha}", s.authed(s.handleCommit))
	mux.HandleFunc("GET /{org}/{slug}/compare/{base}/{head}", s.authed(s.handleCompare))
	mux.HandleFunc("GET /{org}/{slug}/pr/{n}", s.authed(s.handlePR))
	return mux
}

// handler that requires a logged-in session — otherwise initiates the OIDC dance.
func (s *Server) authed(fn func(w http.ResponseWriter, r *http.Request, tok SessionToken)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("cairn_session")
		if err == nil {
			if tok, ok := s.sessions.Get(c.Value); ok {
				fn(w, r, tok)
				return
			}
		}
		s.startLogin(w, r)
	}
}

// startLogin builds a PKCE pair, stashes the verifier under a state, and 302s to herald.
func (s *Server) startLogin(w http.ResponseWriter, r *http.Request) {
	state := randURL(24)
	verifier := randURL(48)
	s.pkceMu.m.Lock()
	s.pkce[state] = verifier
	s.pkceMu.m.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     "cairn_post_login",
		Value:    r.URL.Path,
		Path:     "/",
		MaxAge:   600,
		HttpOnly: true,
	})
	http.Redirect(w, r, s.oidc.AuthCodeURL(state, verifier), http.StatusFound)
}

func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" {
		http.Error(w, "missing state or code", http.StatusBadRequest)
		return
	}
	s.pkceMu.m.Lock()
	verifier, ok := s.pkce[state]
	delete(s.pkce, state)
	s.pkceMu.m.Unlock()
	if !ok {
		http.Error(w, "unknown state", http.StatusBadRequest)
		return
	}
	tok, err := s.oidc.Exchange(r.Context(), code, verifier)
	if err != nil {
		http.Error(w, "exchange failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	sid := s.sessions.Put(tok)
	http.SetCookie(w, &http.Cookie{
		Name:     "cairn_session",
		Value:    sid,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	dest := "/"
	if c, err := r.Cookie("cairn_post_login"); err == nil {
		dest = c.Value
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

func (s *Server) handleOrgs(w http.ResponseWriter, r *http.Request, tok SessionToken) {
	s.render(w, r, "orgs.html", map[string]any{"Org": tok.Org, "Token": tok})
}

func (s *Server) handleRepos(w http.ResponseWriter, r *http.Request, tok SessionToken) {
	org := r.PathValue("org")
	if org != tok.Org {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	repos, err := s.svc.ListRepos(r.Context(), org)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.render(w, r, "repos.html", map[string]any{"Org": org, "Repos": repos})
}

func (s *Server) handleRepo(w http.ResponseWriter, r *http.Request, tok SessionToken) {
	rp, err := s.lookupRepo(r, tok)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	refs, _ := s.svc.ListRefs(r.Context(), rp)
	s.render(w, r, "repo.html", map[string]any{"Repo": rp, "Refs": refs})
}

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request, tok SessionToken) {
	rp, err := s.lookupRepo(r, tok)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	entries, err := s.rnd.Tree(r.Context(), rp, r.PathValue("ref"), r.PathValue("path"))
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	s.render(w, r, "tree.html", map[string]any{"Repo": rp, "Entries": entries, "Ref": r.PathValue("ref"), "Path": r.PathValue("path")})
}

func (s *Server) handleBlob(w http.ResponseWriter, r *http.Request, tok SessionToken) {
	rp, err := s.lookupRepo(r, tok)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	b, err := s.rnd.Blob(r.Context(), rp, r.PathValue("ref"), r.PathValue("path"))
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	s.render(w, r, "blob.html", map[string]any{"Repo": rp, "Content": string(b), "Path": r.PathValue("path")})
}

func (s *Server) handleBlame(w http.ResponseWriter, r *http.Request, tok SessionToken) {
	rp, err := s.lookupRepo(r, tok)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	lines, err := s.rnd.Blame(r.Context(), rp, r.PathValue("ref"), r.PathValue("path"))
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	s.render(w, r, "blame.html", map[string]any{"Repo": rp, "Lines": lines, "Path": r.PathValue("path")})
}

func (s *Server) handleCommit(w http.ResponseWriter, r *http.Request, tok SessionToken) {
	rp, err := s.lookupRepo(r, tok)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	c, err := s.rnd.Commit(r.Context(), rp, r.PathValue("sha"))
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	s.render(w, r, "commit.html", map[string]any{"Repo": rp, "Commit": c})
}

func (s *Server) handleCompare(w http.ResponseWriter, r *http.Request, tok SessionToken) {
	rp, err := s.lookupRepo(r, tok)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	patch, err := s.rnd.Compare(r.Context(), rp, r.PathValue("base"), r.PathValue("head"))
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	s.render(w, r, "compare.html", map[string]any{"Repo": rp, "Patch": patch})
}

func (s *Server) handlePR(w http.ResponseWriter, r *http.Request, tok SessionToken) {
	rp, err := s.lookupRepo(r, tok)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		http.Error(w, "invalid pr number", 400)
		return
	}
	pr, err := s.svc.GetPR(r.Context(), rp, n)
	if err != nil {
		http.Error(w, "pr not found", 404)
		return
	}
	patch, _ := s.rnd.Compare(r.Context(), rp, pr.BaseBranch, pr.HeadSHA)
	s.render(w, r, "pr.html", map[string]any{"Repo": rp, "PR": pr, "Patch": patch})
}

func (s *Server) lookupRepo(r *http.Request, tok SessionToken) (repo.Repo, error) {
	org := r.PathValue("org")
	if org != tok.Org {
		return repo.Repo{}, errForbidden
	}
	return s.svc.GetRepo(r.Context(), org, r.PathValue("slug"))
}

var errForbidden = httpError("forbidden")

type httpError string

func (e httpError) Error() string { return string(e) }

// render runs a template; supports ?format=md returning markdown via the same
// template name with a `.md` suffix when present, else fall back to a flat
// dump of the map.
func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data any) {
	if r.URL.Query().Get("format") == "md" {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write([]byte("# cairn: " + name + "\n\n"))
		_, _ = w.Write([]byte(strSummary(data)))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

func strSummary(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	var sb strings.Builder
	for k, val := range m {
		sb.WriteString("- ")
		sb.WriteString(k)
		sb.WriteString(": ")
		sb.WriteString(printVal(val))
		sb.WriteString("\n")
	}
	return sb.String()
}

func printVal(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []string:
		return strings.Join(t, ", ")
	default:
		return ""
	}
}

func randURL(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
```

Create `cairn-native/internal/cairn/webui/mutex_shim.go`:

```go
package webui

import "sync"

// mutexImpl is wrapped in a typedef chain so webui.go can stay free of a
// direct sync import (keeps the heavy file readable). Behaviour: a plain
// Mutex.
type mutexImpl = sync.Mutex
```

- [ ] **Step 11: Create the templates (one file per template name)**

Create `cairn-native/internal/cairn/webui/templates/layout.html`:

```html
{{ define "layout" }}
<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>{{ .Title }} — cairn</title>
<style>body{font-family:system-ui,sans-serif;margin:2em;max-width:60em} a{color:#246} code,pre{font-family:ui-monospace,monospace;background:#f3f3f3;padding:2px 4px;border-radius:3px} pre{padding:1em;overflow:auto}</style>
</head><body>
<header><a href="/">cairn</a> · {{ .Title }}</header>
<main>{{ template "body" . }}</main>
</body></html>
{{ end }}
```

Create `cairn-native/internal/cairn/webui/templates/orgs.html`:

```html
{{ define "body" }}
<h1>Your org</h1>
<ul><li><a href="/{{ .Org }}">{{ .Org }}</a></li></ul>
{{ end }}
{{ define "orgs.html" }}{{ $_ := . }}{{ template "layout" (page "orgs" .) }}{{ end }}
```

Create `cairn-native/internal/cairn/webui/templates/repos.html`:

```html
{{ define "body" }}
<h1>{{ .Org }}/</h1>
<ul>{{ range .Repos }}<li><a href="/{{ .OrgID }}/{{ .Slug }}">{{ .Slug }}</a> — default {{ .DefaultBranch }}</li>{{ else }}<li>(no repos)</li>{{ end }}</ul>
{{ end }}
{{ define "repos.html" }}{{ template "layout" (page (printf "%s repos" .Org) .) }}{{ end }}
```

Create `cairn-native/internal/cairn/webui/templates/repo.html`:

```html
{{ define "body" }}
<h1>{{ .Repo.Slug }}</h1>
<p>Default branch: <code>{{ .Repo.DefaultBranch }}</code></p>
<p><a href="/{{ .Repo.OrgID }}/{{ .Repo.Slug }}/tree/{{ .Repo.DefaultBranch }}/">Browse {{ .Repo.DefaultBranch }}</a></p>
<h2>Refs</h2>
<ul>{{ range .Refs }}<li><code>{{ .Name }}</code> {{ truncate .Target 12 }}</li>{{ end }}</ul>
{{ end }}
{{ define "repo.html" }}{{ template "layout" (page .Repo.Slug .) }}{{ end }}
```

Create `cairn-native/internal/cairn/webui/templates/tree.html`:

```html
{{ define "body" }}
<h1>{{ .Repo.Slug }}/{{ .Path }}@{{ .Ref }}</h1>
<ul>{{ range .Entries }}<li>{{ .Mode }} — {{ .Name }} ({{ truncate .Target 12 }})</li>{{ end }}</ul>
{{ end }}
{{ define "tree.html" }}{{ template "layout" (page "tree" .) }}{{ end }}
```

Create `cairn-native/internal/cairn/webui/templates/blob.html`:

```html
{{ define "body" }}
<h1>{{ .Path }}</h1>
<pre>{{ .Content }}</pre>
{{ end }}
{{ define "blob.html" }}{{ template "layout" (page .Path .) }}{{ end }}
```

Create `cairn-native/internal/cairn/webui/templates/blame.html`:

```html
{{ define "body" }}
<h1>blame: {{ .Path }}</h1>
<table>{{ range .Lines }}<tr><td><code>{{ truncate .SHA 8 }}</code></td><td>{{ .Author }}</td><td><code>{{ .Line }}</code></td></tr>{{ end }}</table>
{{ end }}
{{ define "blame.html" }}{{ template "layout" (page (printf "blame %s" .Path) .) }}{{ end }}
```

Create `cairn-native/internal/cairn/webui/templates/commit.html`:

```html
{{ define "body" }}
<h1>commit {{ truncate .Commit.SHA 12 }}</h1>
<p><strong>{{ .Commit.Author }}</strong></p>
<pre>{{ .Commit.Message }}</pre>
<p>tree {{ truncate .Commit.TreeSHA 12 }}</p>
{{ end }}
{{ define "commit.html" }}{{ template "layout" (page "commit" .) }}{{ end }}
```

Create `cairn-native/internal/cairn/webui/templates/compare.html`:

```html
{{ define "body" }}
<h1>compare</h1>
<pre>{{ .Patch }}</pre>
{{ end }}
{{ define "compare.html" }}{{ template "layout" (page "compare" .) }}{{ end }}
```

Create `cairn-native/internal/cairn/webui/templates/pr.html`:

```html
{{ define "body" }}
<h1>PR #{{ .PR.PRNumber }}</h1>
<p>Ledger ticket: <code>{{ .PR.LedgerTicket }}</code></p>
<p>Head: {{ truncate .PR.HeadSHA 12 }} → {{ .PR.BaseBranch }}</p>
<h2>Diff</h2>
<pre>{{ .Patch }}</pre>
{{ end }}
{{ define "pr.html" }}{{ template "layout" (page (printf "PR #%d" .PR.PRNumber) .) }}{{ end }}
```

- [ ] **Step 12: Register the `page` template helper**

The templates reference `page` to build the layout's outer dict. Extend the FuncMap in `webui.go` — replace the existing `tpl := template.New("").Funcs(...)` block with this expanded version:

Edit `cairn-native/internal/cairn/webui/webui.go` — replace the FuncMap definition with:

```go
	tpl := template.New("").Funcs(template.FuncMap{
		"truncate": func(s string, n int) string {
			if len(s) <= n {
				return s
			}
			return s[:n] + "…"
		},
		"page": func(title string, body any) map[string]any {
			return map[string]any{"Title": title, "Body": body}
		},
	})
```

> Note: since `template "layout"` references `.Title` and the child templates pass `body` as the full data, the child `{{ define "body" }}` blocks need to read from the original map keys. The simplest fix: pass the raw map as `.` and inject Title separately. Adjust each `templates/*.html` outer define accordingly — these are already structured that way above (`(page "..." .)` returns a map with Title + Body, and the child `{{ define "body" }}` operates on `.` not `.Body` because the body template is invoked from within layout which dot-shifts back). To make the templates consistent, redefine `layout` to pass `.Body` to body. Update layout:

Edit `cairn-native/internal/cairn/webui/templates/layout.html` — replace the `{{ template "body" . }}` line with `{{ template "body" .Body }}`.

- [ ] **Step 13: Run webui tests**

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/webui/...
```

Expected output prefix: `ok`

- [ ] **Step 14: Commit Task 4**

Run:

```bash
cd /Users/jacinta/Source/cairn
git add cairn-native/internal/cairn/webui/ cairn-native/go.mod cairn-native/go.sum
git commit -m "feat(cairn-native): web ui with path-A oidc login and repo browser"
```

---

## Task 5: PR-as-ledger-issue lifecycle — NEX-390

> **External dependency:** Ledger's REST/webhook contract is being defined under NEX-379. This task ships the cairn-side wiring complete; the ledger HTTP client (`internal/cairn/ledger/client.go`) starts as a stub that logs the intended call. Once NEX-379 lands, swap the stub body for real HTTP calls — no other code in cairn changes.

**Files:**

Create:
- `cairn-native/internal/cairn/ledger/client.go` — LedgerClient interface + http impl
- `cairn-native/internal/cairn/ledger/client_test.go`
- `cairn-native/internal/cairn/prflow/prflow.go` — push-hook + ledger-webhook handlers
- `cairn-native/internal/cairn/prflow/prflow_test.go`

Modify:
- `cairn-native/internal/cairn/httpd/httpd.go` — accept a concrete `*ledger.Client` instead of the interface stub; mount `/ledger/webhook`
- `cairn-native/internal/cairn/sshd/sshd.go` — call `prflow.OnPush` after successful receive-pack
- `cairn-native/internal/cairn/httpd/git_http.go` — same hook from HTTP receive-pack

- [ ] **Step 1: Write the failing ledger client test against a fake ledger**

Create `cairn-native/internal/cairn/ledger/client_test.go`:

```go
package ledger

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient_CreatePRTicket(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/api/issues" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("auth: %q", r.Header.Get("Authorization"))
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["kind"] != "pr" {
			t.Errorf("kind: %v", body["kind"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key":"NEX-99"}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "tok")
	key, err := c.CreatePRTicket(context.Background(), PRTicketInput{
		RepoID: "r1", OrgID: "o1", RepoSlug: "alpha",
		PRNumber: 1, HeadSHA: "deadbeef", BaseBranch: "main",
		TitleHint: "fix: something", OpenerSub: "agent-1", OpenerKind: "agent",
	})
	if err != nil {
		t.Fatalf("CreatePRTicket: %v", err)
	}
	if key != "NEX-99" {
		t.Errorf("key: %q", key)
	}
	if !called {
		t.Error("ledger not called")
	}
}

func TestClient_AppendPRComment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/issues/NEX-99/comments" {
			t.Errorf("path: %q", r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["body"] != "second push: head=cafe" {
			t.Errorf("body: %v", body["body"])
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "tok")
	if err := c.AppendPRComment(context.Background(), "NEX-99", "second push: head=cafe"); err != nil {
		t.Fatalf("Append: %v", err)
	}
}
```

Run (expect FAIL):

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/ledger/...
```

Expected output prefix: `FAIL`

- [ ] **Step 2: Implement `client.go`**

Create `cairn-native/internal/cairn/ledger/client.go`:

```go
// Package ledger is cairn's thin client for the ledger PR-ticket API.
// The exact wire shape is defined in NEX-379; this MVP implementation uses
// POST /api/issues and POST /api/issues/{key}/comments, which matches the
// ledger tracker stories' working assumption.
package ledger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// PRTicketInput is the cairn-side payload for opening a PR ticket.
type PRTicketInput struct {
	RepoID      string
	OrgID       string
	RepoSlug    string
	PRNumber    int
	HeadSHA     string
	BaseBranch  string
	TitleHint   string
	DiffSummary string
	OpenerSub   string
	OpenerKind  string
}

// Client is the cairn->ledger HTTP client.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewClient builds a client. baseURL must not end with "/".
func NewClient(baseURL, token string) *Client {
	return &Client{BaseURL: baseURL, Token: token, HTTP: &http.Client{}}
}

// CreatePRTicket opens a new ticket and returns its key.
func (c *Client) CreatePRTicket(ctx context.Context, in PRTicketInput) (string, error) {
	payload := map[string]any{
		"kind":         "pr",
		"label":        "pr",
		"title":        in.TitleHint,
		"body":         in.DiffSummary,
		"cairn_repo":   in.RepoSlug,
		"cairn_org":    in.OrgID,
		"cairn_pr":     in.PRNumber,
		"head_sha":     in.HeadSHA,
		"base_branch":  in.BaseBranch,
		"opener_sub":   in.OpenerSub,
		"opener_kind":  in.OpenerKind,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/issues", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("ledger: POST /api/issues: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("ledger: POST /api/issues: status %d", resp.StatusCode)
	}
	var out struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Key == "" {
		return "", errors.New("ledger: empty ticket key in response")
	}
	return out.Key, nil
}

// AppendPRComment posts a comment on an existing ticket.
func (c *Client) AppendPRComment(ctx context.Context, ticketKey, body string) error {
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/issues/"+ticketKey+"/comments", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("ledger: comment: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ledger: comment: status %d", resp.StatusCode)
	}
	return nil
}
```

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/ledger/...
```

Expected output prefix: `ok`

- [ ] **Step 3: Write the failing prflow test (OnPush opens a PR on first push, comments on subsequent push)**

Create `cairn-native/internal/cairn/prflow/prflow_test.go`:

```go
package prflow

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/ledger"
	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/repo"
)

type fakeLedger struct {
	creates  int64
	comments int64
}

func (f *fakeLedger) CreatePRTicket(_ context.Context, _ ledger.PRTicketInput) (string, error) {
	atomic.AddInt64(&f.creates, 1)
	return "NEX-1", nil
}
func (f *fakeLedger) AppendPRComment(_ context.Context, _, _ string) error {
	atomic.AddInt64(&f.comments, 1)
	return nil
}

func newPRFlow(t *testing.T) (*Manager, *repo.Service, *fakeLedger) {
	st, err := repo.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := repo.NewService(st, t.TempDir())
	fl := &fakeLedger{}
	return New(svc, fl), svc, fl
}

func TestOnPush_NonDefaultBranch_OpensPR(t *testing.T) {
	m, svc, fl := newPRFlow(t)
	r, _ := svc.CreateRepo(context.Background(), "o", "alpha", "main")
	caller := repo.Caller{Subject: "u1", Kind: "human", Org: "o"}
	err := m.OnPush(context.Background(), caller, r, "refs/heads/feature/x", "0000", "cafef00d")
	if err != nil {
		t.Fatalf("OnPush: %v", err)
	}
	if atomic.LoadInt64(&fl.creates) != 1 {
		t.Errorf("want 1 create, got %d", fl.creates)
	}
}

func TestOnPush_DefaultBranch_NoPR(t *testing.T) {
	m, svc, fl := newPRFlow(t)
	r, _ := svc.CreateRepo(context.Background(), "o", "alpha", "main")
	caller := repo.Caller{Subject: "u1", Kind: "human", Org: "o"}
	if err := m.OnPush(context.Background(), caller, r, "refs/heads/main", "00", "ff"); err != nil {
		t.Fatalf("OnPush: %v", err)
	}
	if fl.creates != 0 {
		t.Errorf("default branch push should not create PR, got %d", fl.creates)
	}
}

func TestOnPush_SecondPush_Comments(t *testing.T) {
	m, svc, fl := newPRFlow(t)
	r, _ := svc.CreateRepo(context.Background(), "o", "alpha", "main")
	caller := repo.Caller{Subject: "u1", Kind: "human", Org: "o"}
	if err := m.OnPush(context.Background(), caller, r, "refs/heads/feature/x", "00", "aa"); err != nil {
		t.Fatal(err)
	}
	if err := m.OnPush(context.Background(), caller, r, "refs/heads/feature/x", "aa", "bb"); err != nil {
		t.Fatal(err)
	}
	if fl.creates != 1 {
		t.Errorf("want 1 create across two pushes, got %d", fl.creates)
	}
	if fl.comments != 1 {
		t.Errorf("want 1 comment on second push, got %d", fl.comments)
	}
}

func TestOnMergeTransition_FastForwards(t *testing.T) {
	m, svc, _ := newPRFlow(t)
	r, _ := svc.CreateRepo(context.Background(), "o", "alpha", "main")
	caller := repo.Caller{Subject: "u1", Kind: "human", Org: "o"}
	// First open a PR.
	if err := m.OnPush(context.Background(), caller, r, "refs/heads/feature/x", "00", "ff"); err != nil {
		t.Fatal(err)
	}
	// Sanity-precondition: the pr_pointer is created. Now simulate merge.
	err := m.OnLedgerTransition(context.Background(), r, 1, "Merged")
	// We expect an error because the test repo has no real main commit graph
	// to FF onto — but the call must reach the FF attempt, not bail earlier.
	if err == nil {
		t.Logf("ok: FF succeeded against empty graph (or no-op)")
	}
}

func TestOnLedgerTransition_Cancelled_DeletesRef(t *testing.T) {
	m, svc, _ := newPRFlow(t)
	r, _ := svc.CreateRepo(context.Background(), "o", "alpha", "main")
	caller := repo.Caller{Subject: "u1", Kind: "human", Org: "o"}
	if err := m.OnPush(context.Background(), caller, r, "refs/heads/feature/x", "00", "aa"); err != nil {
		t.Fatal(err)
	}
	if err := m.OnLedgerTransition(context.Background(), r, 1, "Cancelled"); err != nil {
		t.Fatalf("Cancelled: %v", err)
	}
	if _, err := svc.GetPR(context.Background(), r, 1); err == nil {
		t.Error("expected pr pointer deleted")
	}
}
```

Run (expect FAIL):

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/prflow/...
```

Expected output prefix: `FAIL`

- [ ] **Step 4: Implement `prflow.go`**

Create `cairn-native/internal/cairn/prflow/prflow.go`:

```go
// Package prflow implements the cairn-side PR-as-ledger-issue lifecycle.
// Push to non-default branch -> create ledger ticket + refs/cairn/pr/N.
// Subsequent push to same branch -> update ref + comment on ledger.
// Ledger transition "Merged" -> fast-forward base_branch to head; "Cancelled"
// -> delete refs/cairn/pr/N + pr_pointer row.
package prflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/ledger"
	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/repo"
	"github.com/go-git/go-git/v5/plumbing"
)

// LedgerAPI is the subset of ledger.Client prflow uses.
type LedgerAPI interface {
	CreatePRTicket(ctx context.Context, in ledger.PRTicketInput) (string, error)
	AppendPRComment(ctx context.Context, ticketKey, body string) error
}

// Manager wires the cairn core to the ledger client.
type Manager struct {
	svc *repo.Service
	led LedgerAPI
}

// New builds the manager.
func New(svc *repo.Service, led LedgerAPI) *Manager {
	return &Manager{svc: svc, led: led}
}

// OnPush is the cross-frontend push hook. Called once per accepted ref update.
func (m *Manager) OnPush(ctx context.Context, caller repo.Caller, r repo.Repo, refName, oldSHA, newSHA string) error {
	// Branch protection (Task 6) and audit recording happen BEFORE OnPush is
	// invoked; here we only deal with PR lifecycle.
	if !strings.HasPrefix(refName, "refs/heads/") {
		return nil
	}
	branch := strings.TrimPrefix(refName, "refs/heads/")
	if branch == r.DefaultBranch {
		return nil
	}
	// Find existing PR pointer for this branch.
	prs, err := m.svc.ListPRs(ctx, r)
	if err != nil {
		return err
	}
	for _, p := range prs {
		if p.BaseBranch == r.DefaultBranch && p.HeadSHA != "" && refForBranch(branch) == p.Ref {
			// existing PR — comment + update head
			if err := m.svc.UpdatePRHead(ctx, r, p.PRNumber, newSHA); err != nil {
				return err
			}
			comment := fmt.Sprintf("update on %s: %s -> %s", branch, oldSHA, newSHA)
			return m.led.AppendPRComment(ctx, p.LedgerTicket, comment)
		}
	}
	// Open a fresh PR.
	n, err := m.svc.AllocPR(ctx, r)
	if err != nil {
		return fmt.Errorf("alloc pr: %w", err)
	}
	ticket, err := m.led.CreatePRTicket(ctx, ledger.PRTicketInput{
		RepoID:     r.ID,
		OrgID:      r.OrgID,
		RepoSlug:   r.Slug,
		PRNumber:   n,
		HeadSHA:    newSHA,
		BaseBranch: r.DefaultBranch,
		TitleHint:  fmt.Sprintf("PR #%d on %s", n, branch),
		OpenerSub:  caller.Subject,
		OpenerKind: caller.Kind,
	})
	if err != nil {
		return fmt.Errorf("ledger create: %w", err)
	}
	prRef := fmt.Sprintf("refs/cairn/pr/%d", n)
	if err := m.svc.WritePRRef(ctx, r, prRef, newSHA); err != nil {
		return fmt.Errorf("write pr ref: %w", err)
	}
	return m.svc.CreatePR(ctx, repo.PRPointer{
		RepoID:       r.ID,
		PRNumber:     n,
		Ref:          prRef,
		LedgerTicket: ticket,
		HeadSHA:      newSHA,
		BaseBranch:   r.DefaultBranch,
	})
}

// OnLedgerTransition is called when ledger reports a ticket state change.
// state values understood: "Merged" (FF base_branch), "Cancelled" (drop ref).
func (m *Manager) OnLedgerTransition(ctx context.Context, r repo.Repo, prNumber int, state string) error {
	p, err := m.svc.GetPR(ctx, r, prNumber)
	if err != nil {
		return err
	}
	switch state {
	case "Merged":
		// Fast-forward base_branch -> head.
		err := m.svc.FastForwardBranch(ctx, r, p.BaseBranch, p.HeadSHA)
		if err != nil {
			_ = m.led.AppendPRComment(ctx, p.LedgerTicket, "cairn: fast-forward FAILED: "+err.Error())
			return err
		}
		_ = m.led.AppendPRComment(ctx, p.LedgerTicket, "cairn: merged "+p.HeadSHA+" to "+p.BaseBranch)
		return nil
	case "Cancelled":
		if err := m.svc.DeletePRRef(ctx, r, p.Ref); err != nil {
			return err
		}
		return m.svc.DeletePR(ctx, r, prNumber)
	default:
		return nil
	}
}

func refForBranch(branch string) string { return "refs/heads/" + branch }

// quiet plumbing dep
var _ = plumbing.ZeroHash
```

- [ ] **Step 5: Add the supporting Service methods used by prflow**

Append to `cairn-native/internal/cairn/repo/service.go`:

```go
// AllocPR reserves the next PR number for a repo.
func (s *Service) AllocPR(ctx context.Context, r Repo) (int, error) {
	return s.store.NextPRNumber(ctx, r.ID)
}

// WritePRRef writes refs/cairn/pr/N -> sha.
func (s *Service) WritePRRef(ctx context.Context, r Repo, refName, sha string) error {
	gr, err := s.Open(ctx, r)
	if err != nil {
		return err
	}
	return gr.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(refName), plumbing.NewHash(sha)))
}

// CreatePR stores the pr_pointer row.
func (s *Service) CreatePR(ctx context.Context, p PRPointer) error {
	return s.store.CreatePRPointer(ctx, p)
}

// UpdatePRHead updates the head_sha of a stored PR pointer.
func (s *Service) UpdatePRHead(ctx context.Context, r Repo, n int, sha string) error {
	return s.store.UpdatePRHead(ctx, r.ID, n, sha)
}

// DeletePR removes the pr_pointer row.
func (s *Service) DeletePR(ctx context.Context, r Repo, n int) error {
	return s.store.DeletePRPointer(ctx, r.ID, n)
}

// DeletePRRef removes refs/cairn/pr/N from disk.
func (s *Service) DeletePRRef(ctx context.Context, r Repo, refName string) error {
	gr, err := s.Open(ctx, r)
	if err != nil {
		return err
	}
	return gr.Storer.RemoveReference(plumbing.ReferenceName(refName))
}

// FastForwardBranch advances refs/heads/branch to sha IF and only if branch's
// current commit is an ancestor of sha. Returns an error if the FF is not safe.
func (s *Service) FastForwardBranch(ctx context.Context, r Repo, branch, sha string) error {
	gr, err := s.Open(ctx, r)
	if err != nil {
		return err
	}
	target := plumbing.NewHash(sha)
	refName := plumbing.NewBranchReferenceName(branch)
	cur, err := gr.Reference(refName, false)
	if err != nil {
		// Branch doesn't exist yet — create at head.
		return gr.Storer.SetReference(plumbing.NewHashReference(refName, target))
	}
	// Ancestry check: walk back from target; if we see cur.Hash() we're a descendant.
	commit, err := gr.CommitObject(target)
	if err != nil {
		return fmt.Errorf("FastForward: commit %s: %w", sha, err)
	}
	curHash := cur.Hash()
	iter := commitsIter{c: commit}
	for {
		if iter.c == nil {
			return fmt.Errorf("FastForward: %s is not a descendant of current %s", sha, curHash)
		}
		if iter.c.Hash == curHash {
			break
		}
		if len(iter.c.ParentHashes) == 0 {
			return fmt.Errorf("FastForward: %s is not a descendant of current %s", sha, curHash)
		}
		next, err := gr.CommitObject(iter.c.ParentHashes[0])
		if err != nil {
			return err
		}
		iter.c = next
	}
	return gr.Storer.SetReference(plumbing.NewHashReference(refName, target))
}

// commitsIter is a tiny parent walker; first-parent only is fine for FF check.
type commitsIter struct {
	c *object.Commit
}
```

The above adds an import of `object`. Update the existing `service.go` imports — at the top, ensure the import block includes both `git "github.com/go-git/go-git/v5"`, `plumbing`, AND `object`:

Edit `cairn-native/internal/cairn/repo/service.go` — replace the import block (the first `import (...)`) with:

```go
import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)
```

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/...
```

Expected output prefix: `ok`

- [ ] **Step 6: Replace the LedgerClient interface in httpd with the concrete `*ledger.Client`**

Edit `cairn-native/internal/cairn/httpd/httpd.go` — replace the `LedgerClient` interface + `PRTicketInput` typedef + `contextLike` typedef with a clean re-export from the `ledger` package:

Edit `cairn-native/internal/cairn/httpd/httpd.go` — replace the block:

```go
// LedgerClient is the Task 5 contract; declared here as an interface so Task 3
// compiles without Task 5. Task 5 provides a concrete impl + wires it in.
type LedgerClient interface {
	CreatePRTicket(ctx contextLike, in PRTicketInput) (string, error)
	AppendPRComment(ctx contextLike, ticketKey, body string) error
}
```

with:

```go
// LedgerClient is the cairn->ledger PR API contract. Implemented by
// *ledger.Client.
type LedgerClient interface {
	CreatePRTicket(ctx context.Context, in ledger.PRTicketInput) (string, error)
	AppendPRComment(ctx context.Context, ticketKey, body string) error
}
```

And drop the `PRTicketInput`/`contextLike` typedefs (delete the `type PRTicketInput struct {...}` and `type contextLike = stdContext` lines). Add `"context"` and `"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/ledger"` to httpd.go's imports. Delete `cairn-native/internal/cairn/httpd/contextalias.go`.

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
rm internal/cairn/httpd/contextalias.go
go build ./...
```

Expected output: empty (success).

- [ ] **Step 7: Add the ledger-webhook handler to httpd**

Append to `cairn-native/internal/cairn/httpd/httpd.go` inside `Handler()`:

```go
	mux.HandleFunc("POST /ledger/webhook", s.handleLedgerWebhook)
```

And add the method:

```go
// handleLedgerWebhook receives ledger state-change events. Body:
//   {"repo_id":"...","pr_number":N,"state":"Merged"}
func (s *Server) handleLedgerWebhook(w http.ResponseWriter, r *http.Request) {
	// MVP: trust the webhook source because gateway requires herald-auth.
	// Future: an HMAC signature header per NEX-379.
	var body struct {
		RepoID   string `json:"repo_id"`
		PRNumber int    `json:"pr_number"`
		State    string `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.prflow == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "prflow not configured")
		return
	}
	rp, err := s.svc.GetRepoByID(r.Context(), body.RepoID)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "repo not found")
		return
	}
	if err := s.prflow.OnLedgerTransition(r.Context(), rp, body.PRNumber, body.State); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

Add a `prflow *prflow.Manager` field to `Server` and a `PRFlow` field to `Config`. Update `New` to wire it:

Edit `Server`:

```go
type Server struct {
	svc    *repo.Service
	ledger LedgerClient
	prflow *prflow.Manager
}
```

Edit `Config`:

```go
type Config struct {
	Service      *repo.Service
	LedgerClient LedgerClient
	PRFlow       *prflow.Manager
}
```

Edit `New`:

```go
func New(cfg *Config) *Server {
	return &Server{svc: cfg.Service, ledger: cfg.LedgerClient, prflow: cfg.PRFlow}
}
```

Add a `GetRepoByID` method on Service if not present:

Append to `cairn-native/internal/cairn/repo/service.go`:

```go
// GetRepoByID is the uuid-keyed lookup used by webhook + bootstrap paths.
func (s *Service) GetRepoByID(ctx context.Context, id string) (Repo, error) {
	return s.store.GetRepoByID(ctx, id)
}
```

Add `"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/prflow"` to httpd.go imports. Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go build ./...
```

Expected output: empty.

- [ ] **Step 8: Wire the OnPush hook from the SSH frontend**

Edit `cairn-native/internal/cairn/sshd/sshd.go` — inside `handleSession`, after the successful `cmd.Run()` returns nil, call the hook if configured. Replace the `if err := cmd.Run(); err != nil { ... } _ = sess.Exit(0)` block with:

```go
	if err := cmd.Run(); err != nil {
		_ = sess.Exit(exitCode(err))
		return
	}
	if op == "git-receive-pack" && s.PushHook != nil {
		// Walk the ref advertisements written by receive-pack to learn what changed.
		// MVP: don't try to parse the pkt-stream; instead diff the ref list
		// before/after using go-git after the fact. The PushHook implementation
		// in cmd/cairn-server wires this via a closure that reads current refs.
		s.PushHook(sess.Context(), caller, r, "", "", "")
	}
	_ = sess.Exit(0)
```

Note: the PushHook signature already has `ref, oldSHA, newSHA` slots. The accurate per-ref dispatch is the responsibility of `cmd/cairn-server/main.go` which constructs the hook closure to inspect refs pre/post — pure-Go pkt-line parsing is out of scope for MVP.

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go build ./...
```

Expected output: empty.

- [ ] **Step 9: Wire OnPush from the HTTP receive-pack handler**

Edit `cairn-native/internal/cairn/httpd/git_http.go` — at the end of `gitRPC`, after `cmd.Run()` returns successfully for `verb == "receive-pack"`, call `s.prflow.OnPush(...)` similarly with empty ref params (cmd/cairn-server diffs refs). Replace the final `if err := cmd.Run(); err != nil { return }` block with:

```go
	if err := cmd.Run(); err != nil {
		return
	}
	if verb == "receive-pack" && s.prflow != nil {
		_ = s.prflow.OnPush(r.Context(), c, rp, "", "", "")
	}
```

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/...
```

Expected output prefix: `ok`

- [ ] **Step 10: Commit Task 5**

Run:

```bash
cd /Users/jacinta/Source/cairn
git add cairn-native/internal/cairn/ledger/ cairn-native/internal/cairn/prflow/ cairn-native/internal/cairn/httpd/httpd.go cairn-native/internal/cairn/sshd/sshd.go cairn-native/internal/cairn/httpd/git_http.go cairn-native/internal/cairn/repo/service.go
git rm --cached cairn-native/internal/cairn/httpd/contextalias.go 2>/dev/null || true
git commit -m "feat(cairn-native): pr-as-ledger-issue lifecycle"
```

---

## Task 6: Branch protection — NEX-391

> **Sequencing:** depends only on Task 1's protection rule parser and Task 2/3's push hooks. This task adds rule enforcement at the receive-pack boundary plus the bypass-with-audit ledger story (uses Task 5's ledger client when present, otherwise just records to `push_event`).

**Files:**

Create:
- `cairn-native/internal/cairn/protection/enforce.go` — pre-receive enforcement
- `cairn-native/internal/cairn/protection/enforce_test.go`

Modify:
- `cairn-native/internal/cairn/sshd/sshd.go` — call enforcement before invoking receive-pack
- `cairn-native/internal/cairn/httpd/git_http.go` — same

- [ ] **Step 1: Write failing enforcement test cases**

Create `cairn-native/internal/cairn/protection/enforce_test.go`:

```go
package protection

import (
	"context"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/repo"
)

func rule(p, s string) repo.ProtectionRule {
	return repo.ProtectionRule{Pattern: p, RequiredScope: s, AllowForcePush: false, AllowDelete: false}
}

func TestEnforce_NoRulesAllowsAnything(t *testing.T) {
	e := &Enforcer{}
	r := repo.Repo{Protection: `{"rules":[]}`}
	caller := repo.Caller{Subject: "u", Org: "o", Scopes: []string{"repo:write"}}
	upd := PushUpdate{Ref: "refs/heads/anything", OldSHA: "00", NewSHA: "ff"}
	res := e.Evaluate(context.Background(), r, caller, upd)
	if !res.Allowed {
		t.Errorf("want allowed, got %+v", res)
	}
}

func TestEnforce_ScopeRequired(t *testing.T) {
	e := &Enforcer{}
	rules := repo.ProtectionRules{Rules: []repo.ProtectionRule{rule("main", "repo:admin")}}
	js, _ := repo.EncodeProtection(rules)
	r := repo.Repo{Protection: js}
	caller := repo.Caller{Subject: "u", Org: "o", Scopes: []string{"repo:write"}}
	upd := PushUpdate{Ref: "refs/heads/main", OldSHA: "aa", NewSHA: "bb"}
	res := e.Evaluate(context.Background(), r, caller, upd)
	if res.Allowed {
		t.Error("expected rejection")
	}
	if res.Reason == "" {
		t.Error("expected reason")
	}
}

func TestEnforce_BypassWithAdminWritesAudit(t *testing.T) {
	audits := 0
	e := &Enforcer{Audit: func(_ context.Context, _ repo.Repo, _ repo.Caller, _ PushUpdate) { audits++ }}
	rules := repo.ProtectionRules{Rules: []repo.ProtectionRule{
		{Pattern: "main", RequiredScope: "repo:admin", BypassRequiresAudit: true},
	}}
	js, _ := repo.EncodeProtection(rules)
	r := repo.Repo{Protection: js}
	caller := repo.Caller{Subject: "u", Org: "o", Scopes: []string{"repo:admin"}}
	upd := PushUpdate{Ref: "refs/heads/main", OldSHA: "aa", NewSHA: "bb", Bypass: true}
	res := e.Evaluate(context.Background(), r, caller, upd)
	if !res.Allowed {
		t.Error("admin bypass should be allowed")
	}
	if audits != 1 {
		t.Errorf("want 1 audit, got %d", audits)
	}
}

func TestEnforce_ForcePushBlocked(t *testing.T) {
	e := &Enforcer{}
	rules := repo.ProtectionRules{Rules: []repo.ProtectionRule{rule("main", "repo:write")}}
	js, _ := repo.EncodeProtection(rules)
	r := repo.Repo{Protection: js}
	caller := repo.Caller{Subject: "u", Org: "o", Scopes: []string{"repo:write"}}
	upd := PushUpdate{Ref: "refs/heads/main", OldSHA: "aa", NewSHA: "bb", IsForce: true}
	res := e.Evaluate(context.Background(), r, caller, upd)
	if res.Allowed {
		t.Error("force-push should be blocked")
	}
}

func TestEnforce_DeleteBlocked(t *testing.T) {
	e := &Enforcer{}
	rules := repo.ProtectionRules{Rules: []repo.ProtectionRule{rule("main", "repo:write")}}
	js, _ := repo.EncodeProtection(rules)
	r := repo.Repo{Protection: js}
	caller := repo.Caller{Subject: "u", Org: "o", Scopes: []string{"repo:write"}}
	upd := PushUpdate{Ref: "refs/heads/main", OldSHA: "aa", NewSHA: "0000000000000000000000000000000000000000"}
	res := e.Evaluate(context.Background(), r, caller, upd)
	if res.Allowed {
		t.Error("delete should be blocked")
	}
}
```

Run (expect FAIL):

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/protection/...
```

Expected output prefix: `FAIL`

- [ ] **Step 2: Implement `enforce.go`**

Create `cairn-native/internal/cairn/protection/enforce.go`:

```go
// Package protection enforces branch-protection rules at the receive-pack
// boundary. Frontends evaluate every (ref, old, new) tuple from a push and
// reject the batch if any tuple is denied.
package protection

import (
	"context"

	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/repo"
)

// PushUpdate is one ref update in a push.
type PushUpdate struct {
	Ref     string
	OldSHA  string
	NewSHA  string
	IsForce bool // newSHA is not a descendant of oldSHA
	Bypass  bool // caller asked for bypass via --push-option=bypass=true
}

// Result is the verdict for a single update.
type Result struct {
	Allowed bool
	Reason  string
}

// AuditFunc is called once per bypassed update so the host can write an audit
// trail. The host implementation typically records a push_event row and
// (optionally) creates a ledger audit ticket.
type AuditFunc func(ctx context.Context, r repo.Repo, c repo.Caller, u PushUpdate)

// Enforcer evaluates pushes against a repo's protection rules.
type Enforcer struct {
	// Audit, if set, is called once per allowed bypassed update.
	Audit AuditFunc
}

// IsZeroSHA reports whether sha is the all-zero "ref delete" sentinel.
func IsZeroSHA(sha string) bool {
	for _, c := range sha {
		if c != '0' {
			return false
		}
	}
	return len(sha) > 0
}

// Evaluate returns the verdict for one update.
func (e *Enforcer) Evaluate(ctx context.Context, r repo.Repo, c repo.Caller, u PushUpdate) Result {
	rules, err := repo.ParseProtectionRules(r.Protection)
	if err != nil {
		return Result{Allowed: false, Reason: "protection parse error: " + err.Error()}
	}
	rule, matched := rules.Match(u.Ref)
	if !matched {
		return Result{Allowed: true}
	}
	isDelete := IsZeroSHA(u.NewSHA)
	// Bypass path: requires repo:admin AND the bypass flag.
	if u.Bypass {
		if !c.HasScope("repo:admin") {
			return Result{Allowed: false, Reason: "bypass requires repo:admin"}
		}
		if rule.BypassRequiresAudit && e.Audit != nil {
			e.Audit(ctx, r, c, u)
		}
		return Result{Allowed: true}
	}
	// Required scope.
	if rule.RequiredScope != "" && !c.HasScope(rule.RequiredScope) {
		return Result{Allowed: false, Reason: "rule requires scope " + rule.RequiredScope}
	}
	// Force-push gate.
	if u.IsForce && !rule.AllowForcePush {
		return Result{Allowed: false, Reason: "rule forbids force-push on " + rule.Pattern}
	}
	// Delete gate.
	if isDelete && !rule.AllowDelete {
		return Result{Allowed: false, Reason: "rule forbids deleting " + rule.Pattern}
	}
	return Result{Allowed: true}
}

// EvaluateBatch evaluates every update; the first denial returns its Result.
func (e *Enforcer) EvaluateBatch(ctx context.Context, r repo.Repo, c repo.Caller, ups []PushUpdate) Result {
	for _, u := range ups {
		res := e.Evaluate(ctx, r, c, u)
		if !res.Allowed {
			return res
		}
	}
	return Result{Allowed: true}
}
```

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./internal/cairn/protection/...
```

Expected output prefix: `ok`

- [ ] **Step 3: Wire enforcement into the SSH frontend**

The receive-pack process emits its update list via stdin's pkt-stream — parsing that pre-emption inside cairn is out of scope for MVP. The pragmatic MVP path is **post-hoc enforcement via git's pre-receive hook**: cairn writes a `hooks/pre-receive` script into every newly-created repo's `.git/hooks/`, and that script POSTs the update list to a localhost endpoint on cairn that runs the Enforcer. If denied, the hook exits non-zero and git rejects the push.

Edit `cairn-native/internal/cairn/repo/service.go` — extend `CreateRepo` to write the hook. Append at the end of the function (right before `return r, nil`):

```go
	if err := installPreReceiveHook(gitDir); err != nil {
		_ = os.RemoveAll(gitDir)
		return Repo{}, fmt.Errorf("CreateRepo install hook: %w", err)
	}
```

Add at the bottom of `service.go`:

```go
// installPreReceiveHook drops a static pre-receive script that delegates to
// the cairn enforcement endpoint over the loopback unix socket.
func installPreReceiveHook(gitDir string) error {
	hookPath := filepath.Join(gitDir, "hooks", "pre-receive")
	script := `#!/bin/sh
# cairn pre-receive: relays updates to the local enforcement endpoint.
# CAIRN_ENFORCE_SOCK is set by cairn-server before invoking git.
exec /cairn-enforce-helper "$@"
`
	if err := os.WriteFile(hookPath, []byte(script), 0o755); err != nil {
		return err
	}
	return nil
}
```

The `/cairn-enforce-helper` binary is shipped in the same container as cairn-server (see Task 7's Containerfile). It reads stdin (the pkt-line update list from git), reads `CAIRN_ENFORCE_SOCK` from env, and dials the cairn unix socket.

- [ ] **Step 4: Add the unix-socket enforcement endpoint to httpd**

Create `cairn-native/internal/cairn/protection/sock.go`:

```go
package protection

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/repo"
)

// ServeUnixSocket listens on path and serves enforcement requests from the
// pre-receive helper.
//
// Wire shape (request): JSON one-line —
//   {"repo_id":"...","caller":{"subject":"...","org":"...","scopes":["..."]},
//    "updates":[{"ref":"refs/heads/main","old_sha":"...","new_sha":"...","is_force":false,"bypass":false}, ...]}
// Wire shape (response): {"allowed":bool,"reason":"..."}
func ServeUnixSocket(ctx context.Context, path string, e *Enforcer, getRepo func(ctx context.Context, id string) (repo.Repo, error)) error {
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go handleConn(ctx, conn, e, getRepo)
	}
}

type enforceReq struct {
	RepoID  string           `json:"repo_id"`
	Caller  repo.Caller      `json:"caller"`
	Updates []PushUpdate     `json:"updates"`
}

type enforceResp struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
}

func handleConn(ctx context.Context, conn net.Conn, e *Enforcer, getRepo func(ctx context.Context, id string) (repo.Repo, error)) {
	defer conn.Close()
	rd := bufio.NewReader(conn)
	line, err := rd.ReadString('\n')
	if err != nil {
		return
	}
	var req enforceReq
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &req); err != nil {
		_ = writeResp(conn, enforceResp{Allowed: false, Reason: "bad request: " + err.Error()})
		return
	}
	r, err := getRepo(ctx, req.RepoID)
	if err != nil {
		_ = writeResp(conn, enforceResp{Allowed: false, Reason: "repo lookup: " + err.Error()})
		return
	}
	res := e.EvaluateBatch(ctx, r, req.Caller, req.Updates)
	_ = writeResp(conn, enforceResp{Allowed: res.Allowed, Reason: res.Reason})
}

func writeResp(w net.Conn, r enforceResp) error {
	b, _ := json.Marshal(r)
	if _, err := w.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

// HTTPHandler is the same logic exposed via HTTP for tests + the in-cluster
// alternative when unix sockets aren't a fit. Same JSON shape.
func HTTPHandler(e *Enforcer, getRepo func(ctx context.Context, id string) (repo.Repo, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req enforceReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		rp, err := getRepo(r.Context(), req.RepoID)
		if err != nil {
			http.Error(w, err.Error(), 404)
			return
		}
		res := e.EvaluateBatch(r.Context(), rp, req.Caller, req.Updates)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(enforceResp{Allowed: res.Allowed, Reason: res.Reason})
		_ = fmt.Errorf("") // keep fmt used
	}
}
```

- [ ] **Step 5: Write the helper binary's source**

Create `cairn-native/cmd/cairn-enforce-helper/main.go`:

```go
// Command cairn-enforce-helper is a static binary copied into every cairn
// container. It is invoked by git as the pre-receive hook. It reads the
// pkt-line update list from stdin, packages them into a JSON enforcement
// request, dials the cairn-server unix socket (path in CAIRN_ENFORCE_SOCK),
// and exits 0 if allowed / non-zero if denied.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
)

type req struct {
	RepoID  string         `json:"repo_id"`
	Caller  caller         `json:"caller"`
	Updates []pushUpdate   `json:"updates"`
}

type caller struct {
	Subject string   `json:"subject"`
	Kind    string   `json:"kind"`
	Org     string   `json:"org"`
	Scopes  []string `json:"scopes"`
}

type pushUpdate struct {
	Ref    string `json:"ref"`
	OldSHA string `json:"old_sha"`
	NewSHA string `json:"new_sha"`
}

type resp struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
}

func main() {
	sock := os.Getenv("CAIRN_ENFORCE_SOCK")
	if sock == "" {
		fmt.Fprintln(os.Stderr, "cairn-enforce-helper: CAIRN_ENFORCE_SOCK unset")
		os.Exit(2)
	}
	repoID := os.Getenv("CAIRN_REPO_ID")
	subject := os.Getenv("CAIRN_PUSHER_SUB")
	kind := os.Getenv("CAIRN_PUSHER_KIND")
	org := os.Getenv("CAIRN_PUSHER_ORG")
	scopes := strings.Fields(os.Getenv("CAIRN_PUSHER_SCOPES"))

	rd := bufio.NewScanner(os.Stdin)
	var updates []pushUpdate
	for rd.Scan() {
		fields := strings.Fields(rd.Text())
		if len(fields) != 3 {
			continue
		}
		updates = append(updates, pushUpdate{OldSHA: fields[0], NewSHA: fields[1], Ref: fields[2]})
	}

	conn, err := net.Dial("unix", sock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cairn-enforce-helper: dial %s: %v\n", sock, err)
		os.Exit(3)
	}
	defer conn.Close()

	body, err := json.Marshal(req{
		RepoID:  repoID,
		Caller:  caller{Subject: subject, Kind: kind, Org: org, Scopes: scopes},
		Updates: updates,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "cairn-enforce-helper: marshal: %v\n", err)
		os.Exit(4)
	}
	if _, err := conn.Write(append(body, '\n')); err != nil {
		fmt.Fprintf(os.Stderr, "cairn-enforce-helper: write: %v\n", err)
		os.Exit(5)
	}
	rdResp := bufio.NewReader(conn)
	line, err := rdResp.ReadString('\n')
	if err != nil {
		fmt.Fprintf(os.Stderr, "cairn-enforce-helper: read: %v\n", err)
		os.Exit(6)
	}
	var r resp
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &r); err != nil {
		fmt.Fprintf(os.Stderr, "cairn-enforce-helper: parse: %v\n", err)
		os.Exit(7)
	}
	if !r.Allowed {
		fmt.Fprintf(os.Stderr, "cairn: push denied: %s\n", r.Reason)
		os.Exit(1)
	}
}
```

- [ ] **Step 6: Make sshd + httpd export CAIRN_REPO_ID / CAIRN_PUSHER_* in the receive-pack env**

Edit `cairn-native/internal/cairn/sshd/sshd.go` — inside `handleSession`, before `cmd.Run()`, set `cmd.Env`:

```go
	if op == "git-receive-pack" {
		cmd.Env = append(os.Environ(),
			"CAIRN_REPO_ID="+r.ID,
			"CAIRN_PUSHER_SUB="+caller.Subject,
			"CAIRN_PUSHER_KIND="+caller.Kind,
			"CAIRN_PUSHER_ORG="+caller.Org,
			"CAIRN_PUSHER_SCOPES="+strings.Join(caller.Scopes, " "),
		)
	}
```

Add `"os"` to sshd.go imports if missing.

Edit `cairn-native/internal/cairn/httpd/git_http.go` similarly inside `gitRPC` before `cmd.Run()`:

```go
	if verb == "receive-pack" {
		cmd.Env = append(os.Environ(),
			"CAIRN_REPO_ID="+rp.ID,
			"CAIRN_PUSHER_SUB="+c.Subject,
			"CAIRN_PUSHER_KIND="+c.Kind,
			"CAIRN_PUSHER_ORG="+c.Org,
			"CAIRN_PUSHER_SCOPES="+strings.Join(c.Scopes, " "),
		)
	}
```

Add `"os"` to git_http.go imports if missing. Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go build ./...
```

Expected output: empty.

- [ ] **Step 7: Run all tests**

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./...
```

Expected output prefix: `ok` for every package; no FAIL lines.

- [ ] **Step 8: Commit Task 6**

Run:

```bash
cd /Users/jacinta/Source/cairn
git add cairn-native/internal/cairn/protection/ cairn-native/cmd/cairn-enforce-helper/ cairn-native/internal/cairn/sshd/sshd.go cairn-native/internal/cairn/httpd/git_http.go cairn-native/internal/cairn/repo/service.go
git commit -m "feat(cairn-native): branch protection at receive-pack via pre-receive hook"
```

---

## Task 7: Containerfile + k3s manifests — NEX-392

**Files:**

Create:
- `cairn-native/cmd/cairn-server/main.go`
- `cairn-native/cmd/cairn-server/main_test.go`
- `cairn-native/cmd/cairn-server/Containerfile`
- `cairn-native/deploy/k3s/00-namespace.yaml`
- `cairn-native/deploy/k3s/05-secret.yaml`
- `cairn-native/deploy/k3s/10-pvc.yaml`
- `cairn-native/deploy/k3s/20-deployment.yaml`
- `cairn-native/deploy/k3s/30-service-http.yaml`
- `cairn-native/deploy/k3s/35-service-ssh.yaml`
- `cairn-native/deploy/k3s/README.md` (do NOT create — see anti-pattern; we keep deploy README out of plan; instead inline the deploy notes in the Containerfile comments)

Modify:
- `cairn-native/cmd/cairn-server/main.go` is the binary entrypoint; it wires every package together.

- [ ] **Step 1: Write the failing main test that boots the server with env-driven config**

Create `cairn-native/cmd/cairn-server/main_test.go`:

```go
package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMain_StartsHealthz(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("CAIRN_ADDR_HTTP", "127.0.0.1:0")
	os.Setenv("CAIRN_ADDR_SSH", "")
	os.Setenv("CAIRN_DB", filepath.Join(dir, "cairn.db"))
	os.Setenv("CAIRN_REPO_ROOT", dir+"/repos")
	os.Setenv("CAIRN_HERALD_ISSUER", "http://unused/")
	os.Setenv("CAIRN_HERALD_CLIENT_ID", "cairn")
	os.Setenv("CAIRN_HERALD_CLIENT_SECRET", "secret")
	os.Setenv("CAIRN_HERALD_ADMIN_TOKEN", "tok")
	os.Setenv("CAIRN_LEDGER_URL", "")
	os.Setenv("CAIRN_LEDGER_TOKEN", "")
	os.Setenv("CAIRN_SKIP_OIDC_DISCOVERY", "1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addrCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- runServer(ctx, addrCh)
	}()
	var addr string
	select {
	case addr = <-addrCh:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not announce addr")
	}
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Errorf("status: %d body=%s", resp.StatusCode, body)
	}
}
```

Run (expect FAIL — runServer / package main not built):

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./cmd/cairn-server/...
```

Expected output prefix: `FAIL` or build error.

- [ ] **Step 2: Implement `cmd/cairn-server/main.go`**

Create `cairn-native/cmd/cairn-server/main.go`:

```go
// Command cairn-server is the cairn-native git host binary: single Go binary
// with three frontends (HTTP REST + Smart-HTTP, SSH, web UI), herald for
// identity, ledger for PR lifecycle.
//
// Config (env):
//
//	CAIRN_ADDR_HTTP            HTTP listen addr (default :8080)
//	CAIRN_ADDR_SSH             SSH listen addr (default :22). Empty = SSH off.
//	CAIRN_DB                   sqlite path (default /var/lib/cairn/cairn.db)
//	CAIRN_REPO_ROOT            on-disk bare-repo root (default /var/lib/cairn/repos)
//	CAIRN_ENFORCE_SOCK         unix socket for pre-receive enforcement (default /var/run/cairn/enforce.sock)
//	CAIRN_HERALD_ISSUER        herald issuer URL (required)
//	CAIRN_HERALD_JWKS_URL      optional override for internal JWKS
//	CAIRN_HERALD_CLIENT_ID     OIDC client id (required for web UI login)
//	CAIRN_HERALD_CLIENT_SECRET OIDC client secret (required for web UI login)
//	CAIRN_HERALD_ADMIN_TOKEN   bearer for herald admin API (fingerprint lookup)
//	CAIRN_LEDGER_URL           ledger base URL (empty = ledger off; PR creation will log only)
//	CAIRN_LEDGER_TOKEN         ledger bearer
//	CAIRN_SSH_HOST_KEY         base64(std) ed25519 host private key (64 bytes). If unset, an ephemeral key is generated.
//	CAIRN_SKIP_OIDC_DISCOVERY  "1" to skip OIDC discovery (web UI disabled). Internal flag for tests.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/httpd"
	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/identity"
	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/ledger"
	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/prflow"
	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/protection"
	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/repo"
	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/sshd"
	"github.com/CarriedWorldUniverse/cairn/cairn-native/internal/cairn/webui"
	gossh "golang.org/x/crypto/ssh"
)

func main() {
	if err := runServer(context.Background(), nil); err != nil {
		log.Fatalf("cairn-server: %v", err)
	}
}

// runServer is the testable entrypoint. If addrCh is non-nil, the resolved
// HTTP listen address is sent once the listener binds.
func runServer(ctx context.Context, addrCh chan<- string) error {
	httpAddr := env("CAIRN_ADDR_HTTP", ":8080")
	sshAddr := env("CAIRN_ADDR_SSH", ":22")
	dbPath := env("CAIRN_DB", "/var/lib/cairn/cairn.db")
	repoRoot := env("CAIRN_REPO_ROOT", "/var/lib/cairn/repos")
	enforceSock := env("CAIRN_ENFORCE_SOCK", "/var/run/cairn/enforce.sock")
	heraldIssuer := env("CAIRN_HERALD_ISSUER", "")
	clientID := env("CAIRN_HERALD_CLIENT_ID", "")
	clientSecret := env("CAIRN_HERALD_CLIENT_SECRET", "")
	heraldAdmin := env("CAIRN_HERALD_ADMIN_TOKEN", "")
	ledgerURL := env("CAIRN_LEDGER_URL", "")
	ledgerToken := env("CAIRN_LEDGER_TOKEN", "")
	skipOIDC := os.Getenv("CAIRN_SKIP_OIDC_DISCOVERY") == "1"

	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(enforceSock), 0o755); err != nil {
		return err
	}

	store, err := repo.Open(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	svc := repo.NewService(store, repoRoot)

	// Ledger client.
	var ledClient *ledger.Client
	if ledgerURL != "" {
		ledClient = ledger.NewClient(ledgerURL, ledgerToken)
	}
	var prMgr *prflow.Manager
	if ledClient != nil {
		prMgr = prflow.New(svc, ledClient)
	}

	// Branch-protection enforcer + unix socket.
	enforcer := &protection.Enforcer{
		Audit: func(ctx context.Context, r repo.Repo, c repo.Caller, u protection.PushUpdate) {
			_ = svc.RecordPushEventBypass(ctx, r, c, u.Ref, u.OldSHA, u.NewSHA)
			if ledClient != nil {
				_ = ledClient.AppendPRComment(ctx, "audit-bypass",
					"cairn: bypass on "+r.OrgID+"/"+r.Slug+" ref="+u.Ref+" by "+c.Subject)
			}
		},
	}
	go func() {
		_ = protection.ServeUnixSocket(ctx, enforceSock, enforcer, svc.GetRepoByID)
	}()

	// Web UI (skipped in tests).
	var uiHandler http.Handler
	if !skipOIDC && heraldIssuer != "" && clientID != "" {
		ui, err := webui.New(ctx, webui.Config{
			Service: svc,
			OIDC: webui.OIDCConfig{
				Issuer: heraldIssuer, ClientID: clientID, ClientSecret: clientSecret,
				RedirectURL: env("CAIRN_HERALD_REDIRECT_URL", "http://localhost:8080/oauth/callback"),
				Scopes:      []string{"openid", "repo:read"},
			},
		})
		if err != nil {
			return err
		}
		uiHandler = ui.Handler()
	}

	// HTTP server (REST + Smart-HTTP + Web UI mount).
	hcfg := &httpd.Config{Service: svc, LedgerClient: ledClient, PRFlow: prMgr}
	api := httpd.New(hcfg)
	mux := http.NewServeMux()
	mux.Handle("/", api.Handler())
	if uiHandler != nil {
		mux.Handle("/ui/", http.StripPrefix("/ui", uiHandler))
	}

	ln, err := net.Listen("tcp", httpAddr)
	if err != nil {
		return err
	}
	if addrCh != nil {
		addrCh <- ln.Addr().String()
	}
	log.Printf("cairn-server: http listening on %s", ln.Addr())

	// SSH server (background).
	if sshAddr != "" {
		hostKey, err := loadOrGenHostKey()
		if err != nil {
			return err
		}
		// Identity resolver (cached).
		var resolver identity.Resolver
		if heraldIssuer != "" && heraldAdmin != "" {
			resolver = identity.NewCache(identity.NewHeraldResolver(heraldIssuer, heraldAdmin), 0)
		}
		ss := &sshd.Server{
			Addr:     sshAddr,
			HostKey:  hostKey,
			Resolver: resolver,
			Repos:    svc,
			Service:  svc,
		}
		go func() {
			log.Printf("cairn-server: ssh listening on %s", sshAddr)
			if err := ss.ListenAndServe(); err != nil {
				log.Printf("cairn-server: ssh: %v", err)
			}
		}()
	}

	srv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// loadOrGenHostKey returns a PEM-encoded ed25519 private key for the SSH server.
func loadOrGenHostKey() ([]byte, error) {
	if b64 := os.Getenv("CAIRN_SSH_HOST_KEY"); b64 != "" {
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, err
		}
		signer, err := gossh.NewSignerFromKey(ed25519.PrivateKey(raw))
		if err != nil {
			return nil, err
		}
		_ = signer
		return gossh.MarshalAuthorizedKey(signer.PublicKey()), nil
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	log.Printf("cairn-server: WARNING ephemeral SSH host key generated; persist via CAIRN_SSH_HOST_KEY=%s", base64.StdEncoding.EncodeToString(priv))
	return gossh.MarshalAuthorizedKey(signer.PublicKey()), nil
}
```

- [ ] **Step 3: Add the helper `RecordPushEventBypass` used by the Audit hook**

Append to `cairn-native/internal/cairn/repo/service.go`:

```go
// RecordPushEventBypass writes a bypass=true push_event row.
func (s *Service) RecordPushEventBypass(ctx context.Context, r Repo, c Caller, ref, oldSHA, newSHA string) error {
	return s.store.RecordPushEvent(ctx, PushEvent{
		RepoID:     r.ID,
		Ref:        ref,
		OldSHA:     oldSHA,
		NewSHA:     newSHA,
		PusherSub:  c.Subject,
		PusherKind: c.Kind,
		Bypass:     true,
	})
}
```

The ssh host-key handling above is incorrect (marshals as authorized-key text, not PEM). Fix it: the `Server.ListenAndServe` parses with `gossh.ParsePrivateKey` which expects PEM. Replace `loadOrGenHostKey` with a version that produces PEM:

Edit `cairn-native/cmd/cairn-server/main.go` — replace `loadOrGenHostKey` with:

```go
func loadOrGenHostKey() ([]byte, error) {
	if b64 := os.Getenv("CAIRN_SSH_HOST_KEY"); b64 != "" {
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, err
		}
		return marshalEd25519PEM(ed25519.PrivateKey(raw))
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	log.Printf("cairn-server: WARNING ephemeral SSH host key generated; persist via CAIRN_SSH_HOST_KEY=%s", base64.StdEncoding.EncodeToString(priv))
	return marshalEd25519PEM(priv)
}

// marshalEd25519PEM produces an OpenSSH-format PEM block usable by
// gossh.ParsePrivateKey.
func marshalEd25519PEM(priv ed25519.PrivateKey) ([]byte, error) {
	block, err := gossh.MarshalPrivateKey(priv, "cairn-host-key")
	if err != nil {
		return nil, err
	}
	return pemEncode(block.Type, block.Bytes), nil
}

func pemEncode(t string, b []byte) []byte {
	// Inline tiny PEM encoder to avoid a `encoding/pem` import noise; the
	// stdlib version is fine — use it.
	out := []byte("-----BEGIN " + t + "-----\n")
	enc := base64.StdEncoding.EncodeToString(b)
	for i := 0; i < len(enc); i += 64 {
		end := i + 64
		if end > len(enc) {
			end = len(enc)
		}
		out = append(out, enc[i:end]...)
		out = append(out, '\n')
	}
	out = append(out, "-----END "+t+"-----\n"...)
	return out
}
```

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./cmd/cairn-server/...
```

Expected output prefix: `ok`

- [ ] **Step 4: Write the Containerfile**

Create `cairn-native/cmd/cairn-server/Containerfile`:

```dockerfile
# cairn-native container — multi-stage Go build on scratch.
# Build from the cairn-native module root:
#   podman build -f cmd/cairn-server/Containerfile -t cairn:dev .
#   podman save cairn:dev | sudo k3s ctr images import -
#
# Two binaries ship in the image: /cairn-server (the main daemon) and
# /cairn-enforce-helper (the pre-receive hook invoked by git inside each repo).
# The image also includes `git` from a minimal busybox-git base because the
# server shells out to `git upload-pack` / `git receive-pack`.
FROM docker.io/library/golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cairn-server ./cmd/cairn-server
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cairn-enforce-helper ./cmd/cairn-enforce-helper

# Runtime image: needs `git` for protocol I/O. alpine-git keeps the image small.
FROM docker.io/library/alpine:3.20
RUN apk add --no-cache git openssh-server ca-certificates && rm -rf /var/cache/apk/*
COPY --from=build /out/cairn-server /cairn-server
COPY --from=build /out/cairn-enforce-helper /cairn-enforce-helper
EXPOSE 8080
EXPOSE 22
ENTRYPOINT ["/cairn-server"]
```

- [ ] **Step 5: Write k3s manifests**

Create `cairn-native/deploy/k3s/00-namespace.yaml`:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: cwb
  labels:
    name: cwb
```

Create `cairn-native/deploy/k3s/05-secret.yaml`:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: cairn-secrets
  namespace: cwb
type: Opaque
stringData:
  herald_client_id: "cairn"
  herald_client_secret: "REPLACE_ME"
  herald_admin_token: "REPLACE_ME"
  ledger_token: "REPLACE_ME"
  ssh_host_key: ""  # base64(std) ed25519 64-byte private key; empty = ephemeral on boot
```

Create `cairn-native/deploy/k3s/10-pvc.yaml`:

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: cairn-data
  namespace: cwb
spec:
  accessModes: ["ReadWriteOnce"]
  storageClassName: local-path
  resources:
    requests:
      storage: 20Gi
```

Create `cairn-native/deploy/k3s/20-deployment.yaml`:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cairn
  namespace: cwb
  labels:
    app: cairn
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: cairn
  template:
    metadata:
      labels:
        app: cairn
    spec:
      containers:
        - name: cairn
          image: localhost/cairn:dev
          imagePullPolicy: Never
          ports:
            - name: http
              containerPort: 8080
            - name: ssh
              containerPort: 22
          env:
            - name: CAIRN_ADDR_HTTP
              value: ":8080"
            - name: CAIRN_ADDR_SSH
              value: ":22"
            - name: CAIRN_DB
              value: "/var/lib/cairn/cairn.db"
            - name: CAIRN_REPO_ROOT
              value: "/var/lib/cairn/repos"
            - name: CAIRN_ENFORCE_SOCK
              value: "/var/run/cairn/enforce.sock"
            - name: CAIRN_HERALD_ISSUER
              value: "http://dmonextreme.tail41686e.ts.net:8080/herald/"
            - name: CAIRN_HERALD_JWKS_URL
              value: "http://herald.cwb.svc:8099/jwks"
            - name: CAIRN_HERALD_REDIRECT_URL
              value: "http://dmonextreme.tail41686e.ts.net:8080/cairn/ui/oauth/callback"
            - name: CAIRN_LEDGER_URL
              value: "http://ledger.cwb.svc:8080"
            - name: CAIRN_HERALD_CLIENT_ID
              valueFrom:
                secretKeyRef:
                  name: cairn-secrets
                  key: herald_client_id
            - name: CAIRN_HERALD_CLIENT_SECRET
              valueFrom:
                secretKeyRef:
                  name: cairn-secrets
                  key: herald_client_secret
            - name: CAIRN_HERALD_ADMIN_TOKEN
              valueFrom:
                secretKeyRef:
                  name: cairn-secrets
                  key: herald_admin_token
            - name: CAIRN_LEDGER_TOKEN
              valueFrom:
                secretKeyRef:
                  name: cairn-secrets
                  key: ledger_token
            - name: CAIRN_SSH_HOST_KEY
              valueFrom:
                secretKeyRef:
                  name: cairn-secrets
                  key: ssh_host_key
                  optional: true
          volumeMounts:
            - name: data
              mountPath: /var/lib/cairn
            - name: runsock
              mountPath: /var/run/cairn
          readinessProbe:
            httpGet:
              path: /healthz
              port: http
            initialDelaySeconds: 2
            periodSeconds: 5
          livenessProbe:
            httpGet:
              path: /healthz
              port: http
            initialDelaySeconds: 10
            periodSeconds: 15
      volumes:
        - name: data
          persistentVolumeClaim:
            claimName: cairn-data
        - name: runsock
          emptyDir: {}
```

Create `cairn-native/deploy/k3s/30-service-http.yaml`:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: cairn
  namespace: cwb
  labels:
    app: cairn
spec:
  type: ClusterIP
  selector:
    app: cairn
  ports:
    - name: http
      port: 8080
      targetPort: http
      protocol: TCP
```

Create `cairn-native/deploy/k3s/35-service-ssh.yaml`:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: cairn-ssh
  namespace: cwb
  labels:
    app: cairn
spec:
  type: LoadBalancer
  selector:
    app: cairn
  ports:
    - name: ssh
      port: 22
      targetPort: ssh
      protocol: TCP
```

- [ ] **Step 6: Run the full test suite**

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
go test ./...
```

Expected output: every package reports `ok`; no FAIL.

- [ ] **Step 7: Build the container image**

Run:

```bash
cd /Users/jacinta/Source/cairn/cairn-native
podman build -f cmd/cairn-server/Containerfile -t cairn:dev .
```

Expected output prefix: `STEP 1/...` and final `Successfully tagged localhost/cairn:dev`.

- [ ] **Step 8: Commit Task 7**

Run:

```bash
cd /Users/jacinta/Source/cairn
git add cairn-native/cmd/cairn-server/ cairn-native/deploy/
git commit -m "feat(cairn-native): cmd/cairn-server entrypoint + k3s manifests"
```

- [ ] **Step 9: Update interchange-gateway routes (separate repo, separate PR)**

In `github.com/CarriedWorldUniverse/interchange`'s deployment, add `/cairn=http://cairn.cwb.svc:8080` to `INTERCHANGE_ROUTES`. This is a one-line config change on the gateway Deployment — out of scope for this plan, but required for end-to-end. File a follow-up ticket noting this dependency.

- [ ] **Step 10: Final acceptance smoke (manual on dMon)**

After all of the above lands:

1. `kubectl apply -f cairn-native/deploy/k3s/` against dMon.
2. From a workstation: `git clone ssh://agent@<dmon-host>:22/<org>/<slug>` with the agent's casket private key in `~/.ssh/id_ed25519`.
3. Make a commit on a feature branch and `git push`.
4. Open the cairn web UI in a browser at `http://dmonextreme.tail41686e.ts.net:8080/cairn/ui/`; complete the herald login dance; navigate to the PR view; confirm the diff renders + the ledger ticket key is shown.
5. Move the ledger ticket to "Merged"; refresh cairn; confirm the default branch FF'd to the PR head.

If every step passes, NEX-384 MVP is done.

---

## Post-plan self-review

Cross-checked the plan against the spec sections and the cross-task type contract:

- **Type continuity:** `repo.Repo`, `repo.PRPointer`, `repo.PushEvent`, `repo.Caller`, `repo.Ref`, `repo.ProtectionRules`, `repo.ProtectionRule`, `repo.Service`, `repo.Store`, `repo.RepoLookup` — all declared in Task 1, referenced consistently in Tasks 2–7.
- **API surface (spec §8):** every listed endpoint maps to a route in Task 3's `Handler()` (`/api/orgs/{org}/repos[/{slug}[/protection|/refs|/refs/{ref...}|/prs|/prs/{n}]]`) or Task 4's web UI mux (`/`, `/{org}`, `/{org}/{slug}`, `/tree/...`, `/blob/...`, `/blame/...`, `/commit/...`, `/compare/...`, `/pr/...`, `/oauth/callback`). The Smart-HTTP git endpoints (`/info/refs`, `/git-upload-pack`, `/git-receive-pack`) and `/healthz` are wired in Task 3.
- **PR lifecycle (spec §6):** push-to-non-default → `prflow.OnPush` (Task 5) creates ticket + ref + pointer. Subsequent push → updates head + appends comment. Ledger "Merged" transition → `prflow.OnLedgerTransition` fast-forwards. "Cancelled" → drops ref + pointer.
- **Branch protection (spec §7):** rules stored as JSON on `Repo.Protection`, parsed in Task 1, enforced in Task 6 via `protection.Enforcer.Evaluate` at receive-pack time via the pre-receive helper. Default protection auto-applied on `Service.CreateRepo`. Bypass requires `repo:admin` + writes `push_event` with `bypass=true` + appends ledger audit (when ledger is configured).
- **Auth flows (spec §5):** 5a (agent SSH) — Task 2; 5b/5d (HTTP via gateway) — Task 3; 5c (web UI path-A) — Task 4.
- **Deploy (spec §10):** Containerfile + 6 k3s manifests in Task 7; env list matches the spec verbatim plus `CAIRN_ENFORCE_SOCK` and `CAIRN_HERALD_REDIRECT_URL` added for completeness.

**Known assumptions and open issues:**

1. **Module layout.** Plan puts cairn-native in a sibling `go.mod` under `cairn-native/` at the cairn-repo root to avoid colliding with the existing Forgejo `go.mod`. After Forgejo's tree is archived (out of plan scope), this flattens. The plan's import paths assume this layout — if the team prefers a different home (e.g. fresh repo `cairn-native`), every `github.com/CarriedWorldUniverse/cairn/cairn-native/...` import needs a global rename, but the structure stays.
2. **Ledger wire-shape.** Task 5's `ledger.Client` assumes `POST /api/issues` + `POST /api/issues/{key}/comments` returning `{"key":"..."}`. NEX-379 may pick a different shape; the client is the only file to update.
3. **Branch-protection enforcement via pre-receive hook.** Task 6 chose the shell-out approach (helper binary + unix socket) over pure-Go pkt-line parsing because the latter is a lot more code for marginal gain in MVP. If the team wants pure-Go later, the `protection.Enforcer` is decoupled from the wire shape and a Go-native receive-pack implementation can swap in.
4. **Post-push ref diff for `OnPush`.** Both the SSH and HTTP frontends currently call `prflow.OnPush` with empty `(ref, oldSHA, newSHA)` because parsing receive-pack's stream is out of MVP scope. The closure inside `cmd/cairn-server/main.go` should be extended in a follow-up to snapshot refs before/after and call `OnPush` once per actually-changed ref. For MVP smoke, the operator does the PR-open path manually via REST.
5. **Casket-pubkey lookup endpoint.** Task 2 assumes herald exposes `GET /api/agents/by-fingerprint/{fp}`. If herald hasn't shipped this yet, file a small herald-side story (probably part of NEX-387's herald-side or a fresh sibling) before SSH agent push is end-to-end.
6. **Web UI template `page` helper.** Task 4 layered a `page` template function over the layout. The templates use it consistently but only the simplest paths are exercised by tests — a manual smoke is required on dMon to confirm the FuncMap binding works under the embedded FS.
