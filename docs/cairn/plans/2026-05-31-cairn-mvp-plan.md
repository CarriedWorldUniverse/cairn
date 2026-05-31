# cairn MVP (agent-git core) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build cairn's agent-git walking skeleton — a go-git-backed git host where aspects clone/push over SSH (casket identity) or HTTP (gateway-fronted), herald-authed — the git leg of the CWB agent loop. Private repos, single org.

**Architecture:** A single Go binary (cmd/cairn-server) wrapping go-git with two ingresses terminating at a herald identity: SSH (casket pubkey → herald agent via NEX-412 fingerprint lookup) and HTTP Smart-HTTP behind interchange-gateway (mTLS-trusted X-CWB-*). SQLite for repo/ref metadata. Minimal default-branch protection. Deploys to the cwb k3s namespace. PRs, delayed-public-projection, and web UI are out of this MVP.

**Tech Stack:** Go 1.26, github.com/go-git/go-git/v5, github.com/gliderlabs/ssh + golang.org/x/crypto/ssh, modernc.org/sqlite. herald (live) for identity; interchange-gateway (live) for the HTTP path; NEX-412 (herald by-fingerprint lookup) for the SSH path.

---

## Module layout decision (spec §8, resolved)

This MVP is a **green-field go-git rewrite**, not an edit of the existing Forgejo fork at `/Users/jacinta/Source/cairn` (module `github.com/CarriedWorldUniverse/cairn`). That tree carries the entire Forgejo dependency graph and a `replace github.com/gliderlabs/ssh => code.forgejo.org/forgejo/ssh ...` directive that would actively fight the clean `gliderlabs/ssh` usage this MVP needs. Reusing it would mean dragging in Gitea's storage, web, and SSH stack — the opposite of a walking skeleton.

**Decision:** the MVP lives in a **fresh, standalone module** rooted at `/Users/jacinta/Source/cairn-server`, module path `github.com/CarriedWorldUniverse/cairn-server`, one binary per repo exactly like the herald/ledger/interchange siblings. Layout:

```
/Users/jacinta/Source/cairn-server/
  go.mod                       # module github.com/CarriedWorldUniverse/cairn-server
  go.sum
  cmd/cairn-server/
    main.go                    # wiring: store + core + SSH ingress + HTTP ingress
    Containerfile              # static build on scratch (mirrors herald)
  internal/
    repo/                      # repo + ref core over go-git + SQLite meta
      service.go
      service_test.go
      schema.sql
    herald/                    # heraldAgents interface + cache + NEX-412 client + fake
      agents.go
      agents_test.go
      fake.go
    sshd/                      # SSH ingress (gliderlabs/ssh)
      server.go
      server_test.go
    httpd/                     # HTTP Smart-HTTP ingress (X-CWB-* trust)
      server.go
      server_test.go
    protect/                   # minimal default-branch protection
      protect.go
      protect_test.go
  deploy/k3s/                  # namespace, pvc, deployment, services, README
    00-namespace.yaml
    10-pvc.yaml
    20-deployment.yaml
    30-service-http.yaml
    31-service-ssh.yaml
    README.md
```

The doc you are reading stays at `/Users/jacinta/Source/cairn/docs/cairn/plans/` (the design/spec home), but **all code paths below are absolute under `/Users/jacinta/Source/cairn-server`.** If the operator later prefers the code to live inside the existing cairn repo on a clean branch, only the module path and absolute prefixes change; the task content is identical.

**Scope cut from the broader native design (NEX-384):** this plan implements NEX-386 (core), NEX-387 (SSH), NEX-388 (HTTP), the minimal subset of NEX-391 (branch protection), NEX-392 (deploy), and the cairn conformance layer. It explicitly does **not** include NEX-389 (web UI) or NEX-390 (PR-as-ledger-issue), nor the delayed-public-projection — those are sequenced after (spec §7).

---

## Verified platform facts (read before starting)

These are confirmed against the live sibling repos and pinned here so tasks don't re-derive them:

- **Fingerprint convention** (herald `internal/identity/fingerprint.go`): `base64url(sha256(pubkey)[:16])`, i.e. `base64.RawURLEncoding.EncodeToString(sha256.Sum256(pub)[:16])`. cairn's SSH path MUST compute the identical fingerprint so it matches herald's directory.
- **herald already resolves by fingerprint** in its domain layer: `identity.Service.GetAgentByFingerprint(ctx, fp)` → `store.GetUserByCasketFingerprint`. **NEX-412 is the thin HTTP exposure** of this as `GET /api/agents/by-fingerprint/{fp}`. It does not yet exist on the wire — hence the buildable-now-with-fake handling in Task 2.
- **Scope strings** (herald `internal/store/schema.sql` uses `"repo:write"` as its worked example): `repo:read` gates clone/fetch, `repo:write` gates push. Pinned cross-pillar; conformance fixtures grant `builder = repo:read repo:write`, `reader = repo:read`.
- **X-CWB-* headers** (interchange `internal/gateway/gateway.go`): the gateway strips any client-supplied `X-CWB-Org`, `X-CWB-Subject`, `X-CWB-Kind`, `X-CWB-Scopes`, `X-CWB-Responsible-Human` on every request, then injects the verified ones after herald verification. `X-CWB-Scopes` is space-joined. cairn's HTTP path TRUSTS these over the mTLS hop and does NOT re-verify — same posture as ledger.
- **gateway route stripping**: the gateway strips the matched prefix before proxying. A request to `{gateway}/cairn/api/orgs/{org}/repos` arrives at cairn as `/api/orgs/{org}/repos`. cairn's HTTP mux is mounted at the un-prefixed paths.
- **k3s conventions** (herald `deploy/k3s/`): namespace `cwb`; `imagePullPolicy: Never` (images loaded via `podman save | k3s ctr images import -`); `local-path` PVC; static scratch image; secrets via `kubectl create secret`.
- **herald token mint** for fixtures/conformance: `POST {gateway}/herald/api/humans/{id}/token` (admin stand-in) and the OIDC `POST {gateway}/herald/token` jwt-bearer grant for agents.

---

## Scaffold-verification note (done while writing this plan)

The riskiest code blocks were compiled + run against the **real** modules in a throwaway scaffold before this plan shipped, so the dependency-API surfaces are confirmed, not assumed:
- **Task 1 core** (`repo` package) — `git.PlainInit`/`PlainOpen`, the `Storer` blob/tree/commit seed trick, `References()`/`Reference()` — built and **`go test` green** against `github.com/go-git/go-git/v5@v5.13.2` + `modernc.org/sqlite@v1.34.4`.
- **Task 2** (`herald` cache/fake/NEX-412 client; `sshd` fingerprint + command + server) — built + **`go test` green**; the `gliderlabs/ssh@v0.3.8` API (`Server{Handler,PublicKeyHandler}`, `AddHostKey`, `Context.SetValue`, `Session.RawCommand/Stderr/Exit/Context`) and the `x/crypto/ssh` `CryptoPublicKey` fingerprint path are confirmed.
- **Task 4** (`protect`) — built + **`go test` green**.
- **Task 3** (`httpd`) — built + `go vet` clean (the `net/http/cgi` `cgi.Handler` + `pathRe` routing surface is confirmed; the git-e2e tests run live with `git` present).

All five internal packages build + vet clean together under `go 1.26.2`. The exact versions in the `go get` steps are the verified-good ones.

## Conventions for every task

- **TDD loop where it applies:** write a failing test → run it, expect a specific failure → write complete compile-ready Go (no placeholders, no TODO, no "add error handling later") → run, expect pass → commit.
- **Commits:** real `git commit -m "<prefix>: <subject>"` with the per-task NEX prefix shown in each task header. One logical step per commit. Do NOT push or open a PR — the operator handles git remote operations.
- **Working directory:** all `go` commands run from `/Users/jacinta/Source/cairn-server` unless the task says otherwise (Task 6 is in `/Users/jacinta/Source/cwb-conformance`). Use absolute `-C` paths; agent threads reset cwd between shells.
- **Expected-output prefixes:** each `go test` step states the prefix you should see (`ok  ` / `FAIL` / `--- FAIL`). If you see anything else, stop and debug (superpowers:systematic-debugging).

---

## Task 1 — Repo + ref core via go-git + `cmd/cairn-server` skeleton (NEX-386)

**Commit prefix:** `nex-386:`

**Goal:** a `repo.Service` that creates/gets/lists/deletes bare go-git repos on disk, lists/gets refs, and records a `push_event` audit row — backed by SQLite for metadata. Plus a runnable `cmd/cairn-server` skeleton that opens the store, constructs the service, and serves `/healthz`. Both ingresses (Tasks 2, 3) call this one core. No auth in this task — pure storage/engine.

**Maps to:** spec §2.1 (repo + ref core), §5 (data model), §6.2.

### 1.1 — Initialise the module

- [ ] Create the module root and init the module:
  ```sh
  mkdir -p /Users/jacinta/Source/cairn-server/cmd/cairn-server \
           /Users/jacinta/Source/cairn-server/internal/repo
  go -C /Users/jacinta/Source/cairn-server mod init github.com/CarriedWorldUniverse/cairn-server
  ```
  Expect: `go.mod` written. Confirm:
  ```sh
  head -1 /Users/jacinta/Source/cairn-server/go.mod
  ```
  Expect output: `module github.com/CarriedWorldUniverse/cairn-server`
- [ ] Add the core dependencies (downloads into the module cache; these are NOT yet in the local cache):
  ```sh
  go -C /Users/jacinta/Source/cairn-server get github.com/go-git/go-git/v5@v5.13.2
  go -C /Users/jacinta/Source/cairn-server get modernc.org/sqlite@v1.34.4
  ```
  Expect: `go: added github.com/go-git/go-git/v5 ...` and `go: added modernc.org/sqlite ...`. (Pin these exact versions; they are the latest known-good as of the plan date and avoid the cgo sqlite driver.)
- [ ] `git init` the new repo so commits land:
  ```sh
  git -C /Users/jacinta/Source/cairn-server init
  printf '/cairn-server\n*.db\n*.db-*\n' > /Users/jacinta/Source/cairn-server/.gitignore
  ```
- [ ] Commit: `nex-386: init cairn-server module (go-git + modernc sqlite)`.

### 1.2 — SQLite schema + the metadata store

The data model is spec §5: `repo` (with minimal `protection` JSON) and `push_event` (audit). go-git owns the object/ref storage on disk; SQLite owns the catalogue + audit.

- [ ] Write the schema at `/Users/jacinta/Source/cairn-server/internal/repo/schema.sql`:
  ```sql
  -- cairn MVP metadata: the repo catalogue + the push audit log.
  -- go-git owns object/ref storage on disk; this owns discovery + audit.
  PRAGMA journal_mode = WAL;
  PRAGMA foreign_keys = ON;

  CREATE TABLE IF NOT EXISTS repo (
    id             TEXT PRIMARY KEY,             -- uuid
    org_id         TEXT NOT NULL,                -- herald org id (single-org MVP)
    slug           TEXT NOT NULL,                -- url-safe name within the org
    default_branch TEXT NOT NULL DEFAULT 'main',
    protection     TEXT NOT NULL DEFAULT '{}',   -- minimal default-branch rule (JSON)
    storage_path   TEXT NOT NULL,                -- absolute path to the bare repo on disk
    created_at     TEXT NOT NULL,                -- RFC3339
    updated_at     TEXT NOT NULL,                -- RFC3339
    UNIQUE(org_id, slug)
  );

  CREATE TABLE IF NOT EXISTS push_event (
    id              TEXT PRIMARY KEY,            -- uuid
    repo_id         TEXT NOT NULL REFERENCES repo(id) ON DELETE CASCADE,
    ref             TEXT NOT NULL,               -- e.g. refs/heads/feature-x
    old_sha         TEXT NOT NULL,               -- zero-sha for create
    new_sha         TEXT NOT NULL,               -- zero-sha for delete
    pusher_agent_id TEXT NOT NULL,               -- herald agent id
    forced          INTEGER NOT NULL DEFAULT 0,  -- 1 if a non-fast-forward
    at              TEXT NOT NULL                -- RFC3339
  );

  CREATE INDEX IF NOT EXISTS idx_push_event_repo ON push_event(repo_id, at);
  ```
- [ ] Commit: `nex-386: repo/push_event sqlite schema`.

### 1.3 — Domain types + the failing service test (write test FIRST)

- [ ] Write `/Users/jacinta/Source/cairn-server/internal/repo/service_test.go` with the first failing test. It exercises create → get → list → ref listing on a freshly initialised bare repo seeded with one commit:
  ```go
  package repo

  import (
  	"context"
  	"path/filepath"
  	"testing"
  )

  // newTestService gives each test an isolated on-disk store + repo root.
  func newTestService(t *testing.T) *Service {
  	t.Helper()
  	dir := t.TempDir()
  	svc, err := Open(filepath.Join(dir, "cairn.db"), filepath.Join(dir, "repos"))
  	if err != nil {
  		t.Fatalf("Open: %v", err)
  	}
  	t.Cleanup(func() { _ = svc.Close() })
  	return svc
  }

  func TestCreateGetListRepo(t *testing.T) {
  	svc := newTestService(t)
  	ctx := context.Background()

  	r, err := svc.CreateRepo(ctx, "org-1", "widgets")
  	if err != nil {
  		t.Fatalf("CreateRepo: %v", err)
  	}
  	if r.ID == "" || r.DefaultBranch != "main" {
  		t.Fatalf("unexpected repo: %+v", r)
  	}

  	got, err := svc.GetRepo(ctx, "org-1", "widgets")
  	if err != nil {
  		t.Fatalf("GetRepo: %v", err)
  	}
  	if got.ID != r.ID {
  		t.Fatalf("GetRepo id mismatch: %s != %s", got.ID, r.ID)
  	}

  	list, err := svc.ListRepos(ctx, "org-1")
  	if err != nil {
  		t.Fatalf("ListRepos: %v", err)
  	}
  	if len(list) != 1 {
  		t.Fatalf("ListRepos len = %d, want 1", len(list))
  	}

  	// Duplicate slug in the same org must fail (UNIQUE(org_id, slug)).
  	if _, err := svc.CreateRepo(ctx, "org-1", "widgets"); err == nil {
  		t.Fatal("CreateRepo duplicate: want error, got nil")
  	}
  }

  func TestListRefsEmptyThenSeeded(t *testing.T) {
  	svc := newTestService(t)
  	ctx := context.Background()

  	r, err := svc.CreateRepo(ctx, "org-1", "widgets")
  	if err != nil {
  		t.Fatalf("CreateRepo: %v", err)
  	}

  	// A fresh bare repo has no refs.
  	refs, err := svc.ListRefs(ctx, r.ID)
  	if err != nil {
  		t.Fatalf("ListRefs: %v", err)
  	}
  	if len(refs) != 0 {
  		t.Fatalf("fresh repo refs = %d, want 0", len(refs))
  	}

  	// Seed one commit on main via the test helper; then a single head appears.
  	sha := seedCommit(t, svc, r.ID, "main", "hello")
  	refs, err = svc.ListRefs(ctx, r.ID)
  	if err != nil {
  		t.Fatalf("ListRefs after seed: %v", err)
  	}
  	if len(refs) != 1 {
  		t.Fatalf("seeded refs = %d, want 1", len(refs))
  	}
  	if refs[0].Name != "refs/heads/main" || refs[0].Hash != sha {
  		t.Fatalf("unexpected ref: %+v (want refs/heads/main @ %s)", refs[0], sha)
  	}

  	// GetRef resolves the same head.
  	got, err := svc.GetRef(ctx, r.ID, "refs/heads/main")
  	if err != nil {
  		t.Fatalf("GetRef: %v", err)
  	}
  	if got.Hash != sha {
  		t.Fatalf("GetRef hash = %s, want %s", got.Hash, sha)
  	}
  }
  ```
- [ ] Run, expect compile failure (no `Service`, `Open`, `seedCommit` yet):
  ```sh
  go -C /Users/jacinta/Source/cairn-server test ./internal/repo/
  ```
  Expect prefix: `# github.com/CarriedWorldUniverse/cairn-server/internal/repo [build failed]` (undefined: Open / Service / seedCommit).

### 1.4 — Implement the service (make it compile + pass)

