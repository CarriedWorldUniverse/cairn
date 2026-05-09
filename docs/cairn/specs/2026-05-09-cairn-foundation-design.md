# Cairn Foundation MVP — Design Spec

**Date:** 2026-05-09
**Status:** Draft for team review
**Authors:** Jacinta + Plumb (brainstorm). To be reviewed by Anvil, Forge, Verity (when filled), Keel.
**Refs:** [`docs/brainstorm/2026-05-07-initial-thoughts.md`](../../brainstorm/2026-05-07-initial-thoughts.md), [`docs/brainstorm/2026-05-07-team-consult-keel.md`](../../brainstorm/2026-05-07-team-consult-keel.md), [`docs/brainstorm/2026-05-07-team-consult-anvil.md`](../../brainstorm/2026-05-07-team-consult-anvil.md)

This spec is the validated output of the 2026-05-09 design brainstorm. It consolidates locked decisions, names every component the MVP needs, and sequences the build. Operational concerns (deployment, backup, recovery) are documented but not implemented in code at this stage.

---

## 1. Overview

### What "MVP" means

Cairn deployed on `nexus-cw-ec2`, replacing the existing empty Forgejo placeholder. The carried-world team's repos (`cairn`, `casket-go`, `bridle`, `interchange`, `nexus`, `vessel`) are migrated to Cairn as origin, with GitHub configured as a push-mirror DR target. Each team agent has an HKDF-derived Ed25519 keypair, attached to a human user account, and signs every commit. Git history surfaces agent identity at every layer (raw `git log`, Forgejo's web UI, the GitHub mirror). The team uses Cairn as its primary git platform from day one of MVP — Cairn-native features are not deferred, they are the foundation.

### What "MVP" does NOT mean

- Public-facing service (initial dogfooding only — public access deferred)
- Cairn CLI shaped like `gh` (deferred to Phase 2; minimal CLI surface in Phase 1)
- MCP server (deferred to Phase 2; agents read via `?format=md` and write via plain `git`)
- Custom Cairn web UI rework (vanilla Forgejo + minimal author-rendering patch)
- SDKs in Go / Python / C# (deferred)
- CI runners / Forgejo Actions (deferred)
- HMAC key rotation, owner seed rotation (deferred — recovery procedures documented)
- Federation, cross-org agent delegation (deferred indefinitely until needed)

### Success criterion (single sentence)

> Plumb commits to Cairn via plain `git`, the commit is signed by Plumb's HKDF-derived key, attribution shows in `git log`, in Cairn's web UI, in the GitHub push-mirror, and in the `?format=md` rendering of the commit page; the commit is rejected if Plumb's agent record is `pending`, blocked, or missing.

---

## 2. Strategic context

### Why agent-first dogfooding (not vanilla-first)

Switching git hosts without changing per-agent attribution would just be "we moved git hosting." If we're migrating, the migration carries the thesis or it isn't worth the effort. The team currently shares one GitHub account (`nexus-cw`) — commits are flat, agents are indistinguishable. Cairn's reason for existing is to fix exactly that problem. Deploying Cairn without per-agent identity from day one would reproduce the GitHub flatness and waste the migration.

### Why Forgejo fork (not gateway, not greenfield)

Locked on 2026-05-07 in the initial brainstorm. Identity multiplexing is fundamentally a schema change; faking it at a gateway leaves the underlying data model flat and any forensic / audit / signing path that bypasses the gateway sees a flattened picture. Greenfield is a multi-year project. Forgejo gives us git protocol, web UI, auth, hooks, CI plumbing, and 11 years of testing for free; we add the agent layer on top via additive packages and narrow upstream patches.

### Why HKDF-derived agent keys (not registered random keys)

Locked on 2026-05-07. Determinism — same `(seed, slug)` always produces the same keypair, so an agent reinstalls anywhere with the same identity. No central key-registration ceremony per machine; no per-machine key management overhead. The owner's seed is the root of derivation.

### Why ownership is enforced (not deferred)

Operator clarification 2026-05-09. Without an enforced human-owner relation, the accountability chain breaks: a Cairn-attributed commit could in principle exist without a responsible human account behind it. Cairn's value proposition is "every action traces to a responsible human" — that property must be a platform invariant, not a convention.

---

## 3. Locked decisions (consolidated)

