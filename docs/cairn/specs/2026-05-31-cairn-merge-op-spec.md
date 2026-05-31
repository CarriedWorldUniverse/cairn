# cairn server-side merge op — spec

**Status:** approved design · 2026-05-31
**Goal:** merge a pull request server-side in cairn — fast-forward the target branch to the source tip, mark the PR `merged`, and tell the linked ledger issue — so a PR can be merged through cairn instead of a client running git locally.
**Why now:** the conformance journey's merge step is still performed client-side (logged as simulated). This makes it a real cairn operation, the natural follow-on to the PR-as-issue integration ([[project_cairn_repo_strategy]]). It is the cairn-initiated direction; the ledger-Done-auto-triggers-merge direction needs the unbuilt webhook/event capability and is out of scope.

---

## 1. The one-paragraph architecture

cairn gains a merge endpoint on the pull request it already models. On merge it performs a **fast-forward only** advance of the target branch: if the target is an ancestor of the source (no divergence), cairn moves `refs/heads/{target}` to the source tip — a pure go-git ref update, no working tree. It then marks the `pull_request` row `merged` and, **best-effort**, comments the linked ledger issue on behalf of the merger (the same `X-CWB-*` identity-propagation the PR-open path uses). Fast-forward is the only strategy: it is exactly what branch protection permits, so a direct ref-update merge is safe by construction even though it bypasses the push-time pre-receive hook. Diverged branches are rejected (rebase first); merge-commit/squash strategies, conflict resolution, and merge-on-ledger-transition are named future work.

```
  agent ──POST .../pulls/{id}/merge (X-CWB: repo:write)──► cairn
                         │ pull must be open
                         │ target ancestor-of source?
                         │   yes → set refs/heads/{target} = source tip   (the merge)
                         │   no  → 409 (rebase first)
                         │ pull_request.state = merged
                         ▼ best-effort: ledger comment "merged … @ <sha>" (on behalf of merger)
                         200 { id, state:"merged", target, merged_sha, ledger_comment_error? }
```

---

## 2. Scope

**IN**
1. `POST /api/orgs/{org}/repos/{slug}/pulls/{id}/merge` — fast-forward merge, gated `repo:write`, org-match.
2. Fast-forward-only advance of the target ref (go-git, in-process ancestor check).
3. `pull_request.state` → `merged`.
4. Best-effort comment to the linked ledger issue (new ledger-client `CommentIssue`).

**OUT (named future work)**
- Merge-commit / squash strategies; conflict resolution.
- Gating merge on review state / required reviews.
- Transitioning the linked ledger issue on merge (the reviewer owns transitions).
- Merge-on-ledger-transition (ledger → cairn; needs the webhook/event capability).
- Closing a PR without merging; reopening.

---

## 3. API

### `POST /api/orgs/{org}/repos/{slug}/pulls/{id}/merge`
Auth: trusted `X-CWB-*`; requires `org == X-CWB-Org` and scope `repo:write`. No request body.

Responses:
- `200`:
  ```json
  { "id": "<pr-id>", "state": "merged", "target": "main",
    "merged_sha": "<40-hex>", "ledger_comment_error": "" }
  ```
  `ledger_comment_error` is non-empty only when the merge succeeded but the best-effort ledger comment failed.
- `403` — org mismatch or missing `repo:write`.
- `404` — repo or pull not found.
- `409` — pull not open; source/target branch missing; or **not fast-forwardable** (diverged).
- `500` — ref-write failure.

---

## 4. Behaviour (the merge handler)

1. Identity: `org == X-CWB-Org` else `403`; `repo:write` else `403`.
2. `GetRepo(org, slug)` → `404`; `GetPull(repo.ID, id)` → `404`.
3. The pull must be `open`; otherwise `409` ("pull is not open").
4. `FastForward(repo.ID, pull.Source, pull.Target)` (see §5):
   - **already up to date** (source tip already contained in target) → treat as merged, no ref change, `merged_sha` = target tip.
   - **fast-forward** (target ancestor of source) → set `refs/heads/{target}` = source tip; `merged_sha` = source tip.
   - **diverged** (`ErrNotFastForward`) → `409`.
   - **branch missing** → `409`.
5. `SetPullState(repo.ID, id, "merged")`.
6. Best-effort `ledger.CommentIssue(forwardCWB(r), pull.LedgerIssueKey, "merged <source> into <target> @ <sha12>")`. On error: log it and set `ledger_comment_error` in the response — **the merge result is unchanged** (the ref update is the source of truth; a flaky comment must not fail a completed merge).
7. `200`.

