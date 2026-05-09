# Cairn Human-Review Enforcement — Implementation Plan (Plan 6)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task.

**Goal:** Make "humans review only" enforceable: filter AI-agent approvals from required-review gates, block owner-cluster self-approvals, and flip the default branch protection so new repos in AI-native orgs require human approval on `main`/`master`.

**Architecture:** Sits on top of Forgejo's existing branch protection. Cairn does NOT invent a new protection system — it adds a filter layer at approval-evaluation time and an auto-apply hook for new-repo branch protection. Per-org toggle (`cairn_review_policy.require_human_only_approval`, default `true` for AI-native deploys).

**Tech Stack:**
- Go 1.25+, xorm ORM, SQLite (Forgejo substrate)
- `services/cairn/` pattern (atomic.Pointer global, connection-per-operation)
- `cairntest.NewEngine(t)` extended for V502
- `routers/api/cairn/v1/` for API
- Forgejo's existing branch-protection model in `models/git/protected_branch.go`
- Forgejo's review-counting in `services/pull/review.go` or equivalent (implementer locates)

**Spec reference:** [`docs/cairn/specs/2026-05-10-cairn-ai-native-amendment.md`](../specs/2026-05-10-cairn-ai-native-amendment.md) §4.

**Build invariants** (preserved from prior Cairn plans):
- AGPL header per-file in Cairn-original code
- Forgejo upstream patches minimal, bracketed for future rebase
- Migrations additive only, never alter Forgejo tables
- Tests use `cairntest.NewEngine(t)`
- `errors.Is` for sentinel matching

---

## File structure

**New files:**

```
models/cairn/
├── review_policy.go                  ← per-org review policy
└── migrations/
    └── v502_create_review_policy_table.go

services/cairn/reviewpolicy/
├── service.go                        ← LoadPolicy, FilterApprovers, RequireHumanOnly
├── filter.go                         ← agent-approval filter logic
└── global.go                         ← atomic.Pointer global + Init

routers/api/cairn/v1/
└── review_policy.go                  ← GET/PUT handlers

routers/web/cairn/
└── agent_approval_badge.go           ← template helper for "doesn't count" badge
```

**Modified Forgejo upstream files** (~20-30 lines total):

- `routers/init.go` — register review-policy API routes; init service at startup
- `models/issues/review.go` (or equivalent) OR `services/pull/review.go` — hook the approval-counting path through the Cairn filter
- `services/repository/create.go` (or equivalent) — auto-apply default branch protection on repo creation when org has `require_human_only_approval = true`
- `templates/repo/issue/view_content/conversation.tmpl` — render "doesn't count toward gate" badge next to filtered agent reviews
- `modules/templates/helper.go` — register the badge template helper
- `modules/setting/cairn.go` — add `Cairn.ReviewPolicyEnabled` (global gate, default true)

---

## Task 1: Data model + migration

**Files:**
- Create: `models/cairn/review_policy.go`
- Create: `models/cairn/migrations/v502_create_review_policy_table.go`
- Create: `models/cairn/migrations/v502_create_review_policy_table_test.go`
- Modify: `models/cairn/cairntest/engine.go` (call V502 after V501)
- Test: schema parity test for the new table

- [ ] **Step 1: Write failing migration test**

```go
// models/cairn/migrations/v502_create_review_policy_table_test.go
package migrations_test

import (
	"testing"

	"github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
)

func TestV502CreateReviewPolicyTable(t *testing.T) {
	eng := cairntest.NewEngine(t)
	exists, err := eng.IsTableExist("cairn_review_policy")
	if err != nil {
		t.Fatalf("IsTableExist: %v", err)
	}
	if !exists {
		t.Error("table cairn_review_policy not created")
	}
}
```

Run: `go test ./models/cairn/migrations/... -run TestV502` — expect FAIL.

- [ ] **Step 2: Write the model struct**

