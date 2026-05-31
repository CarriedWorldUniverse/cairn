# cairn → ledger PR-as-issue integration — spec

**Status:** approved design · 2026-05-31
**Goal:** opening a pull request in cairn creates a linked tracking issue in ledger, attributed to the agent who opened it — making the cross-service "PR-as-ledger-issue" step a real platform behaviour instead of something a client stitches together by hand.
**Why now:** the conformance journey proved the four pillars *compose* but had to perform the cairn→ledger hand-off client-side (logged as simulated). This is the first of those simulated steps to become real: the north-star **"a PR = a ref + a ledger ticket; reviews live in ledger"** ([[project_cairn_repo_strategy]]).

---

## 1. The one-paragraph architecture

cairn gains its first **outbound, cross-service** behaviour. An aspect pushes a branch as today, then opens a PR with a new cairn endpoint. cairn validates the PR against the repo, then — **on behalf of the opener** — calls ledger to create a tracking issue, forwarding the gateway-injected `X-CWB-*` identity it received (the same in-cluster, mesh-trusted header propagation cairn already uses to reach herald). It records a first-class **pull-request** row binding the source/target refs to the returned ledger issue key, and returns the PR. The PR is idempotent per `(repo, source, target)` while open. This build covers **opening** a PR (and reading one back); merge, close, listing, and reflecting ledger transitions back onto the PR are named future work.

```
  aspect ──push feature──► cairn (git)
  aspect ──POST .../pulls (X-CWB: builder, repo:write+issue:write)──► cairn
                                          │ validate refs, dedupe open PR
                                          │ create issue ON BEHALF OF opener:
                                          ▼
                         ledger.cwb.svc:8081/api/issues   (forwards opener's X-CWB-*)
                                          │  201 → issue key
                                          ▼
                         cairn.pull_requests row {refs ↔ issue key}
                                          │
                                          ▼
                         201 { id, source, target, title, state:"open", ledger_issue_key, url }
```

---

## 2. Scope

**IN**
1. `POST /api/orgs/{org}/repos/{slug}/pulls` — open a PR; creates the linked ledger issue. Gated `repo:write`, org must match `X-CWB-Org`.
2. `GET /api/orgs/{org}/repos/{slug}/pulls/{id}` — fetch one PR (verification + idempotency consumers).
3. A `pull_requests` table + the ref↔issue binding.
4. A cairn→ledger client (`internal/ledger`) that forwards the caller's identity.

**OUT (named future work)**
- Merge / merge-on-ticket-transition (the reverse integration).
- Close / reopen a PR; listing PRs.
- Updating the issue on subsequent pushes to the source branch.
- Reflecting ledger status changes back onto the PR state.
- A repo→default-project mapping (for MVP the opener passes `project`).

---

## 3. API

### `POST /api/orgs/{org}/repos/{slug}/pulls`
Auth: trusted `X-CWB-*` (gateway-injected). Requires `org == X-CWB-Org` and scope `repo:write`.

Request body:
```json
{
  "source": "feature",          // required: existing branch to merge FROM
  "target": "main",             // required: existing branch to merge INTO
  "title": "Add X",             // required
  "description": "…",           // optional → ledger issue description
  "project": "ACME",            // required: ledger project key the issue lands in
  "definition_of_done": "- [ ] …" // optional → ledger issue DoD
}
```

Responses:
- `201` (created) or `200` (existing open PR for the same `source→target` returned idempotently):
  ```json
  { "id": "<pr-id>", "repo": "<slug>", "source": "feature", "target": "main",
    "title": "Add X", "state": "open", "ledger_issue_key": "ACME-7" }
  ```
  (`url` is included only when cairn is configured with a public base — see §6.)
- `400` — missing/invalid field, `source == target`, or a body field ledger rejects.
- `403` — org mismatch, or missing `repo:write` (cairn) / `issue:write` (ledger, on the forwarded identity).
- `404` — repo, `source`, or `target` branch not found.
- `502` — ledger unreachable (transport error).

### `GET /api/orgs/{org}/repos/{slug}/pulls/{id}`
Auth: `repo:read`, org match. Returns the PR record (same shape as above) or `404`.

---

## 4. Data model