| # | Decision | Reason |
|---|---|---|
| 1 | Substrate: Forgejo v15.0.1 | Go stack fit, community governance, GHA-compat CI, low resource floor |
| 2 | Approach: in-tree fork (not gateway) | Honest data model for identity multiplexing |
| 3 | Name: Cairn | Trail-marker metaphor, additive multi-author log |
| 4 | License boundary: GPLv3 (Forgejo-derived) + AGPLv3 (cairn-specific) | Closes the SaaS loophole for cairn-specific code |
| 5 | Module path: `github.com/CarriedWorldUniverse/cairn` | Locked at first commit; Go has no auto-redirect |
| 6 | Patch-stack discipline + additive packages | Minimises upstream collisions on rebase |
| 7 | Identity: Option A (owned agents) with deterministic HKDF derivation | Plumb-on-<server-host> and Plumb-on-laptop are the same agent |
| 8 | Signing: Ed25519 via casket-go primitives | Reuse existing carried-world crypto vocabulary |
| 9 | Email convention: `nexus-{slug}@{owner_domain}` | Deliverable mail address; slug + owner readable in `git log` |
| 10 | Trailers: `Agent-Id`, `Agent-Owner`, `Agent-Domain` (all three) | Machine-readable + human-readable belonging signal |
| 11 | Fingerprint: `cairn:` + base64(HMAC-SHA256(instance_hmac_key, public_key)) | Cryptographically secure; cross-instance unlinkability |
| 12 | Ownership enforced (`agent.user_id NOT NULL`) | No orphan agents at the platform level |
| 13 | Status field (`pending`/`active`); default `pending` | Auto-approve when proposer == owner; explicit approval otherwise |
| 14 | Single endpoint for registration (unified flow) | Auto-approve gate on auth_user == proposed_owner |
| 15 | Storage: SQLite, single DB with Forgejo, Cairn migrations from `v500` | Team-only load; xorm engine handles both |
| 16 | Connection-per-operation discipline + WAL mode + checkpoint goroutine | Avoids long-lived locks; data persists to disk |
| 17 | `?format=md` rendering for commit / PR / issue / file pages | Agents read Cairn natively without MCP |
| 18 | `.well-known/llms.txt`, `.well-known/cairn.json`, `.well-known/security.txt` | Agent and operator discovery |
| 19 | Git history must surface agent ID (no flattening) | Hard requirement; survives push-mirror to GitHub |
| 20 | Branch protection default: opt-out (Cairn design preference) | Multi-agent default is the safe default |

---

## 4. Architecture

### Component map

```
                    nexus-cw-ec2 (t3.micro, AWS)
                    ┌───────────────────────────────────────────────┐
   git+ssh/https ─→ │  Cairn (Forgejo fork @ v15.0.1 + cairn patch  │
   browser      ─→ │  stack)                                       │
                    │                                               │
                    │  ┌─ Cairn additive packages (AGPLv3) ─────┐   │
                    │  │  models/cairn/                         │   │
                    │  │    ├── agent.go                        │   │
                    │  │    ├── agent_blocklist.go              │   │
                    │  │    └── migrations/v500_*.go            │   │
                    │  │  services/cairn/                       │   │
                    │  │    └── identity/                       │   │
                    │  │        ├── identity.go                 │   │
                    │  │        ├── store.go                    │   │
                    │  │        ├── xorm_store.go               │   │
                    │  │        └── instance_init.go            │   │
                    │  │  routers/api/cairn/v1/                 │   │
                    │  │    └── agents.go                       │   │
                    │  │  routers/web/cairn/                    │   │
                    │  │    ├── markdown.go                     │   │
                    │  │    ├── wellknown.go                    │   │
                    │  │    └── templates/md/                   │   │
                    │  │  cmd/cairn/                            │   │
                    │  │    └── (Phase-1 minimum subcommands)   │   │
                    │  └────────────────────────────────────────┘   │
                    │                                               │
                    │  ┌─ Forgejo upstream touch-points ────────┐   │
                    │  │  (narrow patches in patch stack)       │   │
                    │  │  routers/private/hook_pre_receive.go   │   │
                    │  │    + cairn.VerifyAgentCommits hook     │   │
                    │  │  templates/repo/commit_status.tmpl     │   │
                    │  │    + agent badge rendering             │   │
                    │  └────────────────────────────────────────┘   │
                    │                                               │
                    │   forgejo.db (SQLite, WAL)                    │
                    │   instance-hmac.key (mode 0400)               │
                    │                                               │
                    └───────────────────┬───────────────────────────┘
                                        │ Forgejo's built-in
                                        │ push-mirror per repo
                                        ▼
                                github.com/CarriedWorldUniverse/<repos>

   external deps:
      github.com/CarriedWorldUniverse/casket-go  (+ DeriveAgentKey helper)
```

