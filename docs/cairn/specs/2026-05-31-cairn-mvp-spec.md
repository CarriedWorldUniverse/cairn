# cairn — CWB MVP spec (agent-git core)

**Status:** draft for approval · 2026-05-31
**Goal:** the agent-git walking skeleton — a go-git-backed git host where aspects clone/push over SSH (casket identity) or HTTP (gateway-fronted), herald-authed, deployed as a CWB product — so the nexus team versions code on cairn instead of GitHub.
**Why now:** cairn is the **git** leg of the CWB MVP agent loop (auth+git+issues+knowledge — see `cwb-conformance/docs/2026-05-31-cwb-mvp-definition.md`). This spec is the **MVP cut** of the broader cairn-native design (`2026-05-31-cairn-native-spec.md` / NEX-384): the minimum that lets aspects version code autonomously. PRs, the delayed-public-projection, and the web UI are sequenced *after* (§7) — they're core cairn but not the first cut.

Private repos, single org, team dogfooding. No human in the per-action loop: aspects push on their own casket identities.

---

## 1. The one-paragraph architecture

A single Go binary (`cmd/cairn-server`) wraps `go-git` (the protocol/storage engine) with two ingresses that both terminate at a **herald identity**, plus a repo/ref core. The **SSH ingress** (`gliderlabs/ssh`) authenticates an agent by its **casket key** — cairn resolves the key fingerprint to a herald agent (via herald's by-fingerprint lookup, NEX-412), checks active + scope, and dispatches `git-upload-pack`/`receive-pack`. The **HTTP ingress** (Smart-HTTPv2) sits **behind interchange-gateway** and reads the mTLS-trusted `X-CWB-*` identity the gateway injects (same model as ledger). Both map to a herald agent id as the actor; scopes (`repo:read`/`repo:write`) gate clone/push. It's HTTP-native (not the nexus WS-bus) for the HTTP path; SSH is a parallel, inherently-encrypted authenticated ingress. TLS everywhere — SSH self-encrypts, the HTTP hop is mTLS (`project_cwb_tls_everywhere`).

```
  aspect ──casket key over SSH──► cairn SSH ─(fingerprint→herald agent, NEX-412)─┐
                                                                                  ├─► go-git core ─► repos + SQLite meta
  human/tool ──HTTPS via gateway──► cairn HTTP ─(mTLS X-CWB-* from gateway)───────┘
```

---

## 2. Scope — what's IN

1. **Repo + ref core (go-git).** Create/get/list/delete repos; list/get/update/delete refs; bare repos on disk (`go-git` storage; NEX-348 XTS-backed volumes slot in later without API churn). SQLite for repo/ref metadata. Both ingresses call this one core.
2. **SSH ingress (casket identity).** `gliderlabs/ssh`; PublicKey auth → fingerprint `base64url(sha256(pubkey)[:16])` → herald agent (NEX-412 lookup); active + scope check; `git-upload-pack` (clone/fetch) + `git-receive-pack` (push). Cairn's own Ed25519 host key (persisted in a Secret). **Parallel ingress, not gateway-fronted** (git-over-SSH can't traverse an HTTP gateway); SSH's own encryption + casket auth keep it safe.
3. **HTTP ingress (gateway-fronted).** Smart-HTTPv2 (`go-git` http server transport) reached via interchange-gateway at `/cairn/...`; trusts the **mTLS-injected `X-CWB-*`** identity (gateway ran herald verification); clone/push. For humans, tooling, and the conformance suite.
4. **herald identity + scopes.** Actor = herald agent id (SSH: from the fingerprint lookup; HTTP: from `X-CWB-Subject`). `repo:read` → clone/fetch; `repo:write` → push. `org` scopes the owning org (single-org MVP).
5. **Minimal branch protection.** The default branch requires `repo:write` to push and **disallows force-push by default** — cheap safety so the core isn't a free-for-all. Richer rules + org-tree-axis mapping (NEX-391) are deferred.
6. **Deploy as a CWB product.** Containerfile (static go build on scratch + SSH host key) + k3s manifests in `cwb` ns: HTTP ClusterIP behind the gateway (mTLS), SSH via LoadBalancer/NodePort (port 22 needs external reach), PVC for repo storage, gateway route `/cairn`. Mirrors herald/ledger.
7. **TLS everywhere.** SSH self-encrypts; the HTTP hop is mTLS gateway↔cairn (`project_cwb_tls_everywhere`). No plaintext.

---

## 3. Auth + identity model