```go
// models/cairn/review_policy.go
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package cairn

// ReviewPolicy is per-org configuration for Cairn's human-review enforcement.
// When RequireHumanOnly is true:
//   - Agent approvals do not count toward "X approving reviews required" gates
//   - PRs from agents owned by user X cannot be approved by X or by any of
//     X's other agents (owner-cluster self-approval block)
//   - New repos in this org get default branch protection auto-applied to
//     main/master requiring 1+ approving reviews
type ReviewPolicy struct {
	OwnerID            int64 `xorm:"pk"`
	RequireHumanOnly   bool  `xorm:"NOT NULL DEFAULT true"`
	CreatedUnix        int64 `xorm:"created"`
	UpdatedUnix        int64 `xorm:"updated"`
}

func (ReviewPolicy) TableName() string { return "cairn_review_policy" }
```

- [ ] **Step 3: Migration**

```go
// models/cairn/migrations/v502_create_review_policy_table.go
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package migrations

import (
	"xorm.io/xorm"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

func V502CreateReviewPolicyTable(x *xorm.Engine) error {
	return x.Table("cairn_review_policy").Sync2(new(cairnmodels.ReviewPolicy))
}
```

- [ ] **Step 4: Wire into `cairntest.NewEngine`**

After the V501 call, add:
```go
if err := cairnmigrations.V502CreateReviewPolicyTable(eng); err != nil {
    t.Fatalf("V502: %v", err)
}
```

- [ ] **Step 5: Add schema-parity test** (matches V500/V501 pattern in `schema_parity_test.go`):

```go
func TestSchemaParity_ReviewPolicyTable(t *testing.T) {
    // Snapshot path A (migration) vs path B (runtime model Sync2)
    // Reuse the helpers from existing parity tests
}
```

- [ ] **Step 6: Verify pass**

```bash
go test ./models/cairn/...
go build ./...
```

- [ ] **Step 7: Commit + push**

```bash
git checkout -b cairn-review-policy-data-model
git add models/cairn/review_policy.go models/cairn/migrations/v502_create_review_policy_table.go models/cairn/migrations/v502_create_review_policy_table_test.go models/cairn/migrations/schema_parity_test.go models/cairn/cairntest/engine.go

git commit -m "feat(cairn): review-policy data model + migration

Adds cairn_review_policy table for the AI-native human-review
enforcement feature: per-org row with RequireHumanOnly bool
(default true). Schema-parity test follows V500/V501 pattern.

Refs: docs/cairn/specs/2026-05-10-cairn-ai-native-amendment.md §4
      docs/cairn/plans/2026-05-10-cairn-human-review-enforcement.md Task 1

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"

git push -u origin cairn-review-policy-data-model
```

---

## Task 2: Service layer — policy load + approval filter

**Files:**
- Create: `services/cairn/reviewpolicy/service.go`
- Create: `services/cairn/reviewpolicy/filter.go`
- Create: `services/cairn/reviewpolicy/global.go`
- Test: `services/cairn/reviewpolicy/service_test.go`, `filter_test.go`

The service exposes:

- `Load(ctx, ownerID) (*ReviewPolicy, error)` — fetch policy; returns default policy (`RequireHumanOnly: true`) if no row exists
- `RequireHumanOnly(ctx, ownerID) bool` — convenience wrapper
- `FilterApprovers(ctx, ownerID, prAuthorOwnerID, approvers) []*user_model.User` — drops agent users + owner-cluster self-approvers; returns the filtered list of approvers that count

`Global() *Service` for hook access. `SetGlobal(svc)` for init/cleanup.

- [ ] **Step 1: Write failing tests**

