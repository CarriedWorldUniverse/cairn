# Cairn — AI-native amendment to the foundation design

**Status:** approved (brainstormed and agreed 2026-05-10)
**Amends:** [`2026-05-09-cairn-foundation-design.md`](2026-05-09-cairn-foundation-design.md)
**Reframe scope:** product framing + two new MVP features. Plans 1–4 (HKDF identity, push-time signature verification, agent registration, markdown rendering, `.well-known/`) are **unchanged**; this amendment treats them as the substrate for an AI-native platform rather than a feature set in their own right.

---

## 1. Pivot

The foundation spec described Cairn as "agent-native": a Forgejo fork where AI agents are first-class committers with cryptographic identities. After dogfood-deploy planning surfaced operational and product questions on 2026-05-10, the framing tightens:

> **Cairn is AI-native. AI agents are first-class users. Humans review only — they accept, reject, or request changes; they do not edit AI-authored work to ship it.**

Plans 1–4 already deliver the *plumbing* for this: HKDF-derived agent identity, push-time signature verification, owner-attribution, markdown-rendered surfaces for AI consumers. What was missing is the *workflow layer* that makes "AI authors / human reviews" the platform's flagship motion.

This amendment adds two MVP features to close that gap, and corrects one default-policy decision in the original spec.

---

## 2. What this amendment changes

| Item | Original spec | Amended |
|---|---|---|
| Product frame | "Agent-native git platform" | **"AI-native git platform" — AI as first-class user, humans review only** |
| Default branch protection | "Default to opt-out branch protection (Cairn design preference)" | **Default branch protection ON for `main`/`master`, requiring human-only approval** (the original default contradicted the review-only frame) |
| MVP feature scope | Identity + signing + markdown rendering + `.well-known/` | **+ Simplifier (server-side AI summarization), + Human-review enforcement** |
| Build sequence | Plan 5 = deploy current Cairn | **Plan 5 = build Simplifier; Plan 6 = build Human-review enforcement; Plan 7 = deploy AI-native Cairn** |

Personal-substrate ceiling (`project_cairn_deployment_ceiling`) is preserved. This amendment makes Cairn a more useful single-tenant deploy; it does not move toward public hosting.

---

## 3. New feature — Simplifier

### 3.1 Purpose

Generate plain-language summaries of AI-authored work so human reviewers can scrutinize without reading every line of diff. The simplifier is a **simplifier, not a reviewer** — it does not judge, grade, approve, or block. It compresses the AI's output into something a human can read fast.

### 3.2 Identity

Simplifier output posts to PRs and renders into platform views as `cairn` — a system-actor identity, not a Cairn-agent identity. Rationale: the simplifier is a server-side platform feature, not a user-driven actor; treating it as an agent would force HKDF derivation, fingerprinting, and a registration flow for what is fundamentally a built-in primitive.

### 3.3 Backing

Server-side. Cairn ships the integration; each org configures which AI service backs it (endpoint URL, credentials, optional region/auth metadata). The org's credentials cover the cost of all summarization calls for that org.

Cairn-the-software does **not** embed or host an AI model. Self-hosters who don't configure an AI service get no summaries; the rest of Cairn works unchanged. Orgs that do configure one supply the endpoint shape Cairn expects (OpenAI-compatible chat-completions API surface for MVP; other shapes can be added post-MVP via per-shape adapters).

### 3.4 Prompt

**Cairn ships a single standardized prompt for MVP.** Different AI services respond differently to the same prompt; tuning per-org would multiply the QA matrix. We pick one prompt, tune it as a unit, ship it.

Per-org prompt customization is deferred to v1.x.

### 3.5 Scope of summarization

**Default:** PR-level summary only. One paragraph per PR, regenerated when commits are pushed.

**Org may opt into wider scope:**
- Commit-level summaries (one per commit, composed into the PR view)
- File-level summaries (one per changed file, drilled-into per file)

Wider scope = higher cost (more API calls). Default is the cheapest tier.

### 3.6 Public vs private repos — data exposure policy

The simplifier sends repository content to whichever AI service the org configured. That data leaves the org's perimeter. Default behavior depends on repo visibility:

- **Public repos:** simplifier runs by default once the org has an AI service configured. Code in public repos is already public; sending it to an AI service does not change its exposure.
- **Private repos:** simplifier requires **explicit per-repo opt-in**. The repo admin must (a) turn on summarization for that repo and (b) choose what level of detail is sent — `full` (title + body + full diff + commit messages), `commit-messages` (title + body + commit messages, no diff), or `metadata` (title + body + file paths only, no content). Default for newly-private repos: off.

This makes the data-exposure decision explicit and per-repo for content the org considers sensitive. Admin docs must surface this clearly.

### 3.7 Lifecycle

- **Trigger:** PR opened, or commits pushed to a PR's head branch. Debounced — multiple rapid pushes coalesce into one summarization run.
- **Execution:** asynchronous. Queued; a background worker picks the job up, calls the configured AI service, stores the result.
- **Caching:** keyed by `repo_id + pr_number + content_hash`. Same PR state → same summary, no re-call.
- **Regeneration:** automatic on content change. Manual via "Regenerate summary" button on the PR page.
- **Failure mode:** if the AI service returns error, times out, or the org has no service configured, the PR view shows "Summary unavailable" with an admin-debug link. The PR view itself is not blocked.

### 3.8 Surfaces

- **PR HTML page:** summary block at the top, before the conversation. Includes regenerate button (admin-gated post-MVP, anyone-can-trigger in MVP for simplicity).
- **`?format=md` PR view:** summary inlined at the top of the markdown rendering. AI clients reading the PR get the same summary humans see.
- **API:**
  - `GET /api/cairn/v1/orgs/{owner}/summarizer` — admin reads config (credentials redacted)
  - `PUT /api/cairn/v1/orgs/{owner}/summarizer` — admin updates config
  - `GET /api/cairn/v1/repos/{owner}/{repo}/summarizer` — repo admin reads per-repo consent (private only)
  - `PUT /api/cairn/v1/repos/{owner}/{repo}/summarizer` — repo admin sets consent + scope (private only)
  - `GET /api/cairn/v1/repos/{owner}/{repo}/pulls/{n}/summary` — fetch cached summary; 404 if not yet generated
  - `POST /api/cairn/v1/repos/{owner}/{repo}/pulls/{n}/summary/regenerate` — manual trigger
- **Admin UI:** org settings → "AI summarizer" panel; private repo settings → "Enable summarization" panel with field-scope picker.

### 3.9 Storage

`forgejo` table conventions; Cairn-prefix all new tables.

- **`cairn_summarizer_config`** — per-org row:
  - `owner_id` (FK to user/org), `enabled` (bool), `provider` (bridle ProviderID string: claudecode | openai-api | claude-api | bedrock | ollama-local), `endpoint_url` (text, optional), `model_id` (text), `credentials` (encrypted), `levels_enabled` (bitfield: PR | commit | file), `created_at`, `updated_at`
- **`cairn_summarizer_repo_consent`** — per-repo row, only relevant when repo is private:
  - `repo_id` (FK), `enabled` (bool, default false), `data_scope` (enum: full / commit-messages / metadata), `created_at`, `updated_at`
- **`cairn_pr_summary`** — cache:
  - `repo_id`, `pr_number`, `content_hash`, `summary_md` (text), `model_id` (audit), `generated_at`, `token_count` (audit), unique on (`repo_id`, `pr_number`, `content_hash`)

Encryption key for `credentials` lives at the same path as the existing instance HMAC key (admin-managed, one per Cairn instance). Same threat model.

### 3.10 What the simplifier does NOT do

Out of scope for the simplifier feature, even at v1.x:

- Approve, reject, or block PRs
- Comment on review threads as a participant
- Modify commits or open new PRs
- Run tests, scan for vulnerabilities, lint, or otherwise judge the work
- Generate any kind of risk-score, confidence rating, or quality grade

The simplifier is a *renderer*. Anything resembling judgment is a different feature with different UX and trust implications.

---

## 4. New feature — Human-review enforcement

### 4.1 Purpose

Make "humans review only" enforceable. If AI agents are first-class committers, they can also nominally approve PRs — but allowing AI approvals to satisfy "1 review required" gates collapses the human-review property. Human-review enforcement closes that loop.

### 4.2 Layering

Builds on Forgejo's existing branch protection rules. Cairn does not invent a new protection system; it adds a filter layer at approval-evaluation time.