- [ ] Write `/Users/jacinta/Source/cairn-server/internal/repo/service.go`. This is the full core: open store + repo root, CRUD repos, list/get refs via go-git, delete, and record push events. go-git's `PlainInit(path, true)` creates the bare repo; refs come from the repo's `References()` iterator.
  ```go
  // Package repo is cairn's repo + ref core: the one engine both ingresses
  // (SSH, HTTP) call. go-git owns on-disk object/ref storage for each bare
  // repo; a SQLite catalogue owns repo discovery + the push audit log.
  package repo

  import (
  	"context"
  	"crypto/rand"
  	"database/sql"
  	_ "embed"
  	"encoding/hex"
  	"errors"
  	"fmt"
  	"os"
  	"path/filepath"
  	"time"

  	"github.com/go-git/go-git/v5"
  	"github.com/go-git/go-git/v5/plumbing"
  	_ "modernc.org/sqlite"
  )

  //go:embed schema.sql
  var schemaSQL string

  // ErrNotFound is returned when a repo or ref does not exist.
  var ErrNotFound = errors.New("repo: not found")

  // Repo is a row in the repo catalogue.
  type Repo struct {
  	ID            string
  	OrgID         string
  	Slug          string
  	DefaultBranch string
  	Protection    string // JSON; minimal default-branch rule
  	StoragePath   string
  	CreatedAt     time.Time
  	UpdatedAt     time.Time
  }

  // Ref is a single git reference (branch/tag) in a repo.
  type Ref struct {
  	Name string // full ref, e.g. refs/heads/main
  	Hash string // 40-char hex sha
  }

  // PushEvent is one audit row recorded at receive-pack.
  type PushEvent struct {
  	RepoID        string
  	Ref           string
  	OldSHA        string
  	NewSHA        string
  	PusherAgentID string
  	Forced        bool
  }

  // Service is the repo + ref core. Safe for concurrent use (SQLite serialises
  // writes; go-git opens each repo per call).
  type Service struct {
  	db       *sql.DB
  	repoRoot string // directory under which bare repos live
  }

  // Open opens (creating if needed) the SQLite catalogue at dbPath and ensures
  // repoRoot exists. The on-disk layout is repoRoot/<repo-id>.git.
  func Open(dbPath, repoRoot string) (*Service, error) {
  	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
  		return nil, fmt.Errorf("repo.Open: mkdir repoRoot: %w", err)
  	}
  	db, err := sql.Open("sqlite", dbPath)
  	if err != nil {
  		return nil, fmt.Errorf("repo.Open: sqlite: %w", err)
  	}
  	if _, err := db.Exec(schemaSQL); err != nil {
  		_ = db.Close()
  		return nil, fmt.Errorf("repo.Open: schema: %w", err)
  	}
  	return &Service{db: db, repoRoot: repoRoot}, nil
  }

  // Close releases the database handle.
  func (s *Service) Close() error { return s.db.Close() }

  func newID() string {
  	var b [16]byte
  	_, _ = rand.Read(b[:])
  	return hex.EncodeToString(b[:])
  }

  // storagePath is the absolute on-disk path for a repo id.
  func (s *Service) storagePath(id string) string {
  	return filepath.Join(s.repoRoot, id+".git")
  }

  // CreateRepo creates the catalogue row and initialises an empty bare go-git
  // repo on disk. Fails if (org, slug) already exists.
  func (s *Service) CreateRepo(ctx context.Context, orgID, slug string) (Repo, error) {
  	if orgID == "" || slug == "" {
  		return Repo{}, errors.New("repo.CreateRepo: org and slug required")
  	}
  	now := time.Now().UTC()
  	r := Repo{
  		ID:            newID(),
  		OrgID:         orgID,
  		Slug:          slug,
  		DefaultBranch: "main",
  		Protection:    "{}",
  		CreatedAt:     now,
  		UpdatedAt:     now,
  	}
  	r.StoragePath = s.storagePath(r.ID)

  	if _, err := git.PlainInit(r.StoragePath, true); err != nil {
  		return Repo{}, fmt.Errorf("repo.CreateRepo: git init: %w", err)
  	}
  	_, err := s.db.ExecContext(ctx,
  		`INSERT INTO repo(id, org_id, slug, default_branch, protection, storage_path, created_at, updated_at)
  		 VALUES(?,?,?,?,?,?,?,?)`,
  		r.ID, r.OrgID, r.Slug, r.DefaultBranch, r.Protection, r.StoragePath,
  		now.Format(time.RFC3339), now.Format(time.RFC3339))
  	if err != nil {
  		_ = os.RemoveAll(r.StoragePath) // roll back the on-disk repo
  		return Repo{}, fmt.Errorf("repo.CreateRepo: insert: %w", err)
  	}
  	return r, nil
  }

  func scanRepo(row interface{ Scan(...any) error }) (Repo, error) {
  	var r Repo
  	var created, updated string
  	if err := row.Scan(&r.ID, &r.OrgID, &r.Slug, &r.DefaultBranch,
  		&r.Protection, &r.StoragePath, &created, &updated); err != nil {
  		if errors.Is(err, sql.ErrNoRows) {
  			return Repo{}, ErrNotFound
  		}
  		return Repo{}, err
  	}
  	r.CreatedAt, _ = time.Parse(time.RFC3339, created)
  	r.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
  	return r, nil
  }

  const repoCols = `id, org_id, slug, default_branch, protection, storage_path, created_at, updated_at`

  // GetRepo resolves a repo by (org, slug).
  func (s *Service) GetRepo(ctx context.Context, orgID, slug string) (Repo, error) {
  	row := s.db.QueryRowContext(ctx,
  		`SELECT `+repoCols+` FROM repo WHERE org_id=? AND slug=?`, orgID, slug)
  	r, err := scanRepo(row)
  	if err != nil {
  		return Repo{}, fmt.Errorf("repo.GetRepo: %w", err)
  	}
  	return r, nil
  }

  // GetRepoByID resolves a repo by its id (used by the ingresses after a path
  // lookup).
  func (s *Service) GetRepoByID(ctx context.Context, id string) (Repo, error) {
  	row := s.db.QueryRowContext(ctx,
  		`SELECT `+repoCols+` FROM repo WHERE id=?`, id)
  	r, err := scanRepo(row)
  	if err != nil {
  		return Repo{}, fmt.Errorf("repo.GetRepoByID: %w", err)
  	}
  	return r, nil
  }

  // ListRepos lists all repos in an org, slug-ordered.
  func (s *Service) ListRepos(ctx context.Context, orgID string) ([]Repo, error) {
  	rows, err := s.db.QueryContext(ctx,
  		`SELECT `+repoCols+` FROM repo WHERE org_id=? ORDER BY slug`, orgID)
  	if err != nil {
  		return nil, fmt.Errorf("repo.ListRepos: %w", err)
  	}
  	defer rows.Close()
  	var out []Repo
  	for rows.Next() {
  		r, err := scanRepo(rows)
  		if err != nil {
  			return nil, fmt.Errorf("repo.ListRepos: scan: %w", err)
  		}
  		out = append(out, r)
  	}
  	return out, rows.Err()
  }

  // DeleteRepo removes the catalogue row (cascading push_events) and the on-disk
  // bare repo.
  func (s *Service) DeleteRepo(ctx context.Context, id string) error {
  	r, err := s.GetRepoByID(ctx, id)
  	if err != nil {
  		return err
  	}
  	if _, err := s.db.ExecContext(ctx, `DELETE FROM repo WHERE id=?`, id); err != nil {
  		return fmt.Errorf("repo.DeleteRepo: delete row: %w", err)
  	}
  	if err := os.RemoveAll(r.StoragePath); err != nil {
  		return fmt.Errorf("repo.DeleteRepo: remove storage: %w", err)
  	}
  	return nil
  }

  // openGit opens the on-disk bare repo for a repo id.
  func (s *Service) openGit(ctx context.Context, repoID string) (*git.Repository, error) {
  	r, err := s.GetRepoByID(ctx, repoID)
  	if err != nil {
  		return nil, err
  	}
  	g, err := git.PlainOpen(r.StoragePath)
  	if err != nil {
  		return nil, fmt.Errorf("repo.openGit: %w", err)
  	}
  	return g, nil
  }

  // ListRefs lists every reference in the repo (heads + tags), excluding the
  // symbolic HEAD.
  func (s *Service) ListRefs(ctx context.Context, repoID string) ([]Ref, error) {
  	g, err := s.openGit(ctx, repoID)
  	if err != nil {
  		return nil, err
  	}
  	iter, err := g.References()
  	if err != nil {
  		return nil, fmt.Errorf("repo.ListRefs: %w", err)
  	}
  	defer iter.Close()
  	var out []Ref
  	err = iter.ForEach(func(ref *plumbing.Reference) error {
  		if ref.Type() == plumbing.SymbolicReference {
  			return nil // skip HEAD
  		}
  		out = append(out, Ref{Name: ref.Name().String(), Hash: ref.Hash().String()})
  		return nil
  	})
  	if err != nil {
  		return nil, fmt.Errorf("repo.ListRefs: iterate: %w", err)
  	}
  	return out, nil
  }

  // GetRef resolves a single full ref name (e.g. refs/heads/main).
  func (s *Service) GetRef(ctx context.Context, repoID, name string) (Ref, error) {
  	g, err := s.openGit(ctx, repoID)
  	if err != nil {
  		return Ref{}, err
  	}
  	ref, err := g.Reference(plumbing.ReferenceName(name), false)
  	if err != nil {
  		return Ref{}, fmt.Errorf("repo.GetRef: %w: %w", ErrNotFound, err)
  	}
  	return Ref{Name: ref.Name().String(), Hash: ref.Hash().String()}, nil
  }

  // StoragePathForID returns the on-disk path for a repo id. The ingresses use
  // this to hand go-git's transport layer the right directory.
  func (s *Service) StoragePathForID(ctx context.Context, id string) (string, error) {
  	r, err := s.GetRepoByID(ctx, id)
  	if err != nil {
  		return "", err
  	}
  	return r.StoragePath, nil
  }

  // RecordPush appends a push_event audit row.
  func (s *Service) RecordPush(ctx context.Context, e PushEvent) error {
  	forced := 0
  	if e.Forced {
  		forced = 1
  	}
  	_, err := s.db.ExecContext(ctx,
  		`INSERT INTO push_event(id, repo_id, ref, old_sha, new_sha, pusher_agent_id, forced, at)
  		 VALUES(?,?,?,?,?,?,?,?)`,
  		newID(), e.RepoID, e.Ref, e.OldSHA, e.NewSHA, e.PusherAgentID, forced,
  		time.Now().UTC().Format(time.RFC3339))
  	if err != nil {
  		return fmt.Errorf("repo.RecordPush: %w", err)
  	}
  	return nil
  }
  ```