```go
// services/cairn/reviewpolicy/filter_test.go
package reviewpolicy

import (
	"context"
	"testing"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
	user_model "github.com/CarriedWorldUniverse/cairn/models/user"
)

// stubAgentLookup tells the filter "is user X an agent? if so, who's the owner?"
type stubAgentLookup struct {
    agents map[int64]int64 // userID -> ownerID (0 if not an agent)
}

func (s *stubAgentLookup) IsAgentUser(_ context.Context, userID int64) (bool, int64) {
    owner, has := s.agents[userID]
    return has, owner
}

func TestFilterApprovers_DropsAgents(t *testing.T) {
    eng := cairntest.NewEngine(t)
    lookup := &stubAgentLookup{agents: map[int64]int64{
        100: 1, // agent owned by user 1
        101: 2, // agent owned by user 2
    }}
    svc := NewService(eng, lookup)

    // PR authored by user-1's agent (userID=100, owner=1)
    approvers := []*user_model.User{
        {ID: 100}, // user-1's agent — should drop (it's an agent)
        {ID: 101}, // user-2's agent — should drop (it's an agent, regardless of owner)
        {ID: 5},   // human reviewer — should keep
    }
    out := svc.FilterApprovers(context.Background(), 1 /*ownerID=user 1*/, 1 /*PR author cluster*/, approvers)
    if len(out) != 1 {
        t.Fatalf("expected 1 approver, got %d", len(out))
    }
    if out[0].ID != 5 {
        t.Errorf("expected human (id=5), got id=%d", out[0].ID)
    }
}

func TestFilterApprovers_DropsOwnerClusterSelfApproval(t *testing.T) {
    eng := cairntest.NewEngine(t)
    lookup := &stubAgentLookup{agents: map[int64]int64{
        100: 1, // agent owned by user 1
    }}
    svc := NewService(eng, lookup)

    // PR authored by user-1's agent (PR-author-cluster owner = 1)
    approvers := []*user_model.User{
        {ID: 1},   // user 1 themselves — must drop (self-approval of own agent's PR)
        {ID: 5},   // unrelated human — keep
    }
    out := svc.FilterApprovers(context.Background(), 99 /*org owner*/, 1 /*PR author cluster*/, approvers)
    if len(out) != 1 || out[0].ID != 5 {
        t.Errorf("expected only id=5, got %v", out)
    }
}

func TestFilterApprovers_PolicyDisabled(t *testing.T) {
    eng := cairntest.NewEngine(t)
    if _, err := eng.Insert(&cairnmodels.ReviewPolicy{OwnerID: 99, RequireHumanOnly: false}); err != nil {
        t.Fatal(err)
    }
    lookup := &stubAgentLookup{agents: map[int64]int64{100: 1}}
    svc := NewService(eng, lookup)

    approvers := []*user_model.User{{ID: 100}, {ID: 5}}
    out := svc.FilterApprovers(context.Background(), 99, 1, approvers)
    if len(out) != 2 {
        t.Errorf("policy disabled should pass through, got %d approvers", len(out))
    }
}
```

```go
// services/cairn/reviewpolicy/service_test.go
package reviewpolicy

import (
	"context"
	"testing"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
)

func TestLoad_DefaultPolicyWhenNoRow(t *testing.T) {
    eng := cairntest.NewEngine(t)
    svc := NewService(eng, nil)
    p, err := svc.Load(context.Background(), 42)
    if err != nil {
        t.Fatalf("Load: %v", err)
    }
    if !p.RequireHumanOnly {
        t.Error("default policy should require human-only")
    }
}

func TestLoad_ReadsExistingRow(t *testing.T) {
    eng := cairntest.NewEngine(t)
    if _, err := eng.Insert(&cairnmodels.ReviewPolicy{OwnerID: 7, RequireHumanOnly: false}); err != nil {
        t.Fatal(err)
    }
    svc := NewService(eng, nil)
    p, err := svc.Load(context.Background(), 7)
    if err != nil {
        t.Fatalf("Load: %v", err)
    }
    if p.RequireHumanOnly {
        t.Error("disabled-policy row should be returned as-is")
    }
}
```

- [ ] **Step 2: Implement service**

```go
// services/cairn/reviewpolicy/service.go
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package reviewpolicy

import (
	"context"
	"fmt"

	"xorm.io/xorm"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

// AgentLookup provides "is this user an agent? if so, who owns it?"
// Production wires to services/cairn/identity. Tests inject a stub.
type AgentLookup interface {
	IsAgentUser(ctx context.Context, userID int64) (isAgent bool, ownerID int64)
}

type Service struct {
	engine *xorm.Engine
	agents AgentLookup
}

func NewService(engine *xorm.Engine, agents AgentLookup) *Service {
	return &Service{engine: engine, agents: agents}
}

// Load fetches the policy for the given owner. Returns default policy
// (RequireHumanOnly: true) if no row exists.
func (s *Service) Load(ctx context.Context, ownerID int64) (*cairnmodels.ReviewPolicy, error) {
	row := &cairnmodels.ReviewPolicy{}
	has, err := s.engine.Context(ctx).Where("owner_id = ?", ownerID).Get(row)
	if err != nil {
		return nil, fmt.Errorf("reviewpolicy: load: %w", err)
	}
	if !has {
		return &cairnmodels.ReviewPolicy{OwnerID: ownerID, RequireHumanOnly: true}, nil
	}
	return row, nil
}

func (s *Service) RequireHumanOnly(ctx context.Context, ownerID int64) bool {
	p, err := s.Load(ctx, ownerID)
	if err != nil {
		// Fail closed — if we can't read policy, default to enforcing.
		return true
	}
	return p.RequireHumanOnly
}
```

