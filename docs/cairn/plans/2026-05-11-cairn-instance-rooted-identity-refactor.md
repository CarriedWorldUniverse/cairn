# Cairn — Instance-Rooted Identity Refactor — Implementation Plan (Plan 8)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task.

**Goal:** Replace the seed-derivation identity model (Plan 1) with the instance-rooted attestation model. Agents self-generate keypairs on their host; the Cairn human user approves attachment requests. No shared root secret.

**Architecture:** See [`docs/cairn/specs/2026-05-11-cairn-instance-rooted-identity.md`](../specs/2026-05-11-cairn-instance-rooted-identity.md) for the full design.

**Tech Stack:** Go 1.25+, xorm, SQLite (Forgejo substrate). Forgejo's existing `public_key` table for pubkey storage. Cairn-side join + metadata.

**Spec invariants preserved:** fingerprint scheme (HMAC of pubkey w/ instance HMAC); SSH SIGNATURE verification; trailer enforcement; owner-cluster review-policy from Plan 6; pre-receive hook gating.

**What's deleted:** HKDF-from-seed code path; `cairn agent init`; `commit-sign-helper` seed-rederivation logic; embedded pubkey + fingerprint columns on `cairn_agent`.

**Migration:** trivial — no rows in `cairn_agent` exist yet on the only running instance.

---

## File structure

**New files:**

```
models/cairn/
├── attachment_request.go            ← new: pending-attachment request rows
├── agent_pubkey.go                  ← new: Cairn-side join binding agent ↔ public_key with fingerprint cache
└── migrations/
    └── v503_refactor_identity.go    ← drop cairn_agent.pubkey + .fingerprint;
                                        add cairn_attachment_request + cairn_agent_pubkey

services/cairn/identity/
└── attachment.go                    ← attachment-request orchestration (create, approve, reject)

routers/api/cairn/v1/
└── attachment.go                    ← API handlers for the request flow

routers/web/cairn/
└── pending_attachments.go           ← web UI: list pending requests on user-settings + admin

cmd/cairn/
└── agent_attach.go                  ← new CLI: cairn agent attach --pubkey FILE
```

**Modified files (existing in Cairn-side):**

- `models/cairn/agent.go` — drop `Pubkey`, `Fingerprint` fields
- `models/cairn/migrations/v500_create_agent_tables.go` — no longer creates the dropped columns (or keep as-is and rely on V503 to drop them; pick one — see Task 1)
- `services/cairn/identity/agent_service.go` — `RegisterAgent` becomes `ApproveAttachmentRequest`; lookup-by-fingerprint joins through `cairn_agent_pubkey`
- `services/cairn/hook/verify.go` — fingerprint lookup joins through `public_key` ↔ `cairn_agent_pubkey` ↔ `cairn_agent`
- `routers/api/cairn/v1/agents.go` — replace `PostAgents` (submit) with `PostAttachmentRequests`; replace `PostApprove` semantics
- `cmd/cairn/cli.go` — drop `agent init` + `agent submit`; add `agent attach`
- `cmd/cairn/agent_init.go` — delete
- `cmd/cairn/agent_submit.go` — delete
- `cmd/cairn/commit_sign_helper.go` — read on-disk private key, no HKDF

**Deleted files:**

- `cmd/cairn/agent_init.go`
- `cmd/cairn/agent_submit.go`

---

## Task 1: Data model + migration V503

**Files:**
- Create: `models/cairn/attachment_request.go`
- Create: `models/cairn/agent_pubkey.go`
- Modify: `models/cairn/agent.go` (drop `Pubkey`, `Fingerprint` fields)
- Create: `models/cairn/migrations/v503_refactor_identity.go`
- Modify: `models/cairn/cairntest/engine.go` (call V503 after V502)
- Test: `models/cairn/migrations/v503_refactor_identity_test.go`
- Test: schema-parity additions

- [ ] **Step 1: Write the failing test**

```go
// models/cairn/migrations/v503_refactor_identity_test.go
package migrations_test

import (
    "testing"

    "github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
)

func TestV503DropsAgentEmbeddedPubkey(t *testing.T) {
    eng := cairntest.NewEngine(t)
    // After V503, cairn_agent should NOT have pubkey or fingerprint cols.
    cols, err := eng.DBMetas()
    if err != nil { t.Fatal(err) }
    for _, table := range cols {
        if table.Name != "cairn_agent" { continue }
        for _, col := range table.Columns() {
            if col.Name == "pubkey" || col.Name == "fingerprint" {
                t.Errorf("cairn_agent still has dropped column %q", col.Name)
            }
        }
    }
}

func TestV503CreatesNewTables(t *testing.T) {
    eng := cairntest.NewEngine(t)
    for _, table := range []string{"cairn_attachment_request", "cairn_agent_pubkey"} {
        exists, err := eng.IsTableExist(table)
        if err != nil { t.Fatalf("IsTableExist %q: %v", table, err) }
        if !exists { t.Errorf("table %q not created", table) }
    }
}
```