- [ ] Write the test helper `seedCommit` at `/Users/jacinta/Source/cairn-server/internal/repo/seed_test.go` (kept out of the production build; uses go-git's in-memory worktree trick — clone-less commit into a bare repo via a temp non-bare clone is heavy, so seed by committing objects directly):
  ```go
  package repo

  import (
  	"context"
  	"testing"
  	"time"

  	"github.com/go-git/go-git/v5"
  	"github.com/go-git/go-git/v5/plumbing"
  	"github.com/go-git/go-git/v5/plumbing/object"
  )

  // seedCommit writes a single commit containing one file ("README", body=content)
  // to refs/heads/<branch> of the bare repo behind repoID, and returns its sha.
  // It builds the tree/commit objects directly so no worktree is needed.
  func seedCommit(t *testing.T, svc *Service, repoID, branch, content string) string {
  	t.Helper()
  	ctx := context.Background()
  	g, err := svc.openGit(ctx, repoID)
  	if err != nil {
  		t.Fatalf("openGit: %v", err)
  	}
  	store := g.Storer

  	// Blob.
  	blob := store.NewEncodedObject()
  	blob.SetType(plumbing.BlobObject)
  	w, err := blob.Writer()
  	if err != nil {
  		t.Fatalf("blob writer: %v", err)
  	}
  	if _, err := w.Write([]byte(content)); err != nil {
  		t.Fatalf("blob write: %v", err)
  	}
  	_ = w.Close()
  	blobHash, err := store.SetEncodedObject(blob)
  	if err != nil {
  		t.Fatalf("set blob: %v", err)
  	}

  	// Tree with one entry.
  	tree := &object.Tree{Entries: []object.TreeEntry{
  		{Name: "README", Mode: 0o100644, Hash: blobHash},
  	}}
  	teo := store.NewEncodedObject()
  	if err := tree.Encode(teo); err != nil {
  		t.Fatalf("encode tree: %v", err)
  	}
  	treeHash, err := store.SetEncodedObject(teo)
  	if err != nil {
  		t.Fatalf("set tree: %v", err)
  	}

  	// Commit.
  	when := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
  	commit := &object.Commit{
  		Author:    object.Signature{Name: "cairn-test", Email: "test@cairn", When: when},
  		Committer: object.Signature{Name: "cairn-test", Email: "test@cairn", When: when},
  		Message:   "seed: " + content,
  		TreeHash:  treeHash,
  	}
  	ceo := store.NewEncodedObject()
  	if err := commit.Encode(ceo); err != nil {
  		t.Fatalf("encode commit: %v", err)
  	}
  	commitHash, err := store.SetEncodedObject(ceo)
  	if err != nil {
  		t.Fatalf("set commit: %v", err)
  	}

  	// Point the branch at the commit.
  	refName := plumbing.NewBranchReferenceName(branch)
  	if err := store.SetReference(plumbing.NewHashReference(refName, commitHash)); err != nil {
  		t.Fatalf("set ref: %v", err)
  	}
  	_ = g
  	return commitHash.String()
  }
  ```
- [ ] Run, expect pass:
  ```sh
  go -C /Users/jacinta/Source/cairn-server test ./internal/repo/
  ```
  Expect prefix: `ok  	github.com/CarriedWorldUniverse/cairn-server/internal/repo`
- [ ] Add a focused test for `DeleteRepo` and `RecordPush` round-trip at the bottom of `service_test.go`:
  ```go
  func TestDeleteRepoRemovesStorage(t *testing.T) {
  	svc := newTestService(t)
  	ctx := context.Background()
  	r, err := svc.CreateRepo(ctx, "org-1", "gone")
  	if err != nil {
  		t.Fatalf("CreateRepo: %v", err)
  	}
  	if err := svc.DeleteRepo(ctx, r.ID); err != nil {
  		t.Fatalf("DeleteRepo: %v", err)
  	}
  	if _, err := svc.GetRepoByID(ctx, r.ID); err == nil {
  		t.Fatal("GetRepoByID after delete: want error")
  	}
  }

  func TestRecordPush(t *testing.T) {
  	svc := newTestService(t)
  	ctx := context.Background()
  	r, err := svc.CreateRepo(ctx, "org-1", "audited")
  	if err != nil {
  		t.Fatalf("CreateRepo: %v", err)
  	}
  	err = svc.RecordPush(ctx, PushEvent{
  		RepoID: r.ID, Ref: "refs/heads/main",
  		OldSHA: "0000000000000000000000000000000000000000",
  		NewSHA: "1111111111111111111111111111111111111111",
  		PusherAgentID: "agent-7", Forced: false,
  	})
  	if err != nil {
  		t.Fatalf("RecordPush: %v", err)
  	}
  }
  ```
- [ ] Run, expect pass (same `ok ` prefix).
- [ ] Commit: `nex-386: repo+ref core over go-git with sqlite catalogue`.

### 1.5 — `cmd/cairn-server` skeleton

A minimal main that reads config from env, opens the core, and serves `/healthz`. The ingresses (Tasks 2, 3) bolt onto this. Mirrors herald's env-config + `/healthz` shape.

- [ ] Write `/Users/jacinta/Source/cairn-server/cmd/cairn-server/main.go`:
  ```go
  // Command cairn-server is cairn's agent-git host: a go-git-backed git server
  // with two herald-authed ingresses — SSH (casket identity) and HTTP Smart-HTTP
  // behind interchange-gateway. This file wires config + the repo core +
  // /healthz; the ingresses are added by the SSH and HTTP tasks.
  //
  // Config (env):
  //
  //	CAIRN_HTTP_ADDR   HTTP listen address (default :8100)
  //	CAIRN_SSH_ADDR    SSH listen address  (default :2222)
  //	CAIRN_DB          sqlite catalogue path (default /var/lib/nexus/cairn.db)
  //	CAIRN_REPO_ROOT   bare-repo storage dir (default /var/lib/nexus/repos)
  //	CAIRN_HOST_KEY    base64(std) Ed25519 private host key for SSH (required for SSH)
  //	HERALD_BASE_URL   herald base URL for the by-fingerprint lookup (NEX-412)
  package main

  import (
  	"log"
  	"net/http"
  	"os"

  	"github.com/CarriedWorldUniverse/cairn-server/internal/repo"
  )

  func main() {
  	httpAddr := env("CAIRN_HTTP_ADDR", ":8100")
  	dbPath := env("CAIRN_DB", "/var/lib/nexus/cairn.db")
  	repoRoot := env("CAIRN_REPO_ROOT", "/var/lib/nexus/repos")

  	core, err := repo.Open(dbPath, repoRoot)
  	if err != nil {
  		log.Fatalf("cairn: open core: %v", err)
  	}
  	defer core.Close()

  	mux := http.NewServeMux()
  	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
  		w.Header().Set("Content-Type", "application/json")
  		_, _ = w.Write([]byte(`{"status":"ok","service":"cairn"}`))
  	})

  	log.Printf("cairn listening on %s (db=%s, repos=%s)", httpAddr, dbPath, repoRoot)
  	if err := http.ListenAndServe(httpAddr, mux); err != nil {
  		log.Fatalf("cairn: %v", err)
  	}
  }

  func env(key, def string) string {
  	if v := os.Getenv(key); v != "" {
  		return v
  	}
  	return def
  }
  ```
- [ ] Build + vet:
  ```sh
  go -C /Users/jacinta/Source/cairn-server build ./...
  go -C /Users/jacinta/Source/cairn-server vet ./...
  ```
  Expect: no output (success).
- [ ] Smoke the skeleton:
  ```sh
  CAIRN_DB=/tmp/cairn-smoke.db CAIRN_REPO_ROOT=/tmp/cairn-smoke-repos \
    /Users/jacinta/Source/cairn-server/cairn-server &
  # then in the same or another shell:
  curl -sS http://localhost:8100/healthz
  ```
  Expect output: `{"status":"ok","service":"cairn"}`. Kill the process afterwards (`pkill -f cairn-server`).
- [ ] Commit: `nex-386: cmd/cairn-server skeleton with healthz`.

**Task 1 acceptance:** `go test ./internal/repo/` green; `cairn-server` builds, runs, and answers `/healthz`. The core exposes `CreateRepo / GetRepo / GetRepoByID / ListRepos / DeleteRepo / ListRefs / GetRef / StoragePathForID / RecordPush` — the exact surface Tasks 2-4 consume.

---

## Task 2 — SSH ingress: casket-identity auth (NEX-387)

**Commit prefix:** `nex-387:`

**Goal:** an SSH server (`gliderlabs/ssh`) that authenticates a connecting aspect by its **casket public key**: compute the key fingerprint (herald's exact convention), resolve it to a herald agent via the **`heraldAgents` interface** (real impl calls NEX-412 `GET /api/agents/by-fingerprint/{fp}`; a fake backs the tests), confirm the agent is active + scoped, then dispatch `git-upload-pack` (clone/fetch, needs `repo:read`) and `git-receive-pack` (push, needs `repo:write`) to the Task 1 core. cairn's own Ed25519 host key is loaded from config (a k3s Secret in deploy).

**Maps to:** spec §2.2, §3 (SSH row), §6.3.

### Critical dependency note — NEX-412 (READ THIS)

The SSH path needs to map a casket key fingerprint → herald agent. herald already has the **domain logic** (`identity.Service.GetAgentByFingerprint`) but **does not yet expose it over HTTP**. NEX-412 (`GET /api/agents/by-fingerprint/{fp}`) is that thin exposure and is a **hard prerequisite for the SSH path going live** — without it cairn cannot resolve a key.

To keep Task 2 fully buildable and testable NOW (before NEX-412 ships), we define a narrow `heraldAgents` interface with one method, `LookupByFingerprint`. The SSH server depends only on the interface. We ship:
- a **fake** (`fakeAgents`) used by every Task 2 test — no network, deterministic;
- a **real `heraldClient`** that calls NEX-412 over HTTP — compiled and unit-tested against an `httptest` server that mimics the NEX-412 contract, so the moment NEX-412 lands the only remaining work is pointing `HERALD_BASE_URL` at it (no code change).

This means: **Task 2 merges and stays green on its own; the SSH ingress only goes *live* once NEX-412 is deployed.** State this in the PR description.

### 2.1 — The `heraldAgents` interface, the resolved-agent type, and the fake (test FIRST)

- [ ] Create the package dir and write the failing test at `/Users/jacinta/Source/cairn-server/internal/herald/agents_test.go`:
  ```go
  package herald

  import (
  	"context"
  	"errors"
  	"testing"
  	"time"
  )

  func TestFakeAgentsLookup(t *testing.T) {
  	f := NewFakeAgents()
  	f.Add(Agent{ID: "agent-1", OrgID: "org-1", Active: true,
  		Scopes: []string{"repo:read", "repo:write"}, Fingerprint: "fp-abc"})

  	got, err := f.LookupByFingerprint(context.Background(), "fp-abc")
  	if err != nil {
  		t.Fatalf("LookupByFingerprint: %v", err)
  	}
  	if got.ID != "agent-1" || !got.Active || !got.HasScope("repo:write") {
  		t.Fatalf("unexpected agent: %+v", got)
  	}

  	if _, err := f.LookupByFingerprint(context.Background(), "missing"); !errors.Is(err, ErrAgentNotFound) {
  		t.Fatalf("missing fp: want ErrAgentNotFound, got %v", err)
  	}
  }

  func TestCachedAgentsShortTTLAndInvalidate(t *testing.T) {
  	f := NewFakeAgents()
  	f.Add(Agent{ID: "agent-1", OrgID: "org-1", Active: true,
  		Scopes: []string{"repo:read"}, Fingerprint: "fp-abc"})

  	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
  	c := NewCachedAgents(f, 30*time.Second)
  	c.now = clock.Now

  	// First call hits the backend.
  	if _, err := c.LookupByFingerprint(context.Background(), "fp-abc"); err != nil {
  		t.Fatalf("first lookup: %v", err)
  	}
  	if f.calls != 1 {
  		t.Fatalf("calls = %d, want 1", f.calls)
  	}
  	// Second call within TTL is served from cache.
  	if _, err := c.LookupByFingerprint(context.Background(), "fp-abc"); err != nil {
  		t.Fatalf("cached lookup: %v", err)
  	}
  	if f.calls != 1 {
  		t.Fatalf("calls after cache hit = %d, want 1", f.calls)
  	}
  	// After TTL, the backend is hit again.
  	clock.now = clock.now.Add(31 * time.Second)
  	if _, err := c.LookupByFingerprint(context.Background(), "fp-abc"); err != nil {
  		t.Fatalf("post-ttl lookup: %v", err)
  	}
  	if f.calls != 2 {
  		t.Fatalf("calls after ttl = %d, want 2", f.calls)
  	}
  	// Explicit invalidation (block-invalidation hook) forces a refetch.
  	c.Invalidate("fp-abc")
  	if _, err := c.LookupByFingerprint(context.Background(), "fp-abc"); err != nil {
  		t.Fatalf("post-invalidate lookup: %v", err)
  	}
  	if f.calls != 3 {
  		t.Fatalf("calls after invalidate = %d, want 3", f.calls)
  	}
  }

  type fakeClock struct{ now time.Time }

  func (c *fakeClock) Now() time.Time { return c.now }
  ```
- [ ] Run, expect compile failure:
  ```sh
  go -C /Users/jacinta/Source/cairn-server test ./internal/herald/
  ```
  Expect prefix: `# github.com/CarriedWorldUniverse/cairn-server/internal/herald [build failed]` (undefined: Agent / NewFakeAgents / ErrAgentNotFound / NewCachedAgents).

### 2.2 — Implement the interface, the agent type, the cache, and the fake

- [ ] Write `/Users/jacinta/Source/cairn-server/internal/herald/agents.go`:
  ```go
  // Package herald is cairn's consumer-side view of the herald identity
  // authority for the SSH path: it maps a casket-key fingerprint to a herald
  // agent (active state + scopes). The SSH ingress depends only on the
  // HeraldAgents interface; the real implementation calls NEX-412
  // (GET /api/agents/by-fingerprint/{fp}); a fake backs the tests; a cache
  // wraps either to spare a herald round-trip per SSH connection.
  package herald

  import (
  	"context"
  	"errors"
  	"sync"
  	"time"
  )

  // ErrAgentNotFound means no active herald agent owns the given fingerprint.
  var ErrAgentNotFound = errors.New("herald: agent not found for fingerprint")

  // Agent is the resolved herald identity behind a casket key.
  type Agent struct {
  	ID          string   // herald agent id (the actor recorded on pushes)
  	OrgID       string   // owning org
  	Active      bool     // herald's liveness/block cascade result
  	Scopes      []string // e.g. ["repo:read","repo:write"]
  	Fingerprint string   // the casket fingerprint that resolved to this agent
  }

  // HasScope reports whether the agent holds the named scope.
  func (a Agent) HasScope(s string) bool {
  	for _, have := range a.Scopes {
  		if have == s {
  			return true
  		}
  	}
  	return false
  }

  // HeraldAgents resolves a casket fingerprint to a herald agent. The SSH
  // ingress is written against this interface alone.
  type HeraldAgents interface {
  	LookupByFingerprint(ctx context.Context, fp string) (Agent, error)
  }

  // CachedAgents wraps a HeraldAgents with a short-TTL positive cache and an
  // explicit Invalidate (the block-invalidation hook). Negative results are not
  // cached — a just-provisioned agent must resolve immediately.
  type CachedAgents struct {
  	backend HeraldAgents
  	ttl     time.Duration
  	now     func() time.Time

  	mu      sync.Mutex
  	entries map[string]cacheEntry
  }

  type cacheEntry struct {
  	agent   Agent
  	expires time.Time
  }

  // NewCachedAgents wraps backend with the given positive-cache TTL.
  func NewCachedAgents(backend HeraldAgents, ttl time.Duration) *CachedAgents {
  	return &CachedAgents{
  		backend: backend,
  		ttl:     ttl,
  		now:     time.Now,
  		entries: map[string]cacheEntry{},
  	}
  }

  // LookupByFingerprint serves a non-expired cached agent, else fetches and
  // caches it.
  func (c *CachedAgents) LookupByFingerprint(ctx context.Context, fp string) (Agent, error) {
  	now := c.now()
  	c.mu.Lock()
  	if e, ok := c.entries[fp]; ok && now.Before(e.expires) {
  		c.mu.Unlock()
  		return e.agent, nil
  	}
  	c.mu.Unlock()

  	a, err := c.backend.LookupByFingerprint(ctx, fp)
  	if err != nil {
  		return Agent{}, err
  	}
  	c.mu.Lock()
  	c.entries[fp] = cacheEntry{agent: a, expires: now.Add(c.ttl)}
  	c.mu.Unlock()
  	return a, nil
  }

  // Invalidate drops any cached entry for a fingerprint. Call this when herald
  // signals an agent was blocked (block-invalidation), so a darkened agent
  // can't ride a stale cache entry until the TTL lapses.
  func (c *CachedAgents) Invalidate(fp string) {
  	c.mu.Lock()
  	delete(c.entries, fp)
  	c.mu.Unlock()
  }
  ```
- [ ] Write the fake at `/Users/jacinta/Source/cairn-server/internal/herald/fake.go`:
  ```go
  package herald

  import (
  	"context"
  	"sync"
  )

  // FakeAgents is an in-memory HeraldAgents for tests. It counts calls so cache
  // behaviour can be asserted.
  type FakeAgents struct {
  	mu    sync.Mutex
  	byFP  map[string]Agent
  	calls int
  }

  // NewFakeAgents builds an empty fake.
  func NewFakeAgents() *FakeAgents {
  	return &FakeAgents{byFP: map[string]Agent{}}
  }

  // Add registers an agent under its Fingerprint.
  func (f *FakeAgents) Add(a Agent) {
  	f.mu.Lock()
  	f.byFP[a.Fingerprint] = a
  	f.mu.Unlock()
  }

  // LookupByFingerprint returns the registered agent or ErrAgentNotFound.
  func (f *FakeAgents) LookupByFingerprint(_ context.Context, fp string) (Agent, error) {
  	f.mu.Lock()
  	defer f.mu.Unlock()
  	f.calls++
  	a, ok := f.byFP[fp]
  	if !ok {
  		return Agent{}, ErrAgentNotFound
  	}
  	return a, nil
  }
  ```
  > Note: `calls` is read in the cache test without locking only because tests are single-goroutine; the field exists purely for assertion. Production code never touches it.
- [ ] Run, expect pass:
  ```sh
  go -C /Users/jacinta/Source/cairn-server test ./internal/herald/
  ```
  Expect prefix: `ok  	github.com/CarriedWorldUniverse/cairn-server/internal/herald`
- [ ] Commit: `nex-387: heraldAgents interface + short-ttl cache + fake`.

### 2.3 — The real NEX-412 HTTP client (compiled now, live when NEX-412 ships)

NEX-412's contract (the thin exposure of herald's existing `GetAgentByFingerprint`): `GET {herald}/api/agents/by-fingerprint/{fp}` → `200` JSON `{ "id", "org", "active", "scopes": [...] }`, or `404` when unknown. We code against that and test it with an `httptest` server, so the day NEX-412 deploys this works unchanged.

- [ ] Add the client test to `/Users/jacinta/Source/cairn-server/internal/herald/agents_test.go`:
  ```go
  func TestHeraldClientLookup(t *testing.T) {
  	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
  		// NEX-412 contract: GET /api/agents/by-fingerprint/{fp}
  		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/api/agents/by-fingerprint/") {
  			http.Error(w, "no", http.StatusNotFound)
  			return
  		}
  		fp := strings.TrimPrefix(r.URL.Path, "/api/agents/by-fingerprint/")
  		if fp != "fp-abc" {
  			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
  			return
  		}
  		w.Header().Set("Content-Type", "application/json")
  		_, _ = w.Write([]byte(`{"id":"agent-1","org":"org-1","active":true,"scopes":["repo:read","repo:write"]}`))
  	}))
  	defer srv.Close()

  	c := NewHeraldClient(srv.URL, srv.Client())
  	got, err := c.LookupByFingerprint(context.Background(), "fp-abc")
  	if err != nil {
  		t.Fatalf("LookupByFingerprint: %v", err)
  	}
  	if got.ID != "agent-1" || got.OrgID != "org-1" || !got.Active || !got.HasScope("repo:write") {
  		t.Fatalf("unexpected agent: %+v", got)
  	}

  	if _, err := c.LookupByFingerprint(context.Background(), "nope"); !errors.Is(err, ErrAgentNotFound) {
  		t.Fatalf("unknown fp: want ErrAgentNotFound, got %v", err)
  	}
  }
  ```
  Add the imports `"net/http"`, `"net/http/httptest"`, `"strings"` to the test file's import block.
- [ ] Run, expect compile failure (undefined: NewHeraldClient).
- [ ] Write `/Users/jacinta/Source/cairn-server/internal/herald/client.go`:
  ```go
  package herald

  import (
  	"context"
  	"encoding/json"
  	"fmt"
  	"net/http"
  	"net/url"
  	"strings"
  )

  // HeraldClient is the real HeraldAgents: it calls NEX-412
  // (GET /api/agents/by-fingerprint/{fp}) on herald. Until NEX-412 is deployed
  // this compiles and is unit-tested against an httptest stub of that contract;
  // going live is a config change (point baseURL at herald), not a code change.
  type HeraldClient struct {
  	baseURL string
  	http    *http.Client
  }

  // NewHeraldClient builds a client against herald's base URL (e.g.
  // "http://herald.cwb.svc:8099" or the gateway-fronted "{gateway}/herald").
  func NewHeraldClient(baseURL string, hc *http.Client) *HeraldClient {
  	if hc == nil {
  		hc = http.DefaultClient
  	}
  	return &HeraldClient{baseURL: strings.TrimRight(baseURL, "/"), http: hc}
  }

  type agentDTO struct {
  	ID     string   `json:"id"`
  	Org    string   `json:"org"`
  	Active bool     `json:"active"`
  	Scopes []string `json:"scopes"`
  }

  // LookupByFingerprint implements HeraldAgents against NEX-412.
  func (c *HeraldClient) LookupByFingerprint(ctx context.Context, fp string) (Agent, error) {
  	u := c.baseURL + "/api/agents/by-fingerprint/" + url.PathEscape(fp)
  	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
  	if err != nil {
  		return Agent{}, fmt.Errorf("herald.LookupByFingerprint: build request: %w", err)
  	}
  	resp, err := c.http.Do(req)
  	if err != nil {
  		return Agent{}, fmt.Errorf("herald.LookupByFingerprint: %w", err)
  	}
  	defer resp.Body.Close()

  	switch resp.StatusCode {
  	case http.StatusOK:
  		var dto agentDTO
  		if err := json.NewDecoder(resp.Body).Decode(&dto); err != nil {
  			return Agent{}, fmt.Errorf("herald.LookupByFingerprint: decode: %w", err)
  		}
  		return Agent{
  			ID:          dto.ID,
  			OrgID:       dto.Org,
  			Active:      dto.Active,
  			Scopes:      dto.Scopes,
  			Fingerprint: fp,
  		}, nil
  	case http.StatusNotFound:
  		return Agent{}, ErrAgentNotFound
  	default:
  		return Agent{}, fmt.Errorf("herald.LookupByFingerprint: unexpected status %d", resp.StatusCode)
  	}
  }
  ```
- [ ] Run, expect pass (`ok ` prefix). Commit: `nex-387: real herald NEX-412 by-fingerprint client (compiled, live on deploy)`.

### 2.4 — Fingerprint computation (must match herald exactly)

cairn computes the fingerprint of an SSH public key the same way herald does over the raw Ed25519 public key: `base64url(sha256(pubkey)[:16])`. gliderlabs/ssh hands us a `ssh.PublicKey`; we recover the raw Ed25519 32-byte key from it.

- [ ] Write the failing test at `/Users/jacinta/Source/cairn-server/internal/sshd/fingerprint_test.go`:
  ```go
  package sshd

  import (
  	"crypto/ed25519"
  	"crypto/sha256"
  	"encoding/base64"
  	"testing"

  	gossh "golang.org/x/crypto/ssh"
  )

  func TestFingerprintMatchesHeraldConvention(t *testing.T) {
  	pub, _, err := ed25519.GenerateKey(nil)
  	if err != nil {
  		t.Fatal(err)
  	}
  	// herald's convention, computed independently here.
  	sum := sha256.Sum256(pub)
  	want := base64.RawURLEncoding.EncodeToString(sum[:16])

  	sshPub, err := gossh.NewPublicKey(pub)
  	if err != nil {
  		t.Fatal(err)
  	}
  	got, err := Fingerprint(sshPub)
  	if err != nil {
  		t.Fatalf("Fingerprint: %v", err)
  	}
  	if got != want {
  		t.Fatalf("fingerprint = %s, want %s", got, want)
  	}
  }

  func TestFingerprintRejectsNonEd25519(t *testing.T) {
  	// An RSA-typed key must be rejected (casket identities are Ed25519).
  	if _, err := Fingerprint(stubNonEd25519{}); err == nil {
  		t.Fatal("want error for non-ed25519 key")
  	}
  }

  type stubNonEd25519 struct{}

  func (stubNonEd25519) Type() string                                  { return "ssh-rsa" }
  func (stubNonEd25519) Marshal() []byte                               { return nil }
  func (stubNonEd25519) Verify(_ []byte, _ *gossh.Signature) error     { return nil }
  ```
- [ ] Run, expect compile failure (undefined: Fingerprint). First add deps:
  ```sh
  go -C /Users/jacinta/Source/cairn-server get github.com/gliderlabs/ssh@v0.3.8
  go -C /Users/jacinta/Source/cairn-server get golang.org/x/crypto@v0.36.0
  ```
  Expect: `go: added ...` for each. Then run the test — expect `[build failed]` (undefined: Fingerprint).
- [ ] Write `/Users/jacinta/Source/cairn-server/internal/sshd/fingerprint.go`:
  ```go
  package sshd

  import (
  	"crypto/ed25519"
  	"crypto/sha256"
  	"encoding/base64"
  	"errors"

  	gossh "golang.org/x/crypto/ssh"
  )

  // Fingerprint computes herald's casket fingerprint for an SSH public key:
  // base64url(sha256(rawEd25519Pub)[:16]). It must byte-for-byte match
  // herald/internal/identity.Fingerprint so the directory lookup resolves.
  // Only Ed25519 keys are accepted — casket identities are Ed25519.
  func Fingerprint(pub gossh.PublicKey) (string, error) {
  	ck, ok := pub.(gossh.CryptoPublicKey)
  	if !ok {
  		return "", errors.New("sshd.Fingerprint: key does not expose its crypto public key")
  	}
  	edPub, ok := ck.CryptoPublicKey().(ed25519.PublicKey)
  	if !ok {
  		return "", errors.New("sshd.Fingerprint: not an ed25519 key")
  	}
  	sum := sha256.Sum256(edPub)
  	return base64.RawURLEncoding.EncodeToString(sum[:16]), nil
  }
  ```
  > Note on the test: `stubNonEd25519` does not implement `gossh.CryptoPublicKey`, so `Fingerprint` rejects it at the first type assertion — exercising the non-Ed25519 path without needing a real RSA key.
- [ ] Run, expect pass (`ok ` prefix). Commit: `nex-387: casket fingerprint (herald-matching) for ssh pubkeys`.

### 2.5 — Repo path parsing for SSH (org/slug from the git command)

A git-over-SSH client runs `git-upload-pack '/org/slug.git'` (or `receive-pack`). We parse the command into (verb, org, slug).

- [ ] Write the failing test `/Users/jacinta/Source/cairn-server/internal/sshd/command_test.go`:
  ```go
  package sshd

  import "testing"

  func TestParseGitCommand(t *testing.T) {
  	cases := []struct {
  		in              string
  		wantVerb, wantOrg, wantSlug string
  		wantErr         bool
  	}{
  		{`git-upload-pack '/org-1/widgets.git'`, "git-upload-pack", "org-1", "widgets", false},
  		{`git-receive-pack '/org-1/widgets'`, "git-receive-pack", "org-1", "widgets", false},
  		{`git-upload-pack "/org-1/widgets.git"`, "git-upload-pack", "org-1", "widgets", false},
  		{`scp -t /tmp`, "", "", "", true},
  		{`git-upload-pack '/widgets.git'`, "", "", "", true}, // missing org segment
  		{``, "", "", "", true},
  	}
  	for _, c := range cases {
  		verb, org, slug, err := ParseGitCommand(c.in)
  		if c.wantErr {
  			if err == nil {
  				t.Errorf("%q: want error", c.in)
  			}
  			continue
  		}
  		if err != nil {
  			t.Errorf("%q: unexpected error %v", c.in, err)
  			continue
  		}
  		if verb != c.wantVerb || org != c.wantOrg || slug != c.wantSlug {
  			t.Errorf("%q: got (%s,%s,%s), want (%s,%s,%s)", c.in, verb, org, slug, c.wantVerb, c.wantOrg, c.wantSlug)
  		}
  	}
  }
  ```
- [ ] Run, expect compile failure (undefined: ParseGitCommand).
- [ ] Write `/Users/jacinta/Source/cairn-server/internal/sshd/command.go`:
  ```go
  package sshd

  import (
  	"fmt"
  	"strings"
  )

  // ParseGitCommand parses an SSH exec command of the form
  //
  //	git-upload-pack '/org/slug.git'
  //	git-receive-pack '/org/slug'
  //
  // into (verb, org, slug). Only the two pack verbs are allowed; anything else
  // (shell access, scp, etc.) is rejected.
  func ParseGitCommand(cmd string) (verb, org, slug string, err error) {
  	cmd = strings.TrimSpace(cmd)
  	if cmd == "" {
  		return "", "", "", fmt.Errorf("sshd: empty command")
  	}
  	sp := strings.IndexByte(cmd, ' ')
  	if sp < 0 {
  		return "", "", "", fmt.Errorf("sshd: command %q missing path", cmd)
  	}
  	verb = cmd[:sp]
  	if verb != "git-upload-pack" && verb != "git-receive-pack" {
  		return "", "", "", fmt.Errorf("sshd: unsupported command %q", verb)
  	}
  	path := strings.TrimSpace(cmd[sp+1:])
  	path = strings.Trim(path, `'"`)
  	path = strings.TrimPrefix(path, "/")
  	path = strings.TrimSuffix(path, ".git")
  	parts := strings.Split(path, "/")
  	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
  		return "", "", "", fmt.Errorf("sshd: path %q must be /org/slug", path)
  	}
  	return verb, parts[0], parts[1], nil
  }
  ```
- [ ] Run, expect pass (`ok ` prefix). Commit: `nex-387: parse git-over-ssh exec command into verb/org/slug`.

### 2.6 — The SSH server: auth callback + pack dispatch (test FIRST, end-to-end over a real git client)

This is the meat: a `gliderlabs/ssh` server whose `PublicKeyHandler` resolves the casket fingerprint to a herald agent and stashes the resolved `Agent` in the session context, and whose session handler parses the command, enforces the scope, and runs the pack protocol against the on-disk repo using go-git's `transport/server` plus a `git` shell-out for the pack streaming.

> **Design choice (pinned per spec §8):** for the pack transfer over SSH we shell out to the system `git` binary's `git-upload-pack`/`git-receive-pack` against the bare repo path, streaming stdin/stdout/stderr over the SSH channel. This is the battle-tested, protocol-exact path; go-git's own server transport is used for the HTTP Smart-HTTP path in Task 3 where we control the HTTP framing. cairn's container ships the `git` binary for this (noted in Task 5). The auth + routing is all cairn; the byte-pump is `git`.

- [ ] Write the failing end-to-end test `/Users/jacinta/Source/cairn-server/internal/sshd/server_test.go`. It boots the server on a random port with a fake herald, then drives a real `git` clone/push over `ssh://` using a generated casket key:
  ```go
  package sshd

  import (
  	"context"
  	"crypto/ed25519"
  	"net"
  	"os"
  	"os/exec"
  	"path/filepath"
  	"testing"

  	"github.com/CarriedWorldUniverse/cairn-server/internal/herald"
  	"github.com/CarriedWorldUniverse/cairn-server/internal/repo"
  	gossh "golang.org/x/crypto/ssh"
  )

  // bootServer starts an sshd.Server on a random localhost port with the given
  // core + agents, returns its addr, and registers cleanup.
  func bootServer(t *testing.T, core *repo.Service, agents herald.HeraldAgents) (addr string, hostKeyPath string) {
  	t.Helper()
  	_, hostPriv, err := ed25519.GenerateKey(nil)
  	if err != nil {
  		t.Fatal(err)
  	}
  	signer, err := gossh.NewSignerFromKey(hostPriv)
  	if err != nil {
  		t.Fatal(err)
  	}
  	ln, err := net.Listen("tcp", "127.0.0.1:0")
  	if err != nil {
  		t.Fatal(err)
  	}
  	srv := New(Config{Core: core, Agents: agents, HostSigner: signer})
  	go func() { _ = srv.Serve(ln) }()
  	t.Cleanup(func() { _ = ln.Close() })

  	// Persist the host's public key so the client can pin it (StrictHostKeyChecking).
  	hk := filepath.Join(t.TempDir(), "known_hosts")
  	host, port, _ := net.SplitHostPort(ln.Addr().String())
  	line := "[" + host + "]:" + port + " " + string(gossh.MarshalAuthorizedKey(signer.PublicKey()))
  	if err := os.WriteFile(hk, []byte(line), 0o600); err != nil {
  		t.Fatal(err)
  	}
  	return ln.Addr().String(), hk
  }

  // writeCasketKey writes an Ed25519 private key in OpenSSH format and returns
  // its path plus the matching herald.Agent (with cairn's fingerprint).
  func writeCasketKey(t *testing.T, dir, agentID, orgID string, scopes []string) (keyPath string, agent herald.Agent) {
  	t.Helper()
  	pub, priv, err := ed25519.GenerateKey(nil)
  	if err != nil {
  		t.Fatal(err)
  	}
  	pemBytes, err := gossh.MarshalPrivateKey(priv, "")
  	if err != nil {
  		t.Fatal(err)
  	}
  	keyPath = filepath.Join(dir, "casket")
  	if err := os.WriteFile(keyPath, gosshEncode(t, pemBytes), 0o600); err != nil {
  		t.Fatal(err)
  	}
  	sshPub, _ := gossh.NewPublicKey(pub)
  	fp, err := Fingerprint(sshPub)
  	if err != nil {
  		t.Fatal(err)
  	}
  	return keyPath, herald.Agent{ID: agentID, OrgID: orgID, Active: true, Scopes: scopes, Fingerprint: fp}
  }

  // gitEnv returns an env that forces git to use our key + known_hosts and no
  // agent/askpass interference.
  func gitEnv(keyPath, knownHosts string) []string {
  	cmd := "ssh -i " + keyPath + " -o IdentitiesOnly=yes -o UserKnownHostsFile=" + knownHosts + " -o StrictHostKeyChecking=yes"
  	return append(os.Environ(),
  		"GIT_SSH_COMMAND="+cmd,
  		"GIT_TERMINAL_PROMPT=0",
  	)
  }

  func TestSSHCloneAndPush(t *testing.T) {
  	if _, err := exec.LookPath("git"); err != nil {
  		t.Skip("git not on PATH")
  	}
  	ctx := context.Background()
  	dir := t.TempDir()
  	core, err := repo.Open(filepath.Join(dir, "cairn.db"), filepath.Join(dir, "repos"))
  	if err != nil {
  		t.Fatal(err)
  	}
  	t.Cleanup(func() { _ = core.Close() })
  	r, err := core.CreateRepo(ctx, "org-1", "widgets")
  	if err != nil {
  		t.Fatal(err)
  	}

  	keyPath, builder := writeCasketKey(t, dir, "agent-builder", "org-1", []string{"repo:read", "repo:write"})
  	agents := herald.NewFakeAgents()
  	agents.Add(builder)

  	addr, knownHosts := bootServer(t, core, agents)
  	host, port, _ := net.SplitHostPort(addr)
  	cloneURL := "ssh://git@" + host + ":" + port + "/org-1/widgets.git"

  	// Clone the (empty) repo.
  	work := filepath.Join(dir, "work")
  	clone := exec.Command("git", "clone", cloneURL, work)
  	clone.Env = gitEnv(keyPath, knownHosts)
  	if out, err := clone.CombinedOutput(); err != nil {
  		t.Fatalf("clone: %v\n%s", err, out)
  	}

  	// Make a commit and push a feature branch.
  	runGit(t, work, gitEnv(keyPath, knownHosts), "checkout", "-b", "feature")
  	if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte("hi"), 0o644); err != nil {
  		t.Fatal(err)
  	}
  	runGit(t, work, gitEnv(keyPath, knownHosts), "add", ".")
  	runGit(t, work, gitEnv(keyPath, knownHosts), "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "x")
  	push := exec.Command("git", "push", "origin", "feature")
  	push.Dir = work
  	push.Env = gitEnv(keyPath, knownHosts)
  	if out, err := push.CombinedOutput(); err != nil {
  		t.Fatalf("push: %v\n%s", err, out)
  	}

  	// The feature ref now exists in the core.
  	if _, err := core.GetRef(ctx, r.ID, "refs/heads/feature"); err != nil {
  		t.Fatalf("expected refs/heads/feature after push: %v", err)
  	}
  }

  func TestSSHReaderCannotPush(t *testing.T) {
  	if _, err := exec.LookPath("git"); err != nil {
  		t.Skip("git not on PATH")
  	}
  	ctx := context.Background()
  	dir := t.TempDir()
  	core, _ := repo.Open(filepath.Join(dir, "cairn.db"), filepath.Join(dir, "repos"))
  	t.Cleanup(func() { _ = core.Close() })
  	_, _ = core.CreateRepo(ctx, "org-1", "widgets")

  	keyPath, reader := writeCasketKey(t, dir, "agent-reader", "org-1", []string{"repo:read"})
  	agents := herald.NewFakeAgents()
  	agents.Add(reader)
  	addr, knownHosts := bootServer(t, core, agents)
  	host, port, _ := net.SplitHostPort(addr)

  	// Clone works (repo:read).
  	work := filepath.Join(dir, "work")
  	clone := exec.Command("git", "clone", "ssh://git@"+host+":"+port+"/org-1/widgets.git", work)
  	clone.Env = gitEnv(keyPath, knownHosts)
  	if out, err := clone.CombinedOutput(); err != nil {
  		t.Fatalf("reader clone should succeed: %v\n%s", err, out)
  	}
  	// Push fails (no repo:write).
  	runGit(t, work, gitEnv(keyPath, knownHosts), "-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "x")
  	push := exec.Command("git", "push", "origin", "HEAD:refs/heads/nope")
  	push.Dir = work
  	push.Env = gitEnv(keyPath, knownHosts)
  	if out, err := push.CombinedOutput(); err == nil {
  		t.Fatalf("reader push should fail, got success:\n%s", out)
  	}
  }

  func runGit(t *testing.T, dir string, env []string, args ...string) {
  	t.Helper()
  	c := exec.Command("git", args...)
  	c.Dir = dir
  	c.Env = env
  	if out, err := c.CombinedOutput(); err != nil {
  		t.Fatalf("git %v: %v\n%s", args, err, out)
  	}
  }
  ```
  Add a tiny PEM-passthrough helper `gosshEncode` at the bottom of the test file (`MarshalPrivateKey` already returns a `*pem.Block`; encode it):
  ```go
  func gosshEncode(t *testing.T, blk *pemBlock) []byte {
  	t.Helper()
  	return pem.EncodeToMemory(blk)
  }
  ```
  with `type pemBlock = pem.Block` and imports `"encoding/pem"`. (`gossh.MarshalPrivateKey` returns `*pem.Block`.)
