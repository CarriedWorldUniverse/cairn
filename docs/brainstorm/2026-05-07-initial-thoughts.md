# Cairn — Initial Brainstorm

**Date:** 2026-05-07
**Participants:** Jacinta, Plumb (Claude Code instance, AWS/code aspect)
**Updated:** 2026-05-07 — team session notes added (Anvil, Keel); deterministic identity derivation added
**Status:** Design paused pending full-team consult.

## Why this exists

Jacinta floated the idea of building "our own git platform, primarily directed at being used by agents, with CI/CD capabilities and storage." This document is the synthesis of the initial brainstorm — what got decided, what didn't, and what the open questions are when the rest of the team picks this up.

## Strategic posture

Two framings established late in the discussion that should shape every design decision downstream:

- **OSS that may grow.** Cairn is intended as an open-source project that may attract outside interest and grow into a community. Not a private internal tool that happens to have a public mirror. Decisions should be made with future-contributors in mind, not just the current team — license, governance, contribution shape, and public roadmap all become real questions rather than incidental ones.
- **Cairn is nexus's substrate.** The nexus agent harness sits on top of Cairn and uses it as the medium through which the agent team coordinates work — agents commit, branch, PR, and run CI through Cairn. Nexus is the primary first-party client of Cairn's API. Ergonomics from nexus's perspective is a design constraint. The agent-shaped API question (open) is not abstract; the first concrete answer to "what does an agent-shaped API look like?" is "what does nexus need from it?"

## The thing being built

A git platform optimised for agent use. Concretely:

- **A set of agents performs the full PR / branching / merge flow** end-to-end.
- **Multiple agent identities ladder up to one human user.** Today on GitHub all agents share one account (`nexus-cw`), so commits are indistinguishable. Cairn makes per-agent identity a first-class concept while keeping a human owner above them.
- **The UI/API is rendered to be easy for agents to navigate.** Humans get an alternate view. (What "agent-shaped" means concretely is one of the open questions.)
- **CI/CD and storage are part of the platform**, not bolted on.

## Client surfaces

Cairn ships not just a server but the tools agents and humans actually reach for. Three first-class client surfaces:

- **`cairn` CLI** — for humans and for agents that prefer to shell out. Should feel like `gh` or `tea` in ergonomic terms: fast, scriptable, helpful errors, completion. Naming follows the project (`cairn pr open`, `cairn branch`, `cairn agents create`).
- **MCP server** — exposes Cairn operations as MCP tools so agents can use Cairn natively through the same protocol they already use for inter-agent comms. Zero new vocabulary for the team.
- **SDKs** — Go (matches the rest of the stack), Python, and **C#** as first-party. C# is not speculative reach: Wren's Unity / Shattered State work is C#-native, so a C# SDK lets Unity-side code interact with Cairn directly. TypeScript when there's demand from outside the team.

The architectural commitment: a **single source of truth at the API layer**. CLI, MCP server, and SDKs are thin, consistent wrappers — same operations, same error semantics, same auth model.

```
CLI (cairn) ──┐
MCP server ───┼──→ Cairn structured API ──→ services ──→ models
SDKs ──────── ┘
```

All four surfaces live in the same monorepo and ship version-locked.

## Substrate evaluation

Candidates considered: Forgejo, Gitea, GitLab CE, OneDev, Gogs, Sourcehut.

Ruled out:
- **Sourcehut** — email-driven workflow, philosophical mismatch with PR/branch/merge.
- **GitLab CE** — wrong stack (Ruby), 25× the RAM floor of the Go options, optimised for human teams, enterprise-feature-gated.
- **Gogs** — no CI, single-maintainer bottleneck. Forgejo/Gitea both descend from it.
- **OneDev** — Java, smaller ecosystem.

Real choice was **Forgejo vs Gitea** (~95% the same code; Forgejo rebases regularly). Differences that mattered:

| | Forgejo | Gitea |
|---|---|---|
| Governance | Community-run (post-Gitea-Ltd fork) | Corporate (Gitea Ltd since 2022) |
| License | GPLv3 | MIT |
| CI | Forgejo Actions (GHA-compat) | Gitea Actions (GHA-compat) |
| Stack | Go | Go |