### Forgejo touch-points (only two)

| Touch-point | Patch surface | Why minimal |
|---|---|---|
| `routers/private/hook_pre_receive.go` | Add a single call to `cairn.VerifyAgentCommits(ctx, refs, repo)` after Forgejo's existing checks | Hook-extension semantics already exist in Forgejo; we add a check, don't replace upstream's |
| `templates/repo/commit_status.tmpl` (and friends) | Recognise `nexus-{slug}@{domain}` author and render a small badge | Template-only change; no upstream Go code touched |

All other Cairn behaviour lives in additive packages under `models/cairn/`, `services/cairn/`, `routers/api/cairn/`, `routers/web/cairn/`, and `cmd/cairn/`. Upstream rebases collide only at the two named touch-points.

### Storage discipline

Connection-per-operation pattern is mandatory at the Cairn store layer. Each store method acquires a short-lived xorm session, executes, releases — no long-lived sessions held across multiple operations. The pool keeps physical connections warm; "open-exec-release" on the wrapper is a pool acquire/release.

```go
func (s *xormAgentStore) Get(ctx context.Context, slug, domain string) (*Agent, error) {
    sess := s.engine.NewSession()
    defer sess.Close()
    var a Agent
    has, err := sess.Context(ctx).
        Where("slug = ? AND domain = ?", slug, domain).
        Get(&a)
    if err != nil { return nil, err }
    if !has { return nil, ErrAgentNotFound }
    return &a, nil
}
```

SQLite tuning at instance startup (configured in `app.ini`):
- `_journal_mode=WAL`
- `_busy_timeout=5000`
- `_synchronous=NORMAL`
- `_cache=shared`
- WAL auto-checkpoint at default 1000 pages
- Cairn-side goroutine runs `PRAGMA wal_checkpoint(TRUNCATE)` every five minutes

---

## 5. Data model

### Schema

```sql
-- v500_create_agent_tables.go

CREATE TABLE agent (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    fingerprint     VARCHAR(80) NOT NULL UNIQUE,        -- "cairn:<base64>"
    user_id         INTEGER NOT NULL REFERENCES user(id),
    slug            VARCHAR(64) NOT NULL,
    domain          VARCHAR(255) NOT NULL,
    public_key      BLOB NOT NULL,                       -- 32 bytes Ed25519
    status          VARCHAR(16) NOT NULL DEFAULT 'pending',
    created_at      TIMESTAMP NOT NULL,
    activated_at    TIMESTAMP,
    UNIQUE (user_id, slug)
);

CREATE INDEX idx_agent_email_lookup ON agent(slug, domain);

CREATE TABLE agent_blocklist (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id        INTEGER NOT NULL REFERENCES agent(id),
    blocked_at      TIMESTAMP NOT NULL,
    reason          TEXT
);
```

**Migration numbering**: Cairn migrations begin at `v500` to leave room above Forgejo's existing series (currently around `v300`). The migrations register with Forgejo's xorm engine alongside upstream's — single migration table, single ordering, single backup story.

**Slug uniqueness scope**: `(user_id, slug)`. Two human owners can each have an agent named `plumb`; they are distinguished by the `domain` field and the email convention.

**Status field values**: `pending` (proposed but not approved) and `active` (approved, can act). Push verification rejects any commit whose agent isn't `active`.

### Identifiers — three forms, one source of truth

| Identifier | Format | Purpose | Example |
|---|---|---|---|
| **slug** | human-readable name | display, URLs, git log | `plumb` |
| **fingerprint** | crypto-anchored handle | logs, audit, key-file lookup, MCP | `cairn:5f8b3c4d2e1a6f7e9b...` |
| **email** | RFC-compliant address | git commit author, deliverable mail | `nexus-plumb@darksoft.co.nz` |

All three are deterministic given `(slug, domain, public_key, instance_hmac_key)`.

### Fingerprint algorithm

```
fingerprint = "cairn:" + base64(HMAC-SHA256(instance_hmac_key, public_key))
```

- 256-bit HMAC output, base64-encoded (43 chars), prefixed `cairn:` → 50 chars total
- Cryptographically secure: without `instance_hmac_key`, an attacker cannot compute another keypair's fingerprint
- Instance-specific: same agent keypair on Cairn-A vs. Cairn-B produces different fingerprints (cross-instance unlinkability)
- Server-issued: agents do not compute the fingerprint themselves; they receive it from Cairn at registration response

### `instance_hmac_key`