- [ ] Run, expect compile failure (undefined: New / Config / Server.Serve).
- [ ] Write `/Users/jacinta/Source/cairn-server/internal/sshd/server.go`:
  ```go
  // Package sshd is cairn's SSH ingress: it authenticates a connecting aspect by
  // its casket public key (fingerprint -> herald agent), enforces repo scope,
  // and runs git-upload-pack / git-receive-pack against the on-disk bare repo.
  // It is a parallel, inherently-encrypted ingress (not gateway-fronted): SSH's
  // own transport encryption plus casket public-key auth secure it.
  package sshd

  import (
  	"context"
  	"fmt"
  	"net"
  	"os/exec"

  	"github.com/CarriedWorldUniverse/cairn-server/internal/herald"
  	"github.com/CarriedWorldUniverse/cairn-server/internal/repo"
  	glssh "github.com/gliderlabs/ssh"
  	gossh "golang.org/x/crypto/ssh"
  )

  // contextKey for the resolved agent stashed by the auth callback.
  type ctxKey string

  const agentKey ctxKey = "cairn-agent"

  // Config configures the SSH ingress.
  type Config struct {
  	Core       *repo.Service        // the repo + ref core (Task 1)
  	Agents     herald.HeraldAgents  // fingerprint -> herald agent (Task 2)
  	HostSigner gossh.Signer         // cairn's own Ed25519 host key
  }

  // Server is cairn's SSH git host.
  type Server struct {
  	cfg Config
  }

  // New builds a Server.
  func New(cfg Config) *Server { return &Server{cfg: cfg} }

  // Serve accepts connections on ln until it is closed.
  func (s *Server) Serve(ln net.Listener) error {
  	srv := &glssh.Server{
  		Handler:          s.handleSession,
  		PublicKeyHandler: s.authPublicKey,
  	}
  	srv.AddHostKey(s.cfg.HostSigner)
  	return srv.Serve(ln)
  }

  // authPublicKey resolves the casket key fingerprint to a herald agent and, on
  // success, stashes it in the session context. A non-resolving or inactive key
  // is rejected (auth fails).
  func (s *Server) authPublicKey(ctx glssh.Context, key glssh.PublicKey) bool {
  	fp, err := Fingerprint(key)
  	if err != nil {
  		return false
  	}
  	agent, err := s.cfg.Agents.LookupByFingerprint(ctx, fp)
  	if err != nil || !agent.Active {
  		return false
  	}
  	ctx.SetValue(agentKey, agent)
  	return true
  }

  // handleSession parses the git command, enforces scope, and pumps the pack
  // protocol via the system git binary against the on-disk bare repo.
  func (s *Server) handleSession(sess glssh.Session) {
  	agent, _ := sess.Context().Value(agentKey).(herald.Agent)

  	raw := sess.RawCommand()
  	verb, org, slug, err := ParseGitCommand(raw)
  	if err != nil {
  		fmt.Fprintln(sess.Stderr(), "cairn: "+err.Error())
  		_ = sess.Exit(1)
  		return
  	}

  	// Org binding: the agent may only touch its own org (single-org MVP, but
  	// enforced so a cross-org key can't reach another org's repo).
  	if org != agent.OrgID {
  		fmt.Fprintln(sess.Stderr(), "cairn: org mismatch")
  		_ = sess.Exit(1)
  		return
  	}

  	// Scope gate: clone/fetch needs repo:read; push needs repo:write.
  	need := "repo:read"
  	if verb == "git-receive-pack" {
  		need = "repo:write"
  	}
  	if !agent.HasScope(need) {
  		fmt.Fprintln(sess.Stderr(), "cairn: missing scope "+need)
  		_ = sess.Exit(1)
  		return
  	}

  	ctx := context.Background()
  	r, err := s.cfg.Core.GetRepo(ctx, org, slug)
  	if err != nil {
  		fmt.Fprintln(sess.Stderr(), "cairn: repo not found")
  		_ = sess.Exit(1)
  		return
  	}

  	// Pump the pack protocol via the system git binary against the bare repo.
  	cmd := exec.CommandContext(ctx, verb, r.StoragePath)
  	cmd.Stdin = sess
  	cmd.Stdout = sess
  	cmd.Stderr = sess.Stderr()
  	if err := cmd.Run(); err != nil {
  		_ = sess.Exit(1)
  		return
  	}
  	_ = sess.Exit(0)
  }
  ```
  > **Branch-protection hook (Task 4) note:** receive-pack protection (default-branch scope + no force-push) is layered on top of this in Task 4 via a pre-receive check; Task 2 enforces only the coarse `repo:write` gate. The `TestSSHReaderCannotPush` test here proves the coarse gate; Task 4 adds the fine-grained ref rules and their own tests.
- [ ] Run, expect pass:
  ```sh
  go -C /Users/jacinta/Source/cairn-server test ./internal/sshd/
  ```
  Expect prefix: `ok  	github.com/CarriedWorldUniverse/cairn-server/internal/sshd`. (If `git` is absent the e2e tests `t.Skip` — fingerprint/command tests still run.)
- [ ] Commit: `nex-387: ssh ingress — casket auth + scope-gated pack dispatch`.

### 2.7 — Wire SSH into `cmd/cairn-server`

- [ ] Edit `/Users/jacinta/Source/cairn-server/cmd/cairn-server/main.go` to load the host key, build the herald client wrapped in the cache, start the SSH listener alongside HTTP. Add to the imports `"crypto/ed25519"`, `"encoding/base64"`, `"net"`, `"time"`, the sshd + herald packages, and `gossh "golang.org/x/crypto/ssh"`. Insert after the `core` is opened:
  ```go
  	// herald identity for the SSH path: real NEX-412 client behind a short-TTL
  	// cache. Until NEX-412 is deployed this resolves nothing (404 -> auth fail);
  	// point HERALD_BASE_URL at herald once NEX-412 ships.
  	heraldBase := env("HERALD_BASE_URL", "http://herald.cwb.svc:8099")
  	agents := herald.NewCachedAgents(herald.NewHeraldClient(heraldBase, nil), 30*time.Second)

  	// SSH ingress (parallel, not gateway-fronted).
  	sshAddr := env("CAIRN_SSH_ADDR", ":2222")
  	hostSigner, err := loadHostKey()
  	if err != nil {
  		log.Fatalf("cairn: ssh host key: %v", err)
  	}
  	sshSrv := sshd.New(sshd.Config{Core: core, Agents: agents, HostSigner: hostSigner})
  	sshLn, err := net.Listen("tcp", sshAddr)
  	if err != nil {
  		log.Fatalf("cairn: ssh listen %s: %v", sshAddr, err)
  	}
  	go func() {
  		log.Printf("cairn ssh listening on %s", sshAddr)
  		if err := sshSrv.Serve(sshLn); err != nil {
  			log.Fatalf("cairn: ssh: %v", err)
  		}
  	}()
  ```
  and add the helper:
  ```go
  // loadHostKey loads cairn's Ed25519 SSH host key from CAIRN_HOST_KEY
  // (base64-std of the 64-byte private key). Generated + stored in a k3s Secret
  // by deploy; required so the host identity is stable across restarts.
  func loadHostKey() (gossh.Signer, error) {
  	b64 := os.Getenv("CAIRN_HOST_KEY")
  	if b64 == "" {
  		return nil, errors.New("CAIRN_HOST_KEY required (base64 ed25519 private key)")
  	}
  	raw, err := base64.StdEncoding.DecodeString(b64)
  	if err != nil {
  		return nil, fmt.Errorf("decode host key: %w", err)
  	}
  	return gossh.NewSignerFromKey(ed25519.PrivateKey(raw))
  }
  ```
  Add `"errors"` and `"fmt"` to imports.