---

## 5. New pieces

**`repo.Service` (internal/repo):**
- `var ErrNotFastForward = errors.New("repo: not a fast-forward")` and `var ErrAlreadyUpToDate = errors.New("repo: already up to date")`.
- `FastForward(ctx, repoID, source, target string) (mergedSHA string, err error)`:
  - Open the bare repo (go-git). Resolve `refs/heads/{source}` and `refs/heads/{target}`; a missing ref → a wrapped `ErrNotFound`-style error the handler maps to `409`.
  - Load both commits. If the source commit is an ancestor of (or equal to) the target commit → return target tip, `ErrAlreadyUpToDate`.
  - Else if the target commit is an ancestor of the source commit → `SetReference(refs/heads/{target} = source tip)`, return source tip, `nil`.
  - Else → `ErrNotFastForward`.
  - Ancestor test uses go-git's `(*object.Commit).IsAncestor`.
- `SetPullState(ctx, repoID, id, state string) error` — `UPDATE pull_request SET state=? WHERE repo_id=? AND id=?`.

**`ledger.Client` (internal/ledger):**
- `CommentIssue(ctx, fwd http.Header, key, body string) error` — `POST {base}/api/issues/{key}/comments` `{ "body": body }`, forwards the `X-CWB-*` headers (reuses the existing `cwbHeaders` set). Non-2xx → `*APIError`; transport error → wrapped error. (Caller treats both as best-effort.)

**`httpd` (internal/httpd):**
- `IssueCreator` interface grows a `CommentIssue` method (so the handler can call it on the same injected dependency); `*ledger.Client` and the test fake both implement it. Rename is unnecessary — extend the existing `Config.Ledger` interface.
- `handleMergePull` + the route `POST /api/orgs/{org}/repos/{slug}/pulls/{id}/merge`, registered before the `/` git catch-all.

---

## 6. Protection safety

The default-branch protection (`internal/protect`) is enforced by the per-repo **pre-receive hook**, which only runs on `git-receive-pack` (push). A merge via a direct go-git ref update does NOT trigger it. This is safe because the merge is **fast-forward only**: advancing the target to a descendant is precisely the update `protect.Allow` permits (it blocks only non-fast-forward and delete on the default branch). A merge-commit strategy (future) would diverge from this and MUST replicate `protect.Allow` before writing the default branch.

---

## 7. Testing

**repo-core unit:** seed a repo with `main` and a `feature` that descends from `main` → `FastForward` advances `main` to feature tip, returns the sha. Seed a diverged pair → `ErrNotFastForward`, target unchanged. Seed feature == ancestor-of/equal main → `ErrAlreadyUpToDate`. `SetPullState` flips an open PR to merged.

**ledger-client unit:** `CommentIssue` POSTs to `/api/issues/{key}/comments` with `{body}` and the forwarded `X-CWB-*`; non-2xx → `*APIError`.

**httpd unit (fake ledger + seeded refs):**
- ff merge → `200`, target ref now == source tip, `pull_request.state == merged`, fake `CommentIssue` called once with the forwarded subject.
- not-open pull → `409`; diverged → `409`; no `repo:write` → `403`; repo/pull missing → `404`.
- fake `CommentIssue` returns an error → still `200`, `ledger_comment_error` populated, state still merged.

**Conformance payoff (separate `cwb-conformance` change):** the journey's step that fast-forward-merges client-side flips to calling `POST .../pulls/{id}/merge`; it then asserts the default branch advanced to the feature SHA, the PR reads `merged`, and the linked issue received the "merged" comment.

**DoD:** a PR opened on a fast-forwardable feature branch is merged via the endpoint (target advances to the source tip, PR → merged, the linked ledger issue gets a merged comment); a diverged PR is rejected `409`; merging a non-open PR is `409`; a flaky ledger comment does not fail the merge; unit tests green; deployed to dMon and the conformance journey asserts it live.

---

## 8. Build sequence (for the implementation plan)

1. repo-core: `FastForward` + `SetPullState` + sentinels + tests.
2. ledger-client: `CommentIssue` + tests.
3. httpd: extend the `IssueCreator` interface with `CommentIssue`; `handleMergePull` + route + tests (fake ledger).
4. (cairn-server needs no wiring change — the existing `ledger.Client` gains the method; `Config.Ledger` already injected.)
5. Deploy to dMon; flip the conformance journey merge step to call the endpoint + assert.
