# Cairn Registration API Implementation Plan (Plan 2a)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Cairn server exposes the agent registration / lookup / approval / blocking REST API at `/api/cairn/v1/agents/...`, with auto-approve when the proposer is the proposed owner. After this plan: any HTTP client (curl, the future CLI, the future MCP server) can register and manage agents.

**Architecture:** Three layers. (1) `models/cairn/` (already built in Plan 1) — schema and stores. (2) `services/cairn/identity/agent_service.go` — orchestration (fingerprint with instance HMAC, owner resolution, auto-approve gate). (3) `routers/api/cairn/v1/agents.go` — HTTP handlers using Forgejo's existing auth middleware and routing primitives. Tests are layered: store tests (Plan 1, in-memory SQLite), service tests (mock store), handler tests (httptest with the service).

**Tech Stack:** Go 1.25+, Forgejo v15.0.1, xorm, SQLite, Forgejo's existing routing (chi-style mux), Forgejo's auth middleware, `net/http`, `httptest`.

**Spec ref:** [`docs/cairn/specs/2026-05-09-cairn-foundation-design.md`](../specs/2026-05-09-cairn-foundation-design.md), §4.3 (API surface), §6 (registration flow), §10 (config flags).

**Plan 1 dependencies (already on `cairn`):** `Agent`, `AgentBlocklist`, `AgentStatus*` consts, `AgentStore` + `xormAgentStore`, `AgentBlocklistStore` + `xormBlocklistStore`, `Fingerprint`, `ParseAgentEmail`, `LoadInstanceHMACKey`, `VerifyCommitSignature`, `ErrAgentNotFound`, `ErrAgentExists` (defined but not yet wired — Task 1 of this plan).

**Known cross-cutting follow-ups carried in from Plan 1's final review (task #18 in tracker):** ErrAgentExists wiring; shared test-engine helper. Both land in this plan's Task 1.

---

## Task 1: Pre-Plan-2 prep — ErrAgentExists wiring + shared test-engine helper

**Files:**
- Create: `models/cairn/cairntest/engine.go`
- Modify: `services/cairn/identity/xorm_store.go` (Register method — translate unique-constraint errors)
- Modify: `services/cairn/identity/xorm_store_test.go` (use the new helper, add new error-mapping test)
- Modify: `services/cairn/identity/xorm_blocklist_store_test.go` (use the new helper)

**Why:** Plan 1's final code review flagged two issues. (1) `ErrAgentExists` is exported but never returned — the API handler in this plan can't map duplicates to HTTP 409 without it. (2) Each test file calls `xorm.NewEngine(":memory:")` + `eng.SetMapper(names.GonicMapper{})` + `V500CreateAgentTables`. Future test files will silently break if they forget the GonicMapper line — extract a single helper before any third test file lands.

- [ ] **Step 1: New feature branch**

```bash
cd ~/Source/cairn
git checkout cairn
git pull
git checkout -b cairn-prep-erragentexists-and-helper
git config user.name "nexus-cw"
git config user.email "nexus@darksoft.co.nz"
```

- [ ] **Step 2: Create `models/cairn/cairntest/engine.go`**

```go
// Package cairntest provides shared test fixtures for Cairn-side tests.
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package cairntest

import (
	"testing"

	cairnmigrations "github.com/CarriedWorldUniverse/cairn/models/cairn/migrations"
	"xorm.io/xorm"
	"xorm.io/xorm/names"

	_ "github.com/mattn/go-sqlite3"
)

// NewEngine returns an in-memory SQLite engine with the GonicMapper
// configured (matching production at models/db/engine.go) and Cairn's
// V500 migration applied.
//
// Engines returned from NewEngine are isolated per test (`:memory:`),
// closed automatically via t.Cleanup. Tests should not share engines.
func NewEngine(t *testing.T) *xorm.Engine {
	t.Helper()
	eng, err := xorm.NewEngine("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	eng.SetMapper(names.GonicMapper{})
	if err := cairnmigrations.V500CreateAgentTables(eng); err != nil {
		eng.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}
```

- [ ] **Step 3: Migrate `xorm_store_test.go` to use the helper**