| Path | How identity is established | Trust basis |
|---|---|---|
| **SSH** | agent's casket key → fingerprint → herald agent (NEX-412 lookup) | proof-of-possession of the casket private key (SSH PublicKey auth) + herald confirming the agent is active |
| **HTTP** | gateway-injected `X-CWB-{Subject,Org,Kind,Scopes}` | the **mTLS** gateway↔cairn hop (gateway is cryptographically the caller); cairn HTTP reachable only over that hop |

- **Actor:** herald agent id, recorded on pushes (commit-author verification + push events) — per-agent attribution, not a flat string.
- **Scopes:** `repo:read` (clone/fetch), `repo:write` (push). Drawn from herald's cross-service scope vocabulary; pin exact strings in the plan.
- Cairn caches `fingerprint → agent` with a short TTL (invalidate on agent-block) to avoid a herald round-trip per SSH connection.

---

## 4. Dependencies

- **herald** (live) — identity authority. **NEX-412 (`GET /api/agents/by-fingerprint/{fp}`) is a hard prerequisite for the SSH path** — without it cairn can't map a casket key to a herald agent. Small herald story; on cairn's critical path.
- **interchange-gateway** (live) — fronts the HTTP path; injects `X-CWB-*`.
- **mTLS mesh** (platform) — secures gateway↔cairn.
- **No ledger dependency** for the MVP (PR-as-ledger-issue is post-ledger, §7) — so cairn-core is buildable now against the live herald + gateway.

---

## 5. Data model

```
repo
  id, org_id, slug, default_branch, protection (JSON, minimal), storage_uri, created_at, updated_at
push_event           -- audit: who pushed what
  id, repo_id, ref, old_sha, new_sha, pusher_agent_id, at, force(bool)
```

Repos own-linked to a herald `org_id`. `protection` carries the minimal default-branch rule (§2.5). No PR/projection tables in the MVP (those land with their phases).

---

## 6. Build sequence (for the implementation plan)

1. **Spec sign-off** (this doc).
2. **Repo + ref core** (go-git) + SQLite meta; `cmd/cairn-server` skeleton.
3. **SSH ingress** — casket-key auth via NEX-412 lookup; upload/receive-pack dispatch.
4. **HTTP ingress** — Smart-HTTP via gateway; `X-CWB-*` trust.
5. **Minimal branch protection** at receive-pack (default-branch scope + no force-push).
6. **k3s deploy** — Containerfile + manifests; gateway route; SSH LB; mTLS hop.
7. **cwb-conformance cairn layer** — clone/push over SSH (casket) + HTTPS (gateway), scope matrix, through the gateway/SSH ingress.

**DoD:** an aspect, on its casket identity, clones a cairn repo over SSH and pushes a branch (herald-authed via fingerprint lookup); a human/tool clones over HTTPS through the gateway (`X-CWB-*`); both herald-scoped (`repo:read`/`write`); deployed on k3s; the cwb-conformance cairn layer exercises it green. No PR/projection/UI.

---

## 7. Sequenced AFTER this MVP (core cairn, not the first cut)

- **Delayed-public-projection** — phase-2 *within* cairn; the security flagship (live commit → embargoed public projection → Disclose; closes the AI-era patch-diffing window — `project_cairn_delayed_projection`, NEX-25 design re-based on go-git). Core cairn identity; needed before any public/customer repo; not needed for internal dogfooding, so it follows the skeleton.
- **PR-as-ledger-issue** (NEX-390) — the cairn↔ledger integration milestone, after ledger MVP. A PR = `refs/cairn/pr/N` + a ledger ticket; reviews/comments live in ledger.
- **Web UI** (NEX-389) — the human browse/diff surface (post-MVP human layer, on the cut line with ledger UI / commonplace UI / path-A).
- **Richer branch protection** (NEX-391, org-tree axes), **trust tiers** + custom hostnames (commercial), **public-read** (= the projection, not a flag).

Explicitly never-in-cairn (other pillars own these): wikis (commonplace), CI/actions, packages + LFS (porter), project boards (ledger).

---

## 8. Open questions for the plan (small, non-blocking)

- **SSH lib:** `gliderlabs/ssh` vs raw `golang.org/x/crypto/ssh` — gliderlabs is the faster wrapper; pin in the plan.
- **fingerprint→agent caching** TTL + block-invalidation mechanism (NEX-412 consumer side).
- **Module layout:** `cmd/cairn-server` + `internal/cairn` in the existing cairn repo (the cairn-native plan assumed a sibling `cairn-native/go.mod` during the Forgejo transition — confirm the final home).
- **HTTP credential-helper** shape for human `git clone https://` with a herald token — align with the documented helper pattern.
- **Exact `repo:*` scope strings** — pin cross-pillar in the plan.