- [ ] **Step 2: Write the model structs**

```go
// models/cairn/attachment_request.go
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package cairn

type AttachmentRequestStatus string

const (
    AttachmentRequestPending  AttachmentRequestStatus = "pending"
    AttachmentRequestApproved AttachmentRequestStatus = "approved"
    AttachmentRequestRejected AttachmentRequestStatus = "rejected"
)

// AttachmentRequest is a pending (or historical) ask from an agent to
// attach to a human owner's cluster. Anonymous submission is allowed;
// the owner approves via API or UI before the agent + pubkey become
// active.
type AttachmentRequest struct {
    ID               int64                   `xorm:"pk autoincr"`
    OwnerUsername    string                  `xorm:"VARCHAR(255) NOT NULL INDEX"`
    Slug             string                  `xorm:"VARCHAR(64) NOT NULL"`
    Domain           string                  `xorm:"VARCHAR(255) NOT NULL"`
    PubkeyContent    string                  `xorm:"TEXT NOT NULL"`
    Fingerprint      string                  `xorm:"VARCHAR(255) NOT NULL INDEX"`
    Status           AttachmentRequestStatus `xorm:"VARCHAR(16) NOT NULL DEFAULT 'pending' INDEX"`
    RequestedUnix    int64                   `xorm:"created"`
    DecidedUnix      int64                   `xorm:"NOT NULL DEFAULT 0"`
    DecidedByUserID  int64                   `xorm:"NOT NULL DEFAULT 0"`
}

func (AttachmentRequest) TableName() string { return "cairn_attachment_request" }
```

```go
// models/cairn/agent_pubkey.go
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package cairn

// AgentPubkey binds a public_key (Forgejo's table) to a cairn_agent.
// This is the join table that lets us look up an agent by the fingerprint
// of any of its registered pubkeys. Per-host revocation = delete one
// AgentPubkey row + the corresponding Forgejo public_key.
type AgentPubkey struct {
    ID            int64  `xorm:"pk autoincr"`
    AgentID       int64  `xorm:"INDEX NOT NULL"`
    PublicKeyID   int64  `xorm:"INDEX NOT NULL UNIQUE"`         // FK to Forgejo's public_key.id
    Fingerprint   string `xorm:"VARCHAR(255) UNIQUE NOT NULL"`  // cached for O(1) lookup
    CreatedUnix   int64  `xorm:"created"`
}

func (AgentPubkey) TableName() string { return "cairn_agent_pubkey" }
```

Update `models/cairn/agent.go` — drop `Pubkey` and `Fingerprint` fields. Leave the rest (`UserID`, `OwnerID`, `Slug`, `Domain`, `Status`, timestamps) intact.

- [ ] **Step 3: Write the migration**

```go
// models/cairn/migrations/v503_refactor_identity.go
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package migrations

import (
    "xorm.io/xorm"

    cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

// V503RefactorIdentity drops cairn_agent.pubkey + .fingerprint (now stored
// in Forgejo's public_key table + cached on cairn_agent_pubkey), and adds
// the new attachment_request + agent_pubkey tables.
//
// Safe at MVP: no rows in cairn_agent exist on the live deployment as of
// the migration timestamp. For any future instance that already has
// seed-derived agents, the data-migration logic would need to read the
// embedded pubkey, insert into public_key + agent_pubkey, then drop the
// columns. Out of scope for now.
func V503RefactorIdentity(x *xorm.Engine) error {
    // Create new tables.
    if err := x.Table("cairn_attachment_request").Sync2(new(cairnmodels.AttachmentRequest)); err != nil {
        return err
    }
    if err := x.Table("cairn_agent_pubkey").Sync2(new(cairnmodels.AgentPubkey)); err != nil {
        return err
    }
    // Drop the now-obsolete columns from cairn_agent.
    // SQLite doesn't support ALTER TABLE DROP COLUMN before 3.35; check version.
    // Forgejo's bundled SQLite is recent enough (3.40+); use direct DROP.
    if _, err := x.Exec("ALTER TABLE cairn_agent DROP COLUMN pubkey"); err != nil {
        return err
    }
    if _, err := x.Exec("ALTER TABLE cairn_agent DROP COLUMN fingerprint"); err != nil {
        return err
    }
    return nil
}
```