**Decision: Forgejo.** Reasons:

1. **Stack fit** — Go all the way down, matches `nexus`, `bridle`, `casket-go`.
2. **Community governance** — aligned with Carried-World's open-source direction; Forgejo's existence is itself a response to Gitea Ltd's corporate capture.
3. **Already running** — placeholder Forgejo on `nexus-cw-ec2` provides a host for Cairn's repo during early development without standing up new infrastructure.
4. **GPLv3 is fine** — we're open-sourcing this anyway, consistent with the nexus stance.
5. **CI included** — Forgejo Actions is GHA-compatible, so anything agents already know how to write works.

## Approach: gateway vs fork

Three shapes were on the table:

- **A. Gateway / sidecar.** Forgejo stays vanilla; novel logic lives in a Go service in front. Easy upgrades. Multiplexed identity gets faked at the edge.
- **B. Fork.** Modify Forgejo in-tree. Multi-identity-per-user becomes a real schema concept. Costs upstream rebase work and ~500K LOC of inherited code ownership.
- **C. Hybrid.** Start gateway, fork later if seams pinch.

**Decision: B — fork.** Identity multiplexing is fundamentally a schema change; faking it at a gateway means the underlying Forgejo data model still says "one user, one set of commits" and any forensic / audit / signing path that bypasses our gateway sees a flattened picture. If we're going to build this seriously, the model has to be honest.

### Fork is a path, not a destination

The fork inherits a number of existing design decisions from Forgejo — schema, UI pipeline, auth model, notification system. The further development goes, the more some of those inherited decisions may chafe. This is a known tradeoff: the fork buys speed now at the cost of long-term steering freedom.

The mitigation is the patch-stack discipline (below). If Cairn's novel logic lives in additive packages and upstream-touching code is a named patch series, then graduating to a from-scratch substrate later is a **port, not a rewrite**: cairn-specific code lifts cleanly, only the Forgejo-touching patches need rethinking. A deep fork that mutates Forgejo internals everywhere becomes inseparable; clean packages keep the exit ramp open.

If Forgejo makes upstream changes incompatible with Cairn's direction, stopping the rebase is a legitimate response — and the patch-stack discipline makes that decision clean rather than "we've quietly drifted and aren't sure what we've changed."

**Graduation criteria (measurable triggers rather than indefinite drift):**

- **Signal 1 — patch churn:** If upstream changes touching our patch stack exceed a sustained threshold across two consecutive minor releases, escalate to graduation discussion.
- **Signal 2 — abstraction leakage:** If Forgejo internal types appear in `models/cairn/` or `services/cairn/` — because reaching in was easier than adapting — the packages are no longer clean to lift. This happens silently under delivery pressure. Enforce with a CI linter that refuses patches pulling Forgejo internals into cairn packages.

Both signals together give an honest picture. High patch churn with clean packages = painful but graduatable. Any abstraction leakage = graduation cost growing invisibly.

## Name: Cairn

Carried-World naming style is tactile working-objects (forge, anvil, harrow, plumb, bridle, keel, casket). Candidates discussed:

- **Cairn** — stacked stones marking a trail. Each commit a stone, each agent adds to the pile. The metaphor *is* what the system does: a public, additive, signed trail of markers.
- **Ledger** — book of accounts. Strong on the attribution emphasis but more clerical than tactile.
- **Annal** — yearly record. Reads passive.
- **Tackle** — equipment for hauling. Tactile but less semantically loaded.

**Decision: Cairn.**

## Rebase strategy

The cost of an in-tree fork comes from collision with upstream. The strategy is to minimise collisions by construction:

- **Additive over invasive.** Novel code lives in new packages: `models/cairn/`, `services/cairn/`, `routers/api/cairn/`, `templates/cairn/`. Hooks/extension points get added at upstream call sites; logic lives in our packages.
- **Patch-stack discipline.** Cairn maintained as a named series of patches on top of Forgejo main, not a long-running divergent branch. Same model Linux distros use to maintain kernel forks. Each conceptual change is one named patch.
- **`git rerere`** enabled to learn recurring conflicts.
- **Schema migrations** in our own numbered migration files, never co-mingled with upstream's.
- **Cadence:** every Forgejo minor release (~monthly), same-week. Major upstream refactors in areas we touch are deliberate evaluation points, not panics.