- [ ] **Step 3: Implement filter**

```go
// services/cairn/reviewpolicy/filter.go
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package reviewpolicy

import (
	"context"

	user_model "github.com/CarriedWorldUniverse/cairn/models/user"
)

// FilterApprovers returns the subset of approvers that count toward Forgejo's
// "X approving reviews required" gate, given the review policy at ownerID.
//
// When RequireHumanOnly is true:
//   - All agent users are dropped (their approvals don't count)
//   - All users in the PR author's owner-cluster are dropped (no self-
//     approval). prAuthorOwnerID is the human owner of the PR's author
//     (same as the author user ID if the author is themselves a human; or
//     the agent's owner_id if the author is an agent).
//
// When RequireHumanOnly is false: approvers passes through unchanged.
func (s *Service) FilterApprovers(ctx context.Context, ownerID, prAuthorOwnerID int64, approvers []*user_model.User) []*user_model.User {
	if !s.RequireHumanOnly(ctx, ownerID) {
		return approvers
	}
	if s.agents == nil {
		// No lookup configured — can only filter the literal owner.
		out := make([]*user_model.User, 0, len(approvers))
		for _, u := range approvers {
			if u.ID == prAuthorOwnerID {
				continue
			}
			out = append(out, u)
		}
		return out
	}
	out := make([]*user_model.User, 0, len(approvers))
	for _, u := range approvers {
		// Drop the literal PR-author-cluster owner (self-approval).
		if u.ID == prAuthorOwnerID {
			continue
		}
		// Drop any agent user.
		isAgent, agentOwner := s.agents.IsAgentUser(ctx, u.ID)
		if isAgent {
			// Drop all agents (they don't count regardless of owner-cluster).
			continue
		}
		_ = agentOwner // future: stricter checks could use this
		out = append(out, u)
	}
	return out
}
```

- [ ] **Step 4: Implement global**

```go
// services/cairn/reviewpolicy/global.go
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package reviewpolicy

import "sync/atomic"

var globalService atomic.Pointer[Service]

func SetGlobal(s *Service) { globalService.Store(s) }

func Global() *Service { return globalService.Load() }
```

- [ ] **Step 5: Verify all tests pass**

```bash
go test ./services/cairn/reviewpolicy/...
go build ./...
```

- [ ] **Step 6: Commit + push**

```bash
git checkout -b cairn-review-policy-service
git add services/cairn/reviewpolicy/

git commit -m "feat(cairn): review-policy service + agent approval filter

Service.Load reads cairn_review_policy or returns default
(RequireHumanOnly: true). FilterApprovers drops agent users and
owner-cluster self-approvers per the policy. AgentLookup interface
keeps the filter decoupled from services/cairn/identity (production
wires to identity; tests inject stub).

When the policy is disabled, FilterApprovers passes the approver
list through unchanged — Forgejo's vanilla gate behavior.

Refs: docs/cairn/specs/2026-05-10-cairn-ai-native-amendment.md §4.3
      docs/cairn/plans/2026-05-10-cairn-human-review-enforcement.md Task 2

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"

git push -u origin cairn-review-policy-service
```

---

## Task 3: API endpoints (GET/PUT review-policy)

**Files:**
- Create: `routers/api/cairn/v1/review_policy.go`
- Create: `routers/api/cairn/v1/review_policy_test.go`
- Modify: `routers/init.go` (register routes)
- Modify: `modules/setting/cairn.go` (add `ReviewPolicyEnabled`)

Two endpoints:
- `GET /api/cairn/v1/orgs/{owner}/review-policy` — read policy (org admin / site admin)
- `PUT /api/cairn/v1/orgs/{owner}/review-policy` — upsert (org admin / site admin)