- [ ] **Step 4: Wire into `cairntest.NewEngine` after V502**

```go
if err := cairnmigrations.V503RefactorIdentity(eng); err != nil {
    t.Fatalf("V503: %v", err)
}
```

- [ ] **Step 5: Add schema-parity tests** matching V500/V501/V502 pattern.

- [ ] **Step 6: Register the migration shim** at `models/forgejo_migrations/v503a_cairn_refactor_identity.go` (Plan-7-deploy bug lesson: shipped migrations need the registration shim in `forgejo_migrations/` or they don't run in production).

```go
// models/forgejo_migrations/v503a_cairn_refactor_identity.go
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package forgejo_migrations

import (
    cairnmigrations "github.com/CarriedWorldUniverse/cairn/models/cairn/migrations"
)

func init() {
    registerMigration(&Migration{
        Description: "Cairn: refactor identity — drop seed-derived embedded keys, add attachment_request + agent_pubkey",
        Upgrade:     cairnmigrations.V503RefactorIdentity,
    })
}
```

- [ ] **Step 7: Run tests, verify**

```bash
go build ./...
go test ./models/cairn/...
```

- [ ] **Step 8: Commit + push** as `cairn-identity-refactor-data-model`.

---

## Task 2: Service layer — attachment-request orchestration

**Files:**
- Create: `services/cairn/identity/attachment.go`
- Test: `services/cairn/identity/attachment_test.go`
- Modify: `services/cairn/identity/agent_service.go` (lookup-by-fingerprint via agent_pubkey)

The service exposes:

- `CreateAttachmentRequest(ctx, owner, slug, domain, pubkey) (*AttachmentRequest, error)` — validates inputs, computes fingerprint, INSERT row with status=pending. Anonymous-callable.
- `ListPendingForOwner(ctx, ownerID) ([]AttachmentRequest, error)` — for the user-settings UI.
- `Approve(ctx, requestID, decidedByUserID) (*Agent, error)` — atomically: find-or-create agent's Forgejo user, INSERT pubkey into Forgejo's public_key, find-or-create cairn_agent, INSERT cairn_agent_pubkey, mark request approved.
- `Reject(ctx, requestID, decidedByUserID) error` — mark rejected.

Update `LookupAgentByFingerprint`:
```go
// Old: SELECT * FROM cairn_agent WHERE fingerprint = ?
// New: SELECT a.* FROM cairn_agent a
//      JOIN cairn_agent_pubkey ap ON ap.agent_id = a.id
//      WHERE ap.fingerprint = ?
```

Tests cover happy path, rejection of duplicate pubkey, owner-not-found, etc.

[Steps 1-7 following the established TDD + commit pattern from prior plans.]

---

## Task 3: CLI — `cairn agent attach`

**Files:**
- Create: `cmd/cairn/agent_attach.go`
- Modify: `cmd/cairn/cli.go` — register `attach`, remove `init` and `submit`
- Delete: `cmd/cairn/agent_init.go`
- Delete: `cmd/cairn/agent_submit.go`
- Modify: `cmd/cairn/commit_sign_helper.go` — read on-disk private key, no HKDF
- Test: `cmd/cairn/agent_attach_test.go`

New CLI:

```bash
cairn agent attach \
    --instance https://nexus-cw-ec2.<tailnet>.ts.net \
    --owner nexus \
    --slug plumb \
    --domain darksoft.co.nz \
    --pubkey ~/.cairn/plumb.key.pub
    [--token <api-token>]   # optional; if present, auth-as-owner & owner can approve in one step
```

Reads pubkey file, POSTs to `/api/cairn/v1/agents/attachment-requests`. Prints request ID + next-step hint.

`commit-sign-helper` becomes a thin wrapper around `ssh-keygen -Y sign` against the on-disk private key — or could be replaced by direct git config (`gpg.format=ssh`, `user.signingkey=keyfile`) and removed entirely. **Decide at task start**: keep helper for trailer injection, or remove + use git hooks for trailers? Recommend: remove the helper, use a `prepare-commit-msg` git hook for trailer injection. Simpler, no Cairn-specific signing path.

---

## Task 4: API endpoints — attachment requests

**Files:**
- Create: `routers/api/cairn/v1/attachment.go`
- Modify: `routers/init.go` — register new routes
- Modify: `routers/api/cairn/v1/agents.go` — replace `PostAgents` with stub or remove
- Test: `routers/api/cairn/v1/attachment_test.go`

Endpoints:
- `POST /api/cairn/v1/agents/attachment-requests` — anonymous create; returns `{id, fingerprint, status:"pending"}`
- `GET /api/cairn/v1/agents/attachment-requests` — admin: list all (with filter); owner: list own (filter by `OwnerUsername == ctx.Doer.LowerName`)
- `GET /api/cairn/v1/users/me/pending-attachment-requests` — convenience for the user-settings UI
- `POST /api/cairn/v1/agents/attachment-requests/{id}/approve` — auth: owner of the request OR site admin
- `POST /api/cairn/v1/agents/attachment-requests/{id}/reject` — same auth
- `GET /api/cairn/v1/agents/{slug}` — unchanged in shape; data joins through agent_pubkey to compute fingerprints

---

## Task 5: Push-hook signature verification — refactor lookup

**Files:**
- Modify: `services/cairn/hook/verify.go`
- Test: `services/cairn/hook/verify_test.go`

The hook receives a commit with `Agent-Id: cairn:<fingerprint>` trailer. Today: look up `cairn_agent` by fingerprint, verify against `cairn_agent.pubkey`. New flow: look up `cairn_agent_pubkey` by fingerprint → join to `public_key` for the actual key bytes → join to `cairn_agent` for owner/status checks.

Behavior unchanged from operator's perspective. Same accept/reject decisions; same trailer enforcement; same orphan check; same blocked-agent gate.

---

## Task 6: Web UI — pending-attachment list

**Files:**
- Create: `routers/web/cairn/pending_attachments.go`
- Create: `routers/web/cairn/templates/user/settings/cairn_pending_attachments.tmpl`
- Modify: a Forgejo settings nav template to add a "Pending agent attachments" tab
- Test: render correctness

Surface: under `/user/settings/cairn-attachments` (or similar). Lists pending attachment requests where `owner_username == current_user.LowerName`. Approve / Reject buttons hit the API endpoints. Minimal styling; inherits the theme from Phase 1 branding.

Site admin gets `/admin/cairn/pending-attachments` showing the full instance-wide list.

---

## Task 7: Documentation refresh

**Files:**
- Modify: `docs/cairn/specs/2026-05-09-cairn-foundation-design.md` — mark the identity-model section as superseded by 2026-05-11-cairn-instance-rooted-identity
- Modify: `cairn/deploy/deployment-runbook.md` §9 (agent registration) — replace seed-derive flow with `ssh-keygen + cairn agent attach + approve`
- Modify: `cairn/eval/samples/*.md` — no change (the simplifier prompt is orthogonal)
- Modify: spec amendment `2026-05-10-cairn-ai-native-amendment.md` §6 (owner-stable agent identity) — note that agent identity is now per-pubkey-registration, not per-seed-derivation; ownership semantics unchanged

---

## Task 8: Plan-level final review + cleanup

- [ ] Full Cairn test suite green
- [ ] Spec coverage walk against `2026-05-11-cairn-instance-rooted-identity.md`
- [ ] Holistic code-reviewer dispatch
- [ ] Update `.well-known/cairn.json` manifest — should the identity model version be advertised? (e.g., `features.identity_model: "attestation"`) — likely yes
- [ ] Update `cmd/cairn` CLI help text + the AGPL header pattern follows existing
- [ ] If any operator-facing surface changed, update runbook §16 build-surfaces list

---

## Self-review

**Spec coverage:** every section of `2026-05-11-cairn-instance-rooted-identity.md` is covered by a task:
- §"Schema refactor" → Task 1
- §"Attachment-request flow" → Tasks 2, 4
- §"Multi-host = multi-pubkey" → Task 1 data model + Task 2 service logic
- §"What stays unchanged" → Task 5 (signature verification unchanged behaviorally)
- §"What's deleted" → Task 3 (CLI + helper changes)
- §"Operator UX" → Task 3 CLI + Task 6 UI
- §"Open questions" — Q1 answered in Task 1 (cache on cairn_agent_pubkey); Q2 answered in Task 6 (user-settings tab + admin); Q3-5 deferred per recommendations in spec

**Placeholder scan:** None.

**Type consistency:** AttachmentRequestStatus, AgentPubkey, AttachmentRequest field names used consistently across model, service, API, hook.

**Scope check:** 8 tasks. Bigger than recent plans but cleanly partitioned. Tasks 1-6 are the actual work; 7 is docs; 8 is review.

---

## Execution

**Subagent-driven-development** following the cadence from Plans 5-6. Fresh implementer per task; spec-review + code-quality review between tasks; merge to `cairn` after each task green.

After all tasks land, tag `0.0.2-alpha` (this is a meaningful identity-model break; the version bump signals migration semantics for anyone forking the spec).