Find the existing `newTestEngine` helper (top of `services/cairn/identity/xorm_store_test.go`). Replace ALL test calls of `newTestEngine(t)` with `cairntest.NewEngine(t)`. Delete the old `newTestEngine` function (it's superseded). Remove any `defer eng.Close()` lines (the helper handles cleanup via `t.Cleanup`).

Add the import:

```go
"github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
```

Drop the import for `xorm.io/xorm/names` and `_ "github.com/mattn/go-sqlite3"` if no longer used in this file.

- [ ] **Step 4: Migrate `xorm_blocklist_store_test.go` similarly**

Same change pattern. Replace `newTestEngine(t)` with `cairntest.NewEngine(t)`. Add the same import.

- [ ] **Step 5: Run tests, expect PASS**

```bash
go test ./services/cairn/identity/... ./models/cairn/...
```

Expected: all tests pass with the helper change. (Pure refactor — no behaviour change yet.)

- [ ] **Step 6: Write the failing test for ErrAgentExists in `services/cairn/identity/xorm_store_test.go`**

Append at the end of the file:

```go
func TestXormAgentStore_DuplicateRegisterReturnsErrAgentExists(t *testing.T) {
	eng := cairntest.NewEngine(t)
	s := NewXormAgentStore(eng)

	ctx := context.Background()
	a := &cairn.Agent{
		Fingerprint: "cairn:dup-test",
		UserID:      1,
		Slug:        "plumb",
		Domain:      "darksoft.co.nz",
		PublicKey:   []byte{1},
		Status:      cairn.AgentStatusActive,
		CreatedAt:   time.Now(),
	}
	if err := s.Register(ctx, a); err != nil {
		t.Fatal(err)
	}

	// Same (user_id, slug) — should return ErrAgentExists.
	a2 := *a
	a2.ID = 0
	a2.Fingerprint = "cairn:dup-test-2"
	err := s.Register(ctx, &a2)
	if err == nil {
		t.Fatal("expected error registering duplicate (user_id, slug)")
	}
	if !errors.Is(err, ErrAgentExists) {
		t.Errorf("err = %v, want ErrAgentExists", err)
	}

	// Same fingerprint — should also return ErrAgentExists.
	a3 := *a
	a3.ID = 0
	a3.UserID = 99
	a3.Slug = "different"
	// keep a3.Fingerprint == a.Fingerprint (the duplicate)
	err = s.Register(ctx, &a3)
	if err == nil {
		t.Fatal("expected error registering duplicate fingerprint")
	}
	if !errors.Is(err, ErrAgentExists) {
		t.Errorf("err = %v, want ErrAgentExists", err)
	}
}
```

Add the `"errors"` import if not already present.

- [ ] **Step 7: Run, expect failure**

```bash
go test ./services/cairn/identity/ -run TestXormAgentStore_DuplicateRegisterReturnsErrAgentExists -v
```

Expected: FAIL with `err = ... (raw SQLite UNIQUE constraint error), want ErrAgentExists`.

- [ ] **Step 8: Update `Register` in `services/cairn/identity/xorm_store.go` to translate the error**

Replace the `Register` method:

```go
func (s *xormAgentStore) Register(ctx context.Context, a *cairn.Agent) error {
	sess := s.engine.NewSession()
	defer sess.Close()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}
	if _, err := sess.Context(ctx).Insert(a); err != nil {
		if isUniqueViolation(err) {
			return ErrAgentExists
		}
		return err
	}
	return nil
}

// isUniqueViolation reports whether err is a database-driver unique-
// constraint error. Recognises SQLite (modernc/mattn) and Postgres
// shapes; returns false for unknown drivers (caller will see the raw
// error).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// SQLite: "UNIQUE constraint failed: ..." (mattn) or
	//        "constraint failed: UNIQUE ..." (modernc).
	// Postgres: pq driver wraps PG SQLSTATE 23505 in messages
	//           containing "duplicate key value".
	switch {
	case strings.Contains(msg, "UNIQUE constraint failed"):
		return true
	case strings.Contains(msg, "constraint failed: UNIQUE"):
		return true
	case strings.Contains(msg, "duplicate key value"):
		return true
	}
	return false
}
```

Add the `"strings"` import if not already present.

- [ ] **Step 9: Run all tests, expect PASS**

```bash
go test ./services/cairn/identity/...
```

Expected: all tests pass including the new `TestXormAgentStore_DuplicateRegisterReturnsErrAgentExists`.

- [ ] **Step 10: Commit**

```bash
git add models/cairn/cairntest/ services/cairn/identity/
git commit -m "$(cat <<'EOF'
refactor(cairn): extract cairntest.NewEngine + wire ErrAgentExists

Pre-Plan-2 prep landing two follow-ups from Plan 1's final review:

1. cairntest.NewEngine(t) — shared test-engine helper at
   models/cairn/cairntest/. Wraps xorm in-memory SQLite + GonicMapper
   + V500 migration + t.Cleanup. Future test files use this single
   path instead of redefining the setup (which previously risked
   forgetting GonicMapper and producing wrong column names).

2. ErrAgentExists is now actually returned from xormAgentStore.Register
   when the underlying driver reports a unique-constraint violation.
   Recognises SQLite (mattn + modernc shapes) and Postgres SQLSTATE
   23505. The Plan 2 API handler can now map duplicate (user_id, slug)
   or duplicate fingerprint to HTTP 409 portably across backends.

xorm_store_test.go and xorm_blocklist_store_test.go migrated to the
new helper. Old newTestEngine helper deleted.

Refs: docs/cairn/specs/2026-05-09-cairn-foundation-design.md §6
      docs/cairn/plans/2026-05-09-cairn-foundation-identity-layer.md
      task #18 in project tracker

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
git push -u origin cairn-prep-erragentexists-and-helper
```

Then merge to `cairn`:

```bash
git checkout cairn
git pull
git merge cairn-prep-erragentexists-and-helper
git push origin cairn
```

If push to `cairn` is blocked by sandbox, the controller will complete the merge.

---

## Task 2: AgentService — orchestration layer

**Files:**
- Create: `services/cairn/identity/agent_service.go`
- Create: `services/cairn/identity/agent_service_test.go`

**Why:** Spec §6's registration flow has logic that doesn't belong in the HTTP handler (which should only handle parsing, status codes, JSON marshalling) or in the store (which should only do data access). The service owns: computing the fingerprint with the instance HMAC key, deciding `auto-approve` vs `pending` based on whether the requester is the proposed owner, validating that proposed_owner exists, and the approve/block transitions.

The service depends on `AgentStore` and `AgentBlocklistStore` via interfaces — testable in isolation with table-driven tests using fake stores or `cairntest.NewEngine(t)` with real ones.

- [ ] **Step 1: New feature branch**

```bash
cd ~/Source/cairn
git checkout cairn
git pull
git checkout -b cairn-agent-service
git config user.name "nexus-cw"
git config user.email "nexus@darksoft.co.nz"
```

- [ ] **Step 2: Define the `UserResolver` interface and `RegisterRequest` shape**

Forgejo's user model lives in `models/user/`. Cairn needs to look up users by username (to resolve `proposed_owner`) but shouldn't depend on Forgejo's full user package directly — that creates rebase pain. We define a small interface that the API layer implements with a Forgejo-flavoured concrete type.

Write the failing test in `services/cairn/identity/agent_service_test.go`:

```go
package identity

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
)

// fakeUserResolver implements UserResolver for tests.
type fakeUserResolver struct {
	usernameToID map[string]int64
}

func (f *fakeUserResolver) UserIDByUsername(ctx context.Context, name string) (int64, error) {
	id, ok := f.usernameToID[name]
	if !ok {
		return 0, ErrUserNotFound
	}
	return id, nil
}

func (f *fakeUserResolver) UsernameByID(ctx context.Context, id int64) (string, error) {
	for name, uid := range f.usernameToID {
		if uid == id {
			return name, nil
		}
	}
	return "", ErrUserNotFound
}

const testHMACKey = "0123456789abcdef0123456789abcdef" // 32 bytes

func newTestService(t *testing.T) (*AgentService, *fakeUserResolver) {
	t.Helper()
	eng := cairntest.NewEngine(t)
	store := NewXormAgentStore(eng)
	blocklist := NewXormBlocklistStore(eng)
	users := &fakeUserResolver{
		usernameToID: map[string]int64{
			"alice": 1,
			"bob":     2,
		},
	}
	svc := NewAgentService([]byte(testHMACKey), store, blocklist, users)
	return svc, users
}

func TestAgentService_Register_AutoApproveWhenProposerIsOwner(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	got, err := svc.Register(ctx, RegisterRequest{
		ProposedOwner: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKey:     pub,
	}, &Caller{UserID: 1, Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}

	if got.Status != cairn.AgentStatusActive {
		t.Errorf("status = %q, want %q (auto-approve when proposer == owner)", got.Status, cairn.AgentStatusActive)
	}
	if got.ActivatedAt == nil || got.ActivatedAt.IsZero() {
		t.Error("ActivatedAt not set on auto-approved agent")
	}
	if got.Fingerprint == "" {
		t.Error("Fingerprint not populated")
	}
}

func TestAgentService_Register_PendingWhenProposerIsNotOwner(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	// Bob proposes a Plumb-as-alice-agent — not auto-approved.
	got, err := svc.Register(ctx, RegisterRequest{
		ProposedOwner: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKey:     pub,
	}, &Caller{UserID: 2, Username: "bob"})
	if err != nil {
		t.Fatal(err)
	}

	if got.Status != cairn.AgentStatusPending {
		t.Errorf("status = %q, want %q (pending when proposer != owner)", got.Status, cairn.AgentStatusPending)
	}
	if got.ActivatedAt != nil {
		t.Error("ActivatedAt should be nil for pending agents")
	}
}

func TestAgentService_Register_PendingWhenAnonymous(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	// No caller (anonymous proposal — agent on a remote machine).
	got, err := svc.Register(ctx, RegisterRequest{
		ProposedOwner: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKey:     pub,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if got.Status != cairn.AgentStatusPending {
		t.Errorf("status = %q, want %q (anonymous proposal is pending)", got.Status, cairn.AgentStatusPending)
	}
}

func TestAgentService_Register_RejectsUnknownOwner(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	_, err := svc.Register(ctx, RegisterRequest{
		ProposedOwner: "no-such-user",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKey:     pub,
	}, nil)
	if err == nil {
		t.Fatal("expected error for unknown owner")
	}
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("err = %v, want ErrUserNotFound", err)
	}
}

func TestAgentService_Register_RejectsDuplicate(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	req := RegisterRequest{
		ProposedOwner: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKey:     pub,
	}
	caller := &Caller{UserID: 1, Username: "alice"}

	if _, err := svc.Register(ctx, req, caller); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Register(ctx, req, caller)
	if err == nil {
		t.Fatal("expected duplicate error")
	}
	if !errors.Is(err, ErrAgentExists) {
		t.Errorf("err = %v, want ErrAgentExists", err)
	}
}

func TestAgentService_Register_FingerprintIsDeterministic(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	got1, err := svc.Register(ctx, RegisterRequest{
		ProposedOwner: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKey:     pub,
	}, &Caller{UserID: 1, Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}

	// Compute the expected fingerprint independently and verify match.
	want := Fingerprint([]byte(testHMACKey), pub)
	if got1.Fingerprint != want {
		t.Errorf("Fingerprint = %q, want %q", got1.Fingerprint, want)
	}
}

func TestAgentService_Approve_TransitionsPendingToActive(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	// Anonymous proposal -> pending.
	pending, err := svc.Register(ctx, RegisterRequest{
		ProposedOwner: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKey:     pub,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Owner approves.
	if err := svc.Approve(ctx, pending.Fingerprint, &Caller{UserID: 1, Username: "alice"}); err != nil {
		t.Fatal(err)
	}

	got, err := svc.GetByFingerprint(ctx, pending.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != cairn.AgentStatusActive {
		t.Errorf("status = %q, want active", got.Status)
	}
}

func TestAgentService_Approve_RejectsNonOwner(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	pending, _ := svc.Register(ctx, RegisterRequest{
		ProposedOwner: "alice", Slug: "plumb", Domain: "darksoft.co.nz", PublicKey: pub,
	}, nil)

	err := svc.Approve(ctx, pending.Fingerprint, &Caller{UserID: 2, Username: "bob"})
	if err == nil {
		t.Fatal("expected error when non-owner approves")
	}
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("err = %v, want ErrForbidden", err)
	}
}

func TestAgentService_Block_RejectsNonOwner(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	a, _ := svc.Register(ctx, RegisterRequest{
		ProposedOwner: "alice", Slug: "plumb", Domain: "darksoft.co.nz", PublicKey: pub,
	}, &Caller{UserID: 1, Username: "alice"})

	err := svc.Block(ctx, a.Fingerprint, "test", &Caller{UserID: 2, Username: "bob"})
	if err == nil {
		t.Fatal("expected error when non-owner blocks")
	}
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("err = %v, want ErrForbidden", err)
	}
}

func TestAgentService_Block_OwnerCanBlock(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	a, _ := svc.Register(ctx, RegisterRequest{
		ProposedOwner: "alice", Slug: "plumb", Domain: "darksoft.co.nz", PublicKey: pub,
	}, &Caller{UserID: 1, Username: "alice"})

	if err := svc.Block(ctx, a.Fingerprint, "key compromised", &Caller{UserID: 1, Username: "alice"}); err != nil {
		t.Fatal(err)
	}

	blocked, err := svc.IsBlocked(ctx, a.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Error("agent not blocked after owner Block")
	}
}
```

- [ ] **Step 3: Run, expect failure**

```bash
go test ./services/cairn/identity/ -run TestAgentService -v
```

Expected: FAIL — undefined `AgentService`, `NewAgentService`, `RegisterRequest`, `Caller`, `UserResolver`, `ErrUserNotFound`, `ErrForbidden`.

- [ ] **Step 4: Implement `services/cairn/identity/agent_service.go`**

```go
package identity

import (
	"context"
	"crypto/ed25519"
	"errors"
	"time"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

// ErrUserNotFound is returned when a referenced username does not
// resolve to a user record.
var ErrUserNotFound = errors.New("cairn identity: user not found")

// ErrForbidden is returned when an authenticated caller attempts an
// action that requires being the agent's owner (approve, block).
var ErrForbidden = errors.New("cairn identity: forbidden")

// UserResolver looks up Forgejo user records by username or id.
// The API layer implements this against models/user; tests provide
// a fake.
type UserResolver interface {
	UserIDByUsername(ctx context.Context, name string) (int64, error)
	UsernameByID(ctx context.Context, id int64) (string, error)
}

// Caller represents the authenticated user making a request, or nil
// for anonymous requests.
type Caller struct {
	UserID   int64
	Username string
}

// RegisterRequest is the input to AgentService.Register.
type RegisterRequest struct {
	ProposedOwner string             // username
	Slug          string             // bare slug, e.g. "plumb"
	Domain        string             // e.g. "darksoft.co.nz"
	PublicKey     ed25519.PublicKey  // 32 bytes
}

// AgentService orchestrates the registration / approval / blocking
// flow on top of AgentStore + AgentBlocklistStore + UserResolver.
//
// The service owns the instance HMAC key (used to compute fingerprints)
// and the auto-approve gate (caller's user_id == proposed_owner.user_id).
type AgentService struct {
	hmacKey   []byte
	store     AgentStore
	blocklist AgentBlocklistStore
	users     UserResolver
}

// NewAgentService constructs an AgentService.
func NewAgentService(hmacKey []byte, store AgentStore, blocklist AgentBlocklistStore, users UserResolver) *AgentService {
	return &AgentService{
		hmacKey:   hmacKey,
		store:     store,
		blocklist: blocklist,
		users:     users,
	}
}

// Register is the unified registration flow. If caller is the proposed
// owner (caller.UserID == proposed owner's id), the agent is created
// active. Otherwise (different user, or anonymous) the agent is pending.
//
// Returns ErrUserNotFound if proposed_owner doesn't exist; ErrAgentExists
// for duplicate (user_id, slug) or duplicate fingerprint.
func (s *AgentService) Register(ctx context.Context, req RegisterRequest, caller *Caller) (*cairn.Agent, error) {
	ownerID, err := s.users.UserIDByUsername(ctx, req.ProposedOwner)
	if err != nil {
		return nil, err
	}

	autoApprove := caller != nil && caller.UserID == ownerID

	now := time.Now()
	agent := &cairn.Agent{
		Fingerprint: Fingerprint(s.hmacKey, req.PublicKey),
		UserID:      ownerID,
		Slug:        req.Slug,
		Domain:      req.Domain,
		PublicKey:   []byte(req.PublicKey),
		CreatedAt:   now,
	}
	if autoApprove {
		agent.Status = cairn.AgentStatusActive
		agent.ActivatedAt = &now
	} else {
		agent.Status = cairn.AgentStatusPending
	}

	if err := s.store.Register(ctx, agent); err != nil {
		return nil, err
	}
	return agent, nil
}

// Approve transitions an agent from pending to active. Caller must be
// the agent's owner.
func (s *AgentService) Approve(ctx context.Context, fingerprint string, caller *Caller) error {
	if caller == nil {
		return ErrForbidden
	}
	a, err := s.store.GetByFingerprint(ctx, fingerprint)
	if err != nil {
		return err
	}
	if a.UserID != caller.UserID {
		return ErrForbidden
	}
	return s.store.Approve(ctx, fingerprint)
}

// Block adds the agent to the blocklist. Caller must be the agent's
// owner.
func (s *AgentService) Block(ctx context.Context, fingerprint, reason string, caller *Caller) error {
	if caller == nil {
		return ErrForbidden
	}
	a, err := s.store.GetByFingerprint(ctx, fingerprint)
	if err != nil {
		return err
	}
	if a.UserID != caller.UserID {
		return ErrForbidden
	}
	return s.blocklist.Block(ctx, a.ID, reason)
}

// GetByFingerprint is a thin wrapper around the store; included on
// the service so handlers don't have to reach across two layers.
func (s *AgentService) GetByFingerprint(ctx context.Context, fingerprint string) (*cairn.Agent, error) {
	return s.store.GetByFingerprint(ctx, fingerprint)
}

// IsBlocked reports whether the agent identified by fingerprint is
// in the blocklist.
func (s *AgentService) IsBlocked(ctx context.Context, fingerprint string) (bool, error) {
	a, err := s.store.GetByFingerprint(ctx, fingerprint)
	if err != nil {
		return false, err
	}
	return s.blocklist.IsBlocked(ctx, a.ID)
}

// ListByUser returns agents owned by the given user. Empty status
// means all statuses.
func (s *AgentService) ListByUser(ctx context.Context, userID int64, status cairn.AgentStatus) ([]*cairn.Agent, error) {
	return s.store.ListByUser(ctx, userID, status)
}
```

- [ ] **Step 5: Run, expect PASS**

```bash
go test ./services/cairn/identity/ -run TestAgentService -v
```

Expected: all 10 service tests pass.

- [ ] **Step 6: Run the full identity-package suite for regression check**

```bash
go test ./services/cairn/identity/...
```

Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add services/cairn/identity/agent_service.go services/cairn/identity/agent_service_test.go
git commit -m "$(cat <<'EOF'
feat(cairn): add AgentService orchestration layer

AgentService sits between the API handlers (Plan 2) and the stores
(Plan 1). Owns:
- The instance HMAC key (for Fingerprint)
- Owner resolution via the new UserResolver interface (Forgejo user
  package implements; tests use a fake)
- The auto-approve gate (caller.UserID == proposed_owner.UserID)
- Owner-permission checks for Approve and Block (ErrForbidden)

Interfaces, errors:
- UserResolver { UserIDByUsername, UsernameByID }
- Caller { UserID, Username }
- RegisterRequest { ProposedOwner, Slug, Domain, PublicKey }
- ErrUserNotFound, ErrForbidden

10 tests cover: auto-approve happy path, pending when proposer isn't
owner, pending for anonymous, unknown-owner rejection, duplicate
rejection, fingerprint determinism, approve transition, approve
non-owner rejection, block non-owner rejection, block by owner.

Refs: docs/cairn/specs/2026-05-09-cairn-foundation-design.md §6

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
git push -u origin cairn-agent-service
```

Then merge to `cairn` (controller will complete if push blocked):

```bash
git checkout cairn
git pull
git merge cairn-agent-service
git push origin cairn
```

---

## Task 3: API endpoint — POST /api/cairn/v1/agents (registration)

**Files:**
- Create: `routers/api/cairn/v1/agents.go` (handlers — start small, add more in later tasks)
- Create: `routers/api/cairn/v1/agents_test.go` (handler tests via httptest)
- Create: `routers/api/cairn/v1/forgejo_user_resolver.go` (concrete UserResolver impl using Forgejo's models/user)
- Create: `routers/api/cairn/v1/types.go` (request/response JSON types)

**Why:** This task lands the registration endpoint and the supporting infrastructure (request/response types, Forgejo's `UserResolver` impl, basic handler test harness). Subsequent API tasks (4–7) reuse this scaffolding.

The handler accepts a JSON request, calls `AgentService.Register`, returns the created agent's fingerprint + status. Auth context (Caller) comes from Forgejo's existing middleware — for unauthenticated calls, Caller is nil and the agent goes pending.

- [ ] **Step 1: New feature branch**

```bash
cd ~/Source/cairn
git checkout cairn
git pull
git checkout -b cairn-api-register
git config user.name "nexus-cw"
git config user.email "nexus@darksoft.co.nz"
```

- [ ] **Step 2: Create `routers/api/cairn/v1/types.go`**

```go
package v1

// RegisterRequestJSON is the wire format for POST /api/cairn/v1/agents.
//
// PublicKeyHex is hex-encoded so the JSON is text-only and copy-paste
// friendly. The handler decodes it before passing to the service.
type RegisterRequestJSON struct {
	ProposedOwner string `json:"proposed_owner"`
	Slug          string `json:"slug"`
	Domain        string `json:"domain"`
	PublicKeyHex  string `json:"public_key"`
}

// AgentJSON is the wire format for agent resources returned by the
// API. ActivatedAt is omitted when nil (pending agents).
type AgentJSON struct {
	Fingerprint  string `json:"fingerprint"`
	OwnerName    string `json:"owner"`
	Slug         string `json:"slug"`
	Domain       string `json:"domain"`
	PublicKeyHex string `json:"public_key"`
	Status       string `json:"status"`
	CreatedAt    string `json:"created_at"`            // RFC3339
	ActivatedAt  string `json:"activated_at,omitempty"` // RFC3339, omitted if nil
}

// ErrorJSON is the wire format for error responses.
type ErrorJSON struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

// BlockRequestJSON is the wire format for POST /agents/{fp}/block.
type BlockRequestJSON struct {
	Reason string `json:"reason"`
}
```

- [ ] **Step 3: Create `routers/api/cairn/v1/forgejo_user_resolver.go`**

This is the concrete `UserResolver` that bridges Cairn's interface to Forgejo's `models/user` package.

```go
package v1

import (
	"context"

	cairnidentity "github.com/CarriedWorldUniverse/cairn/services/cairn/identity"

	user_model "github.com/CarriedWorldUniverse/cairn/models/user"
)

// forgejoUserResolver implements cairnidentity.UserResolver against
// Forgejo's user model.
type forgejoUserResolver struct{}

// NewForgejoUserResolver returns a UserResolver that looks up users
// in Forgejo's standard user table.
func NewForgejoUserResolver() cairnidentity.UserResolver {
	return &forgejoUserResolver{}
}

func (r *forgejoUserResolver) UserIDByUsername(ctx context.Context, name string) (int64, error) {
	u, err := user_model.GetUserByName(ctx, name)
	if err != nil {
		if user_model.IsErrUserNotExist(err) {
			return 0, cairnidentity.ErrUserNotFound
		}
		return 0, err
	}
	return u.ID, nil
}

func (r *forgejoUserResolver) UsernameByID(ctx context.Context, id int64) (string, error) {
	u, err := user_model.GetUserByID(ctx, id)
	if err != nil {
		if user_model.IsErrUserNotExist(err) {
			return "", cairnidentity.ErrUserNotFound
		}
		return "", err
	}
	return u.Name, nil
}
```

NOTE: the exact Forgejo function names (`GetUserByName`, `IsErrUserNotExist`) and import path may differ slightly — verify against the actual `models/user/` package. If the API in this Forgejo version is different, adapt and document the deviation in the commit message.

- [ ] **Step 4: Write the failing test in `routers/api/cairn/v1/agents_test.go`**

```go
package v1

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
	cairnidentity "github.com/CarriedWorldUniverse/cairn/services/cairn/identity"
)