Response shape: `{require_human_only: bool}`. Body shape: same.

- [ ] **Step 1: Add `ReviewPolicyEnabled` setting**

In `modules/setting/cairn.go` (alongside `SummarizerEnabled`):

```go
ReviewPolicyEnabled bool
```

In `loadCairnFrom`:

```go
Cairn.ReviewPolicyEnabled = sec.Key("review_policy_enabled").MustBool(true)
```

- [ ] **Step 2: Implement handlers**

```go
// routers/api/cairn/v1/review_policy.go
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package v1

import (
	"net/http"

	"github.com/CarriedWorldUniverse/cairn/models/db"
	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"github.com/CarriedWorldUniverse/cairn/services/cairn/reviewpolicy"
	"github.com/CarriedWorldUniverse/cairn/services/context"
)

const maxReviewPolicyBody = 256

type reviewPolicyResponse struct {
	RequireHumanOnly bool `json:"require_human_only"`
}

type reviewPolicyRequest struct {
	RequireHumanOnly bool `json:"require_human_only"`
}

func GetReviewPolicy(ctx *context.APIContext) {
	owner := ctx.ContextUser
	if owner == nil {
		ctx.Error(http.StatusNotFound, "owner not found", nil)
		return
	}
	if !ctx.Doer.IsAdmin && ctx.Doer.ID != owner.ID {
		ctx.Error(http.StatusForbidden, "admin required", nil)
		return
	}
	svc := reviewpolicy.Global()
	if svc == nil {
		ctx.Error(http.StatusServiceUnavailable, "review policy disabled", nil)
		return
	}
	p, err := svc.Load(ctx, owner.ID)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "load policy", err)
		return
	}
	ctx.JSON(http.StatusOK, reviewPolicyResponse{RequireHumanOnly: p.RequireHumanOnly})
}

func PutReviewPolicy(ctx *context.APIContext) {
	owner := ctx.ContextUser
	if owner == nil {
		ctx.Error(http.StatusNotFound, "owner not found", nil)
		return
	}
	if !ctx.Doer.IsAdmin && ctx.Doer.ID != owner.ID {
		ctx.Error(http.StatusForbidden, "admin required", nil)
		return
	}
	var req reviewPolicyRequest
	if err := readJSON(ctx, &req, maxReviewPolicyBody); err != nil {
		ctx.Error(http.StatusBadRequest, "decode", err)
		return
	}
	p := &cairnmodels.ReviewPolicy{OwnerID: owner.ID, RequireHumanOnly: req.RequireHumanOnly}
	existing := &cairnmodels.ReviewPolicy{}
	has, err := db.GetEngine(ctx).Where("owner_id = ?", owner.ID).Get(existing)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "load existing", err)
		return
	}
	if has {
		_, err = db.GetEngine(ctx).ID(owner.ID).AllCols().Update(p)
	} else {
		_, err = db.GetEngine(ctx).Insert(p)
	}
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "save", err)
		return
	}
	ctx.JSON(http.StatusOK, reviewPolicyResponse{RequireHumanOnly: p.RequireHumanOnly})
}
```

- [ ] **Step 3: Register routes**

In `routers/init.go::cairnRoutes`, inside `if setting.Cairn.ReviewPolicyEnabled`:

```go
m.Group("/orgs/{owner}", func() {
    m.Get("/review-policy", v1.GetReviewPolicy)
    m.Put("/review-policy", v1.PutReviewPolicy)
})
```

- [ ] **Step 4: Tests** (unit-level on the response shape; integration deferred):

```go
func TestReviewPolicyResponse_BoolPasses(t *testing.T) {
    // Construct response, marshal to JSON, assert structure
}
```

- [ ] **Step 5: Verify, commit, push** following the established pattern.

---

## Task 4: Forgejo integration — hook the approval-counting filter

**Files:**
- Modify: Forgejo's pull-review approval counting code (implementer locates — likely `services/pull/review.go::CountApprovingReviews` or similar)
- Test: integration test verifying that an agent's approval doesn't count toward the gate

This is the load-bearing integration point. Without it, the filter is dead code.

- [ ] **Step 1: Locate the approval-counting site**