- Generated at instance init: 32 bytes from `crypto/rand`
- Stored at `~cairn-data/instance-hmac.key`, mode `0400`, owned by Cairn system user
- Loaded at startup into a process-resident byte slice
- Never logged, never exposed via API, never returned in `.well-known/cairn.json`
- Backup: included in the documented backup set alongside `forgejo.db`. Loss is recoverable for stored-fingerprint lookups (they remain valid in the DB) but new registrations after key loss won't deterministically rederive previous fingerprints
- Rotation: deferred post-MVP. Future maintenance command will recompute every agent's fingerprint and rewrite stored values

### Trailers (commit metadata)

Every agent commit carries three trailers:

```
Agent-Id: plumb
Agent-Owner: alice
Agent-Domain: darksoft.co.nz
```

- `Agent-Id`: agent slug (matches the email's local-part stripped of `nexus-` prefix)
- `Agent-Owner`: owner's Cairn username (the canonical answer to "who is responsible")
- `Agent-Domain`: redundant with email's domain, but explicit and machine-parseable

Trailers are part of the signed commit material. Tampering invalidates the signature. Cross-validation at push time catches the case where signature is correct but trailers lie.

---

## 6. Identity model

### Derivation

`casket-go` adds a new function:

```go
func DeriveAgentKey(seed []byte, slug string) (priv ed25519.PrivateKey, pub ed25519.PublicKey, err error)
```

Internally:
1. `derived := HKDF-SHA256(seed, salt=nil, info="cairn-agent-v1:"+slug, length=32)`
2. `priv, pub = ed25519.NewKeyFromSeed(derived)`

Properties:
- Deterministic: same `(seed, slug)` → same keypair, on any machine
- Independent: different slugs produce independent keys (HKDF isolation)
- Compromise of one agent's private key does not expose the seed or other agents' keys

### Owner seed

- 32+ bytes of high-entropy material
- Stored at `~/.config/cairn/seed`, mode `0600`
- Replicated to every machine where the owner's agents run (secure transfer required: Signal, age-encrypted email, USB hand-off — never via Git)
- Compromise is a major event: requires generating a new seed, deriving new keys for every agent, re-registering all agents under new fingerprints

### Registration — the unified flow

One endpoint, with auto-approve when proposer == proposed_owner:

```
POST /api/cairn/v1/agents
   Body: {
     proposed_owner: "alice",
     slug:           "plumb",
     domain:         "darksoft.co.nz",
     public_key:     <hex>
   }
   Optional: Authorization: Bearer <token>

Cairn:
   - Resolve proposed_owner → user_id (404 if not found)
   - Check (user_id, slug) is unique (409 if conflict)
   - Compute fingerprint = "cairn:" + base64(HMAC-SHA256(instance_hmac_key, pubkey))
   - If auth_user.id == proposed_owner.user_id:
       INSERT agent (status='active', activated_at=NOW(), ...)
   - Else:
       INSERT agent (status='pending', activated_at=NULL, ...)
   - Return: { fingerprint, status }
```

The agent never has to know whether it's auto-approved or pending. The CLI submits; the server applies the right gate.

### Approval (only for `pending` agents)

```
POST /api/cairn/v1/agents/<fingerprint>/approve
   Authorization: Bearer <owner_token>

Cairn:
   - Verify token.user_id == agent.user_id
   - Verify agent.status == 'pending' (idempotent if already active)
   - UPDATE agent SET status='active', activated_at=NOW()
```

Owner can list pending agents (`GET /api/cairn/v1/agents?status=pending&user=me`) and approve via CLI (`cairn agents approve <fingerprint>`) or web UI (a simple page at `/<user>/-/agents`).

### Push verification

Pre-receive hook (in `routers/private/hook_pre_receive.go`, calling out to `services/cairn/identity/`):

```
For each new commit:
    parse author email →
        if not "nexus-{slug}@{domain}" pattern:
            treat as human commit, run vanilla Forgejo path, skip Cairn check
        if agent commit:
            agent = AgentStore.GetByEmail(slug, domain)
            if not found:                     → REJECT (orphan agent)
            if agent.status != 'active':      → REJECT (pending or unknown status)
            if AgentBlocklistStore.IsBlocked(agent.id):  → REJECT (blocked)
            verify(commit_signature, agent.public_key)
            if invalid signature:             → REJECT
            verify trailers match:
                Agent-Id == agent.slug
                Agent-Owner == owner.username
                Agent-Domain == agent.domain
            if mismatch:                       → REJECT
            otherwise:                         → ACCEPT
```

Each store call uses a short-lived session per the connection discipline.

**Tag pushes** are out of scope for MVP signature enforcement.
The pre-receive hook gates only `IsBranch()` ref updates.
Annotated tags pointing at unsigned commits are accepted; the tag
ref itself is not signed by the agent. This is a deliberate
narrowing for MVP — agent commits flow primarily through branch
PRs, and tag-signing has a different threat model (release-signing
rather than per-commit attribution).

Post-MVP, if tag-signing becomes a workflow requirement, the
gate at `routers/private/hook_pre_receive.go` should extend to
`refFullName.IsBranch() || refFullName.IsTag()`, and `BuildCommitList`
already produces the right SHA range via `git rev-list oldSHA..newSHA`
for the tagged commit chain.

**Trailer presence is optional but consistency is enforced.**
A commit with no Cairn trailers (Agent-Id / Agent-Owner / Agent-Domain)
is accepted if its signature is valid — the cryptographic gate is
the source of truth. A commit with present-but-mismatched Cairn
trailers is rejected. This means: if you write the trailers, write
them honestly; if you don't write them, the signature alone speaks
for the commit.

Future tightening: a `setting.Cairn.RequireTrailers` flag could make
all three trailers mandatory. Currently no such flag exists.

### Auth model — git push vs. commit signing

Two separate concerns, often confused:

- **Git push authentication**: handled by Forgejo's existing path (SSH key or HTTPS token associated with the *human owner's* Forgejo user). Agents do not have their own Forgejo user accounts in MVP; they push through the human owner's auth.
- **Commit signing**: handled by Cairn's pre-receive verification. The commits *inside* the push are signed by the agent's HKDF-derived key. Cairn verifies each commit's signature against the registered agent's public key.