New table (cairn's SQLite catalogue, alongside `repo` / `push_event`):
```
pull_request
  id              text PK            -- 16-byte hex, like repo ids
  repo_id         text               -- → repo.id
  source_ref      text               -- branch name (not full ref)
  target_ref      text
  title           text
  ledger_issue_key text              -- e.g. "ACME-7"
  state           text               -- 'open' | 'merged' | 'closed' (only 'open' set in this build)
  opened_by       text               -- X-CWB-Subject of the opener
  created_at      text               -- RFC3339

-- one OPEN pr per (repo, source, target):
CREATE UNIQUE INDEX pr_open_uniq ON pull_request(repo_id, source_ref, target_ref)
  WHERE state = 'open';
```
`state` carries `merged`/`closed` for forward-compatibility; this build only ever writes `open`.

---

## 5. Identity & scope (on-behalf-of)

cairn does **not** get its own service identity. It relays the gateway-injected `X-CWB-*` headers (`Subject`, `Org`, `Scopes`, `Responsible-Human`) verbatim to ledger — the same identity-propagation pattern the mesh already relies on (the gateway stripped any client-forged headers at the boundary; intra-cluster mTLS makes the relay trustworthy — `project_cwb_tls_everywhere`). Consequences:
- The ledger issue's reporter + tenancy are the **opener** — automatic, correct attribution.
- Opening a PR requires the opener to hold **both** `repo:write` (cairn gate) **and** `issue:write` (ledger gate, on the forwarded scopes). Each service enforces its own scope on the one propagated identity; cairn never escalates.
- No new herald identity, no ledger change.

---

## 6. The cairn → ledger client (`internal/ledger`)

Mirrors `internal/herald` (cairn's existing outbound client):
- `NewLedgerClient(baseURL string, hc *http.Client)`; `LEDGER_BASE_URL` env, default `http://ledger.cwb.svc:8081`.
- `CreateIssue(ctx, fwd Identity, in IssueInput) (IssueResult, error)` where `fwd` carries the opener's `X-CWB-*` to set as request headers, and `IssueInput` is `{project, type, summary, description, definitionOfDone, externalRefs}`.
- POSTs `…/api/issues`; on `201` decodes `{ key }`; non-2xx → a typed error carrying ledger's status + body (so the handler can mirror the status); transport error → distinct error (→ `502`).
- The handler builds the ExternalRef: `{ tracker:"cairn", key:"<org>/<slug>@<source>", description:"<source>→<target> @ <headSHA(12)>" }`. cairn does NOT know its own public gateway URL, so `url` is set only when an optional `CAIRN_PUBLIC_BASE` env is configured (`<CAIRN_PUBLIC_BASE>/<org>/<slug>`), otherwise omitted — the stable identifier is the `key`, which needs no URL.

cairn-server wires the client (like the herald client) and threads it into the HTTP ingress (`httpd.Config` gains a `Pulls` collaborator or the ledger client + repo core).

---

## 7. Open-handler flow

1. Parse + validate body: `source`, `target`, `title`, `project` non-empty; `source != target`.
2. Identity: `org == X-CWB-Org` else `403`; `repo:write` else `403`.
3. `repo, err := Core.GetRepo(org, slug)` → `404` if absent.
4. `Core.GetRef(repo.ID, "refs/heads/"+source)` and `…/target` both resolve → `404` if either missing. Capture the source head SHA for the ExternalRef.
5. Idempotency: if an `open` PR exists for `(repo.ID, source, target)` → return it `200` (no new issue).
6. `ledger.CreateIssue(fwd=opener X-CWB, {project, type:"Story", summary:title, description, definitionOfDone, externalRefs:[ref]})`.
   - non-2xx → mirror ledger's status + body (e.g. `403`/`400`); **persist nothing**.
   - transport error → `502`; persist nothing.
7. Insert the `pull_request` row with the returned issue key; return `201`.

A crash between step 6 (issue created) and step 7 (row inserted) can orphan a ledger issue. Accepted for MVP and noted; a later reconciliation (or moving the issue-create to reference a pre-allocated PR id) closes it.

---

## 8. Error handling summary

| Condition | Status | Side effect |
|---|---|---|
| missing/empty field, `source==target` | 400 | none |
| org mismatch / no `repo:write` | 403 | none |
| repo / source / target not found | 404 | none |
| ledger rejects (no `issue:write`, bad project, …) | mirror ledger status (403/400/…) | none |
| ledger transport failure | 502 | none |
| success | 201 | issue + PR row |
| duplicate open PR | 200 | none (returns existing) |

---

## 9. Testing

**cairn unit (with a fake ledger client):**
- open → validates, calls ledger with the forwarded `X-CWB-*`, inserts a PR row with the returned key, returns 201.
- idempotent: second open for the same `source→target` returns the existing PR, calls ledger zero extra times.
- validation: `source==target`, missing branch, missing field → 4xx, no ledger call, no row.
- ledger error: fake returns 403/400 → cairn mirrors it, **no PR row**; transport error → 502.
- `GET pulls/{id}` returns the row; unknown id → 404.

**Conformance payoff (separate change, in `cwb-conformance`):** the journey's step 3 flips from client-simulated to real — the journey opens the PR via `POST .../pulls`, then asserts the ledger issue **auto-appeared**, is linked (ExternalRef ↔ PR), and is reported by the opener. One simulated step becomes an assertion.

**DoD:** an aspect with `repo:write`+`issue:write`, having pushed a feature branch, opens a PR through the gateway and a linked ledger issue exists (reporter = the opener, ExternalRef → the branch); re-opening is idempotent; missing scope/branch/project fail cleanly with no orphaned state; cairn unit tests green; deployed to dMon and the conformance journey asserts it live.

---

## 10. Build sequence (for the implementation plan)

1. `pull_request` schema + repo-core methods (`CreatePull`, `GetPull`, `FindOpenPull`).
2. `internal/ledger` client (`CreateIssue`, forwards identity) + a fake for tests.
3. `httpd` wiring: the `pulls` routes + open/get handlers + scope/org gates.
4. cairn-server: `LEDGER_BASE_URL` (+ optional `CAIRN_PUBLIC_BASE`) + construct + inject the ledger client.
5. Containerfile/manifests: no new infra (ledger already deployed); set `LEDGER_BASE_URL`.
6. Deploy to dMon; flip the conformance journey step 3 to assert it.