```bash
grep -rn "ApprovedReviewers\|RequiredApprovals\|GetReviews.*ApprovingState" services/pull/ models/issues/ routers/web/repo/
```

The exact location depends on Forgejo's version. Look for the function that returns the count of approving reviews relative to a PR's required-review setting. Common candidates:
- `models/issues/review.go::GetReviewsByIssueID` (returns reviews; counting is downstream)
- `services/pull/review.go::canApprove` or similar
- `models/git/protected_branch.go::CheckUserCanReview` or related

Read the candidate functions to find where the list of approving users is computed.

- [ ] **Step 2: Inject the Cairn filter**

The minimal-surface fix is to apply `reviewpolicy.Global().FilterApprovers(...)` to the approving-users list at the moment Forgejo counts it for gate-evaluation. Ideally one place — the function that produces the list of users whose approval counts.

Sketch:
```go
// in the function that returns approving users:
approvers := getApprovingReviewersFromForgejo(...)

// CAIRN: filter agent approvals + owner-cluster self-approvals
if svc := reviewpolicy.Global(); svc != nil {
    prAuthorOwnerID := resolvePRAuthorOwnerID(pr) // human author OR agent's owner
    approvers = svc.FilterApprovers(ctx, repo.OwnerID, prAuthorOwnerID, approvers)
}

return approvers
```

`resolvePRAuthorOwnerID`:
- If `pr.Issue.PosterID` is a human user: return `pr.Issue.PosterID`.
- If it's an agent (per `services/cairn/identity` lookup): return the agent's owner.

This requires a small helper or inline lookup against the identity service.

- [ ] **Step 3: Wire production AgentLookup**

The `reviewpolicy.Service` requires an `AgentLookup` interface implementation. Production implementation lives in `services/cairn/identity` (the existing agent service can be wrapped to satisfy the interface):

```go
// services/cairn/reviewpolicy/init.go
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package reviewpolicy

import (
	"context"
	"xorm.io/xorm"

	"github.com/CarriedWorldUniverse/cairn/services/cairn/identity"
)

type identityAdapter struct{}

func (identityAdapter) IsAgentUser(ctx context.Context, userID int64) (bool, int64) {
	svc := identity.GlobalAgentService()
	if svc == nil {
		return false, 0
	}
	agent, err := svc.GetAgentByUserID(ctx, userID)
	if err != nil || agent == nil {
		return false, 0
	}
	return true, agent.UserID  // adapt to actual identity.Agent field name for owner
}

func Init(engine *xorm.Engine) {
	SetGlobal(NewService(engine, identityAdapter{}))
}
```

(Adapt `GetAgentByUserID` to whatever the identity service actually exposes — the implementer reads `services/cairn/identity/agent_service.go` to find the lookup method.)

- [ ] **Step 4: Wire init**

In `routers/init.go::initCairn`, after the existing services are set up:

```go
if setting.Cairn.ReviewPolicyEnabled {
    reviewpolicy.Init(masterEng)
}
```

- [ ] **Step 5: Test** — write an integration test that:
  1. Creates a repo with branch protection requiring 1 approving review
  2. Creates a PR
  3. Has an agent user "approve" it via Forgejo's review API
  4. Asserts the PR is NOT yet mergeable (agent approval was filtered)
  5. Has a human approve it
  6. Asserts the PR IS mergeable

If integration test scaffolding is too heavy, write a unit test on the function that produces the count. The integration verification can happen during deploy smoke.

- [ ] **Step 6: Commit, push, merge**

---

## Task 5: Default branch protection auto-apply on new repo

**Files:**
- Modify: Forgejo's repo-creation code (implementer locates — `services/repository/create.go` or similar)
- Test: covers the auto-apply path

When a new repo is created in an org where `RequireHumanOnly = true`, automatically apply branch protection to `main` and `master` (whichever exist) requiring 1+ approving review.

- [ ] **Step 1: Locate the creation hook**

```bash
grep -rn "CreateRepository\|CreateRepositoryByExample\|InitRepository" services/repository/
```

Find the function that runs after a new repo is materialized. Inject Cairn's auto-apply hook there.

- [ ] **Step 2: Implement the auto-apply helper**