Accountability chain: push auth = human owner; commit sig = agent owned by the same human. Both lead back to the same responsible account.

---

## 7. Read paths and discovery

### `?format=md` rendering

A middleware/handler in `routers/web/cairn/markdown.go` intercepts `?format=md` or `Accept: text/markdown` for these page types in MVP:

- `/:owner/:repo/commits/:hash` — commit detail
- `/:owner/:repo/pulls/:id` — PR detail
- `/:owner/:repo/issues/:id` — issue detail
- `/:owner/:repo/src/branch/:branch/:path` — file content with metadata

Renders via Cairn-side Go templates under `routers/web/cairn/templates/md/`. **Generated server-side from structured data**, not HTML-to-markdown conversion. Templates are hand-curated for the four MVP pages; comprehensive coverage post-MVP.

Example output (commit page):

```markdown
# Commit abc123def456

**Author:** nexus-plumb <nexus-plumb@darksoft.co.nz>
**Agent:** plumb (under alice) — `cairn:5f8b3c4d2e1a6f7e9b...`
**Signed:** ✓ verified
**Date:** 2026-05-09T14:22:11Z

## Message

Add agent registration endpoint

Agent-Id: plumb
Agent-Owner: alice
Agent-Domain: darksoft.co.nz

## Diff

```diff
... (rendered diff)
```
```

### `.well-known/` discovery

Three handlers in `routers/web/cairn/wellknown.go`:

#### `.well-known/llms.txt` (text/markdown)

Hand-curated AI-agent-friendly description of the instance:

```markdown
# Cairn (cairn.darksoft, v0.1.0)
Agent-native git platform. Fork of Forgejo with per-agent identity
multiplexing via HKDF-derived Ed25519 keypairs.

## Reading content
Any page is fetchable as clean markdown via `?format=md` or
`Accept: text/markdown`. Useful reads:
- /[owner]/[repo]/commits/[hash]?format=md — commit details
- /[owner]/[repo]/pulls/[id]?format=md — PR overview
- /[owner]/[repo]/issues/[id]?format=md — issue details
- /[owner]/[repo]/src/branch/[branch]/[path]?format=md — file content

## Identity
GET /api/cairn/v1/agents/[fingerprint]/identity → public key for an agent.
Derivation: (user_seed, agent_slug) → HKDF-SHA256 → Ed25519.
Email convention: nexus-{slug}@{domain}.

## Manifest
/.well-known/cairn.json carries the machine-readable capability manifest.
```

#### `.well-known/cairn.json` (application/json)

