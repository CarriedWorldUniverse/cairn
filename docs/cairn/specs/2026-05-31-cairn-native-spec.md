# cairn — native MVP spec (CWB-shape git host, go-git backed)

**Status:** draft for approval · 2026-05-31
**Goal:** the minimum git-hosting service that fits CWB natively — repos own-linked to herald orgs, herald the single IdP for humans and agents, ledger the issue tracker, PRs as ledger tickets. A single Go binary on k3s, deliberately small.
**Why now:** the Forgejo-fork model is being abandoned (NEX-136 closed). With path-A (NEX-393) putting herald in front of human login, cairn moves to a native build. This spec is what we build first.

This is the **MVP cut** of NEX-384. The full surface — wikis, actions, packages, mirrors, project boards, LFS, web editing — is **explicitly de-scoped**, see §9. We're building 10% of Forgejo's surface area but 100% of what CWB actually needs.

---

## 1. The one-paragraph architecture

Cairn is a single Go binary wrapping `go-git` (the protocol/storage engine — used by GitLab Runner, Argo, Flux, gh CLI internals) with three frontends and an authorization layer. The **SSH frontend** authenticates agents via their casket Ed25519 public key (no separate SSH key — the casket pubkey IS the SSH identity); the **HTTP frontend** receives requests pre-authenticated by interchange-gateway, reading verified identity from `X-CWB-*` headers; the **web UI** uses the herald path-A authz_code flow for browser-based human login. Repos belong to herald orgs; permissions are scope strings (`repo:read|write|admin`) drawn from herald's vocabulary. PRs are not a separate data model — a PR is a `refs/cairn/pr/N` pointer plus a ledger ticket with a `pr` tag; lifecycle, comments, reviews all live in ledger. Storage primitives (per-repo XTS git disks, casket envelope, S3 backup/replicator) reuse from NEX-348 unchanged.

```
                            ┌──────────── cairn ────────────┐
   agent ──ssh + casket───►  ssh frontend                    │
   human ──https + cookie─►  http frontend  ── go-git ──────►│ repos/
   relier ─/authorize────►  (via interchange-gateway +       │
                          │   path-A herald session)         │
                          │                                  │
                          │  repos · refs · protection rules │
                          │  PR-ref → ledger ticket          │
                          └──────────────────────────────────┘
                              ▲                          ▲
                              │ herald JWKS              │ ledger API
                              │ (heraldauth verify)      │ (PR lifecycle)
                              │
                  herald (NEX-376 + NEX-393)         ledger (NEX-137)
```

Everything outside that diagram — issues, CI, wikis, packages, project boards — is someone else's job in CWB. Cairn does git. That's it.

---

## 2. MVP scope — what's IN