### 4.3 Org-level toggle

`cairn_review_policy.require_human_only_approval` — per-org boolean. Default `true` for AI-native deploys.

When `true`:
- **Approval filter.** When Forgejo computes whether a PR has met the "X approving reviews required" threshold, Cairn filters out approvals from agent users (any user in `cairn_agent`). Only non-agent (human) approvals count toward the threshold.
- **Owner-cluster self-approval block.** A PR authored by an agent owned by user X cannot be counted as approved by *anyone in X's owner-cluster* — not by user X themselves, and not by any of X's other agents. Both are X's tools or X-as-author proxies; counting either as "human review" defeats the gate. (Jacinta cannot approve a PR opened by her `cairn-builder` agent, because that PR is effectively her work; her `cairn-ops` agent cannot approve it either.) The basic agent-filter already covers the cross-agent case (agent approvals never count); the additional rule here covers the human-owner case (X cannot self-approve their own agent's PRs).

When `false`:
- Forgejo's default approval-counting behavior applies. Useful for self-hosters who want a vanilla-Forgejo experience.

### 4.4 Branch protection default — corrected

Original spec note: *"Default to opt-out branch protection (Cairn design preference)."* That predates the AI-native pivot and is wrong for this product.

**Replacement default:** When a new repo is created in an org with `require_human_only_approval = true`, Cairn auto-applies a branch protection rule to `main` (and `master` if it exists) requiring **at least one approving review**. Combined with the approval filter, this means: AI agents can push to feature branches and open PRs; nothing lands on `main` without a human approval.

Self-hosters with `require_human_only_approval = false` get the original opt-out default.

### 4.5 Surfaces

- **Admin UI:** org settings → "Review policy" panel with the toggle + a one-paragraph explainer.
- **API:**
  - `GET /api/cairn/v1/orgs/{owner}/review-policy`
  - `PUT /api/cairn/v1/orgs/{owner}/review-policy`
- **PR page:** when an agent's approval is filtered, the PR's "Reviewers" widget should make this visible — e.g., the agent shows as having reviewed but with a "doesn't count toward gate" badge. Avoids confusion when a PR has visible approvals but isn't mergeable.

### 4.6 Storage

- **`cairn_review_policy`** — per-org row:
  - `owner_id` (FK), `require_human_only_approval` (bool), `created_at`, `updated_at`

---

## 5. Agent permissions model

### 5.1 MVP — inheritance

An agent has the same effective permissions as its owner-user has on any given resource. If user X can write to `repo Y`, X's agents can write to `repo Y`. If X cannot, X's agents cannot. Cairn does **not** add a per-agent ACL layer in MVP.

This is the cheapest model that ships and works correctly for the personal-substrate ceiling.

### 5.2 Deferred design item — per-agent role definition

Post-MVP, the user should be able to define each of their agents' roles independently — e.g., `commit-only`, `pr-author`, `reviewer`, `read-only`, `issues-only`, `full`. The exact role taxonomy is TBD when the feature is built.

The effective capability becomes `min(user_perms, agent_role_capability)`: the agent can never exceed its owner's permissions, but the user can voluntarily restrict each agent below their own ceiling. Useful for least-privilege agent design ("my drafting agent can open PRs but not push to main; my review agent can only read").

**Storage:** no schema work in MVP. When this lands, additive migration adds `roles` to `cairn_agent` (likely JSON or a join table). Cairn's migration pattern handles this without backfill complexity.

This is the **only** deferred design item in the AI-native amendment. Everything else either ships in MVP or is explicitly out of scope.

---

## 6. Owner-stable agent identity (clarification)

To prevent re-litigation later: an agent's HKDF-derived identity is **derived from its owner-user's seed**, and the owner-user is **always a human**, never an org. Repo transfers (user→org, org→user, org→org) and user→org conversions do **not** change agent identity.

Concrete:
- Agent `cairn-ops` owned by `alice` has fingerprint derived from `alice`'s seed.
- If `alice`'s repo `cairn-experiments` transfers to org `darksoft`, the agent stays attached to `alice`. Its fingerprint, signature derivation, and `Agent-Owner` trailer are unchanged.
- The agent can push to `darksoft/cairn-experiments` because `alice` (the human) has write access to org-owned repos via her org membership. Forgejo handles the permission check; Cairn checks signature and identity as before.