```json
{
  "cairn_version": "0.1.0",
  "forgejo_version": "15.0.1",
  "instance_name": "cairn.darksoft",
  "fingerprint_algo": "HMAC-SHA256",
  "signing_algo": "Ed25519",
  "derivation_algo": "HKDF-SHA256",
  "derivation_info_prefix": "cairn-agent-v1:",
  "email_convention": "nexus-{slug}@{domain}",
  "trailers": ["Agent-Id", "Agent-Owner", "Agent-Domain"],
  "endpoints": {
    "agents": "/api/cairn/v1/agents",
    "agent_identity": "/api/cairn/v1/agents/{fingerprint}/identity",
    "manifest": "/.well-known/cairn.json",
    "llms_txt": "/.well-known/llms.txt"
  },
  "features": {
    "markdown_rendering": true,
    "agent_proposals": true,
    "mcp_server": false,
    "sdks": []
  }
}
```

The instance HMAC key is **never** exposed in the manifest. The fingerprint algorithm is published (transparency); the key remains secret.

#### `.well-known/security.txt` (text/plain)

Static file with vulnerability-reporting contact info per RFC 9116.

---

## 8. CLI minimum surface (Phase 1)

Located at `cmd/cairn/`. Subcommands:

| Command | Purpose |
|---|---|
| `cairn auth login` | Authenticate against a Cairn instance, store token at `~/.config/cairn/<host>/token` |
| `cairn agent init --slug <s> --domain <d> [--key-from random\|hkdf]` | Generate keypair locally, store at `~/.config/cairn/<host>/<slug>.key` (mode 0600), print metadata |
| `cairn agent submit [--owner <u>]` | Submit the proposal to the Cairn server (auto-active when authed as owner) |
| `cairn agent show <slug>` | Print local agent metadata + Cairn-side fingerprint and status |
| `cairn agents list [--status pending\|active]` | List agents (current user's, or with `--all` if admin) |
| `cairn agents pending` | Owner: list pending registrations awaiting your approval |
| `cairn agents approve <fingerprint>` | Owner: approve a pending registration |
| `cairn agents block <fingerprint> [--reason <r>]` | Owner: add to blocklist |
| `cairn commit-sign-helper --slug <s>` | Git invokes this; reads commit blob from stdin, signs with derived key, writes signature to stdout |

Phase-2's full `gh`-shaped CLI (`cairn pr open`, `cairn issue list`, etc.) is a separate effort.

---

## 9. casket-go upstream additions

A new file in `github.com/CarriedWorldUniverse/casket-go`:

**`agent.go`:**
```go
// DeriveAgentKey derives a deterministic Ed25519 keypair for a Cairn agent
// from an owner's identity seed and an agent slug.
//
// The same (seed, slug) pair always produces the same keypair, on any machine.
// Different slugs produce independent keys (HKDF isolation).
//
// info string: "cairn-agent-v1:" + slug
func DeriveAgentKey(seed []byte, slug string) (ed25519.PrivateKey, ed25519.PublicKey, error)
```

Plus tests with known-vector cases so derivation stability is verifiable across casket-go versions.

Lands as its own PR in `casket-go`. Cairn imports it via `github.com/CarriedWorldUniverse/casket-go` (post-org-migration path).

---

## 10. Configuration

`app.ini` `[cairn]` section flags:

| Flag | Default | Purpose |
|---|---|---|
| `enabled` | `true` | Instance-level off-switch (emergency: act as vanilla Forgejo) |
| `enforce_signatures` | `false` initially, `true` after migration | Push-time signature verification toggle |
| `reject_orphan_agents` | `true` | Reject `nexus-{slug}@…` commits whose agent isn't registered (vs. silently treating as human) |
| `hmac_key_path` | `~cairn-data/instance-hmac.key` | Where the HMAC key lives |
| `markdown_endpoints_enabled` | `true` | Toggle for `?format=md` rendering |
| `wal_checkpoint_interval_minutes` | `5` | Cairn-side periodic WAL checkpoint goroutine |

`enforce_signatures = false` is the deployment-time default for migration safety. Operator flips to `true` after agents are registered.

---

## 11. Deployment

### Hosting target

`nexus-cw-ec2` (t3.micro free tier, AWS). Replaces the existing empty Forgejo placeholder. SQLite (no Postgres process to run). TLS via Caddy in front, or Forgejo's built-in ACME — operator's call.

### Deployment artefacts (under `cairn/deploy/`)

- `systemd/cairn.service` — systemd unit
- `caddy/Caddyfile` — TLS + reverse proxy (alternative to built-in ACME)
- `sqlite-tuning.md` — recommended PRAGMA settings + checkpoint goroutine config
- `migration-runbook.md` — repo-by-repo migration procedure

### Bootstrap order (deployment runbook)

This sequence avoids locking yourself out and avoids the "historical commits with non-agent emails" rejection problem:

1. Build Cairn binary from the `cairn` branch (with `lock-day-one` patches applied)
2. Deploy to `nexus-cw-ec2`; install systemd unit; start service
3. First-run setup generates `instance-hmac.key`, creates admin user (Jacinta)
4. Configure SSH/token auth for git push as Jacinta
5. While `enforce_signatures = false`: migrate repos
   - For each org repo: create empty Cairn repo, `git push --mirror`, configure push-mirror back to GitHub
   - Order: `casket-go` → `bridle` → `interchange` → `nexus` → `vessel` → `cairn`
   - For repos with meaningful issue/PR history (currently only `cairn`), use Forgejo's GitHub-import to bring it over
6. Place Jacinta's seed file at `~/.config/cairn/seed` on every machine where her agents will run
7. Register each team agent: `cairn agent init` + `cairn agent submit` (auto-active because Jacinta is authenticated)
8. Configure each agent's git client: author name / email / signing helper per Section 6
9. Flip `enforce_signatures = true`, restart Cairn
10. Verify a fresh agent push round-trips successfully (commit shows in `git log`, `?format=md`, GitHub mirror)

### Push-mirror to GitHub

Forgejo's built-in feature configured per repo. Source-of-truth is Cairn; GitHub is DR. Mirrored commits show "Unverified" on GitHub for MVP (agent SSH-signing keys aren't registered with GitHub user accounts). Acceptable.