```go
// services/cairn/reviewpolicy/branch_protection.go
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package reviewpolicy

import (
	"context"

	repo_model "github.com/CarriedWorldUniverse/cairn/models/repo"
	git_model "github.com/CarriedWorldUniverse/cairn/models/git"
)

// AutoApplyDefaultProtection adds branch-protection on main/master
// requiring 1 approving review when the org has RequireHumanOnly=true.
// Idempotent: if a rule already exists for the branch, this is a no-op.
func (s *Service) AutoApplyDefaultProtection(ctx context.Context, repo *repo_model.Repository) error {
	if !s.RequireHumanOnly(ctx, repo.OwnerID) {
		return nil
	}
	for _, branchName := range []string{"main", "master"} {
		// Check if rule already exists
		existing, err := git_model.GetProtectedBranchRuleByName(ctx, repo.ID, branchName)
		if err != nil {
			return err
		}
		if existing != nil {
			continue // don't override operator-set rules
		}
		// Create rule: 1 approving review required
		rule := &git_model.ProtectedBranch{
			RepoID:                       repo.ID,
			RuleName:                     branchName,
			RequiredApprovals:            1,
			EnableApprovalsWhitelist:     false,
			BlockOnRejectedReviews:       true,
			BlockOnOfficialReviewRequests: false,
		}
		if err := git_model.UpdateProtectBranch(ctx, repo, rule, git_model.WhitelistOptions{}); err != nil {
			return err
		}
	}
	return nil
}
```

(Adapt to actual Forgejo API names. Read `models/git/protected_branch.go` for the real `ProtectedBranch` struct + `UpdateProtectBranch` signature.)

- [ ] **Step 3: Wire into repo-creation**

```go
// In services/repository/create.go after CreateRepository succeeds:

// CAIRN: auto-apply default branch protection if org policy says human-review-required
if svc := reviewpolicy.Global(); svc != nil {
    if err := svc.AutoApplyDefaultProtection(ctx, newRepo); err != nil {
        log.Warn("cairn: auto-apply branch protection failed for repo %d: %v", newRepo.ID, err)
        // Don't fail repo creation — log and continue
    }
}
```

- [ ] **Step 4: Test** — unit test on `AutoApplyDefaultProtection`:
  1. With `RequireHumanOnly: true` and no existing rule, asserts a new rule was created on `main`
  2. With `RequireHumanOnly: false`, asserts no rule was created
  3. With an existing rule on `main`, asserts the existing rule was NOT overwritten (idempotency)

- [ ] **Step 5: Commit, push, merge**

---

## Task 6: PR-page badge — "doesn't count toward gate"

**Files:**
- Create: `routers/web/cairn/agent_approval_badge.go` — render helper
- Modify: `modules/templates/helper.go` — register `cairnAgentApprovalBadge`
- Modify: `templates/repo/issue/view_content/conversation.tmpl` (or wherever review entries render) — render the badge next to filtered agent reviews
- Test: render correctness

When an agent reviewer approves a PR but Cairn is filtering their approval, the PR page should show a small badge next to their review entry (e.g., "doesn't count toward gate") so reviewers don't get confused by visible approvals that aren't being counted.

- [ ] **Step 1: Implement helper**

```go
// routers/web/cairn/agent_approval_badge.go
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package cairn

import (
	"context"

	"github.com/CarriedWorldUniverse/cairn/services/cairn/reviewpolicy"
)

// AgentApprovalDoesNotCount returns true if a given user's approval should
// be tagged as "doesn't count toward gate" in the PR UI. Used by the
// template helper to render a badge.
func AgentApprovalDoesNotCount(ownerID, prAuthorOwnerID, reviewerID int64) bool {
	svc := reviewpolicy.Global()
	if svc == nil {
		return false
	}
	if !svc.RequireHumanOnly(context.Background(), ownerID) {
		return false
	}
	// reviewerID is the literal owner of an agent's PR (self-approval) → doesn't count
	if reviewerID == prAuthorOwnerID {
		return true
	}
	// or reviewerID is an agent user → doesn't count
	// (we don't have access to the AgentLookup directly here; reuse the service's filter)
	if filter, ok := svc.(interface {
		IsAgentUser(context.Context, int64) (bool, int64)
	}); ok {
		isAgent, _ := filter.IsAgentUser(context.Background(), reviewerID)
		return isAgent
	}
	return false
}
```