The unavoidably-invasive surface is the User/Auth schema. Multi-identity is not something you can make purely additive. Everything else can be kept clean.

**Module path:** Set to the final Cairn path from the **first fork commit** — not Forgejo's. Go has no import redirect; changing it mid-development after external contributors have started is painful. Lock it on day one.

## Bootstrap path

- Phase 1: Cairn repo lives in the existing Forgejo on `nexus-cw-ec2`. (Or, currently, in this GitHub placeholder until that's seeded.)
- Phase 2: Cairn becomes self-hosting once it can host its own development.
- Phase 3: Migration of TheCarriedWorld repos to Cairn (subject to team decision and timing).

## Prior art: code.storage (Pierre Computer Company)

Surfaced after the main discussion. A funded commercial bet on essentially the same thesis: API-first git infrastructure explicitly for "AI-driven coding platforms, agentic frameworks, and applications handling code-like artifacts." Native git operations, TS/Python/Go SDKs, webhooks, GitHub sync. Built on specialised distributed git storage (claims 60× faster clones than S3/R2-based solutions). Tiered storage — warm at $1/GB/month, cold at $0.15/GB/month, plus metered bandwidth. 99.99% SLA. Closed source, commercial usage-based pricing.

**What it tells us:**
- The agent-native git platform thesis is real enough that a company has raised on it. Validation, not refutation.
- Their design vocabulary is worth studying — "ephemeral branches" and "in-memory writes" name patterns we'd otherwise have to invent. Both relevant to the agent-API open question.
- The warm/cold tier split is a useful frame for Cairn's storage design — agent-generated branches are likely mostly throwaway.
- Their SDK surface (TS/Python/Go) tells us what callers expect.

**Why it doesn't displace Cairn:**
Closed-source SaaS as the substrate of agent identity, commit provenance, and code custody is the wrong ownership and threat model for what we're building. Cairn is the truck; code.storage is the rental car.

**Market timing note:** People are actively leaving GitHub. The window for a credible open-source alternative with agent-native design is open now. This shifts the urgency on public-facing posture — being visible with even a minimal README and roadmap is worth more than waiting until the code is further along.

## Open questions for the team

### Identity model

Three shapes:
- **A. Owned agents.** Each agent belongs to exactly one human user. New `agent` table, `user_id` FK. Commits authored by the agent; render as "Plumb (under alice)". Matches the existing team model.
- **B. First-class agents.** Agents are top-level entities, can be delegated to/by multiple humans. Richer model, supports cross-org agent reuse later.
- **C. Personas.** Just named contexts a single user swaps into. Cheapest, but throws away per-agent signing keys and revocation.

**Team lean: Option A for MVP, schema designed with Option B migration in mind.** Build Option A, but design the FK relationship so that adding a delegation/trust table later doesn't require a destructive migration. Option C is off the table — it loses per-agent signing keys, which is the point.

**This is the highest-leverage question to resolve first.** Once the identity model is settled, the signing ceremony, capability tokens, and `agents` table schema all follow quickly. (Crypto-side derivation, signing primitive, and revocation are treated in the **Crypto** section below.)

### Git history must surface the agent ID (hard requirement)

Multi-agent-under-one-user is the load-bearing feature, and **the git commit history must show *which agent* wrote each commit** — not a flattened "everything by alice." `git log` on vanilla tooling, on a mirror, on GitHub if mirrored, on `git blame`, anywhere — the agent ID is visible in the artefact. No flattening to single-author at any point in the pipeline.

Concrete encoding (needs anvil's pass on the exact convention):

- **Author** = agent identity. Name carries the agent slug (`Plumb`); email is a stable agent-derived address (e.g. `plumb@agents.alice.cairn` or `alice+plumb@cairn`). Same convention everywhere agents commit.
- **Committer** matches author by default; can carry the human owner if a future workflow wants that distinction. Cairn's renderer treats author as the canonical attribution.
- **Commit trailers** carry machine-readable metadata: `Agent-Id: plumb`, `Agent-Owner: alice`, optional `Agent-Capability: <scope>`. Trailers are stable across rebase / cherry-pick (unlike author overrides during merge), and parseable by external tooling.
- **Signature** by the agent's HKDF-derived Ed25519 key (per Crypto section). Cairn (and any Cairn-aware client) verifies signature against `(user, agent_slug)` derivation; non-Cairn-aware tooling falls back to standard pubkey verification.

Result: `git log` and `git blame` work with vanilla git tooling out of the box and show real per-agent attribution. Cairn-aware UIs additionally render agent badges, capability scopes, and signature-verification status.

### Agent API shape

What "agent-shaped" means concretely beyond standard Forgejo REST. Team notes:

- **Semantic PR operations** — `POST /cairn/pr/open` takes intent (reviewers, CI policy, label targets) rather than raw field set.
- **Typed events / webhook shapes** — machine-readable payloads with stable schemas. Design the event schema first; treat the webhook payload as the API contract.
- **Structured diffs** — semantic diff (file-by-file, change type, context lines) as the primary surface; raw patch as fallback only.
- **Capability-token-scoped endpoints** — per-agent tokens with explicit capability claims (can-merge, can-approve, read-only). Ties directly into crypto.

**Agent-friendly UI:** structured output as primary, HTML as secondary. `?format=json` or `Accept: application/vnd.cairn+json` on any page returns a clean structured payload. Human UI is built on top of the same payload — one canonical data layer, two renderers, not two codepaths.

### MCP tool surface

Not 1:1 with REST — MCP tools should map to *agent intents*, not API endpoints. Candidate set:

- `cairn_open_pr` (branch, title, body, reviewers, ci_required)
- `cairn_get_pr_status` (pr_id) — structured state, not HTML
- `cairn_merge` (pr_id, strategy)
- `cairn_create_branch` (base, name)
- `cairn_commit` (branch, files, message)
- `cairn_watch_ci` (pr_id, timeout) — wait for CI result, return pass/fail/pending

Capability-token scope per MCP tool is the right auth model. Each token specifies which tools the agent can invoke; the MCP server enforces it.

### Crypto

Per-agent Ed25519 keypairs, registered in Cairn at agent-creation time. Commits signed with the agent's key. Revocation via the agent record — mark key revoked, Cairn rejects commit-signatures after that timestamp.

**Deterministic identity derivation (requirement):** The CLI and MCP server must be able to derive agent keypairs deterministically from the user account — same input always produces the same keypair, no external key storage needed. An agent on a fresh machine reconstructs its signing identity from the user's credentials alone.

The mechanism is **hierarchical deterministic key derivation** (same pattern as HD wallets):

1. The human user has a stable **identity seed** — distinct from their auth credential, generated once at account creation and stored in the OS credential store.
2. Per-agent keypairs are derived as: `HKDF(identity_seed, agent_slug || user_id)` → Ed25519 keypair. HKDF (RFC 5869) is the correct primitive for deriving multiple independent keys from one master.
3. Same user + same agent slug → same keypair, on any machine.

The `agent_slug` in the derivation must be stable and canonical (e.g. `plumb`, `anvil`) — not a mutable display name.

**CLI surface this implies:**
- `cairn auth login` stores the identity seed (or derives one from the auth credential) in the local credential store.
- `cairn agents identity <agent-slug>` derives and displays the public key — used for registration and verification.
- When the CLI/MCP acts as an agent, it derives the keypair at call time. No per-agent key files to provision, rotate, or lose.

**Why a separate identity seed rather than the auth credential directly:** Auth credential rotation (password change, token refresh) should not silently rotate all agent identities — that would invalidate registered public keys server-side. Decoupling the identity seed from the auth credential means identity is stable across auth changes. The seed itself has its own rotation path (explicit, deliberate).

**Security properties:** Derived keys are isolated — compromising one agent's key does not expose the root seed or other agents' keys (HKDF isolation). Root-level rotation invalidates all derived keys simultaneously, which is the right revocation primitive for "this user's account is compromised."

**Per-agent surgical revocation — `agent_blocklist` (operator):** Root rotation is the *nuclear* primitive. The *surgical* complement is a server-side blocklist of (user, agent_slug) pairs. CLI/MCP still derives the same keypair every time — that's the operator's deterministic-forging requirement — but the broker/API rejects any frame or call whose derived identity is in the blocklist. One compromised agent is locked out without rotating the root seed and without invalidating the other agents. Combines cleanly with keel's "casket-go Ed25519, revocation by DB record" position: signing primitive stays casket-go Ed25519, the keypair *seed* is the HKDF derivation, and the DB record is the blocklist row.

Verity still owns: key storage format, seed generation ceremony, rotation policy, and revocation semantics (including the threat model for blocklist propagation and replay). Engineering owns: the HKDF derivation path, the `agents` and `agent_blocklist` table schemas, and the API surface for key registration.

### License

GPLv3 is the floor (inherited from Forgejo). Real choice is **GPLv3 vs AGPLv3**. AGPL closes the SaaS loophole — anyone running modified Cairn as a network service must release their modifications. For agent-platform infrastructure, where the most likely competitive scenario is someone running a hosted Cairn-based service, AGPL is the right call.

**Team lean: AGPLv3.** Cairn's own code starts AGPL from the first commit. Forgejo dependency is GPL by inheritance; Cairn as a whole is GPL-compatible for now, AGPL-clean once graduated. If Cairn graduates off Forgejo, the license can be re-chosen freely — AGPL from day one is forward-compatible with that. Decide before any first-party Cairn code is written.

### Public-repo timing

Currently private. Minimum viable public surface: this thoughts doc, a short README, a roadmap. CONTRIBUTING.md, code of conduct, and issue templates can come incrementally. Given market timing, earlier is better.

### Governance

Who decides, once outside contributors arrive. Options: BDFL, small maintainer trust, sponsor-org-with-charter (CarriedWorldUniverse), eventual foundation. Decide before the first outside contributor lands a PR.

### CI/CD topology

Forgejo Actions runner placement. Heavy compute → <server-host>-Linux. Light → nexus-cw-ec2? Per-agent runner tokens? Forge owns this.

### Storage architecture and S3 cache layer

Forgejo can address S3 for LFS / attachments / packages, but git's hot path (pack files, deltas, ref reads) punishes naive S3 backing — this is exactly the gap code.storage's "60× faster than S3/R2" claim is built around. Cairn needs a caching layer between hot git ops and cold S3.

Candidate shapes:
- **Local-SSD read-through cache** on the Cairn host. Simplest. Bounded by host disk; LRU eviction; write-through or write-back to S3. Single-node story is clean; HA story requires more thought.
- **Dedicated cache service** — Redis (or similar) for metadata + a filesystem cache for blob/pack data. More moving parts, scales horizontally, but introduces a cache-coherence problem.
- **FS-style cache layer fronting S3** — JuiceFS, SeaweedFS, Garage. Pre-built solutions to "fast cached filesystem on top of object storage." Inherits their design choices (and bugs).

Forge owns this — storage and heavy compute is his domain. Real prerequisites for a decision: expected repo sizes, hot-set ratio, write rate from agents, and whether HA is in scope for MVP.

### CLI surface

Probably cobra-Go conventions, gh-style verb-noun structure. Whether agent-flavoured commands are first-class (`cairn agents pr open …`) or implicit via auth context needs a real pass.

### Contribution shape

When to go public, what the initial contribution surface looks like, how outside PRs get triaged.

## Locked decisions (won't relitigate without substantive objection)

- Substrate: Forgejo
- Approach: Fork (B), understood as a path not a destination
- Name: Cairn
- Rebase strategy: patch stack + additive packages + per-minor-release cadence
- Module path set to final Cairn path from first fork commit
- Bootstrap via existing Forgejo, then self-host
- License: AGPLv3 for Cairn's own code
- Identity model: Option A for MVP, schema designed for Option B migration
- Agent API: structured/semantic over raw REST; one data layer, two renderers
- Crypto: deterministic per-agent keypairs via HKDF from user identity seed; separate identity seed from auth credential

## Re-entry point

When the team session resumes, start at the **identity model** — the signing ceremony is now shaped (HKDF from identity seed, Verity owns storage/rotation). Remaining open: identity seed generation ceremony details, and the Option A schema design with B-migration path. Once identity is locked: agent API shape, then MCP tool surface.