---

## 12. Operational concerns

### Backup discipline

Backup set:
- `forgejo.db` (SQLite) — snapshotted via `sqlite3 .backup`, shipped to S3 nightly
- `instance-hmac.key` — to AWS Secrets Manager or encrypted-S3 sidecar
- Forgejo data directory (LFS, attachments, avatars) — tar+gzip+S3 nightly
- `app.ini` — versioned in git (without secrets) or in S3

Documented in `cairn/deploy/backup-runbook.md` (to be written during deployment).

### Logging & observability

Phase 1 uses Forgejo's existing logger. Cairn adds log lines for:
- Push verification (accept/reject with reason and agent fingerprint)
- Agent registration (proposal, auto-approve / pending)
- Approval / blocklist additions
- Instance HMAC key load (success/failure at startup)

Structured logs / metrics / traces are post-MVP.

### Recovery procedures

| Scenario | Procedure |
|---|---|
| Agent's private key compromised | Block the agent; register a new agent under a different slug (e.g., `plumb2`); update local git config to use the new slug. Old commits remain verifiable; new commits use the new identity |
| Owner's seed compromised | All derived keys are compromised. Generate a new seed, derive new keys for every agent, re-register all agents (will receive new fingerprints), block all old fingerprints. Major event |
| `instance-hmac.key` lost | Existing stored fingerprints in the DB remain usable. New registrations cannot deterministically reproduce previous fingerprints. Restore from backup |
| Cairn instance lost | GitHub push-mirror has all repo content. Cairn DB content (issues, PRs, agent registry) requires backup restore. Without backup: re-create agent registrations (CLI run again) |

---

## 13. Security model

### What Cairn protects

- **Commit attribution integrity**: every accepted Cairn-attributed commit has a verified signature and traces to a registered, owned, active agent
- **Agent identity uniqueness within an instance**: `(user_id, slug)` is unique; the fingerprint is HMAC-bound to the instance
- **Cross-instance unlinkability**: the same agent keypair on different Cairn instances has different fingerprints — observers can't trivially correlate
- **Accountability chain**: every agent action traces to a responsible human owner via the `agent.user_id` foreign key

### What Cairn does not protect (in MVP)

- **Push-time human authentication** beyond Forgejo's existing SSH/token auth
- **Pre-existing commits** in repo history (pre-Cairn) — the `enforce_signatures` flag is `false` during migration; historical commits are not verified
- **Off-instance fingerprint verification** — external parties verify signatures via the public key (which is exposed); they cannot independently recompute the fingerprint
- **Cross-org agent delegation** — single human owner per agent
- **Capability scoping per agent** — every active agent can do everything the owner can do (per-tool capability tokens are post-MVP)

### Threat model (assumptions)

- Adversaries cannot read the instance's filesystem (so can't steal `instance-hmac.key`, `forgejo.db`, or owner seeds)
- Adversaries cannot intercept TLS-secured connections to the instance
- The owner exercises reasonable seed hygiene (mode-0600 file, transferred via secure channels)
- An agent's private key is held only on machines the owner controls

If any of these assumptions break, the platform's accountability chain weakens. Verity (when filled) will own the proper threat-modelling pass before public-facing rollout.

---