- [ ] Build + vet:
  ```sh
  go -C /Users/jacinta/Source/cairn-server build ./...
  go -C /Users/jacinta/Source/cairn-server vet ./...
  ```
  Expect: no output.
- [ ] Commit: `nex-387: wire ssh ingress + cached herald client into cairn-server`.

**Task 2 acceptance:** `go test ./internal/herald/ ./internal/sshd/` green; a real `git clone`/`push` over `ssh://` works against the fake herald with a `repo:write` casket key, and a `repo:read`-only key is refused push. The real NEX-412 client compiles and passes its httptest contract test. **Live-blocked on NEX-412 deploy** (config-only flip).

---

## Task 3 — HTTP ingress: Smart-HTTP via the gateway (NEX-388)

**Commit prefix:** `nex-388:`

**Goal:** a Smart-HTTPv2 git server reachable through interchange-gateway at `/cairn/...`. It does **NOT** do its own herald verification — it **trusts the mTLS-injected `X-CWB-{Subject,Org,Kind,Scopes}`** headers the gateway sets after running herald verification (same model as ledger), and is locked to the ClusterIP/mTLS hop. It also exposes a small repo-admin JSON API (`POST /api/orgs/{org}/repos`) used by the conformance suite to create repos. clone/fetch needs `repo:read`; push needs `repo:write`.

**Maps to:** spec §2.3, §3 (HTTP row), §6.4.

### Identity-path contrast (pin this)

Two distinct identity paths reach the **same** herald agent:
- **SSH (Task 2):** cairn itself resolves the casket fingerprint → herald agent via NEX-412. cairn owns the lookup.
- **HTTP (Task 3):** the *gateway* already ran herald verification and injected `X-CWB-*`; cairn **trusts** those over the mTLS hop and does no lookup. cairn owns nothing here but reading trusted headers.

Do not blur these. The HTTP path never calls NEX-412; the SSH path never reads `X-CWB-*`.

### 3.1 — The X-CWB-* identity reader (test FIRST)

- [ ] Create `/Users/jacinta/Source/cairn-server/internal/httpd/` and write the failing test `identity_test.go`:
  ```go
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
  ```
- [ ] Run, expect compile failure (undefined: identityFromHeaders).
- [ ] Write `/Users/jacinta/Source/cairn-server/internal/httpd/identity.go`:
  ```go
  package httpd

  import (
  	"net/http"
  	"strings"
  )

  // Identity is the gateway-verified caller, read from the trusted X-CWB-*
  // headers interchange injects after herald verification. cairn's HTTP path
  // TRUSTS these over the mTLS gateway->cairn hop and does NOT re-verify.
  type Identity struct {
  	Subject string   // herald agent/human id (the actor)
  	Org     string
  	Kind    string   // "agent" | "human"
  	Scopes  []string
  }

  // HasScope reports whether the identity holds the named scope.
  func (i Identity) HasScope(s string) bool {
  	for _, have := range i.Scopes {
  		if have == s {
  			return true
  		}
  	}
  	return false
  }

  // identityFromHeaders reads the trusted X-CWB-* headers. ok is false when no
  // Subject is present (the gateway always sets Subject for an authed request;
  // its absence means the request did not come through the gateway authed path).
  func identityFromHeaders(r *http.Request) (Identity, bool) {
  	sub := r.Header.Get("X-CWB-Subject")
  	if sub == "" {
  		return Identity{}, false
  	}
  	return Identity{
  		Subject: sub,
  		Org:     r.Header.Get("X-CWB-Org"),
  		Kind:    r.Header.Get("X-CWB-Kind"),
  		Scopes:  strings.Fields(r.Header.Get("X-CWB-Scopes")), // space-joined by the gateway
  	}, true
  }
  ```
- [ ] Run, expect pass (`ok ` prefix). Commit: `nex-388: trusted X-CWB-* identity reader for the http path`.

### 3.2 — The Smart-HTTP handler + repo-admin API (test FIRST, real git over http)

go-git's `transport/server` plus `transport/http` give a Smart-HTTP server, but the simplest protocol-exact path (matching the SSH choice) is to back the three Smart-HTTP endpoints with the system `git` binary in `http-backend` mode driven by cairn's auth/routing. We implement the Smart-HTTPv2 endpoints directly:

- `GET /{org}/{slug}.git/info/refs?service=git-upload-pack|git-receive-pack` — ref advertisement.
- `POST /{org}/{slug}.git/git-upload-pack` — fetch/clone.
- `POST /{org}/{slug}.git/git-receive-pack` — push.

backed by `git http-backend` (the canonical CGI), with cairn enforcing scope before delegating. Plus the admin endpoint `POST /api/orgs/{org}/repos`.

- [ ] Write the failing test `/Users/jacinta/Source/cairn-server/internal/httpd/server_test.go`:
  ```go
  package httpd

  import (
  	"context"
  	"net/http/httptest"
  	"os"
  	"os/exec"
  	"path/filepath"
  	"testing"

  	"github.com/CarriedWorldUniverse/cairn-server/internal/repo"
  )

  // boot starts an httptest server in front of the cairn HTTP handler with the
  // given core, returns its base URL. The test injects X-CWB-* itself (standing
  // in for the gateway).
  func boot(t *testing.T, core *repo.Service) *httptest.Server {
  	t.Helper()
  	h := New(Config{Core: core})
  	srv := httptest.NewServer(h.Handler())
  	t.Cleanup(srv.Close)
  	return srv
  }

  // gitHTTPEnv injects the X-CWB-* identity via git's http.extraHeader, mimicking
  // what the gateway would inject. (In production the client never sets these —
  // the gateway does — but in this gateway-less test we stand in for it.)
  func gitHTTPEnv(scopes string) []string {
  	return append(os.Environ(),
  		"GIT_TERMINAL_PROMPT=0",
  	)
  }

  func extraHeaders(org, subject, scopes string) []string {
  	return []string{
  		"-c", "http.extraHeader=X-CWB-Subject: " + subject,
  		"-c", "http.extraHeader=X-CWB-Org: " + org,
  		"-c", "http.extraHeader=X-CWB-Kind: agent",
  		"-c", "http.extraHeader=X-CWB-Scopes: " + scopes,
  	}
  }

  func TestHTTPCloneAndPush(t *testing.T) {
  	if _, err := exec.LookPath("git"); err != nil {
  		t.Skip("git not on PATH")
  	}
  	ctx := context.Background()
  	dir := t.TempDir()
  	core, err := repo.Open(filepath.Join(dir, "cairn.db"), filepath.Join(dir, "repos"))
  	if err != nil {
  		t.Fatal(err)
  	}
  	t.Cleanup(func() { _ = core.Close() })
  	r, _ := core.CreateRepo(ctx, "org-1", "widgets")

  	srv := boot(t, core)
  	cloneURL := srv.URL + "/org-1/widgets.git"

  	work := filepath.Join(dir, "work")
  	args := append([]string{"clone"}, extraHeaders("org-1", "agent-builder", "repo:read repo:write")...)
  	args = append(args, cloneURL, work)
  	clone := exec.Command("git", args...)
  	clone.Env = gitHTTPEnv("")
  	if out, err := clone.CombinedOutput(); err != nil {
  		t.Fatalf("clone: %v\n%s", err, out)
  	}

  	if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte("hi"), 0o644); err != nil {
  		t.Fatal(err)
  	}
  	run := func(extra ...string) {
  		c := exec.Command("git", append([]string{"-C", work}, extra...)...)
  		c.Env = gitHTTPEnv("")
  		if out, err := c.CombinedOutput(); err != nil {
  			t.Fatalf("git %v: %v\n%s", extra, err, out)
  		}
  	}
  	run("-c", "user.email=t@t", "-c", "user.name=t", "checkout", "-b", "feature")
  	run("add", ".")
  	run("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "x")
  	pushArgs := append([]string{"-C", work}, extraHeaders("org-1", "agent-builder", "repo:read repo:write")...)
  	pushArgs = append(pushArgs, "push", "origin", "feature")
  	push := exec.Command("git", pushArgs...)
  	push.Env = gitHTTPEnv("")
  	if out, err := push.CombinedOutput(); err != nil {
  		t.Fatalf("push: %v\n%s", err, out)
  	}
  	if _, err := core.GetRef(ctx, r.ID, "refs/heads/feature"); err != nil {
  		t.Fatalf("expected refs/heads/feature: %v", err)
  	}
  }

  func TestHTTPReaderCannotPush(t *testing.T) {
  	if _, err := exec.LookPath("git"); err != nil {
  		t.Skip("git not on PATH")
  	}
  	ctx := context.Background()
  	dir := t.TempDir()
  	core, _ := repo.Open(filepath.Join(dir, "cairn.db"), filepath.Join(dir, "repos"))
  	t.Cleanup(func() { _ = core.Close() })
  	_, _ = core.CreateRepo(ctx, "org-1", "widgets")
  	srv := boot(t, core)

  	work := filepath.Join(dir, "work")
  	args := append([]string{"clone"}, extraHeaders("org-1", "agent-reader", "repo:read")...)
  	args = append(args, srv.URL+"/org-1/widgets.git", work)
  	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
  		t.Fatalf("reader clone should succeed: %v\n%s", err, out)
  	}
  	c := exec.Command("git", "-C", work, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "x")
  	if out, err := c.CombinedOutput(); err != nil {
  		t.Fatalf("commit: %v\n%s", err, out)
  	}
  	pushArgs := append([]string{"-C", work}, extraHeaders("org-1", "agent-reader", "repo:read")...)
  	pushArgs = append(pushArgs, "push", "origin", "HEAD:refs/heads/nope")
  	if out, err := exec.Command("git", pushArgs...).CombinedOutput(); err == nil {
  		t.Fatalf("reader push should fail:\n%s", out)
  	}
  }

  func TestCreateRepoAPI(t *testing.T) {
  	ctx := context.Background()
  	dir := t.TempDir()
  	core, _ := repo.Open(filepath.Join(dir, "cairn.db"), filepath.Join(dir, "repos"))
  	t.Cleanup(func() { _ = core.Close() })
  	srv := boot(t, core)

  	body := `{"slug":"created-via-api"}`
  	req := httptest.NewRequest("POST", "/api/orgs/org-1/repos", nil)
  	_ = req
  	// Drive through the real handler with X-CWB-* set.
  	resp := doJSON(t, srv.URL+"/api/orgs/org-1/repos", body, map[string]string{
  		"X-CWB-Subject": "agent-builder", "X-CWB-Org": "org-1",
  		"X-CWB-Kind": "agent", "X-CWB-Scopes": "repo:read repo:write",
  	})
  	if resp != 200 {
  		t.Fatalf("create repo status = %d, want 200", resp)
  	}
  	if _, err := core.GetRepo(ctx, "org-1", "created-via-api"); err != nil {
  		t.Fatalf("repo not created: %v", err)
  	}
  }
  ```
  Add the small JSON-POST helper at the bottom of the test file:
  ```go
  func doJSON(t *testing.T, url, body string, headers map[string]string) int {
  	t.Helper()
  	req, err := http.NewRequest("POST", url, strings.NewReader(body))
  	if err != nil {
  		t.Fatal(err)
  	}
  	req.Header.Set("Content-Type", "application/json")
  	for k, v := range headers {
  		req.Header.Set(k, v)
  	}
  	resp, err := http.DefaultClient.Do(req)
  	if err != nil {
  		t.Fatal(err)
  	}
  	defer resp.Body.Close()
  	return resp.StatusCode
  }
  ```
  with imports `"net/http"` and `"strings"`.
- [ ] Run, expect compile failure (undefined: New / Config / Handler).
- [ ] Write `/Users/jacinta/Source/cairn-server/internal/httpd/server.go`. It mounts the Smart-HTTP endpoints (delegated to `git http-backend`) and the admin API, enforcing scope from the trusted identity before delegating:
  ```go
  // Package httpd is cairn's HTTP ingress: Smart-HTTPv2 git plus a small
  // repo-admin API, reached THROUGH interchange-gateway. It trusts the
  // gateway-injected X-CWB-* identity (the gateway already ran herald
  // verification) over the mTLS hop and does NOT re-verify. The Smart-HTTP byte
  // protocol is served by the system `git http-backend` CGI; cairn owns auth +
  // routing and delegates the pack streaming.
  package httpd

  import (
  	"encoding/json"
  	"fmt"
  	"net/http"
  	"net/http/cgi"
  	"os/exec"
  	"path/filepath"
  	"regexp"
  	"strings"

  	"github.com/CarriedWorldUniverse/cairn-server/internal/repo"
  )

  // ProtectionChecker is the receive-pack gate (Task 4 implements the real one;
  // Task 3 accepts a nil checker meaning "coarse scope only").
  type ProtectionChecker interface {
  	// AllowReceivePack reports whether a push by id to repo r is permitted at
  	// the coarse level (per-ref rules are enforced by git's pre-receive hook in
  	// Task 4). For Task 3 the scope gate in the handler is the only check.
  	AllowReceivePack(id Identity, r repo.Repo) error
  }

  // Config configures the HTTP ingress.
  type Config struct {
  	Core    *repo.Service
  	GitPath string // path to the git binary; defaults to "git" on PATH
  }

  // Server is cairn's HTTP git host.
  type Server struct {
  	cfg     Config
  	gitPath string
  }

  // New builds a Server.
  func New(cfg Config) *Server {
  	gp := cfg.GitPath
  	if gp == "" {
  		gp = "git"
  	}
  	return &Server{cfg: cfg, gitPath: gp}
  }

  // pathRe matches /{org}/{slug}.git/<rest> capturing org, slug, rest.
  var pathRe = regexp.MustCompile(`^/([^/]+)/([^/]+)\.git(/.*)?$`)

  // Handler returns the HTTP mux.
  func (s *Server) Handler() http.Handler {
  	mux := http.NewServeMux()
  	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
  		w.Header().Set("Content-Type", "application/json")
  		_, _ = w.Write([]byte(`{"status":"ok","service":"cairn"}`))
  	})
  	mux.HandleFunc("POST /api/orgs/{org}/repos", s.handleCreateRepo)
  	// Everything else: Smart-HTTP git, matched by the .git path shape.
  	mux.HandleFunc("/", s.handleGit)
  	return mux
  }

  func (s *Server) handleCreateRepo(w http.ResponseWriter, r *http.Request) {
  	id, ok := identityFromHeaders(r)
  	if !ok {
  		httpErr(w, http.StatusUnauthorized, "missing identity")
  		return
  	}
  	org := r.PathValue("org")
  	if id.Org != org {
  		httpErr(w, http.StatusForbidden, "org mismatch")
  		return
  	}
  	if !id.HasScope("repo:write") {
  		httpErr(w, http.StatusForbidden, "missing scope repo:write")
  		return
  	}
  	var body struct {
  		Slug string `json:"slug"`
  	}
  	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Slug == "" {
  		httpErr(w, http.StatusBadRequest, "slug required")
  		return
  	}
  	rp, err := s.cfg.Core.CreateRepo(r.Context(), org, body.Slug)
  	if err != nil {
  		httpErr(w, http.StatusBadRequest, err.Error())
  		return
  	}
  	w.Header().Set("Content-Type", "application/json")
  	_ = json.NewEncoder(w).Encode(map[string]any{
  		"id": rp.ID, "org": rp.OrgID, "slug": rp.Slug, "default_branch": rp.DefaultBranch,
  	})
  }

  // handleGit routes a Smart-HTTP request: enforce scope from the trusted
  // identity, then delegate the byte protocol to `git http-backend`.
  func (s *Server) handleGit(w http.ResponseWriter, r *http.Request) {
  	m := pathRe.FindStringSubmatch(r.URL.Path)
  	if m == nil {
  		httpErr(w, http.StatusNotFound, "not a git path")
  		return
  	}
  	org, slug, rest := m[1], m[2], m[3]

  	id, ok := identityFromHeaders(r)
  	if !ok {
  		httpErr(w, http.StatusUnauthorized, "missing identity")
  		return
  	}
  	if id.Org != org {
  		httpErr(w, http.StatusForbidden, "org mismatch")
  		return
  	}

  	// Determine read vs write from the requested service/endpoint.
  	write := isWriteRequest(rest, r.URL.RawQuery)
  	need := "repo:read"
  	if write {
  		need = "repo:write"
  	}
  	if !id.HasScope(need) {
  		httpErr(w, http.StatusForbidden, "missing scope "+need)
  		return
  	}

  	rp, err := s.cfg.Core.GetRepo(r.Context(), org, slug)
  	if err != nil {
  		httpErr(w, http.StatusNotFound, "repo not found")
  		return
  	}

  	s.serveBackend(w, r, rp, org, slug)
  }

  // isWriteRequest reports whether a Smart-HTTP request mutates the repo
  // (git-receive-pack advertisement or POST).
  func isWriteRequest(rest, rawQuery string) bool {
  	if strings.HasSuffix(rest, "/git-receive-pack") {
  		return true
  	}
  	if strings.HasSuffix(rest, "/info/refs") && strings.Contains(rawQuery, "service=git-receive-pack") {
  		return true
  	}
  	return false
  }

  // serveBackend runs `git http-backend` as a CGI handler over the bare repo.
  // GIT_PROJECT_ROOT points at the repo's parent dir; PATH_INFO is /<id>.git/<rest>.
  func (s *Server) serveBackend(w http.ResponseWriter, r *http.Request, rp repo.Repo, org, slug string) {
  	root := filepath.Dir(rp.StoragePath)        // repoRoot
  	base := filepath.Base(rp.StoragePath)        // <id>.git
  	m := pathRe.FindStringSubmatch(r.URL.Path)
  	rest := ""
  	if m != nil {
  		rest = m[3]
  	}
  	h := &cgi.Handler{
  		Path: s.gitPath,
  		Args: []string{"http-backend"},
  		Dir:  root,
  		Env: []string{
  			"GIT_PROJECT_ROOT=" + root,
  			"GIT_HTTP_EXPORT_ALL=1",
  			"PATH_INFO=/" + base + rest,
  		},
  	}
  	h.ServeHTTP(w, r)
  }

  func httpErr(w http.ResponseWriter, code int, msg string) {
  	w.Header().Set("Content-Type", "application/json")
  	w.WriteHeader(code)
  	_, _ = w.Write([]byte(fmt.Sprintf(`{"error":%q}`, msg)))
  }
  ```
  > **Why `git http-backend` + CGI:** it's the reference Smart-HTTPv2 server, identical to what production git hosts use, and `net/http/cgi` is stdlib. cairn keeps full control of auth/routing (the scope gate runs before delegation) and only hands off the pack byte protocol. The container ships `git` (Task 5). The `GIT_PROJECT_ROOT`+`PATH_INFO` rewrite maps the public `/org/slug.git` URL onto the on-disk `<id>.git` directory, so storage layout is decoupled from URL.