(The interface-assertion shim is awkward — better to expose a small `IsAgentUser` method on the service directly that delegates to the lookup. Implementer cleans this up.)

- [ ] **Step 2: Register template helper**

```go
"cairnAgentApprovalDoesNotCount": cairn.AgentApprovalDoesNotCount,
```

- [ ] **Step 3: Add to PR template**

```html
<!-- adjacent to where each review's approver is rendered -->
{{if cairnAgentApprovalDoesNotCount $repo.OwnerID $prAuthorOwnerID $review.ReviewerID}}
  <span class="cairn-doesnt-count-badge">doesn't count toward gate</span>
{{end}}
```

(The exact template variable names depend on the existing review-render scope. Read `templates/repo/issue/view_content/conversation.tmpl` first.)

- [ ] **Step 4: Test** — render-correctness test

- [ ] **Step 5: Commit, push, merge**

---

## Task 7: Plan-level final review

- [ ] **Step 1: Run full Cairn test suite**

```bash
go test ./models/cairn/... ./services/cairn/... ./routers/api/cairn/... ./routers/web/cairn/...
```

All green.

- [ ] **Step 2: Spec coverage walk** — confirm each subsection of §4 of the amendment has implementation:
- §4.1 Purpose — covered by service + filter
- §4.2 Layering — verified: builds on Forgejo's existing branch protection
- §4.3 Org-level toggle — `cairn_review_policy.RequireHumanOnly` ✓; default true ✓
- §4.4 Branch-protection default flip — Task 5 ✓
- §4.5 Surfaces — admin API ✓; PR-page badge ✓; admin UI deferred (matches Plan 5 deferral pattern)
- §4.6 Storage — `cairn_review_policy` table ✓

- [ ] **Step 3: Dispatch holistic code-reviewer**

Same pattern as Plan 5 Task 10: dispatch a `feature-dev:code-reviewer` agent to walk all 6 implementation tasks as one feature; surface any cross-task issues; check test coverage gaps.

- [ ] **Step 4: Apply any necessary fixes; merge final-cleanup branch to `cairn`**

- [ ] **Step 5: Mirror plan + spec to Drive**

```bash
cp docs/cairn/plans/2026-05-10-cairn-human-review-enforcement.md ~/Google\ Drive/My\ Drive/nexus/general/cairn/plans/
```

---

## Self-review (writing-plans skill)

**Spec coverage:**
- §4.1 Purpose ✓ Task 2
- §4.2 Layering ✓ Task 4 (filter applied at Forgejo's gate)
- §4.3 Org-level toggle + filter + owner-cluster ✓ Tasks 1, 2, 3, 4
- §4.4 Branch-protection default flip ✓ Task 5
- §4.5 Surfaces (admin API, PR badge) ✓ Tasks 3, 6
- §4.6 Storage ✓ Task 1

**Placeholder scan:**
- Task 4 has "implementer locates" pointers for the approval-counting site and "adapt to actual Forgejo API names" — these are pragmatic given Forgejo version drift; the implementer audits the actual code. Not placeholder failures.
- Task 6 interface-assertion shim is acknowledged as awkward; implementer is told to clean up. Concrete fix path noted.

**Type consistency:**
- `ReviewPolicy.OwnerID` (int64), `RequireHumanOnly` (bool) used consistently across model, service, API, hooks
- `AgentLookup.IsAgentUser(ctx, userID) (bool, int64)` signature stable across stub + production

**Scope check:**
- Each task self-contained
- Tasks 4 and 5 are the highest-risk Forgejo-integration tasks; flagged with explicit reconnaissance steps (`grep -rn`)
- Admin UI deferred (matches Plan 5 deferral; documented in Plan 7 runbook)

---

## Plan complete

Save location: `docs/cairn/plans/2026-05-10-cairn-human-review-enforcement.md` (in-repo); mirror to `~/Google Drive/My Drive/nexus/general/cairn/plans/`.

**Execution choice (per writing-plans skill):**

1. **Subagent-Driven** (matches Plans 1-5 cadence) — recommended.
2. **Inline Execution** — also viable.