## 14. Build sequence

Implementation order, each step landing as one or more PRs on the `cairn` branch:

| # | Step | Notes |
|---|---|---|
| 1 | Land `lock-day-one` PR | Module path + license boundary already prepared in PR; merge first |
| 2 | Add `DeriveAgentKey` to casket-go (separate repo) | Cairn imports this — has to land before Cairn's identity code |
| 3 | Cairn package skeletons | `models/cairn/`, `services/cairn/identity/`, `routers/api/cairn/v1/`, `routers/web/cairn/`, `cmd/cairn/` with stub `doc.go` files |
| 4 | Schema + first migration | `agent` and `agent_blocklist` tables, registered with xorm engine |
| 5 | Identity primitives | `Fingerprint()`, `ParseAgentEmail()`, `VerifyCommitSignature()`, `LoadInstanceHMACKey()` + tests |
| 6 | API routes | Registration, list, identity, approve, block — with tests |
| 7 | CLI minimum subcommands | `auth login`, `agent init`, `agent submit`, `commit-sign-helper`, `agents pending/approve/list/block` |
| 8 | Pre-receive hook integration | The Forgejo touch-point patch; tests for accept/reject paths |
| 9 | Author rendering template patch | Forgejo touch-point #2; visual-check test |
| 10 | Markdown rendering | `?format=md` for commit/PR/issue/file pages |
| 11 | `.well-known/` handlers | `llms.txt`, `cairn.json`, `security.txt` |
| 12 | Deployment artefacts | systemd unit, Caddyfile, runbooks |
| 13 | Deploy + migrate per the runbook | (Section 11) |
| 14 | Flip `enforce_signatures = true`, validate end-to-end | Success criterion verification |

Each step is independently reviewable and testable.

**Deployment-runbook sync rule:** the deployment runbook at [`cairn/deploy/deployment-runbook.md`](../../../cairn/deploy/deployment-runbook.md) documents the operational consumer surface — CLI commands, config flags, endpoints, file paths, expected outputs. **Every build-sequence PR that changes any of those surfaces must update the runbook in the same change.** The runbook's §16 names exactly which surfaces it depends on; cross-reference it before merging any Cairn-specific feature.

---

## 15. Forward-compatibility (deferred items, named so they don't surprise us)

| Item | Status |
|---|---|
| MCP server | Deferred to Phase 2; manifest declares `mcp_server: false` |
| `gh`-shaped CLI surface | Deferred to Phase 2; minimal CLI in Phase 1 |
| Go / Python / C# SDKs | Deferred; manifest declares `sdks: []` |
| Custom Cairn UI rework | Deferred; vanilla Forgejo + author-rendering patch is MVP UI |
| Public-facing access | Deferred until after dogfooding stabilises |
| CI runner topology / Forgejo Actions | Deferred; team uses external CI in the meantime |
| HMAC key rotation | Deferred; recovery procedure documented |
| Owner seed rotation | Deferred; recovery procedure documented |
| Per-agent capability tokens | Deferred; agents currently inherit owner's full capabilities |
| Cross-org agent delegation | Indefinite |
| Federation / ActivityPub | Indefinite |
| Agent SSH-signing keys uploaded to GitHub for verified-badge rendering on the mirror | Optional; not blocking |

---

## 16. Open items / risks

- **Anvil's review** of the locked design has not landed; this spec consolidates Plumb's brainstorm with Jacinta and Keel's consult. Anvil's input on the API shape and module-path discipline is locked in by the existing brainstorm doc; their review of this spec is a follow-up.
- **Verity (security aspect) is unfilled.** The threat model assumptions in §13 will need a real review before public-facing rollout.
- **`enforce_signatures = false` window during migration** is when the platform is operating without its core invariant. Document this clearly in the runbook so it's not an unintentional state.
- **First-time SQLite tuning** may surface unforeseen behaviour under real load. The connection-per-operation discipline is the structural fix, but the `?` cases (e.g., long-running migrations that hold connections) need attention during deployment.
- **Forgejo's existing GitHub-import** for repo history (issues/PRs) is one path; if it has bugs we hit during migration, fall back to "fresh start, history is in git only."

---

## 17. Approval

This spec consolidates the 2026-05-09 brainstorm with Jacinta. Pending review by:
- [ ] Jacinta (operator)
- [ ] Anvil (code architecture)
- [ ] Forge (deployment / storage)
- [ ] Keel (relay-adjacent / network)
- [ ] Verity (security — when role is filled)

Once approved, this spec hands off to `superpowers:writing-plans` for the implementation plan.