1. **Repo + ref CRUD** via `go-git`. Repos belong to herald orgs; per-repo bare git directory; pluggable storage so NEX-348 XTS-disk-per-repo slots in later without API churn.
2. **SSH frontend** — `gliderlabs/ssh` wrapping `golang.org/x/crypto/ssh`. Auth via casket pubkey (lookup by `fingerprint = base64url(sha256(pubkey)[:16])`); the matched agent must be active in herald + have the right scope for the requested repo. Commands handled: `git-upload-pack` (clone/fetch), `git-receive-pack` (push).
3. **HTTP frontend** — Smart-HTTPv2. Reached via interchange-gateway at `/cairn/<org>/<repo>/...`; gateway has already authenticated the caller and injected `X-CWB-Subject/Org/Kind/Scopes`. Cairn trusts those headers (gateway is the boundary, cairn is in-cluster ClusterIP).
4. **Web UI** — server-rendered HTML + minimal JS (HTMX). Pages: org listing, repo listing within org, repo overview, tree browser, blob view, blame, ref listing, commit view, diff between commits, PR view (renders the linked ledger ticket + the diff). Every page has a `?format=md` mode returning markdown for agents that want to scrape via HTTP.
5. **Human login** — path-A OIDC client. Cairn registers as a herald OIDC client; humans land at herald's `/login`, return with an authz_code, cairn exchanges for an access token, stores in a session cookie.
6. **Repo + org model** — repos belong to herald orgs (`org_id` references herald's canonical UUID); repo `slug` unique within org; `default_branch`, `protection_rules JSON`, timestamps.
7. **Branch protection** — declarative per-repo rules: pattern (`main`, `release/*`) + required scope + optional org-tree minimum (accountability axis above level N). Evaluated at `receive-pack` time. Bypass requires `bypass=true` flag + writes a ledger audit ticket.
8. **PR-as-ledger-issue** — push to a non-default branch creates `refs/cairn/pr/N` + a ledger ticket with `kind=pr, label=pr`. Subsequent pushes update the ref + append diff range to the ledger ticket. Ledger transitions drive PR state (`In Review → Merged → Done` updates the target branch in cairn).
9. **Storage primitives reuse** — NEX-348 + children (casket envelope, S3 backup, per-repo XTS, casket key derivation) transfer directly. Cairn calls them via a thin storage adapter; the wrapping service changes, the primitives don't.
10. **k3s deploy** — Containerfile (multi-stage Go build, scratch base) + manifests (Deployment + ClusterIP for HTTP + LoadBalancer/NodePort for SSH on port 22). Same pattern as herald + interchange-gateway.

---

## 3. Data model (MVP)

What CAIRN owns:

```
repo
  id              uuid PK
  org_id          uuid             -- herald org (no FK; herald is a separate service)
  slug            text             -- unique within org
  default_branch  text             -- e.g. "main"
  protection      text JSON        -- branch protection rules (see §7)
  storage_uri     text             -- where this repo lives (file:///var/lib/cairn/repos/<org>/<slug>.git, or s3://, or xts://...)
  created_at, updated_at

pr_pointer
  repo_id         uuid FK→repo
  pr_number       int              -- monotonic per repo
  ref             text             -- "refs/cairn/pr/N"
  ledger_ticket   text             -- e.g. "NEX-1234" or whatever ledger emits
  head_sha        text             -- current head; updated on each push
  base_branch     text             -- target branch
  primary_key (repo_id, pr_number)

push_event             -- ephemeral audit trail; rotated/pruned
  id              uuid PK
  repo_id         uuid FK→repo
  ref             text
  old_sha         text
  new_sha         text
  pusher_sub      text             -- herald subject id
  pusher_kind     text             -- "agent" | "human"
  at              timestamp
  bypass          bool             -- true if branch-protection bypass was used
```

What CAIRN does **NOT** own (in herald):
- Orgs, users (humans + agents), scope grants
- Casket pubkey → agent mapping (cairn fetches from herald, caches briefly)

What CAIRN does **NOT** own (in ledger):
- PR title, description, comments, reviews, lifecycle status
- Issues
- Knowledge / wiki content

Invariants:
- `repo.org_id` references herald's canonical org UUID. Validation (org exists, requester is in it) happens at API time via the gateway's `X-CWB-Org` header.
- `pr_number` is monotonic per repo, gap-allowed (PRs that get cancelled before code arrives leave gaps).
- `pr_pointer.ledger_ticket` is opaque to cairn — it's whatever ledger returned.

---

## 4. Authz model

Scopes (cairn's slice of herald's vocabulary):

| Scope | What it permits |
|---|---|
| `repo:read` | Clone, fetch, browse via web UI |
| `repo:write` | Push to non-protected branches; open PRs |
| `repo:admin` | Bypass branch protection (with audit); update protection rules; delete refs |
| `repo:create` | Create new repos within an org |

Org-tree axes (when herald's full org-tree lands; MVP uses flat scopes):
- **Accountability axis (up):** for a branch protection rule like "main requires accountability ≥ N above the repo's owning team," cairn asks herald for the requester's accountability level over the team. This integration is deferred behind herald's tree implementation — MVP gates everything on flat scopes.

Everywhere a request mutates state, cairn checks the appropriate scope against the gateway-verified `X-CWB-Scopes` header (HTTP) or the herald-token verified via heraldauth (SSH, where the gateway isn't in the path).

---

## 5. Auth flows

### 5a. Agent SSH clone/push

1. Agent runs `git clone ssh://agent@cairn.cwb.svc/<org>/<repo>`.
2. Agent's SSH client presents its casket key (loaded from the agent's keyfile).
3. Cairn's sshd-callback computes the fingerprint, looks up the agent via heraldauth's identity helper (or a small admin REST query — design choice in implementation plan), confirms the agent is active + has `repo:read` (clone) or `repo:write` (push) for the target repo.
4. Cairn dispatches `git-upload-pack` or `git-receive-pack` to go-git with the repo's storage backend.
5. On push, branch protection (§7) is evaluated server-side; rejected refs are reported back to the client through git's normal protocol.

### 5b. Human HTTP clone/push

1. Human runs `git clone https://cwb/cairn/<org>/<repo>` with a credential helper that presents a herald access token in `Authorization: Bearer <token>`.
2. Request hits interchange-gateway. Gateway verifies the bearer via heraldauth, strips spoofed identity headers, injects verified `X-CWB-*` headers, proxies to cairn.
3. Cairn reads `X-CWB-Scopes`, confirms the right scope for the operation, dispatches to go-git's HTTP server transport.
4. Credential helper pattern documented separately: humans run `git config credential.helper cwb-helper`, helper fetches the human's current access token from a local keyring (token obtained via path-A login earlier).

### 5c. Human web UI

1. Human visits `https://cwb/cairn/...`. Cairn checks for a session cookie. None → 302 to herald `/authorize` with PKCE.
2. Herald path-A flow (NEX-393): login → consent (auto for first-party) → 302 back with `code`.
3. Cairn `/oauth/callback` exchanges code for access token, stores in a server-side session keyed by a `cairn_session` cookie (random 32-byte id), redirects to the originally-requested page.
4. Subsequent requests carry the cookie; cairn looks up the session, reads the access token, verifies via heraldauth on each request (cached for the access token's lifetime).

### 5d. Agent HTTP (rare; CI-like)

Same as 5b but the credential helper is the agent's runtime: presents the agent's herald token from a Secret/keyfile.

---

## 6. PR-as-ledger-issue lifecycle

A PR is **two things**, no more:

1. A git ref: `refs/cairn/pr/N` pointing at the pushed head commit
2. A ledger ticket: `kind=pr`, label `pr`, title from latest commit subject, body containing repo + branch + diff summary, fields for `base_branch` + `head_sha` + `cairn_repo_id`

Lifecycle:

| Trigger | Cairn does | Ledger does |
|---|---|---|
| Push to non-default branch | Compute `N = next_pr_number(repo)`; write `refs/cairn/pr/N`; record `pr_pointer` row | Create ticket via API; cairn stores the ticket key in `pr_pointer.ledger_ticket` |
| Push to same branch again | Update `refs/cairn/pr/N`; update `pr_pointer.head_sha` | Cairn appends a comment with the new diff range |
| Ledger ticket transitions to "In Review" | (nothing) | (just a ledger state change; cairn cares about the merge transition only) |
| Ledger ticket transitions to "Merged" (or equivalent) | Cairn observes via webhook/poll, fast-forwards `base_branch` to the PR head (or rejects + comments back if the FF fails) | (cairn writes back the merge SHA into the ticket as a comment) |
| Ledger ticket transitions to "Cancelled" | Delete `refs/cairn/pr/N` and the `pr_pointer` row | (ticket stays as historical audit) |

Reviews, comments, approvals, requested changes are **all ledger primitives** — cairn doesn't render or store them. The web UI's PR view fetches the ledger ticket and renders it inline alongside the diff.

The merge mechanism is fast-forward only for MVP. Rebase/merge-commit/squash come later (design space, not blocking).

---

## 7. Branch protection

Rules stored as JSON on `repo.protection`:

```jsonc
{
  "rules": [
    {
      "pattern": "main",
      "required_scope": "repo:admin",
      "allow_force_push": false,
      "allow_delete": false,
      "bypass_requires_audit": true
    },
    {
      "pattern": "release/*",
      "required_scope": "repo:admin",
      "allow_force_push": false,
      "allow_delete": false
    }
  ]
}
```

Evaluated at `receive-pack` time. First-match-wins by `pattern` order. The default-for-MVP rule (auto-applied to every repo on creation) protects the default branch with `required_scope: "repo:admin"` + no force-push + no delete + audit-on-bypass.

Bypass: a push can include `--push-option=bypass=true`; cairn checks for the `repo:admin` scope (regardless of rule), allows the operation, writes a `push_event` row with `bypass=true`, and creates a ledger audit ticket noting who bypassed what.

What MVP does NOT include: required reviews (lives in ledger's PR lifecycle), required status checks (no CI in cairn's scope), required signed commits (later), required linear history (later).

---

## 8. API surface (MVP)

Reached via interchange-gateway at `/cairn/...`.

```
GET    /cairn/api/orgs/{org}/repos                # list repos in org
POST   /cairn/api/orgs/{org}/repos                # create repo
GET    /cairn/api/orgs/{org}/repos/{slug}         # repo metadata
PUT    /cairn/api/orgs/{org}/repos/{slug}/protection  # update branch protection
DELETE /cairn/api/orgs/{org}/repos/{slug}         # delete repo (repo:admin)

GET    /cairn/api/orgs/{org}/repos/{slug}/refs    # list refs
GET    /cairn/api/orgs/{org}/repos/{slug}/refs/{ref-name}  # get ref
DELETE /cairn/api/orgs/{org}/repos/{slug}/refs/{ref-name}  # delete ref (repo:admin + protection check)

GET    /cairn/api/orgs/{org}/repos/{slug}/prs        # list PRs
GET    /cairn/api/orgs/{org}/repos/{slug}/prs/{N}    # PR detail (ref + ledger ticket key)

# Smart-HTTP git
GET    /cairn/{org}/{slug}/info/refs?service=git-upload-pack
POST   /cairn/{org}/{slug}/git-upload-pack
GET    /cairn/{org}/{slug}/info/refs?service=git-receive-pack
POST   /cairn/{org}/{slug}/git-receive-pack

# Web UI
GET    /cairn/                                    # org list
GET    /cairn/{org}                               # repo list
GET    /cairn/{org}/{slug}                        # repo overview
GET    /cairn/{org}/{slug}/tree/{ref}/{path}      # tree browser
GET    /cairn/{org}/{slug}/blob/{ref}/{path}      # blob view
GET    /cairn/{org}/{slug}/blame/{ref}/{path}     # blame
GET    /cairn/{org}/{slug}/commit/{sha}           # commit view
GET    /cairn/{org}/{slug}/compare/{base}/{head}  # diff
GET    /cairn/{org}/{slug}/pr/{N}                 # PR view (diff + linked ledger ticket inline)
GET    /cairn/oauth/callback                      # path-A OIDC callback

# Internal (in-cluster only, never gateway-routed)
GET    /healthz
```

Every UI page supports `?format=md` returning a markdown representation (so agents can scrape via plain HTTP without parsing HTML).

---

## 9. Explicitly DEFERRED (NOT in MVP)

These are NOT in cairn-native. Marked here so the boundary is unambiguous:

- **Wikis** — commonplace handles knowledge.
- **Actions / CI / runners** — separate concern.
- **Packages** (npm, container registry, etc.) — porter is the blob layer.
- **Mirrors / repo migrations / GitHub import** — defer.
- **Project boards / kanban** — ledger.
- **LFS** — porter.
- **Web-based code editing** — read in browser only.
- **Pull-request UI from scratch** — the diff renders; reviews live in ledger.
- **Required reviews / required status checks** — ledger PR lifecycle for reviews; no CI in scope.
- **Required signed commits / linear history / squash merges** — fast-forward merge only for MVP.
- **Issue tracking** — ledger.
- **Notifications / activity feeds** — separate concern; ledger emits events, a future notifier consumes.
- **Per-user settings page** — admin REST for now.
- **Repo forks / cross-repo PRs** — defer until there's a use case.
- **Tags / releases UI** — refs are listed; release notes belong somewhere else (probably ledger or porter).
- **Anonymous public read** — every request is herald-authed in MVP; public-read scope added later if needed.
- **Multi-tenant on shared storage** — single-tenant per-repo storage backend (XTS disks per NEX-348).

---

## 10. Deployment (MVP)

Same shape as herald + interchange-gateway:

- **Containerfile** at `cmd/cairn-server/Containerfile` — multi-stage Go build (CGO_ENABLED=0, scratch base). Image target < 30 MB.
- **k3s manifests** at `deploy/k3s/` — Deployment + ClusterIP Service for HTTP (port 8080) + LoadBalancer/NodePort Service for SSH (port 22). PVC for repo storage (`local-path` MVP, upgradable to XTS-backed via NEX-348). Secret `cairn-secrets` with `ssh_host_key` + any other persistent material.
- **Gateway routing** — update interchange-gateway's `INTERCHANGE_ROUTES` to include `/cairn=http://cairn.cwb.svc:8080`. Public-paths if any cairn endpoints need to be auth-free (none in MVP — even health is internal).
- **Health probe** — `/healthz` on the HTTP service.
- **Environment**:
  - `CAIRN_ADDR_HTTP` (default `:8080`)
  - `CAIRN_ADDR_SSH` (default `:22`)
  - `CAIRN_DB` (default `/var/lib/cairn/cairn.db` — sqlite for metadata)
  - `CAIRN_REPO_ROOT` (default `/var/lib/cairn/repos`)
  - `CAIRN_HERALD_ISSUER` (the public herald issuer URL)
  - `CAIRN_HERALD_JWKS_URL` (optional override pointing at the in-cluster JWKS; same shape as interchange-gateway's override)
  - `CAIRN_HERALD_CLIENT_ID` / `CAIRN_HERALD_CLIENT_SECRET` (the OIDC client creds)
  - `CAIRN_LEDGER_URL` + `CAIRN_LEDGER_TOKEN` (talking to ledger — defer the exact contract to the ledger-port story)

---

## 11. Build sequence (for the implementation plan)

Each step independently testable:

1. **Spec + decisions sign-off** (this doc).
2. **Repo + ref CRUD core** (NEX-386) — go-git wrapped in a CWB-shaped API. SQLite metadata store. No frontends yet; tested via direct API calls with a fake herald token.
3. **SSH frontend** (NEX-387) — gliderlabs/ssh, casket-pubkey-as-SSH-identity callback, `git-upload-pack` / `git-receive-pack` dispatch. Smoke: a real agent clones + pushes.
4. **HTTP frontend** (NEX-388) — go-git's Smart-HTTPv2 server, gateway-fronted, scope-gated. Smoke: `git clone https://...` works.
5. **Branch protection** (NEX-391) — rule evaluation at `receive-pack` time, bypass-with-audit. Hard to test fully until ledger is wired (audit ticket creation), so MVP step skips the audit-ticket side and just logs.
6. **PR-as-ledger-issue** (NEX-390) — push-to-non-default-branch triggers ledger ticket creation; ledger merge-transition triggers FF; subsequent pushes append diff comment. Requires ledger contract from NEX-379.
7. **Web UI** (NEX-389) — server-rendered HTML + path-A OIDC client wire-up. Depends on herald NEX-393.
8. **Containerfile + k3s manifests** (NEX-392) — deploy onto dMon k3s, integration-test the full flow.

Per-step ordering: 2 can land first as the foundation. 3 + 4 can go in parallel. 5 is small, can interleave. 6 needs ledger contract done (NEX-379). 7 needs herald path-A done (NEX-393 / 398-400). 8 is the wrap-up.

**Definition of done for cairn-native MVP:** an agent clones via SSH using its casket key, pushes a branch, a PR is created in ledger automatically, a human browser-logs-in via herald, views the diff in cairn's web UI, comments + transitions the ledger ticket to merged, cairn fast-forwards the target branch — all on k3s on dMon.

---

## 12. Open questions for the plan (small, non-blocking)

- **SSH library:** `gliderlabs/ssh` vs raw `golang.org/x/crypto/ssh`. Pin in NEX-387 plan; gliderlabs is the higher-level wrapper, faster to a working frontend.
- **Casket pubkey lookup:** does cairn cache `fingerprint → agent_id` mappings, or query herald on every SSH connection? Pin in NEX-387 plan; probably cache with short TTL + invalidate on agent-block events.
- **Ledger contract for PR-as-ticket:** how does cairn issue tickets + watch transitions? REST + webhook? REST + poll? Pin in NEX-390 plan after NEX-379 lands.
- **Repo storage layout:** single bare-git directory per repo (MVP file://) vs per-repo XTS disk (NEX-352) from the start? MVP file:// is simpler; the storage adapter interface keeps the upgrade clean.
- **Cairn's own ledger ticket type:** does ledger need a new `kind=pr` value or does cairn use an existing `kind=task` with a `pr` label? Decide with the ledger work.
- **HTMX vs vanilla form posts:** the UI is small enough that vanilla forms might be fine. HTMX is nice-to-have for partial updates. Decide in NEX-389 plan.
- **Where the binary lives:** existing `cairn` repo (Forgejo content stays alongside until removed, or rebuild repo from scratch). Pin operationally — recommend a fresh subdirectory `cmd/cairn-server/` in the existing repo, with the old Forgejo tree eventually archived or moved to a `legacy-forgejo/` branch.