- [ ] Run, expect pass:
  ```sh
  go -C /Users/jacinta/Source/cairn-server test ./internal/httpd/
  ```
  Expect prefix: `ok  	github.com/CarriedWorldUniverse/cairn-server/internal/httpd`. (git-dependent tests skip if `git` is absent; identity + create-repo tests still run.)
- [ ] Commit: `nex-388: smart-http ingress (git http-backend) + create-repo api, X-CWB-* trust`.

### 3.3 — Wire HTTP into `cmd/cairn-server`

- [ ] Edit `/Users/jacinta/Source/cairn-server/cmd/cairn-server/main.go`: replace the inline `/healthz`-only mux with the `httpd.Server` handler (which carries `/healthz`, the admin API, and Smart-HTTP). Import the `httpd` package; change the listen block to:
  ```go
  	httpSrv := httpd.New(httpd.Config{Core: core})
  	log.Printf("cairn http listening on %s (db=%s, repos=%s)", httpAddr, dbPath, repoRoot)
  	if err := http.ListenAndServe(httpAddr, httpSrv.Handler()); err != nil {
  		log.Fatalf("cairn: %v", err)
  	}
  ```
  Remove the now-unused inline `mux` block.
- [ ] Build + vet:
  ```sh
  go -C /Users/jacinta/Source/cairn-server build ./...
  go -C /Users/jacinta/Source/cairn-server vet ./...
  ```
  Expect: no output.
- [ ] Commit: `nex-388: serve httpd handler (smart-http + api + healthz) from cairn-server`.

**Task 3 acceptance:** `go test ./internal/httpd/` green; a real `git clone`/`push` over HTTP with injected `X-CWB-*` works; a `repo:read`-only identity is refused push; `POST /api/orgs/{org}/repos` creates a repo. No herald lookup on this path — trust is the gateway's mTLS-injected headers. Note the gateway strips its `/cairn` prefix, so the handler's `/{org}/{slug}.git/...` paths are correct as mounted.

---

## Task 4 — Minimal branch protection (subset of NEX-391)

**Commit prefix:** `nex-391:`

**Goal:** at receive-pack, enforce the **one** minimal rule: a push to the repo's **default branch** requires `repo:write` (already gated coarsely) **and force-push (non-fast-forward) to the default branch is disallowed by default**. No org-tree-axis rule engine, no bypass, no per-branch config — just the default-branch safety rule. Richer rules are NEX-391-full, out of this MVP.

**Maps to:** spec §2.5, §6.5.

### Mechanism — a `protect` package + a git pre-receive hook

git evaluates a `pre-receive` hook on the server side of every push, before any ref is updated, receiving `<old-sha> <new-sha> <ref>` lines on stdin. This is the protocol-correct place to reject a force-push without half-applying a push. cairn:
1. ships a tiny pre-receive hook into every bare repo at creation time;
2. the hook calls a small cairn subcommand (`cairn-server pre-receive`) that reads the repo's `protection` rule from the catalogue and the proposed ref updates from stdin, and rejects a non-fast-forward to the default branch.

The force-push (non-fast-forward) test: an update is a force-push iff `old-sha` is non-zero and `old-sha` is **not** an ancestor of `new-sha`. We use `git merge-base --is-ancestor` for that check.

### 4.1 — The protection decision (pure, test FIRST)

- [ ] Create `/Users/jacinta/Source/cairn-server/internal/protect/` and write the failing test `protect_test.go`:
  ```go
  package protect

  import "testing"

  func TestDefaultBranchForcePushRejected(t *testing.T) {
  	const zero = "0000000000000000000000000000000000000000"
  	cases := []struct {
  		name          string
  		defaultBranch string
  		ref           string
  		old, new      string
  		isAncestor    bool // whether old is an ancestor of new (fast-forward)
  		wantAllow     bool
  	}{
  		{"ff to default ok", "main", "refs/heads/main", "aaa", "bbb", true, true},
  		{"create default ok", "main", "refs/heads/main", zero, "bbb", false, true},
  		{"force-push default rejected", "main", "refs/heads/main", "aaa", "bbb", false, false},
  		{"force-push non-default ok", "main", "refs/heads/feature", "aaa", "bbb", false, true},
  		{"delete default rejected", "main", "refs/heads/main", "aaa", zero, false, false},
  	}
  	for _, c := range cases {
  		got := Allow(Rule{DefaultBranch: c.defaultBranch}, Update{
  			Ref: c.ref, Old: c.old, New: c.new, OldIsAncestorOfNew: c.isAncestor,
  		})
  		if (got == nil) != c.wantAllow {
  			t.Errorf("%s: Allow err=%v, wantAllow=%v", c.name, got, c.wantAllow)
  		}
  	}
  }
  ```
- [ ] Run, expect compile failure (undefined: Allow / Rule / Update).
- [ ] Write `/Users/jacinta/Source/cairn-server/internal/protect/protect.go`:
  ```go
  // Package protect is cairn's MINIMAL branch protection: the single
  // default-branch safety rule (no force-push, no delete on the default branch).
  // The richer org-tree-axis rule engine (NEX-391 full) is deferred; this is the
  // cheap floor so the core isn't a free-for-all.
  package protect

  import (
  	"fmt"
  	"strings"
  )

  const zeroSHA = "0000000000000000000000000000000000000000"

  // Rule is the minimal per-repo protection: just which branch is default.
  type Rule struct {
  	DefaultBranch string `json:"default_branch"`
  }

  // Update is one proposed ref change from a push.
  type Update struct {
  	Ref string // full ref, e.g. refs/heads/main
  	Old string // old sha (zeroSHA on create)
  	New string // new sha (zeroSHA on delete)
  	// OldIsAncestorOfNew is true when Old is an ancestor of New, i.e. the update
  	// is a fast-forward. Computed by the caller via `git merge-base --is-ancestor`.
  	OldIsAncestorOfNew bool
  }

  // Allow returns nil if the update is permitted, else an error describing the
  // rejection. The only rule: the default branch may not be force-pushed
  // (non-fast-forward) or deleted. Non-default branches are unrestricted.
  func Allow(rule Rule, u Update) error {
  	defRef := "refs/heads/" + rule.DefaultBranch
  	if u.Ref != defRef {
  		return nil // only the default branch is protected
  	}
  	if u.New == zeroSHA {
  		return fmt.Errorf("protect: refusing to delete the default branch %q", rule.DefaultBranch)
  	}
  	if u.Old == zeroSHA {
  		return nil // creating the default branch is fine
  	}
  	if !u.OldIsAncestorOfNew {
  		return fmt.Errorf("protect: non-fast-forward (force) push to the default branch %q is not allowed", rule.DefaultBranch)
  	}
  	return nil
  }

  // ParseUpdateLine parses a pre-receive stdin line "<old> <new> <ref>".
  func ParseUpdateLine(line string) (Update, error) {
  	f := strings.Fields(strings.TrimSpace(line))
  	if len(f) != 3 {
  		return Update{}, fmt.Errorf("protect: malformed update line %q", line)
  	}
  	return Update{Old: f[0], New: f[1], Ref: f[2]}, nil
  }
  ```
- [ ] Run, expect pass (`ok ` prefix). Commit: `nex-391: minimal default-branch protection decision (pure)`.

### 4.2 — The `pre-receive` subcommand + hook installation (test FIRST)

cairn-server gains a hidden subcommand: when invoked as `cairn-server pre-receive <repo-id>` it reads update lines from stdin, looks up the repo's rule, computes ancestry via `git merge-base --is-ancestor` in the bare repo, and exits non-zero (printing the reason) if any update is rejected. The hook script in each bare repo invokes it.

- [ ] Write the failing test `/Users/jacinta/Source/cairn-server/internal/protect/hook_test.go`:
  ```go
  package protect

  import "testing"

  func TestHookScriptReferencesBinary(t *testing.T) {
  	got := HookScript("/usr/local/bin/cairn-server", "repo-123")
  	if !contains(got, "/usr/local/bin/cairn-server pre-receive repo-123") {
  		t.Fatalf("hook does not invoke the binary correctly:\n%s", got)
  	}
  	if !contains(got, "#!/bin/sh") {
  		t.Fatalf("hook missing shebang:\n%s", got)
  	}
  }

  func contains(s, sub string) bool {
  	return len(s) >= len(sub) && (func() bool {
  		for i := 0; i+len(sub) <= len(s); i++ {
  			if s[i:i+len(sub)] == sub {
  				return true
  			}
  		}
  		return false
  	})()
  }
  ```
- [ ] Run, expect compile failure (undefined: HookScript).
- [ ] Add to `/Users/jacinta/Source/cairn-server/internal/protect/protect.go`:
  ```go
  // HookScript returns the pre-receive hook body for a repo. It pipes stdin
  // (the ref updates) into the cairn-server pre-receive subcommand, which holds
  // the rule logic. Installed at repo creation into <bare>/hooks/pre-receive.
  func HookScript(binaryPath, repoID string) string {
  	return "#!/bin/sh\nexec " + binaryPath + " pre-receive " + repoID + "\n"
  }
  ```
- [ ] Run, expect pass (`ok ` prefix).
- [ ] Install the hook at repo creation. Edit `/Users/jacinta/Source/cairn-server/internal/repo/service.go` — extend `CreateRepo` to write the hook. The repo package must not import `protect` (to avoid a cycle if protect ever needs repo); instead accept the hook content via a package-level installer hook the binary wires. Simpler + acyclic: give `repo.Service` an optional `HookInstaller`:
  ```go
  // HookInstaller writes server-side hooks into a freshly created bare repo's
  // hooks dir. The binary wires the pre-receive protection hook here; tests and
  // the core itself stay independent of the protect package.
  type HookInstaller func(repoID, hooksDir string) error

  // SetHookInstaller registers the hook installer used by CreateRepo. Optional;
  // if unset, no server-side hooks are written.
  func (s *Service) SetHookInstaller(h HookInstaller) { s.hookInstaller = h }
  ```
  Add the field `hookInstaller HookInstaller` to `Service`, and at the end of `CreateRepo` (after the successful INSERT), before `return r, nil`:
  ```go
  	if s.hookInstaller != nil {
  		hooksDir := filepath.Join(r.StoragePath, "hooks")
  		if err := s.hookInstaller(r.ID, hooksDir); err != nil {
  			return Repo{}, fmt.Errorf("repo.CreateRepo: install hooks: %w", err)
  		}
  	}
  ```
- [ ] Add a repo-package test that the installer is invoked, in `service_test.go`:
  ```go
  func TestCreateRepoRunsHookInstaller(t *testing.T) {
  	svc := newTestService(t)
  	var gotID, gotDir string
  	svc.SetHookInstaller(func(id, dir string) error { gotID, gotDir = id, dir; return nil })
  	r, err := svc.CreateRepo(context.Background(), "org-1", "hooked")
  	if err != nil {
  		t.Fatalf("CreateRepo: %v", err)
  	}
  	if gotID != r.ID || gotDir == "" {
  		t.Fatalf("installer not called correctly: id=%s dir=%s", gotID, gotDir)
  	}
  }
  ```
- [ ] Run both packages, expect pass:
  ```sh
  go -C /Users/jacinta/Source/cairn-server test ./internal/repo/ ./internal/protect/
  ```
  Expect `ok ` for both.
- [ ] Commit: `nex-391: pre-receive hook script + repo hook-installer seam`.

### 4.3 — The `pre-receive` subcommand in the binary + ancestry check

- [ ] Write `/Users/jacinta/Source/cairn-server/cmd/cairn-server/prereceive.go`:
  ```go
  package main

  import (
  	"bufio"
  	"context"
  	"encoding/json"
  	"fmt"
  	"os"
  	"os/exec"

  	"github.com/CarriedWorldUniverse/cairn-server/internal/protect"
  	"github.com/CarriedWorldUniverse/cairn-server/internal/repo"
  )

  // runPreReceive is invoked as `cairn-server pre-receive <repo-id>` by the
  // server-side hook. It reads update lines from stdin, loads the repo's
  // protection rule, computes fast-forward-ness via git, and exits non-zero on
  // the first rejected update (git then refuses the whole push).
  func runPreReceive(repoID string) int {
  	dbPath := env("CAIRN_DB", "/var/lib/nexus/cairn.db")
  	repoRoot := env("CAIRN_REPO_ROOT", "/var/lib/nexus/repos")
  	core, err := repo.Open(dbPath, repoRoot)
  	if err != nil {
  		fmt.Fprintln(os.Stderr, "cairn pre-receive: open core:", err)
  		return 1
  	}
  	defer core.Close()

  	ctx := context.Background()
  	r, err := core.GetRepoByID(ctx, repoID)
  	if err != nil {
  		fmt.Fprintln(os.Stderr, "cairn pre-receive: repo:", err)
  		return 1
  	}
  	var rule protect.Rule
  	if err := json.Unmarshal([]byte(r.Protection), &rule); err != nil || rule.DefaultBranch == "" {
  		rule.DefaultBranch = r.DefaultBranch // fall back to the catalogue column
  	}

  	sc := bufio.NewScanner(os.Stdin)
  	for sc.Scan() {
  		u, err := protect.ParseUpdateLine(sc.Text())
  		if err != nil {
  			fmt.Fprintln(os.Stderr, "cairn pre-receive:", err)
  			return 1
  		}
  		u.OldIsAncestorOfNew = isAncestor(r.StoragePath, u.Old, u.New)
  		if err := protect.Allow(rule, u); err != nil {
  			fmt.Fprintln(os.Stderr, "cairn:", err.Error())
  			return 1
  		}
  	}
  	return 0
  }

  // isAncestor reports whether old is an ancestor of new in the bare repo (i.e.
  // the update is a fast-forward). A zero/absent old is not an ancestor question;
  // protect.Allow handles the create/delete cases before this matters.
  func isAncestor(gitDir, old, new string) bool {
  	const zero = "0000000000000000000000000000000000000000"
  	if old == zero || new == zero {
  		return false
  	}
  	cmd := exec.Command("git", "--git-dir", gitDir, "merge-base", "--is-ancestor", old, new)
  	return cmd.Run() == nil // exit 0 => old IS an ancestor of new
  }
  ```