Implication for orgs: if an org wants to admission-control which agents may push to its repos (independent of which humans have write access), that's a future per-org feature. Not a current problem.

---

## 7. Personal-substrate ceiling preserved

`project_cairn_deployment_ceiling` says: Cairn's deployment scope is "personal substrate" — Jacinta + her agent team, no external users. This amendment does not move that ceiling.

- Multi-org capability lives in the schema (`cairn_summarizer_config`, `cairn_review_policy` are keyed by `owner_id`). Self-hosters who run multiple orgs get the feature; Jacinta's instance uses one org and pays no complexity for the multi-org dimension.
- The simplifier is per-org configurable, which means *one* configuration in Jacinta's deploy. Same answer as a hardcoded global: works.
- Public hosting is still a separate, future commitment.

---

## 8. Cairn-ops aspect (operational layer)

Adjacent to this amendment but **not** a Cairn-the-software feature: a Claude Code instance plays the `cairn-ops` aspect role on or near Jacinta's deploy. It uses Cairn's existing public surfaces (HTTP API, `?format=md` views, agent registration, signed pushes) to handle routine ops — backups, agent approvals, monitoring, deploy hygiene.

For now (2026-05-10), Plumb plays this role through Jacinta's Claude subscription, in addition to its build-out duties. When a dedicated `cairn-ops` aspect is split off, the role lifts off Plumb cleanly: no Cairn-the-software changes required.

This is documented here so it doesn't get conflated with the platform features above. The aspect is a *consumer* of Cairn, not a *part* of Cairn.

---

## 9. Build sequence (revised)

| Plan | Title | Status |
|---|---|---|
| 1 | Identity layer (HKDF + tables) | done |
| 2a | Registration API | done |
| 2b | Registration CLI | done |
| 3 | Push verification | done |
| 4 | Read paths (markdown + `.well-known/`) | done |
| **5** | **Build Simplifier** | next |
| **6** | **Build Human-review enforcement** | after Plan 5 |
| **7** | **Deploy AI-native Cairn to nexus-cw-ec2** | after Plan 6 |
| 8 (post-deploy, separate track) | Bring up cairn-ops aspect | independent of Plans 5–7 |

Plan 5 supersedes the original Plan 5 (which was "deploy current Cairn"). The deployment runbook (`cairn/deploy/deployment-runbook.md`) needs amendments to reflect the AI-native MVP — including the simplifier admin surfaces, the per-org config flow, the human-review enforcement toggle, and the corrected branch-protection default. Those amendments happen at the start of Plan 7, not now.

---

## 10. Self-review notes

Items checked against the original foundation spec for consistency:

- **HKDF identity model** — unchanged. Simplifier identity (`cairn` system actor) is *outside* the agent identity model by design; not a contradiction.
- **Push-time signature verification** — unchanged. AI agents commit and sign exactly as before.
- **`?format=md` rendering** — extended (simplifier inlines summary at top). No conflict with the existing renderers; it's an additional block before the existing content.
- **`.well-known/cairn.json` manifest** — should advertise the new endpoints (`features.simplifier_enabled`, `features.review_policy`) once Plans 5/6 land. Not changed by this amendment per se, but Plan 5/6 implementers must update it.
- **Personal-substrate ceiling** — preserved (§7).
- **AGPL boundary** — all new code (simplifier, review policy, admin UIs, data model additions) is Cairn-original, AGPLv3. No Forgejo-substrate changes are introduced beyond the existing thin patches.

No contradictions surfaced.

---

## 11. Out-of-scope explicitly

- AI judging / scoring / grading PR quality
- AI-driven auto-merge
- AI-driven test generation, lint, vulnerability scanning, or any quality-judgment feature
- Per-agent ACL beyond inheritance (deferred — §5.2)
- Multi-tenant / public hosting features (deferred — `project_cairn_deployment_ceiling`)
- Per-org prompt customization for the simplifier (deferred — §3.4)
- Streaming summarization output (deferred — MVP is batch generate-and-store)
- Cairn-side hosted AI model (out of scope at any horizon — Cairn stays model-agnostic at the platform level; simplifier reaches an external service)