// fakeUserResolver mirrors the one in identity package tests.
type fakeUserResolver struct {
	usernameToID map[string]int64
}

func (f *fakeUserResolver) UserIDByUsername(ctx context.Context, name string) (int64, error) {
	id, ok := f.usernameToID[name]
	if !ok {
		return 0, cairnidentity.ErrUserNotFound
	}
	return id, nil
}

func (f *fakeUserResolver) UsernameByID(ctx context.Context, id int64) (string, error) {
	for name, uid := range f.usernameToID {
		if uid == id {
			return name, nil
		}
	}
	return "", cairnidentity.ErrUserNotFound
}

const testHMACKey = "0123456789abcdef0123456789abcdef"

func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	eng := cairntest.NewEngine(t)
	store := cairnidentity.NewXormAgentStore(eng)
	blocklist := cairnidentity.NewXormBlocklistStore(eng)
	users := &fakeUserResolver{
		usernameToID: map[string]int64{
			"alice": 1,
			"bob":     2,
		},
	}
	svc := cairnidentity.NewAgentService([]byte(testHMACKey), store, blocklist, users)
	return NewHandler(svc)
}

func TestPostAgents_AutoApproveWhenAuthedAsOwner(t *testing.T) {
	h := newTestHandler(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	body, _ := json.Marshal(RegisterRequestJSON{
		ProposedOwner: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKeyHex:  hex.EncodeToString(pub),
	})

	req := httptest.NewRequest(http.MethodPost, "/api/cairn/v1/agents", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// Inject Caller into context (in production this comes from middleware).
	req = req.WithContext(WithCaller(req.Context(), &cairnidentity.Caller{UserID: 1, Username: "alice"}))

	w := httptest.NewRecorder()
	h.PostAgents(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}

	var got AgentJSON
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != string(cairn.AgentStatusActive) {
		t.Errorf("status = %q, want active", got.Status)
	}
	if got.Fingerprint == "" {
		t.Error("fingerprint missing")
	}
}

func TestPostAgents_PendingWhenAnonymous(t *testing.T) {
	h := newTestHandler(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	body, _ := json.Marshal(RegisterRequestJSON{
		ProposedOwner: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKeyHex:  hex.EncodeToString(pub),
	})

	req := httptest.NewRequest(http.MethodPost, "/api/cairn/v1/agents", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No Caller — anonymous.

	w := httptest.NewRecorder()
	h.PostAgents(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}

	var got AgentJSON
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.Status != string(cairn.AgentStatusPending) {
		t.Errorf("status = %q, want pending", got.Status)
	}
}

func TestPostAgents_RejectsUnknownOwner(t *testing.T) {
	h := newTestHandler(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	body, _ := json.Marshal(RegisterRequestJSON{
		ProposedOwner: "nobody",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKeyHex:  hex.EncodeToString(pub),
	})

	req := httptest.NewRequest(http.MethodPost, "/api/cairn/v1/agents", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.PostAgents(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

func TestPostAgents_RejectsDuplicate(t *testing.T) {
	h := newTestHandler(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	body, _ := json.Marshal(RegisterRequestJSON{
		ProposedOwner: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKeyHex:  hex.EncodeToString(pub),
	})

	send := func() int {
		req := httptest.NewRequest(http.MethodPost, "/api/cairn/v1/agents", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(WithCaller(req.Context(), &cairnidentity.Caller{UserID: 1, Username: "alice"}))
		w := httptest.NewRecorder()
		h.PostAgents(w, req)
		return w.Code
	}

	if code := send(); code != http.StatusCreated {
		t.Fatalf("first request status = %d, want 201", code)
	}
	if code := send(); code != http.StatusConflict {
		t.Errorf("duplicate request status = %d, want 409", code)
	}
}

func TestPostAgents_RejectsMalformedHex(t *testing.T) {
	h := newTestHandler(t)

	body, _ := json.Marshal(RegisterRequestJSON{
		ProposedOwner: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKeyHex:  "not-hex-z",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/cairn/v1/agents", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.PostAgents(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestPostAgents_RejectsWrongPubkeyLength(t *testing.T) {
	h := newTestHandler(t)

	body, _ := json.Marshal(RegisterRequestJSON{
		ProposedOwner: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKeyHex:  hex.EncodeToString([]byte{1, 2, 3, 4}), // not 32 bytes
	})

	req := httptest.NewRequest(http.MethodPost, "/api/cairn/v1/agents", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.PostAgents(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}
```

- [ ] **Step 5: Run, expect failure**

```bash
go test ./routers/api/cairn/v1/...
```

Expected: FAIL — `undefined: Handler, NewHandler, WithCaller, PostAgents`.

- [ ] **Step 6: Implement `routers/api/cairn/v1/agents.go`**

```go
// Package v1 implements Cairn's REST API at /api/cairn/v1.
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package v1

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
	cairnidentity "github.com/CarriedWorldUniverse/cairn/services/cairn/identity"
)

// Handler is the HTTP handler set for /api/cairn/v1.
type Handler struct {
	svc *cairnidentity.AgentService
}

// NewHandler returns a Handler bound to the given service.
func NewHandler(svc *cairnidentity.AgentService) *Handler {
	return &Handler{svc: svc}
}

// callerKey is the context key for the authenticated Caller.
// Cairn-internal type to avoid collisions with other context keys.
type callerKey struct{}

// WithCaller attaches a Caller to ctx. Used by the auth middleware
// (in production) and tests (for injection).
func WithCaller(ctx context.Context, c *cairnidentity.Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// callerFromCtx returns the Caller, or nil for anonymous requests.
func callerFromCtx(ctx context.Context) *cairnidentity.Caller {
	c, _ := ctx.Value(callerKey{}).(*cairnidentity.Caller)
	return c
}

// PostAgents handles POST /api/cairn/v1/agents.
func (h *Handler) PostAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "")
		return
	}

	var in RegisterRequestJSON
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	if in.ProposedOwner == "" || in.Slug == "" || in.Domain == "" || in.PublicKeyHex == "" {
		writeError(w, http.StatusBadRequest, "missing_fields", "proposed_owner, slug, domain, and public_key are required")
		return
	}

	pubBytes, err := hex.DecodeString(in.PublicKeyHex)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_public_key_hex", err.Error())
		return
	}
	if len(pubBytes) != ed25519.PublicKeySize {
		writeError(w, http.StatusBadRequest, "invalid_public_key_size", "expected 32 bytes")
		return
	}

	agent, err := h.svc.Register(r.Context(), cairnidentity.RegisterRequest{
		ProposedOwner: in.ProposedOwner,
		Slug:          in.Slug,
		Domain:        in.Domain,
		PublicKey:     ed25519.PublicKey(pubBytes),
	}, callerFromCtx(r.Context()))

	switch {
	case err == nil:
		// continue below
	case errors.Is(err, cairnidentity.ErrUserNotFound):
		writeError(w, http.StatusNotFound, "owner_not_found", "")
		return
	case errors.Is(err, cairnidentity.ErrAgentExists):
		writeError(w, http.StatusConflict, "agent_exists", "agent with this slug or fingerprint already exists")
		return
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	ownerName, _ := h.svc.UserResolverUsername(r.Context(), agent.UserID)
	writeAgent(w, http.StatusCreated, agent, ownerName)
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, code int, errorCode, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(ErrorJSON{Error: errorCode, Message: message})
}

// writeAgent writes an agent as JSON with the given HTTP status code.
func writeAgent(w http.ResponseWriter, code int, a *cairn.Agent, ownerName string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	out := AgentJSON{
		Fingerprint:  a.Fingerprint,
		OwnerName:    ownerName,
		Slug:         a.Slug,
		Domain:       a.Domain,
		PublicKeyHex: hex.EncodeToString(a.PublicKey),
		Status:       string(a.Status),
		CreatedAt:    a.CreatedAt.UTC().Format(time.RFC3339),
	}
	if a.ActivatedAt != nil {
		out.ActivatedAt = a.ActivatedAt.UTC().Format(time.RFC3339)
	}
	_ = json.NewEncoder(w).Encode(out)
}
```

NOTE: this references `h.svc.UserResolverUsername(...)` which doesn't exist on AgentService yet. Add it now to `services/cairn/identity/agent_service.go`:

```go
// UserResolverUsername returns the username for a user id, or empty
// string on lookup failure (caller decides whether that's an error).
// Convenience wrapper used by the API layer.
func (s *AgentService) UserResolverUsername(ctx context.Context, userID int64) (string, error) {
	return s.users.UsernameByID(ctx, userID)
}
```

- [ ] **Step 7: Run, expect PASS**

```bash
go test ./routers/api/cairn/v1/...
```

Expected: all six handler tests pass.

- [ ] **Step 8: Run the full Cairn suite for regression**

```bash
go test ./services/cairn/... ./models/cairn/... ./routers/api/cairn/...
```

Expected: PASS across the board.

- [ ] **Step 9: Commit**

```bash
git add routers/api/cairn/v1/ services/cairn/identity/agent_service.go
git commit -m "$(cat <<'EOF'
feat(cairn): POST /api/cairn/v1/agents registration handler

Lands the registration endpoint with the auto-approve gate. Auth
context comes from Forgejo middleware via context.Context (the
production wiring lives in the route-registration task; tests
inject Caller directly via WithCaller).

Files:
- routers/api/cairn/v1/types.go — JSON wire formats
- routers/api/cairn/v1/agents.go — Handler + PostAgents
- routers/api/cairn/v1/forgejo_user_resolver.go — concrete
  UserResolver against Forgejo's models/user
- services/cairn/identity/agent_service.go — add
  UserResolverUsername convenience method for handlers

Status mapping:
- 201: agent created (status=active if auto-approved, pending else)
- 400: malformed JSON, missing fields, invalid pubkey hex, wrong
       pubkey length
- 404: proposed_owner not found
- 409: duplicate (user_id, slug) or duplicate fingerprint
- 500: anything else

Six handler tests cover happy path (auto-approve), pending-when-
anonymous, unknown-owner 404, duplicate 409, malformed hex 400,
wrong-length pubkey 400.

Refs: docs/cairn/specs/2026-05-09-cairn-foundation-design.md §6

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
git push -u origin cairn-api-register
```

Then merge to `cairn` (controller will complete if push blocked).

---

## Task 4: API endpoint — POST /api/cairn/v1/agents/:fingerprint/approve

**Files:**
- Modify: `routers/api/cairn/v1/agents.go` (add `PostApprove` method)
- Modify: `routers/api/cairn/v1/agents_test.go` (add approve handler tests)

**Why:** Spec §6 — pending agents need an explicit approve action by the owner. This endpoint authenticates the caller, checks they're the agent's owner, and transitions status pending → active.

- [ ] **Step 1: New feature branch**

```bash
cd ~/Source/cairn
git checkout cairn
git pull
git checkout -b cairn-api-approve
git config user.name "nexus-cw"
git config user.email "nexus@darksoft.co.nz"
```

- [ ] **Step 2: Write the failing tests**

Append to `routers/api/cairn/v1/agents_test.go`:

```go
func TestPostApprove_OwnerCanApprove(t *testing.T) {
	h := newTestHandler(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	// Step 1: register anonymously → pending.
	body, _ := json.Marshal(RegisterRequestJSON{
		ProposedOwner: "alice", Slug: "plumb", Domain: "darksoft.co.nz",
		PublicKeyHex: hex.EncodeToString(pub),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cairn/v1/agents", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.PostAgents(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("register status = %d", w.Code)
	}
	var pending AgentJSON
	json.Unmarshal(w.Body.Bytes(), &pending)

	// Step 2: owner approves.
	approveReq := httptest.NewRequest(http.MethodPost,
		"/api/cairn/v1/agents/"+pending.Fingerprint+"/approve", nil)
	approveReq = approveReq.WithContext(WithCaller(approveReq.Context(),
		&cairnidentity.Caller{UserID: 1, Username: "alice"}))
	approveReq = WithFingerprintParam(approveReq, pending.Fingerprint)
	approveW := httptest.NewRecorder()
	h.PostApprove(approveW, approveReq)

	if approveW.Code != http.StatusOK {
		t.Fatalf("approve status = %d, want 200; body=%s", approveW.Code, approveW.Body.String())
	}
	var got AgentJSON
	json.Unmarshal(approveW.Body.Bytes(), &got)
	if got.Status != string(cairn.AgentStatusActive) {
		t.Errorf("status = %q, want active", got.Status)
	}
}

func TestPostApprove_NonOwnerForbidden(t *testing.T) {
	h := newTestHandler(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	body, _ := json.Marshal(RegisterRequestJSON{
		ProposedOwner: "alice", Slug: "plumb", Domain: "darksoft.co.nz",
		PublicKeyHex: hex.EncodeToString(pub),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cairn/v1/agents", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.PostAgents(w, req)
	var pending AgentJSON
	json.Unmarshal(w.Body.Bytes(), &pending)

	approveReq := httptest.NewRequest(http.MethodPost, "/", nil)
	approveReq = approveReq.WithContext(WithCaller(approveReq.Context(),
		&cairnidentity.Caller{UserID: 2, Username: "bob"}))
	approveReq = WithFingerprintParam(approveReq, pending.Fingerprint)
	approveW := httptest.NewRecorder()
	h.PostApprove(approveW, approveReq)

	if approveW.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", approveW.Code)
	}
}

func TestPostApprove_UnauthenticatedUnauthorized(t *testing.T) {
	h := newTestHandler(t)

	approveReq := httptest.NewRequest(http.MethodPost, "/", nil)
	approveReq = WithFingerprintParam(approveReq, "cairn:does-not-matter")
	approveW := httptest.NewRecorder()
	h.PostApprove(approveW, approveReq)

	if approveW.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", approveW.Code)
	}
}

func TestPostApprove_NotFound(t *testing.T) {
	h := newTestHandler(t)

	approveReq := httptest.NewRequest(http.MethodPost, "/", nil)
	approveReq = approveReq.WithContext(WithCaller(approveReq.Context(),
		&cairnidentity.Caller{UserID: 1, Username: "alice"}))
	approveReq = WithFingerprintParam(approveReq, "cairn:does-not-exist")
	approveW := httptest.NewRecorder()
	h.PostApprove(approveW, approveReq)

	if approveW.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", approveW.Code)
	}
}
```

- [ ] **Step 3: Run, expect failure**

```bash
go test ./routers/api/cairn/v1/ -run TestPostApprove -v
```

Expected: FAIL — `undefined: PostApprove, WithFingerprintParam`.

- [ ] **Step 4: Implement `PostApprove` and `WithFingerprintParam` in `routers/api/cairn/v1/agents.go`**

Append to `agents.go`:

```go
// fingerprintKey is the context key for the URL :fingerprint path param.
type fingerprintKey struct{}

// WithFingerprintParam attaches the URL :fingerprint param to the
// request context. Test injection helper; production wiring uses the
// router's path-param extraction.
func WithFingerprintParam(r *http.Request, fp string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), fingerprintKey{}, fp))
}

// fingerprintFromCtx returns the :fingerprint URL param.
func fingerprintFromCtx(ctx context.Context) string {
	fp, _ := ctx.Value(fingerprintKey{}).(string)
	return fp
}

// PostApprove handles POST /api/cairn/v1/agents/:fingerprint/approve.
func (h *Handler) PostApprove(w http.ResponseWriter, r *http.Request) {
	caller := callerFromCtx(r.Context())
	if caller == nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "")
		return
	}

	fp := fingerprintFromCtx(r.Context())
	if fp == "" {
		writeError(w, http.StatusBadRequest, "missing_fingerprint", "")
		return
	}

	err := h.svc.Approve(r.Context(), fp, caller)
	switch {
	case err == nil:
		// fall through to load + return updated agent
	case errors.Is(err, cairnidentity.ErrAgentNotFound):
		writeError(w, http.StatusNotFound, "agent_not_found", "")
		return
	case errors.Is(err, cairnidentity.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "only the agent's owner may approve")
		return
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	a, err := h.svc.GetByFingerprint(r.Context(), fp)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	ownerName, _ := h.svc.UserResolverUsername(r.Context(), a.UserID)
	writeAgent(w, http.StatusOK, a, ownerName)
}
```

- [ ] **Step 5: Run, expect PASS**

```bash
go test ./routers/api/cairn/v1/...
```

Expected: all tests pass (10 in this file now: 6 from Task 3 + 4 new approve tests).

- [ ] **Step 6: Commit**

```bash
git add routers/api/cairn/v1/agents.go routers/api/cairn/v1/agents_test.go
git commit -m "$(cat <<'EOF'
feat(cairn): POST /api/cairn/v1/agents/:fingerprint/approve

Owner-only endpoint for transitioning pending agents to active.

Status mapping:
- 200: approved (returns updated AgentJSON)
- 401: no authenticated caller
- 403: caller is not the agent's owner
- 404: agent not found
- 500: anything else

Adds WithFingerprintParam test helper to inject :fingerprint URL
param into the request context (production wiring extracts from the
router's path-param map; the route registration task wires that up).

Refs: docs/cairn/specs/2026-05-09-cairn-foundation-design.md §6

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
git push -u origin cairn-api-approve
```

Then merge to `cairn`.

---

## Task 5: API endpoint — POST /api/cairn/v1/agents/:fingerprint/block

**Files:**
- Modify: `routers/api/cairn/v1/agents.go` (add `PostBlock`)
- Modify: `routers/api/cairn/v1/agents_test.go` (add tests)

**Why:** Spec §6 — owner can block compromised or rotated-out agents. The endpoint mirrors PostApprove's auth pattern (owner-only, 401/403/404/500), with a request body carrying the optional `reason`.

- [ ] **Step 1: New feature branch**

```bash
cd ~/Source/cairn
git checkout cairn
git pull
git checkout -b cairn-api-block
git config user.name "nexus-cw"
git config user.email "nexus@darksoft.co.nz"
```

- [ ] **Step 2: Write failing tests**

Append to `agents_test.go`:

```go
func TestPostBlock_OwnerCanBlock(t *testing.T) {
	h := newTestHandler(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	// Auto-approved registration (alice authed).
	body, _ := json.Marshal(RegisterRequestJSON{
		ProposedOwner: "alice", Slug: "plumb", Domain: "darksoft.co.nz",
		PublicKeyHex: hex.EncodeToString(pub),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cairn/v1/agents", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(WithCaller(req.Context(), &cairnidentity.Caller{UserID: 1, Username: "alice"}))
	w := httptest.NewRecorder()
	h.PostAgents(w, req)
	var a AgentJSON
	json.Unmarshal(w.Body.Bytes(), &a)

	// Owner blocks.
	blockBody, _ := json.Marshal(BlockRequestJSON{Reason: "key compromised"})
	blockReq := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(blockBody))
	blockReq.Header.Set("Content-Type", "application/json")
	blockReq = blockReq.WithContext(WithCaller(blockReq.Context(),
		&cairnidentity.Caller{UserID: 1, Username: "alice"}))
	blockReq = WithFingerprintParam(blockReq, a.Fingerprint)
	blockW := httptest.NewRecorder()
	h.PostBlock(blockW, blockReq)

	if blockW.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body=%s", blockW.Code, blockW.Body.String())
	}

	// Verify it's actually blocked via the service.
	blocked, err := h.svc.IsBlocked(context.Background(), a.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Error("agent not blocked after PostBlock")
	}
}

func TestPostBlock_NonOwnerForbidden(t *testing.T) {
	h := newTestHandler(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	body, _ := json.Marshal(RegisterRequestJSON{
		ProposedOwner: "alice", Slug: "plumb", Domain: "darksoft.co.nz",
		PublicKeyHex: hex.EncodeToString(pub),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cairn/v1/agents", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(WithCaller(req.Context(), &cairnidentity.Caller{UserID: 1, Username: "alice"}))
	w := httptest.NewRecorder()
	h.PostAgents(w, req)
	var a AgentJSON
	json.Unmarshal(w.Body.Bytes(), &a)

	blockReq := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{"reason":"x"}`)))
	blockReq.Header.Set("Content-Type", "application/json")
	blockReq = blockReq.WithContext(WithCaller(blockReq.Context(),
		&cairnidentity.Caller{UserID: 2, Username: "bob"}))
	blockReq = WithFingerprintParam(blockReq, a.Fingerprint)
	blockW := httptest.NewRecorder()
	h.PostBlock(blockW, blockReq)

	if blockW.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", blockW.Code)
	}
}

func TestPostBlock_UnauthenticatedUnauthorized(t *testing.T) {
	h := newTestHandler(t)

	blockReq := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{"reason":"x"}`)))
	blockReq.Header.Set("Content-Type", "application/json")
	blockReq = WithFingerprintParam(blockReq, "cairn:any")
	blockW := httptest.NewRecorder()
	h.PostBlock(blockW, blockReq)

	if blockW.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", blockW.Code)
	}
}

func TestPostBlock_NotFound(t *testing.T) {
	h := newTestHandler(t)

	blockReq := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{"reason":"x"}`)))
	blockReq.Header.Set("Content-Type", "application/json")
	blockReq = blockReq.WithContext(WithCaller(blockReq.Context(),
		&cairnidentity.Caller{UserID: 1, Username: "alice"}))
	blockReq = WithFingerprintParam(blockReq, "cairn:does-not-exist")
	blockW := httptest.NewRecorder()
	h.PostBlock(blockW, blockReq)

	if blockW.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", blockW.Code)
	}
}
```

- [ ] **Step 3: Run, expect failure**

```bash
go test ./routers/api/cairn/v1/ -run TestPostBlock -v
```

Expected: FAIL — `undefined: PostBlock`.

- [ ] **Step 4: Implement `PostBlock` in `agents.go`**

Append to `agents.go`:

```go
// PostBlock handles POST /api/cairn/v1/agents/:fingerprint/block.
func (h *Handler) PostBlock(w http.ResponseWriter, r *http.Request) {
	caller := callerFromCtx(r.Context())
	if caller == nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "")
		return
	}

	fp := fingerprintFromCtx(r.Context())
	if fp == "" {
		writeError(w, http.StatusBadRequest, "missing_fingerprint", "")
		return
	}

	var in BlockRequestJSON
	// Body is optional — empty body means no reason.
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
	}

	err := h.svc.Block(r.Context(), fp, in.Reason, caller)
	switch {
	case err == nil:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "blocked"})
		return
	case errors.Is(err, cairnidentity.ErrAgentNotFound):
		writeError(w, http.StatusNotFound, "agent_not_found", "")
	case errors.Is(err, cairnidentity.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "only the agent's owner may block")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}
```

- [ ] **Step 5: Run, expect PASS**

```bash
go test ./routers/api/cairn/v1/...
```

Expected: all tests pass (now 14 in this file).

- [ ] **Step 6: Commit**

```bash
git add routers/api/cairn/v1/agents.go routers/api/cairn/v1/agents_test.go
git commit -m "$(cat <<'EOF'
feat(cairn): POST /api/cairn/v1/agents/:fingerprint/block

Owner-only endpoint to add an agent to the blocklist. Same auth
contract as Approve: 401 anonymous, 403 non-owner, 404 unknown,
500 anything else. 200 on success returns {"status": "blocked"}.

Body is optional — {"reason":"<text>"} attaches a reason string,
empty body just blocks without one.

Refs: docs/cairn/specs/2026-05-09-cairn-foundation-design.md §6

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
git push -u origin cairn-api-block
```

Then merge to `cairn`.

---

## Task 6: API endpoints — GET /agents/:fingerprint/identity and GET /agents

**Files:**
- Modify: `routers/api/cairn/v1/agents.go` (add `GetIdentity`, `GetAgents`)
- Modify: `routers/api/cairn/v1/agents_test.go` (add tests)

**Why:** Spec §4.3 — the pre-receive hook (Plan 3) needs to fetch agent public keys by fingerprint to verify signatures. The owner UI / CLI needs to list their agents (filterable by status).

- [ ] **Step 1: New feature branch**

```bash
cd ~/Source/cairn
git checkout cairn
git pull
git checkout -b cairn-api-identity-and-list
git config user.name "nexus-cw"
git config user.email "nexus@darksoft.co.nz"
```

- [ ] **Step 2: Write the failing tests**

Append to `agents_test.go`:

```go
func TestGetIdentity_ReturnsPublicKey(t *testing.T) {
	h := newTestHandler(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	body, _ := json.Marshal(RegisterRequestJSON{
		ProposedOwner: "alice", Slug: "plumb", Domain: "darksoft.co.nz",
		PublicKeyHex: hex.EncodeToString(pub),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cairn/v1/agents", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(WithCaller(req.Context(), &cairnidentity.Caller{UserID: 1, Username: "alice"}))
	w := httptest.NewRecorder()
	h.PostAgents(w, req)
	var a AgentJSON
	json.Unmarshal(w.Body.Bytes(), &a)

	idReq := httptest.NewRequest(http.MethodGet, "/", nil)
	idReq = WithFingerprintParam(idReq, a.Fingerprint)
	idW := httptest.NewRecorder()
	h.GetIdentity(idW, idReq)

	if idW.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", idW.Code)
	}

	var got AgentJSON
	json.Unmarshal(idW.Body.Bytes(), &got)
	if got.PublicKeyHex != hex.EncodeToString(pub) {
		t.Errorf("public_key = %q, want %q", got.PublicKeyHex, hex.EncodeToString(pub))
	}
	if got.Slug != "plumb" {
		t.Errorf("slug = %q, want plumb", got.Slug)
	}
}

func TestGetIdentity_NotFound(t *testing.T) {
	h := newTestHandler(t)

	idReq := httptest.NewRequest(http.MethodGet, "/", nil)
	idReq = WithFingerprintParam(idReq, "cairn:does-not-exist")
	idW := httptest.NewRecorder()
	h.GetIdentity(idW, idReq)

	if idW.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", idW.Code)
	}
}

func TestGetAgents_ListsCurrentUsersAgents(t *testing.T) {
	h := newTestHandler(t)

	// Register three agents under alice.
	for _, slug := range []string{"plumb", "anvil", "forge"} {
		pub, _, _ := ed25519.GenerateKey(rand.Reader)
		body, _ := json.Marshal(RegisterRequestJSON{
			ProposedOwner: "alice", Slug: slug, Domain: "darksoft.co.nz",
			PublicKeyHex: hex.EncodeToString(pub),
		})
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(WithCaller(req.Context(),
			&cairnidentity.Caller{UserID: 1, Username: "alice"}))
		w := httptest.NewRecorder()
		h.PostAgents(w, req)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/cairn/v1/agents", nil)
	listReq = listReq.WithContext(WithCaller(listReq.Context(),
		&cairnidentity.Caller{UserID: 1, Username: "alice"}))
	listW := httptest.NewRecorder()
	h.GetAgents(listW, listReq)

	if listW.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", listW.Code)
	}

	var got []AgentJSON
	if err := json.Unmarshal(listW.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("len = %d, want 3", len(got))
	}
}

func TestGetAgents_StatusFilter(t *testing.T) {
	h := newTestHandler(t)

	// Active under alice.
	pub1, _, _ := ed25519.GenerateKey(rand.Reader)
	body1, _ := json.Marshal(RegisterRequestJSON{
		ProposedOwner: "alice", Slug: "plumb", Domain: "darksoft.co.nz",
		PublicKeyHex: hex.EncodeToString(pub1),
	})
	req1 := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body1))
	req1.Header.Set("Content-Type", "application/json")
	req1 = req1.WithContext(WithCaller(req1.Context(),
		&cairnidentity.Caller{UserID: 1, Username: "alice"}))
	w1 := httptest.NewRecorder()
	h.PostAgents(w1, req1)

	// Pending: register anonymously under alice.
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	body2, _ := json.Marshal(RegisterRequestJSON{
		ProposedOwner: "alice", Slug: "anvil", Domain: "darksoft.co.nz",
		PublicKeyHex: hex.EncodeToString(pub2),
	})
	req2 := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	h.PostAgents(w2, req2)

	// List active only.
	listReq := httptest.NewRequest(http.MethodGet, "/api/cairn/v1/agents?status=active", nil)
	listReq = listReq.WithContext(WithCaller(listReq.Context(),
		&cairnidentity.Caller{UserID: 1, Username: "alice"}))
	listW := httptest.NewRecorder()
	h.GetAgents(listW, listReq)

	if listW.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", listW.Code)
	}

	var got []AgentJSON
	json.Unmarshal(listW.Body.Bytes(), &got)
	if len(got) != 1 {
		t.Errorf("active filter len = %d, want 1", len(got))
	}
	if got[0].Slug != "plumb" {
		t.Errorf("slug = %q, want plumb", got[0].Slug)
	}
}

func TestGetAgents_RequiresAuth(t *testing.T) {
	h := newTestHandler(t)

	listReq := httptest.NewRequest(http.MethodGet, "/", nil)
	listW := httptest.NewRecorder()
	h.GetAgents(listW, listReq)

	if listW.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", listW.Code)
	}
}
```

- [ ] **Step 3: Run, expect failure**

```bash
go test ./routers/api/cairn/v1/ -run "TestGetIdentity|TestGetAgents" -v
```

Expected: FAIL — `undefined: GetIdentity, GetAgents`.

- [ ] **Step 4: Implement the two handlers in `agents.go`**

Append to `agents.go`:

```go
// GetIdentity handles GET /api/cairn/v1/agents/:fingerprint/identity.
// Returns the agent's public key + metadata. Public — no auth required
// (the public key is, by definition, public).
func (h *Handler) GetIdentity(w http.ResponseWriter, r *http.Request) {
	fp := fingerprintFromCtx(r.Context())
	if fp == "" {
		writeError(w, http.StatusBadRequest, "missing_fingerprint", "")
		return
	}

	a, err := h.svc.GetByFingerprint(r.Context(), fp)
	if err != nil {
		if errors.Is(err, cairnidentity.ErrAgentNotFound) {
			writeError(w, http.StatusNotFound, "agent_not_found", "")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	ownerName, _ := h.svc.UserResolverUsername(r.Context(), a.UserID)
	writeAgent(w, http.StatusOK, a, ownerName)
}

// GetAgents handles GET /api/cairn/v1/agents — list the authed user's
// own agents. Optional ?status= filter accepts "pending" or "active".
func (h *Handler) GetAgents(w http.ResponseWriter, r *http.Request) {
	caller := callerFromCtx(r.Context())
	if caller == nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "")
		return
	}

	status := cairn.AgentStatus(r.URL.Query().Get("status"))

	agents, err := h.svc.ListByUser(r.Context(), caller.UserID, status)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	out := make([]AgentJSON, 0, len(agents))
	for _, a := range agents {
		ownerName, _ := h.svc.UserResolverUsername(r.Context(), a.UserID)
		j := AgentJSON{
			Fingerprint:  a.Fingerprint,
			OwnerName:    ownerName,
			Slug:         a.Slug,
			Domain:       a.Domain,
			PublicKeyHex: hex.EncodeToString(a.PublicKey),
			Status:       string(a.Status),
			CreatedAt:    a.CreatedAt.UTC().Format(time.RFC3339),
		}
		if a.ActivatedAt != nil {
			j.ActivatedAt = a.ActivatedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, j)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(out)
}
```

- [ ] **Step 5: Run, expect PASS**

```bash
go test ./routers/api/cairn/v1/...
```

Expected: all 18 tests pass.

- [ ] **Step 6: Commit**

```bash
git add routers/api/cairn/v1/agents.go routers/api/cairn/v1/agents_test.go
git commit -m "$(cat <<'EOF'
feat(cairn): GET /agents/:fingerprint/identity + GET /agents

Two read endpoints:

- GET /agents/:fingerprint/identity — public, returns agent metadata
  including the public key as hex. Used by the pre-receive hook
  (Plan 3) for signature verification.

- GET /agents — auth-required, returns the caller's own agents.
  Optional ?status=pending|active filter.

Status mapping:
- 200: success
- 400: missing fingerprint param
- 401: GetAgents only — no caller
- 404: GetIdentity only — agent not found
- 500: anything else

Refs: docs/cairn/specs/2026-05-09-cairn-foundation-design.md §4.3

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
git push -u origin cairn-api-identity-and-list
```

Then merge to `cairn`.

---

## Task 7: Route registration + Forgejo integration

**Files:**
- Create: `routers/api/cairn/v1/routes.go`
- Modify: `routers/web/web.go` (the file that wires Forgejo's main HTTP routing) — add the Cairn API routes
- Modify: `services/cairn/identity/agent_service.go` — add a global init that loads the instance HMAC key once at startup

**Why:** The handlers exist (Tasks 3-6) but nothing routes traffic to them. Forgejo wires its routes in `routers/web/web.go` via a chi-style mux. We add a small Cairn-specific route group and bind it.

The `instance_hmac_key` needs to be loaded from disk at startup and held in memory for the lifetime of the process. This task adds the load step and provides the AgentService instance to the route group.

The auth middleware integration: when Forgejo's existing auth middleware authenticates a request, we extract the user and stuff a `*Caller` into the context for the Cairn handlers. This requires a small middleware that reads Forgejo's user from the context and translates it.

NOTE: this task touches Forgejo upstream code (`routers/web/web.go`), which means it's a real patch on the patch stack. Keep the diff minimal: a single function call into `cairn-route-mounting`.

- [ ] **Step 1: New feature branch**

```bash
cd ~/Source/cairn
git checkout cairn
git pull
git checkout -b cairn-api-route-mount
git config user.name "nexus-cw"
git config user.email "nexus@darksoft.co.nz"
```

- [ ] **Step 2: Inspect Forgejo's existing routing in `routers/web/web.go`**

Read `routers/web/web.go` to find where API routes are mounted. Look for patterns like `m.Group("/api/v1", ...)` or `r.Route("/api/v1", ...)`. The exact router framework (chi vs. Forgejo's wrapper) varies across versions.

Document the discovered pattern in your commit message.

- [ ] **Step 3: Create the Cairn route mount function in `routers/api/cairn/v1/routes.go`**

```go
package v1

import (
	"context"
	"net/http"
	"sync"

	cairnidentity "github.com/CarriedWorldUniverse/cairn/services/cairn/identity"
	"github.com/CarriedWorldUniverse/cairn/modules/setting"
)

// Globals — single AgentService for the lifetime of the process.
//
// Initialized once at startup via Init(). Cairn config flags (HMAC key
// path) live in modules/setting.

var (
	initOnce       sync.Once
	globalService  *cairnidentity.AgentService
	globalHandler  *Handler
)

// Init is called once at server startup. Reads the instance HMAC key
// from disk (generating on first run if missing), constructs the
// AgentService, and stores it globally for the route handlers to use.
//
// Caller is responsible for ensuring this runs after the database
// engine is initialized but before the HTTP server starts accepting
// connections.
func Init(ctx context.Context, hmacKeyPath string, store cairnidentity.AgentStore, blocklist cairnidentity.AgentBlocklistStore, users cairnidentity.UserResolver) error {
	var initErr error
	initOnce.Do(func() {
		key, err := cairnidentity.LoadInstanceHMACKey(hmacKeyPath)
		if err != nil {
			initErr = err
			return
		}
		globalService = cairnidentity.NewAgentService(key, store, blocklist, users)
		globalHandler = NewHandler(globalService)
	})
	return initErr
}

// MountRoutes wires the Cairn API endpoints onto the provided router.
// The exact router type is whatever Forgejo's router exposes (chi.Router
// or a Forgejo wrapper). Caller passes a route group rooted at /api/cairn/v1.
//
// To keep upstream collisions minimal, this is a single function call
// from Forgejo's web.go, with all routing details kept Cairn-side.
func MountRoutes(group RouteGroup) {
	if globalHandler == nil {
		panic("cairn api v1: MountRoutes called before Init")
	}

	// Auth middleware that extracts Forgejo's authed user (already
	// populated by Forgejo's middleware chain) and stuffs a *Caller
	// into context for the Cairn handlers.
	withCaller := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			caller := extractForgejoUser(r.Context())
			if caller != nil {
				r = r.WithContext(WithCaller(r.Context(), caller))
			}
			next(w, r)
		}
	}

	group.Post("/agents", withCaller(globalHandler.PostAgents))
	group.Get("/agents", withCaller(globalHandler.GetAgents))
	group.Get("/agents/{fingerprint}/identity", withFP(globalHandler.GetIdentity))
	group.Post("/agents/{fingerprint}/approve", withFP(withCaller(globalHandler.PostApprove)))
	group.Post("/agents/{fingerprint}/block", withFP(withCaller(globalHandler.PostBlock)))
}

// withFP extracts the {fingerprint} URL param from the route and stuffs
// it into context for the handler.
func withFP(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fp := chiURLParam(r, "fingerprint")
		r = r.WithContext(context.WithValue(r.Context(), fingerprintKey{}, fp))
		next(w, r)
	}
}

// RouteGroup is the minimal interface Cairn needs from Forgejo's
// router. Forgejo's actual router type satisfies this; this interface
// keeps Cairn from depending on the concrete router type and minimizes
// rebase friction.
type RouteGroup interface {
	Get(pattern string, h http.HandlerFunc)
	Post(pattern string, h http.HandlerFunc)
}

// extractForgejoUser pulls Forgejo's authed user out of the request
// context and translates to a Cairn Caller. Returns nil for anonymous
// requests.
//
// Implementation detail: Forgejo stores the authed user on context via
// services/context (see ctxKey types there). This is intentionally
// placed at the route layer rather than in cairnidentity so the
// Cairn-domain code stays free of Forgejo type imports.
func extractForgejoUser(ctx context.Context) *cairnidentity.Caller {
	// IMPLEMENTATION NOTE: this function depends on Forgejo's exact
	// context-key for the authed user, which varies across Forgejo
	// versions. Read services/context/context.go (or similar) to find
	// the right key + accessor. The shape is something like:
	//
	//   ctx := services.GetContext(r)
	//   if ctx.Doer == nil { return nil }
	//   return &cairnidentity.Caller{UserID: ctx.Doer.ID, Username: ctx.Doer.Name}
	//
	// Adapt to whatever Forgejo's actual API is.
	return extractForgejoUserActual(ctx)
}

// chiURLParam extracts a URL path parameter. Implemented in a separate
// file (routes_chi.go or similar) that imports the actual chi (or
// Forgejo wrapper) package.
func chiURLParam(r *http.Request, name string) string {
	return chiURLParamActual(r, name)
}
```

NOTE: `extractForgejoUserActual` and `chiURLParamActual` are implementation thunks that live in a separate file alongside this one — they do the actual work of reading Forgejo's context and chi's URL params. The split keeps the routing logic testable without dragging in the full Forgejo runtime.

- [ ] **Step 4: Create `routers/api/cairn/v1/routes_forgejo.go` with the implementation thunks**

This file is the concrete Forgejo binding. Read `services/context/` (or wherever Forgejo's auth context lives) to get the actual API.

```go
package v1

import (
	"context"
	"net/http"

	cairnidentity "github.com/CarriedWorldUniverse/cairn/services/cairn/identity"
	"github.com/go-chi/chi/v5"

	// ADJUST IMPORT: this is Forgejo's services/context package which
	// holds the authed user. Verify the actual import path in this
	// Forgejo version.
	services_context "github.com/CarriedWorldUniverse/cairn/services/context"
)

func extractForgejoUserActual(ctx context.Context) *cairnidentity.Caller {
	c := services_context.GetWebContext(ctx)
	if c == nil || c.Doer == nil {
		return nil
	}
	return &cairnidentity.Caller{
		UserID:   c.Doer.ID,
		Username: c.Doer.Name,
	}
}

func chiURLParamActual(r *http.Request, name string) string {
	return chi.URLParam(r, name)
}
```

NOTE: the import paths and exact API for Forgejo's services/context are version-specific. If the calls don't compile, find the right package via grep:

```bash
grep -rn 'Doer ' services/context/ models/context/ 2>/dev/null | head
```

Document the actual path used in the commit message.

- [ ] **Step 5: Mount the routes in Forgejo's `routers/web/web.go`**

Find where API routes are registered. Add Cairn's route mount. Example pattern (the actual location varies by Forgejo version):

```go
// Within the appropriate route group in routers/web/web.go, add:

m.Group("/api/cairn/v1", func() {
    cairnv1.MountRoutes(chiRouteGroupAdapter{m})
}, /* any required middleware */)
```

If Forgejo's router doesn't directly satisfy `cairnv1.RouteGroup`, write a small adapter type in `routers/web/web.go`:

```go
type chiRouteGroupAdapter struct {
    m *web.Route // or whatever Forgejo's actual router type is
}

func (a chiRouteGroupAdapter) Get(pattern string, h http.HandlerFunc)  { a.m.Get(pattern, h) }
func (a chiRouteGroupAdapter) Post(pattern string, h http.HandlerFunc) { a.m.Post(pattern, h) }
```

- [ ] **Step 6: Wire `cairnv1.Init` into the server startup sequence**

Find where Forgejo initializes its services at startup (typically `cmd/web.go` or a function called from there). Add the Cairn init right after the database is ready and before HTTP listens:

```go
// In the startup sequence:
import cairnv1 "github.com/CarriedWorldUniverse/cairn/routers/api/cairn/v1"

// ...

if err := cairnv1.Init(ctx, setting.Cairn.HMACKeyPath, /* store */, /* blocklist */, /* users */); err != nil {
    log.Fatal("cairn: %v", err)
}
```

The store/blocklist constructions need real `*xorm.Engine` from Forgejo's `models/db`. Use `db.GetEngine(ctx)` to get the engine.

The user resolver is `cairnv1.NewForgejoUserResolver()`.

- [ ] **Step 7: Add `setting.Cairn.HMACKeyPath` to Forgejo's settings**

Modify `modules/setting/cairn.go` (create if doesn't exist):

```go
package setting

// Cairn holds Cairn-specific configuration loaded from app.ini's
// [cairn] section.
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
var Cairn = struct {
	Enabled                       bool
	EnforceSignatures             bool
	RejectOrphanAgents            bool
	HMACKeyPath                   string
	MarkdownEndpointsEnabled      bool
	WALCheckpointIntervalMinutes  int
}{
	Enabled:                      true,
	EnforceSignatures:            false,
	RejectOrphanAgents:           true,
	HMACKeyPath:                  "/etc/cairn/instance-hmac.key",
	MarkdownEndpointsEnabled:     true,
	WALCheckpointIntervalMinutes: 5,
}

func loadCairnFrom(rootCfg ConfigProvider) {
	sec := rootCfg.Section("cairn")
	Cairn.Enabled = sec.Key("enabled").MustBool(true)
	Cairn.EnforceSignatures = sec.Key("enforce_signatures").MustBool(false)
	Cairn.RejectOrphanAgents = sec.Key("reject_orphan_agents").MustBool(true)
	Cairn.HMACKeyPath = sec.Key("hmac_key_path").MustString("/etc/cairn/instance-hmac.key")
	Cairn.MarkdownEndpointsEnabled = sec.Key("markdown_endpoints_enabled").MustBool(true)
	Cairn.WALCheckpointIntervalMinutes = sec.Key("wal_checkpoint_interval_minutes").MustInt(5)
}
```

Then call `loadCairnFrom(rootCfg)` from Forgejo's `loadFromConf` function in `modules/setting/setting.go` (or equivalent).

- [ ] **Step 8: Run a smoke test against a live binary**

Build, run, curl:

```bash
go build -o /tmp/cairn-api-test .

mkdir -p /tmp/cairn-api-data /etc/cairn 2>/dev/null
sudo touch /etc/cairn/instance-hmac.key 2>/dev/null  # or use a path you have write access to
# Adjust HMACKeyPath in app.ini if /etc/cairn isn't writable.

cat > /tmp/cairn-api-test.ini <<EOF
[database]
DB_TYPE = sqlite3
PATH = /tmp/cairn-api-data/cairn.db

[repository]
ROOT = /tmp/cairn-api-data/repos

[server]
APP_DATA_PATH = /tmp/cairn-api-data
DOMAIN = localhost
HTTP_PORT = 3000

[cairn]
hmac_key_path = /tmp/cairn-api-data/instance-hmac.key
EOF

/tmp/cairn-api-test migrate --config /tmp/cairn-api-test.ini
/tmp/cairn-api-test web --config /tmp/cairn-api-test.ini &
SERVER_PID=$!
sleep 3

# Exercise the registration endpoint anonymously (should get pending).
curl -i -X POST http://localhost:3000/api/cairn/v1/agents \
    -H 'Content-Type: application/json' \
    -d '{"proposed_owner":"alice","slug":"plumb","domain":"darksoft.co.nz","public_key":"6663f5c30b8e08e0e4f5c30b8e08e0e4f5c30b8e08e0e4f5c30b8e08e0e4f5c3"}'

# Note: 404 expected if alice isn't a Forgejo user yet — that's fine,
# it confirms the endpoint is wired and reaching the user resolver.

kill $SERVER_PID
rm -rf /tmp/cairn-api-test /tmp/cairn-api-data /tmp/cairn-api-test.ini
```

Expected: an HTTP response from the server (200, 404, or whatever — the point is the endpoint exists and doesn't 404 from "no route"). If you get "404 page not found" from Go's mux on the URL itself, the route isn't mounted.

- [ ] **Step 9: Commit**

```bash
git add routers/api/cairn/v1/routes.go routers/api/cairn/v1/routes_forgejo.go routers/web/web.go modules/setting/cairn.go modules/setting/setting.go cmd/web.go
git commit -m "$(cat <<'EOF'
feat(cairn): mount /api/cairn/v1 routes + load instance HMAC at startup

Wires the Cairn API endpoints (Tasks 3-6) into Forgejo's HTTP router
and adds the startup init that loads the instance HMAC key once and
constructs the AgentService.

Files:
- routers/api/cairn/v1/routes.go — Init() + MountRoutes() + the small
  RouteGroup interface to keep Cairn agnostic of Forgejo's router type
- routers/api/cairn/v1/routes_forgejo.go — concrete Forgejo bindings
  (extract Doer from services/context, chi.URLParam for path params)
- routers/web/web.go — small additive patch to register Cairn's
  /api/cairn/v1 group
- modules/setting/cairn.go — Cairn-specific config flags ([cairn]
  section in app.ini)
- modules/setting/setting.go — call loadCairnFrom() during startup
- cmd/web.go — invoke cairnv1.Init() after DB ready, before listen

Smoke-tested against a live binary: POST /api/cairn/v1/agents reaches
the handler and returns the expected 404 for an unknown owner.

Refs: docs/cairn/specs/2026-05-09-cairn-foundation-design.md §4.3, §10

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
git push -u origin cairn-api-route-mount
```

Then merge to `cairn`.

---

## End-of-plan verification

After all 7 tasks land:

- [ ] **Run the full test suite**

```bash
cd ~/Source/cairn
git checkout cairn
git pull
go test ./services/cairn/... ./models/cairn/... ./routers/api/cairn/...
```

Expected: all tests pass.

- [ ] **End-to-end live API smoke test**

Build, migrate, run, curl the registration flow:

```bash
go build -o /tmp/cairn-plan2-test .
mkdir -p /tmp/cairn-plan2-data
cat > /tmp/cairn-plan2.ini <<EOF
[database]
DB_TYPE = sqlite3
PATH = /tmp/cairn-plan2-data/cairn.db
[repository]
ROOT = /tmp/cairn-plan2-data/repos
[server]
APP_DATA_PATH = /tmp/cairn-plan2-data
DOMAIN = localhost
HTTP_PORT = 3000
[security]
INSTALL_LOCK = true
[cairn]
hmac_key_path = /tmp/cairn-plan2-data/instance-hmac.key
EOF

/tmp/cairn-plan2-test migrate --config /tmp/cairn-plan2.ini
/tmp/cairn-plan2-test admin user create --config /tmp/cairn-plan2.ini \
    --username alice --email nexus@darksoft.co.nz --password test12345 --admin

/tmp/cairn-plan2-test web --config /tmp/cairn-plan2.ini &
sleep 3

# Generate a real Ed25519 keypair and register.
PUBKEY_HEX=$(go run -v - <<'GO'
package main
import (
    "crypto/ed25519"
    "crypto/rand"
    "encoding/hex"
    "fmt"
)
func main() {
    pub, _, _ := ed25519.GenerateKey(rand.Reader)
    fmt.Print(hex.EncodeToString(pub))
}
GO
)

curl -i -X POST http://localhost:3000/api/cairn/v1/agents \
    -H 'Content-Type: application/json' \
    -d "{\"proposed_owner\":\"alice\",\"slug\":\"plumb\",\"domain\":\"darksoft.co.nz\",\"public_key\":\"$PUBKEY_HEX\"}"

# Should return 201 with status="pending" (anonymous, so not auto-approved)
```

Expected: HTTP 201 with a JSON body containing `"fingerprint": "cairn:..."` and `"status": "pending"`.

Clean up:

```bash
pkill -f cairn-plan2-test
rm -rf /tmp/cairn-plan2-test /tmp/cairn-plan2-data /tmp/cairn-plan2.ini
```

- [ ] **Notify the operator**

Plan 2a complete. Cairn now has a working registration API. Plan 2b (CLI) builds the operator-side tooling that calls these endpoints.

---

## Notes for the executing agent

- This plan touches Forgejo upstream code in two places (`routers/web/web.go`, `cmd/web.go`, `modules/setting/setting.go`). All other changes are in cairn-namespaced packages. Keep the upstream-touching diffs minimal.
- Forgejo version-specific APIs may differ from what's described here. If the actual API doesn't match (e.g., `services_context.GetWebContext` doesn't exist), grep the codebase to find the right symbol. Document deviations in commit messages.
- The Cairn handlers are tested via `httptest.NewRecorder()` with `Caller` injected directly via `WithCaller`. Production wiring routes through Forgejo's auth middleware; the route-mount task (Task 7) is where that integration actually happens. Tasks 3-6 are deliberately decoupled from Forgejo's runtime.
- The `Init()` global pattern (Task 7) is unfortunate but matches Forgejo's conventions. Cairn doesn't currently have a service-locator/DI framework; converting to one is post-MVP scope.
- Per Plan 1's task #18, the blocklist `UNIQUE(agent_id)` schema-parity test is a Plan 3 concern, not Plan 2. Don't add it here.