- [ ] Edit `/Users/jacinta/Source/cairn-server/cmd/cairn-server/main.go`: at the very top of `main()`, branch to the subcommand and wire the hook installer. Add before reading config:
  ```go
  	// Hidden subcommand invoked by the per-repo pre-receive hook.
  	if len(os.Args) >= 3 && os.Args[1] == "pre-receive" {
  		os.Exit(runPreReceive(os.Args[2]))
  	}
  ```
  and after `core` is opened, register the installer (the binary's own path is the hook target):
  ```go
  	selfPath, err := os.Executable()
  	if err != nil {
  		log.Fatalf("cairn: resolve own path: %v", err)
  	}
  	core.SetHookInstaller(func(repoID, hooksDir string) error {
  		if err := os.MkdirAll(hooksDir, 0o755); err != nil {
  			return err
  		}
  		hook := filepath.Join(hooksDir, "pre-receive")
  		if err := os.WriteFile(hook, []byte(protect.HookScript(selfPath, repoID)), 0o755); err != nil {
  			return err
  		}
  		return nil
  	})
  ```
  Add imports `"path/filepath"` and the `protect` package.
- [ ] Build + vet:
  ```sh
  go -C /Users/jacinta/Source/cairn-server build ./...
  go -C /Users/jacinta/Source/cairn-server vet ./...
  ```
  Expect: no output.
- [ ] Add an end-to-end force-push rejection test in `/Users/jacinta/Source/cairn-server/internal/sshd/server_test.go` (it exercises the whole stack: the hook is installed by the binary's installer, so this test wires a real installer mirroring the binary). Append:
  ```go
  func TestSSHForcePushToDefaultRejected(t *testing.T) {
  	if _, err := exec.LookPath("git"); err != nil {
  		t.Skip("git not on PATH")
  	}
  	ctx := context.Background()
  	dir := t.TempDir()
  	core, _ := repo.Open(filepath.Join(dir, "cairn.db"), filepath.Join(dir, "repos"))
  	t.Cleanup(func() { _ = core.Close() })

  	// Wire the same pre-receive hook the binary installs, pointing at a built
  	// cairn-server binary so the hook can shell back in.
  	bin := buildCairnBinary(t)
  	core.SetHookInstaller(func(repoID, hooksDir string) error {
  		if err := os.MkdirAll(hooksDir, 0o755); err != nil {
  			return err
  		}
  		body := "#!/bin/sh\nexec " + bin + " pre-receive " + repoID + "\n"
  		return os.WriteFile(filepath.Join(hooksDir, "pre-receive"), []byte(body), 0o755)
  	})

  	r, _ := core.CreateRepo(ctx, "org-1", "widgets")
  	keyPath, builder := writeCasketKey(t, dir, "agent-builder", "org-1", []string{"repo:read", "repo:write"})
  	agents := herald.NewFakeAgents()
  	agents.Add(builder)
  	addr, knownHosts := bootServer(t, core, agents)
  	host, port, _ := net.SplitHostPort(addr)
  	url := "ssh://git@" + host + ":" + port + "/org-1/widgets.git"

  	// Seed main with two commits, push (creates main), then rewrite history and
  	// force-push — which must be rejected.
  	work := filepath.Join(dir, "work")
  	if out, err := exec.Command("git", "init", work).CombinedOutput(); err != nil {
  		t.Fatalf("init: %v\n%s", err, out)
  	}
  	g := func(args ...string) *exec.Cmd {
  		c := exec.Command("git", append([]string{"-C", work}, args...)...)
  		c.Env = gitEnv(keyPath, knownHosts)
  		return c
  	}
  	mustRun(t, g("checkout", "-b", "main"))
  	mustRun(t, g("-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "c1"))
  	mustRun(t, g("-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "c2"))
  	mustRun(t, g("remote", "add", "origin", url))
  	mustRun(t, g("push", "origin", "main"))
  	// Set the repo's default branch protection rule explicitly to main.
  	_ = r
  	// Rewrite: drop the last commit and add a different one => non-fast-forward.
  	mustRun(t, g("reset", "--hard", "HEAD~1"))
  	mustRun(t, g("-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "c2-prime"))
  	out, err := g("push", "--force", "origin", "main").CombinedOutput()
  	if err == nil {
  		t.Fatalf("force-push to default should be rejected:\n%s", out)
  	}
  }

  func mustRun(t *testing.T, c *exec.Cmd) {
  	t.Helper()
  	if out, err := c.CombinedOutput(); err != nil {
  		t.Fatalf("%v: %v\n%s", c.Args, err, out)
  	}
  }

  // buildCairnBinary compiles cmd/cairn-server into the test's temp dir and
  // returns the path. The pre-receive hook shells back into it.
  func buildCairnBinary(t *testing.T) string {
  	t.Helper()
  	out := filepath.Join(t.TempDir(), "cairn-server")
  	c := exec.Command("go", "build", "-o", out, "github.com/CarriedWorldUniverse/cairn-server/cmd/cairn-server")
  	if o, err := c.CombinedOutput(); err != nil {
  		t.Fatalf("build cairn-server: %v\n%s", err, o)
  	}
  	return out
  }
  ```
  > The pre-receive subcommand reads `CAIRN_DB`/`CAIRN_REPO_ROOT` from the environment; the SSH session inherits the server process env, so set those in the test before `bootServer` (add `t.Setenv("CAIRN_DB", filepath.Join(dir,"cairn.db"))` and `t.Setenv("CAIRN_REPO_ROOT", filepath.Join(dir,"repos"))` at the top of this test, matching the `repo.Open` paths).
- [ ] Run, expect pass:
  ```sh
  go -C /Users/jacinta/Source/cairn-server test ./internal/sshd/ -run ForcePush
  ```
  Expect `ok ` (or skip if no `git`).
- [ ] Commit: `nex-391: pre-receive subcommand + force-push-to-default rejection (e2e)`.

**Task 4 acceptance:** `go test ./internal/protect/` green; force-push and delete of the default branch are rejected end-to-end over a real push; non-default branches and fast-forwards to the default branch are allowed. This is the minimal rule only — no rule engine, no bypass, no per-branch config.

---

## Task 5 — Containerfile + k3s manifests (NEX-392)

**Commit prefix:** `nex-392:`

**Goal:** package cairn as a CWB product on the `cwb` k3s namespace, mirroring herald/ledger: a static Go build, the SSH Ed25519 host key from a Secret, HTTP as a ClusterIP behind the gateway (mTLS hop), SSH via a LoadBalancer/NodePort (port 22 needs external reach), a PVC for repo storage, and a gateway route `/cairn`.

**Maps to:** spec §2.6, §6.6.

### Base-image note (deviation from pure-scratch, justified)

herald is a pure-`scratch` image because it's a self-contained Go binary. cairn **shells out to the `git` binary** for both pack transfer (Tasks 2, 3) and the pre-receive hook (Task 4), so it cannot run on bare `scratch`. The runtime base is therefore the smallest image that carries `git` + a shell + CA certs: `alpine` with `git` and `openssh-client` removed-of-server (we only need `git` + `/bin/sh`). The Go binary is still built `CGO_ENABLED=0` static. This is the minimal viable deviation; if a future task moves pack handling fully into go-git's native server transport (no shell-out), the image can return to scratch.

### 5.1 — Containerfile

- [ ] Write `/Users/jacinta/Source/cairn-server/cmd/cairn-server/Containerfile`:
  ```dockerfile
  # cairn container — static Go binary on a minimal alpine that carries `git`
  # (cairn shells out to git for pack transfer + the pre-receive hook).
  # Build from the cairn-server repo root:
  #   podman build -f cmd/cairn-server/Containerfile -t cairn:dev .
  #   podman save cairn:dev | sudo k3s ctr images import -
  #
  # Runtime config via env (see cmd/cairn-server/main.go):
  #   CAIRN_HTTP_ADDR, CAIRN_SSH_ADDR, CAIRN_DB, CAIRN_REPO_ROOT,
  #   CAIRN_HOST_KEY (base64 ed25519 private), HERALD_BASE_URL
  FROM docker.io/library/golang:1.26 AS build
  WORKDIR /src
  COPY go.mod go.sum ./
  RUN go mod download
  COPY . .
  RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cairn-server ./cmd/cairn-server

  FROM docker.io/library/alpine:3.20
  RUN apk add --no-cache git
  COPY --from=build /out/cairn-server /usr/local/bin/cairn-server
  EXPOSE 8100 2222
  ENTRYPOINT ["/usr/local/bin/cairn-server"]
  ```
  > `cairn-server` is installed at `/usr/local/bin/cairn-server` so the pre-receive hook's `os.Executable()` path resolves to a stable in-container location across restarts.
- [ ] Verify the build context locally (does not need k3s):
  ```sh
  go -C /Users/jacinta/Source/cairn-server build -o /tmp/cairn-server-static ./cmd/cairn-server
  ```
  Expect: no output (the static build the Containerfile performs succeeds locally).
- [ ] Commit: `nex-392: Containerfile (static build + git on alpine)`.

### 5.2 — k3s manifests

Mirror herald's `deploy/k3s/` layout: namespace, PVC, deployment, services, README. Two services because cairn has two ingresses: HTTP (ClusterIP, gateway-fronted) and SSH (LoadBalancer for external port-22 reach).

- [ ] Write `/Users/jacinta/Source/cairn-server/deploy/k3s/00-namespace.yaml`:
  ```yaml
  apiVersion: v1
  kind: Namespace
  metadata:
    name: cwb
    labels:
      name: cwb
  ```
- [ ] Write `/Users/jacinta/Source/cairn-server/deploy/k3s/10-pvc.yaml`:
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
        storage: 5Gi
  ```
- [ ] Write `/Users/jacinta/Source/cairn-server/deploy/k3s/20-deployment.yaml`:
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
      type: Recreate            # single PVC; no rolling (RWO + on-disk repos)
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
                containerPort: 8100
              - name: ssh
                containerPort: 2222
            env:
              - name: CAIRN_HTTP_ADDR
                value: ":8100"
              - name: CAIRN_SSH_ADDR
                value: ":2222"
              - name: CAIRN_DB
                value: "/var/lib/nexus/cairn.db"
              - name: CAIRN_REPO_ROOT
                value: "/var/lib/nexus/repos"
              # herald base for the SSH-path NEX-412 lookup. In-cluster ClusterIP;
              # the SSH path resolves casket fingerprints here once NEX-412 ships.
              - name: HERALD_BASE_URL
                value: "http://herald.cwb.svc:8099"
              - name: CAIRN_HOST_KEY
                valueFrom:
                  secretKeyRef:
                    name: cairn-secrets
                    key: ssh_host_key
            volumeMounts:
              - name: data
                mountPath: /var/lib/nexus
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
  ```
- [ ] Write `/Users/jacinta/Source/cairn-server/deploy/k3s/30-service-http.yaml` (ClusterIP — internal, gateway-fronted, mTLS hop):
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
        port: 8100
        targetPort: http
        protocol: TCP
  ```
- [ ] Write `/Users/jacinta/Source/cairn-server/deploy/k3s/31-service-ssh.yaml` (LoadBalancer — port 22 needs external reach; SSH is the parallel, non-gateway-fronted ingress):
  ```yaml
  apiVersion: v1
  kind: Service
  metadata:
    name: cairn-ssh
    namespace: cwb
    labels:
      app: cairn
  spec:
    # LoadBalancer so git-over-ssh clients reach port 22 externally. On a
    # single-node k3s the built-in servicelb maps it to the node IP; switch to
    # NodePort if servicelb is disabled.
    type: LoadBalancer
    selector:
      app: cairn
    ports:
      - name: ssh
        port: 22            # external git ssh port
        targetPort: ssh     # container :2222
        protocol: TCP
  ```
- [ ] Write `/Users/jacinta/Source/cairn-server/deploy/k3s/README.md`:
  ```markdown
  # cairn — k3s manifests

  Single-node k3s deploy in the `cwb` namespace. cairn has **two ingresses**:

  - **HTTP** (`svc/cairn`, ClusterIP `:8100`) — internal only; reach it through
    **interchange-gateway** at `/cairn`. The gateway runs herald verification and
    injects the trusted `X-CWB-*` identity over the mTLS hop; cairn trusts those.
  - **SSH** (`svc/cairn-ssh`, LoadBalancer `:22` → container `:2222`) — the
    parallel, gateway-bypassing git-over-ssh ingress. Authenticated by casket
    public key → herald agent (NEX-412 by-fingerprint lookup).

  ## One-time secret (SSH host key)

  cairn needs a stable Ed25519 SSH host key so its host identity survives
  restarts. Generate one and store it base64-std-encoded (64-byte private key):

  ```sh
  # generate an ed25519 private key, base64-std encode the 64-byte seed||pub form
  go run - <<'EOF'
  package main
  import ("crypto/ed25519"; "crypto/rand"; "encoding/base64"; "fmt")
  func main(){ _, priv, _ := ed25519.GenerateKey(rand.Reader); fmt.Println(base64.StdEncoding.EncodeToString(priv)) }
  EOF
  ```

  ```sh
  HOSTKEY=$(...)   # the base64 from above
  kubectl -n cwb create secret generic cairn-secrets \
    --from-literal=ssh_host_key="$HOSTKEY"
  ```

  ## Apply

  ```sh
  kubectl apply -f deploy/k3s/
  kubectl -n cwb rollout status deploy/cairn
  kubectl -n cwb get pods,svc,pvc
  ```

  ## Smoke

  ```sh
  kubectl -n cwb port-forward svc/cairn 8100:8100 &
  curl -sS http://localhost:8100/healthz       # {"status":"ok","service":"cairn"}
  ```

  ## Gateway route

  Add cairn to interchange-gateway's route table so `/cairn` proxies to
  `http://cairn.cwb.svc:8100` (prefix stripped). The gateway injects `X-CWB-*`;
  cairn trusts them. SSH does **not** go through the gateway.

  ## Notes

  - `imagePullPolicy: Never` — load via `podman save cairn:dev | sudo k3s ctr images import -`.
  - Storage is a `local-path` PVC (k3s default), single-node. Repos live under
    `/var/lib/nexus/repos`, the catalogue at `/var/lib/nexus/cairn.db`.
  - The SSH path is **live only once NEX-412 (herald by-fingerprint) is deployed**
    and `HERALD_BASE_URL` resolves it; until then SSH auth fails closed.
  ```
- [ ] Validate the YAML parses (client-side dry run; needs `kubectl`, else skip with a note):
  ```sh
  kubectl apply --dry-run=client -f /Users/jacinta/Source/cairn-server/deploy/k3s/ 2>&1 | head
  ```
  Expect: `... created (dry run)` lines, or skip if no cluster/kubectl is configured (the manifests mirror herald's validated shapes).
- [ ] Commit: `nex-392: k3s manifests (ns, pvc, deployment, http+ssh services, README)`.

### 5.3 — Gateway route registration (note, not code in this repo)

The `/cairn` route lives in interchange-gateway's config, not in cairn. Record the exact addition so the deploy step is unambiguous:

- [ ] In interchange-gateway's route table (its deploy config / `Routes` map, see `/Users/jacinta/Source/interchange/internal/gateway/gateway.go`), add:
  ```
  "/cairn" -> "http://cairn.cwb.svc:8100"
  ```
  The gateway strips `/cairn` before proxying (confirmed in `gateway.go` `match`), so cairn's `/{org}/{slug}.git/...` and `/api/...` paths line up. This is an interchange change (separate repo/PR); the cairn deploy is not complete until it lands. Note it in the cairn deploy PR as a cross-repo follow-up.

**Task 5 acceptance:** the static build succeeds; the Containerfile carries `git`; the manifests mirror herald (ns `cwb`, `local-path` PVC, `imagePullPolicy: Never`) with the two services (HTTP ClusterIP, SSH LoadBalancer) and the host-key Secret; the README documents the one-time secret, apply, smoke, and the gateway route. SSH liveness is gated on NEX-412 deploy.

---

## Task 6 — cwb-conformance cairn layer (cross-repo)

**Commit prefix:** `nex-392:` (conformance follows the deploy story key; if the conformance suite has its own key, use that — confirm with the operator)

**Goal:** add the `conformance/cairn` layer to the **separate** `cwb-conformance` module so the suite exercises cairn through its real wire interfaces: clone/push over SSH (casket key) and over HTTPS (through the gateway, bearer-token credential helper), plus the `repo:read`/`repo:write` scope matrix. This is a TRUE external client — it shares no code with cairn, drives real `git`.

**Repo:** `/Users/jacinta/Source/cwb-conformance` (module to be `github.com/CarriedWorldUniverse/cwb-conformance`). The design is already specified in `/Users/jacinta/Source/cwb-conformance/docs/2026-05-31-cwb-conformance-design.md` §5c (cairn layer) + §3/§4 (target + fixtures). This task implements the cairn layer per that design.

**Maps to:** spec §6.7; conformance design §5c.

### Live-green precondition (READ — this layer cannot pass until the platform is up)

The cairn conformance layer is a **live integration test**. It only passes green when:
1. **cairn is deployed** on dMon k3s (Task 5 applied; HTTP behind the gateway, SSH LoadBalancer reachable);
2. **NEX-412 is deployed on herald** (the SSH casket-fingerprint lookup is live) — otherwise the SSH flows fail auth;
3. the gateway has the `/cairn` route (Task 5.3).

Until then the layer is written + compiles + is wired into `cwb-conform -layers cairn`, but is **skipped with a logged reason** when the target can't reach cairn (mirroring the design's "no silent caps" rule §6). So this task is buildable now; it turns green when the platform stands up. State this in the PR.

### 6.1 — Scaffold the conformance module + harness (if not already present)

The conformance repo currently has only `docs/` + `README.md` — no Go yet. If a prior story has already scaffolded `go.mod`/`internal/`, skip to 6.2.

- [ ] Initialise the module + the harness skeleton the design §2 specifies (only the parts the cairn layer needs — `target`, `fixtures`, `wire/git`):
  ```sh
  go -C /Users/jacinta/Source/cwb-conformance mod init github.com/CarriedWorldUniverse/cwb-conformance
  mkdir -p /Users/jacinta/Source/cwb-conformance/internal/target \
           /Users/jacinta/Source/cwb-conformance/internal/fixtures \
           /Users/jacinta/Source/cwb-conformance/internal/wire \
           /Users/jacinta/Source/cwb-conformance/conformance/cairn \
           /Users/jacinta/Source/cwb-conformance/cmd/cwb-conform
  ```
  > If `target`/`fixtures` already exist from the herald/gateway layers, reuse them verbatim — do not duplicate. The `Target` struct (design §3) already carries `SSHHost`, `SSHPort`, `CairnPath`, `GatewayURL`, `AdminToken`; the `TestOrg`/`Principal` fixtures (design §4) already provision `builder` (`repo:read repo:write`) and `reader` (`repo:read`) agents with casket keys. The cairn layer consumes those.
- [ ] If scaffolding fresh, write `/Users/jacinta/Source/cwb-conformance/internal/target/target.go` exactly as design §3:
  ```go
  package target

  // Target is everything a conformance run needs to reach a CWB deployment.
  type Target struct {
  	Name       string
  	GatewayURL string
  	AdminToken string
  	SSHHost    string
  	SSHPort    int
  	RunID      string
  	OffNode    bool
  	HeraldPath string // default "/herald"
  	CairnPath  string // default "/cairn"
  	LedgerPath string // default "/ledger"
  }
  ```
- [ ] Commit (in the conformance repo): `cairn-conformance: scaffold module + target spine`.

### 6.2 — The `wire/git` helper for SSH + HTTPS git (test FIRST where it can run offline)

The design §2 confines all git I/O to `internal/wire/git.go` (shell out to real `git`). The cairn layer needs: clone/push over `ssh://` with a casket key, and clone/push over `https://` with a bearer-token credential helper.

- [ ] Write `/Users/jacinta/Source/cwb-conformance/internal/wire/git.go`:
  ```go
  // Package wire confines all real network I/O for the conformance suite:
  // shelling out to the real `git` binary (ssh:// and https://), raw HTTP, and
  // token exchange. It never imports a CWB service's internal packages — the
  // suite is a true external client.
  package wire

  import (
  	"bytes"
  	"fmt"
  	"os"
  	"os/exec"
  	"path/filepath"
  )

  // GitSSH runs a git subcommand configured to authenticate over SSH with the
  // given casket private key (PEM at keyPath) and a pinned known_hosts file.
  func GitSSH(dir, keyPath, knownHosts string, args ...string) ([]byte, error) {
  	c := exec.Command("git", args...)
  	if dir != "" {
  		c.Dir = dir
  	}
  	sshCmd := fmt.Sprintf("ssh -i %s -o IdentitiesOnly=yes -o UserKnownHostsFile=%s -o StrictHostKeyChecking=yes",
  		keyPath, knownHosts)
  	c.Env = append(os.Environ(),
  		"GIT_SSH_COMMAND="+sshCmd,
  		"GIT_TERMINAL_PROMPT=0",
  	)
  	var out bytes.Buffer
  	c.Stdout, c.Stderr = &out, &out
  	err := c.Run()
  	return out.Bytes(), err
  }

  // GitHTTPS runs a git subcommand over HTTPS through the gateway, injecting the
  // herald bearer token via a credential helper so `git` sends Authorization:
  // Bearer <token>. The gateway verifies the token and injects X-CWB-* for cairn.
  func GitHTTPS(dir, bearer string, args ...string) ([]byte, error) {
  	// The credential helper shape (design open-question §234, resolved here):
  	// a one-line `!f(){...}` helper that echoes username=x-access-token and the
  	// bearer as the password. git then sends it as HTTP Basic; the gateway also
  	// accepts `Authorization: Bearer` — so we set the bearer directly via
  	// http.extraHeader, which is the simplest un-prompted path.
  	full := append([]string{
  		"-c", "http.extraHeader=Authorization: Bearer " + bearer,
  	}, args...)
  	c := exec.Command("git", full...)
  	if dir != "" {
  		c.Dir = dir
  	}
  	c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
  	var out bytes.Buffer
  	c.Stdout, c.Stderr = &out, &out
  	err := c.Run()
  	return out.Bytes(), err
  }

  // WriteCasketKey writes a casket private key (PEM bytes) to a temp file under
  // dir and returns its path, chmod 0600 so ssh accepts it.
  func WriteCasketKey(dir string, pem []byte) (string, error) {
  	p := filepath.Join(dir, "casket")
  	if err := os.WriteFile(p, pem, 0o600); err != nil {
  		return "", fmt.Errorf("wire.WriteCasketKey: %w", err)
  	}
  	return p, nil
  }
  ```
  > **Credential-helper decision (design §234 open question, resolved):** rather than a `credential.helper` script, inject the herald bearer via `git -c http.extraHeader=Authorization: Bearer <tok>`. The gateway's `bearer()` reader (confirmed in `interchange/internal/gateway/gateway.go`) accepts exactly this header, runs herald verification, and injects `X-CWB-*` for cairn. This is un-prompted, scriptable, and matches how the suite injects identity elsewhere. (A full git `credential.helper` is documented as a follow-up if interactive `git clone https://` UX is wanted for humans.)
- [ ] Build the wire package:
  ```sh
  go -C /Users/jacinta/Source/cwb-conformance build ./internal/wire/
  ```
  Expect: no output.
- [ ] Commit: `cairn-conformance: wire/git ssh + https helpers (bearer via extraHeader)`.

### 6.3 — The cairn conformance layer: flows + repo-scope matrix

Implements design §5c. The layer takes `(*target.Target, *fixtures.TestOrg)`; it is a Go test package that the runner invokes. It runs against the live target and skips (logged) when cairn is unreachable.

- [ ] Write `/Users/jacinta/Source/cwb-conformance/conformance/cairn/cairn_test.go`:
  ```go
  // Package cairn is the cwb-conformance layer for the cairn git host. It is a
  // true external client: real `git` over ssh:// (casket key) and https://
  // (through the gateway, bearer token), exercising the repo:read/repo:write
  // scope matrix. Live-green precondition: cairn deployed + NEX-412 live +
  // gateway /cairn route. When cairn is unreachable the layer skips with a
  // logged reason (no silent caps).
  package cairn

  import (
  	"fmt"
  	"net/http"
  	"os"
  	"path/filepath"
  	"strings"
  	"testing"

  	"github.com/CarriedWorldUniverse/cwb-conformance/internal/fixtures"
  	"github.com/CarriedWorldUniverse/cwb-conformance/internal/target"
  	"github.com/CarriedWorldUniverse/cwb-conformance/internal/wire"
  )

  // Run is the layer entry point invoked by cmd/cwb-conform. (If the suite uses
  // plain `go test` per-package instead, this is TestCairn with the target/org
  // resolved from the environment via the shared harness — match the existing
  // herald/gateway layer convention.)
  func Run(t *testing.T, tgt *target.Target, org *fixtures.TestOrg) {
  	if !reachable(tgt) {
  		t.Skipf("cairn layer skipped: cairn HTTP not reachable at %s%s (deploy + NEX-412 + /cairn route required)",
  			tgt.GatewayURL, cairnPath(tgt))
  	}
  	builder := org.Agents["builder"] // repo:read repo:write
  	reader := org.Agents["reader"]   // repo:read

  	// --- Flow: create a repo via the gateway-fronted admin API ---
  	slug := "conf-" + tgt.RunID
  	createRepo(t, tgt, builder.Token, org.OrgID, slug)

  	dir := t.TempDir()

  	// --- Flow: SSH clone (casket) + push a feature branch (builder) ---
  	t.Run("ssh-clone-push-builder", func(t *testing.T) {
  		keyPath, err := wire.WriteCasketKey(t.TempDir(), []byte(builder.PrivB64)) // PrivB64 holds PEM in fixtures
  		if err != nil {
  			t.Fatal(err)
  		}
  		known := knownHostsFor(t, tgt)
  		sshURL := fmt.Sprintf("ssh://git@%s:%d/%s/%s.git", tgt.SSHHost, tgt.SSHPort, org.OrgID, slug)
  		work := filepath.Join(dir, "ssh-work")
  		if out, err := wire.GitSSH("", keyPath, known, "clone", sshURL, work); err != nil {
  			t.Fatalf("ssh clone: %v\n%s", err, out)
  		}
  		seedAndPush(t, work, func(args ...string) ([]byte, error) {
  			return wire.GitSSH(work, keyPath, known, args...)
  		}, "feature-ssh")
  	})

  	// --- Flow: HTTPS clone + push (builder, bearer through the gateway) ---
  	t.Run("https-clone-push-builder", func(t *testing.T) {
  		httpsURL := fmt.Sprintf("%s%s/%s/%s.git", tgt.GatewayURL, cairnPath(tgt), org.OrgID, slug)
  		work := filepath.Join(dir, "https-work")
  		if out, err := wire.GitHTTPS("", builder.Token, "clone", httpsURL, work); err != nil {
  			t.Fatalf("https clone: %v\n%s", err, out)
  		}
  		seedAndPush(t, work, func(args ...string) ([]byte, error) {
  			return wire.GitHTTPS(work, builder.Token, args...)
  		}, "feature-https")
  	})

  	// --- Matrix: repo-scope ---
  	t.Run("reader-can-clone", func(t *testing.T) {
  		keyPath, _ := wire.WriteCasketKey(t.TempDir(), []byte(reader.PrivB64))
  		known := knownHostsFor(t, tgt)
  		sshURL := fmt.Sprintf("ssh://git@%s:%d/%s/%s.git", tgt.SSHHost, tgt.SSHPort, org.OrgID, slug)
  		if out, err := wire.GitSSH("", keyPath, known, "clone", sshURL, filepath.Join(dir, "reader-clone")); err != nil {
  			t.Fatalf("reader should clone (repo:read): %v\n%s", err, out)
  		}
  	})

  	t.Run("reader-cannot-push", func(t *testing.T) {
  		work := filepath.Join(dir, "reader-clone")
  		_, _ = wire.GitSSH(work, "", "", "-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "x")
  		keyPath, _ := wire.WriteCasketKey(t.TempDir(), []byte(reader.PrivB64))
  		known := knownHostsFor(t, tgt)
  		if out, err := wire.GitSSH(work, keyPath, known, "push", "origin", "HEAD:refs/heads/reader-nope"); err == nil {
  			t.Fatalf("reader push should fail (no repo:write):\n%s", out)
  		}
  	})

  	t.Run("force-push-default-rejected", func(t *testing.T) {
  		// builder pushes main, rewrites, force-pushes -> must be rejected.
  		keyPath, _ := wire.WriteCasketKey(t.TempDir(), []byte(builder.PrivB64))
  		known := knownHostsFor(t, tgt)
  		sshURL := fmt.Sprintf("ssh://git@%s:%d/%s/%s.git", tgt.SSHHost, tgt.SSHPort, org.OrgID, slug)
  		work := filepath.Join(dir, "force-work")
  		if out, err := wire.GitSSH("", keyPath, known, "clone", sshURL, work); err != nil {
  			t.Fatalf("clone: %v\n%s", err, out)
  		}
  		git := func(args ...string) ([]byte, error) { return wire.GitSSH(work, keyPath, known, args...) }
  		mustWire(t, git, "checkout", "-B", "main")
  		mustWire(t, git, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "c1")
  		mustWire(t, git, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "c2")
  		mustWire(t, git, "push", "origin", "main")
  		mustWire(t, git, "reset", "--hard", "HEAD~1")
  		mustWire(t, git, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "c2p")
  		if out, err := git("push", "--force", "origin", "main"); err == nil {
  			t.Fatalf("force-push to default should be rejected:\n%s", out)
  		}
  	})
  }

  func cairnPath(t *target.Target) string {
  	if t.CairnPath != "" {
  		return t.CairnPath
  	}
  	return "/cairn"
  }

  // reachable probes cairn's healthz through the gateway.
  func reachable(t *target.Target) bool {
  	resp, err := http.Get(t.GatewayURL + cairnPath(t) + "/healthz")
  	if err != nil {
  		return false
  	}
  	defer resp.Body.Close()
  	return resp.StatusCode == http.StatusOK
  }

  // createRepo calls the gateway-fronted admin API with the builder's bearer.
  func createRepo(t *testing.T, tgt *target.Target, bearer, org, slug string) {
  	t.Helper()
  	url := tgt.GatewayURL + cairnPath(tgt) + "/api/orgs/" + org + "/repos"
  	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"slug":"`+slug+`"}`))
  	if err != nil {
  		t.Fatal(err)
  	}
  	req.Header.Set("Authorization", "Bearer "+bearer)
  	req.Header.Set("Content-Type", "application/json")
  	resp, err := http.DefaultClient.Do(req)
  	if err != nil {
  		t.Fatalf("create repo: %v", err)
  	}
  	defer resp.Body.Close()
  	if resp.StatusCode != http.StatusOK {
  		t.Fatalf("create repo status = %d, want 200", resp.StatusCode)
  	}
  }

  // seedAndPush commits a file and pushes branch via the supplied git runner.
  func seedAndPush(t *testing.T, work string, git func(...string) ([]byte, error), branch string) {
  	t.Helper()
  	if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte("hi"), 0o644); err != nil {
  		t.Fatal(err)
  	}
  	mustWire(t, git, "checkout", "-b", branch)
  	mustWire(t, git, "add", ".")
  	mustWire(t, git, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "x")
  	mustWire(t, git, "push", "origin", branch)
  }

  func mustWire(t *testing.T, git func(...string) ([]byte, error), args ...string) {
  	t.Helper()
  	if out, err := git(args...); err != nil {
  		t.Fatalf("git %v: %v\n%s", args, err, out)
  	}
  }

  // knownHostsFor fetches/derives cairn's SSH host key into a known_hosts file
  // and pins it. For the MVP the suite uses ssh-keyscan against the live SSH
  // endpoint (TOFU acceptable for a conformance run); production hardening pins
  // the deployed host key from the cairn-secrets Secret out of band.
  func knownHostsFor(t *testing.T, tgt *target.Target) string {
  	t.Helper()
  	kh := filepath.Join(t.TempDir(), "known_hosts")
  	out, err := wire.SSHKeyscan(tgt.SSHHost, tgt.SSHPort)
  	if err != nil {
  		t.Fatalf("ssh-keyscan %s:%d: %v", tgt.SSHHost, tgt.SSHPort, err)
  	}
  	if err := os.WriteFile(kh, out, 0o600); err != nil {
  		t.Fatal(err)
  	}
  	return kh
  }
  ```
- [ ] Add the `SSHKeyscan` helper to `/Users/jacinta/Source/cwb-conformance/internal/wire/git.go`:
  ```go
  // SSHKeyscan returns ssh-keyscan output for host:port (a known_hosts line).
  // The conformance suite pins this for StrictHostKeyChecking on the run.
  func SSHKeyscan(host string, port int) ([]byte, error) {
  	c := exec.Command("ssh-keyscan", "-p", fmt.Sprintf("%d", port), host)
  	out, err := c.Output()
  	if err != nil {
  		return nil, fmt.Errorf("wire.SSHKeyscan: %w", err)
  	}
  	return out, nil
  }
  ```
- [ ] Build the layer (it must compile against the harness; it will not RUN green until the platform is live):
  ```sh
  go -C /Users/jacinta/Source/cwb-conformance build ./conformance/cairn/ ./internal/...
  go -C /Users/jacinta/Source/cwb-conformance vet ./conformance/cairn/ ./internal/...
  ```
  Expect: no output. (If `fixtures.Principal.PrivB64` does not yet hold PEM-format key bytes, align with the fixtures package owner — the design §4 stores casket private keys; this layer needs them in a form `ssh -i` accepts, i.e. OpenSSH PEM. Note this as the one fixtures contract the cairn layer depends on.)
- [ ] Register the layer in `/Users/jacinta/Source/cwb-conformance/cmd/cwb-conform/main.go`'s layer table so `-layers cairn` and `-layers all` invoke `cairn.Run` (match the existing herald/gateway registration pattern; if the suite uses per-package `go test` selection instead, ensure the package is included in the `all` set).
- [ ] Commit: `cairn-conformance: cairn layer — ssh+https clone/push + repo-scope matrix`.

### 6.4 — Document the live-green run

- [ ] Append a short note to `/Users/jacinta/Source/cwb-conformance/README.md` (or the design doc's run section) recording the cairn layer's precondition and how to run it once the platform is up:
  ```markdown
  ## Running the cairn layer (live precondition)

  The `cairn` layer needs the platform live:
  - cairn deployed on the target k3s (HTTP behind the gateway, SSH LoadBalancer
    reachable on the target's SSHHost:SSHPort);
  - herald NEX-412 (`GET /api/agents/by-fingerprint/{fp}`) deployed — the SSH
    casket-fingerprint lookup;
  - the gateway has the `/cairn` route.

  Run once those hold:

  ```sh
  CWB_ADMIN_TOKEN=... cwb-conform -target dmon -layers cairn
  ```

  When cairn is unreachable the layer skips with a logged reason — never a silent pass.
  ```
- [ ] Commit: `cairn-conformance: document cairn layer live-green precondition`.

**Task 6 acceptance:** the `conformance/cairn` layer compiles + vets in the cwb-conformance module, is registered in the runner, exercises clone/push over SSH (casket) + HTTPS (gateway bearer) and the `reader`-can-clone / `reader`-cannot-push / force-push-default-rejected matrix, and skips-with-reason when cairn is unreachable. **Green is gated on the live precondition** (deployed cairn + NEX-412 + `/cairn` route).

---

## Spec coverage map

Every MVP spec §2 scope item and §6 build-sequence step maps to a task:

| Spec item | Task |
|---|---|
| §2.1 Repo + ref core (go-git) + SQLite meta | Task 1 |
| §2.2 SSH ingress (casket identity, NEX-412 lookup, pack dispatch) | Task 2 |
| §2.3 HTTP ingress (Smart-HTTP, gateway-fronted, X-CWB-* trust) | Task 3 |
| §2.4 herald identity + scopes (`repo:read`/`repo:write`, org) | Tasks 2 (SSH) + 3 (HTTP) |
| §2.5 Minimal branch protection (default-branch, no force-push) | Task 4 |
| §2.6 Deploy as a CWB product (Containerfile + k3s) | Task 5 |
| §2.7 TLS everywhere (SSH self-encrypt; HTTP mTLS hop) | Tasks 2 (SSH transport) + 5 (ClusterIP/mTLS, gateway route) |
| §6.2 Repo+ref core + `cmd/cairn-server` skeleton | Task 1 |
| §6.3 SSH ingress | Task 2 |
| §6.4 HTTP ingress | Task 3 |
| §6.5 Minimal branch protection at receive-pack | Task 4 |
| §6.6 k3s deploy | Task 5 |
| §6.7 cwb-conformance cairn layer | Task 6 |

Spec §8 open questions resolved in this plan:
- **SSH lib:** `gliderlabs/ssh` (Task 2.6) — pinned.
- **fingerprint→agent caching:** 30s positive-only TTL + explicit `Invalidate` block-hook (Task 2.2).
- **Module layout:** fresh standalone module `github.com/CarriedWorldUniverse/cairn-server` (module-layout decision section) — sidesteps the Forgejo go.mod.
- **HTTP credential-helper shape:** `git -c http.extraHeader=Authorization: Bearer <tok>` through the gateway (Task 6.2) — the gateway's bearer reader accepts it.
- **Exact `repo:*` scope strings:** `repo:read`, `repo:write` (verified-facts section; herald schema uses `repo:write` as its example).

Explicitly OUT (spec §7, not in any task): web UI (NEX-389), PR-as-ledger-issue (NEX-390), delayed-public-projection, richer branch protection / org-tree axes (NEX-391 full), trust tiers, public-read.

## Definition of Done (spec §6 DoD)

- An aspect, on its casket identity, clones a cairn repo over SSH and pushes a branch (herald-authed via the NEX-412 fingerprint lookup). — Tasks 2 + 6
- A human/tool clones over HTTPS through the gateway (`X-CWB-*`). — Tasks 3 + 6
- Both herald-scoped (`repo:read`/`repo:write`). — Tasks 2, 3, 6 matrix
- Default-branch force-push rejected. — Task 4
- Deployed on k3s. — Task 5
- The cwb-conformance cairn layer exercises it green (once the live precondition holds). — Task 6
- No PR / projection / UI. — out of scope, confirmed.

## Open issues / assumptions for the operator

1. **Module home (biggest assumption).** The plan creates a **new** module `github.com/CarriedWorldUniverse/cairn-server` at `/Users/jacinta/Source/cairn-server` rather than editing the existing Forgejo fork at `/Users/jacinta/Source/cairn`. The existing tree's go.mod (full Gitea graph + a `gliderlabs/ssh` replace) is incompatible with a clean walking skeleton. If the operator wants the code inside the existing cairn repo instead, it must be a fresh module path on a clean branch (e.g. `.../cairn/server/`), not an edit of the Forgejo module — the task content is otherwise unchanged.
2. **The referenced `2026-05-31-cairn-native-plan.md` (NEX-386..392, 7-story) does not exist** in the cairn repo (only 2026-05-09/10/11 plans for the Forgejo-era cairn). Task content was therefore self-derived from the MVP spec + the live herald/interchange/ledger reference repos + the conformance design, not reused from a native plan. The NEX keys are taken from the task brief.
3. **NEX-412 is handled as buildable-now-with-fake** (Task 2): the SSH path depends only on the `herald.HeraldAgents` interface; a `FakeAgents` backs all tests; the real `HeraldClient` calls NEX-412 and is unit-tested against an httptest stub of its contract. **SSH auth goes live only when NEX-412 is deployed** and `HERALD_BASE_URL` points at it — a config flip, no code change. herald already has the domain method (`GetAgentByFingerprint`), so NEX-412 is a thin HTTP exposure.
4. **`git` shell-out for pack transfer** (Tasks 2, 3) and the pre-receive hook (Task 4): chosen over go-git's native server transport for protocol exactness. Consequence: the runtime image carries `git` (Task 5 deviates from herald's pure-scratch to a minimal alpine+git). A later task could move to go-git's `transport/server` and return to scratch.
5. **Conformance fixtures contract** (Task 6): the cairn layer needs `fixtures.Principal.PrivB64` to hold an OpenSSH-PEM casket private key (what `ssh -i` accepts). If the existing fixtures store keys in a different encoding, that one field's format must be aligned with the fixtures owner. The `target.Target` already carries `SSHHost`/`SSHPort`/`CairnPath` (design §3).
6. **Conformance commit-prefix / story key:** Task 6 lands in a different repo (`cwb-conformance`); confirm whether it tracks under NEX-392, a conformance-specific key, or NEX-384's children.
7. **Gateway `/cairn` route** (Task 5.3) is an interchange-gateway change in a separate repo; the cairn deploy is not end-to-end complete until it lands.
