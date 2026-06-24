# cairn — cairn→cairn full fidelity (`refs/cairn/meta`)

**Status:** draft for approval · 2026-06-24
**Goal:** "cairn to cairn is cairn." Today a cairn→git→clone round-trip keeps only the git projection — branches flatten to children-of-root, real line parent/base is lost, and **no change rows are reconstructed** (change-ids, sealed state, conflicts gone). Preserve the cairn change-graph + conflicts across a cairn↔cairn push/clone via a metadata ref, so a clone from a cairn remote reconstructs the *exact* cairn catalogue, not a lossy projection. (Implements the operator's "refs/cairn/* + paired push+import" mechanism.)

**Decisions (pinned):** a single `refs/cairn/meta` ref holding a serialized catalogue snapshot; preserve **lines (real tree) + changes (sealed/has_conflict) + conflicts**; the **op-log stays local** (per-clone operational history, like git's reflog — never pushed); **fidelity on CLONE** (fresh import) in v1, pull-meta-merge deferred; **origin IDs are reused** (change-ids and line-ids are stable identity, so the clone IS the same repo); fidelity follows the **data** (a remote that has `refs/cairn/meta` imports with fidelity) rather than only the `remote_kind` flag — `remote_kind=cairn` just decides whether *push* writes the meta.

---

## 1. The metadata snapshot

A versioned JSON document capturing the cairn-specific catalogue (everything git can't carry):
```json
{
  "version": 1,
  "lines":   [{ "id","name","parent_line","tip_commit","base_commit","status" }],
  "changes": [{ "id","line_id","author","head_commit","status","sealed","has_conflict" }],
  "conflicts":[{ "change_id","path","base_blob","ours_blob","theirs_blob","marked_blob","status" }]
}
```
- All shas/blobs referenced are git objects already pushed via the normal refs (the meta references them by hash; it carries no new content beyond the catalogue rows). The conflict blob hashes are the existing conflict-object blobs (already in the object store and reachable, so they survive the push).
- `op-log` and `bisect`/`stash` session state are **excluded** (local-only).
- Deterministic ordering (by id) so the meta tree hash is stable for unchanged state.

## 2. Export (`internal/change/meta.go` — `ExportMeta`)
`func (e *Engine) ExportMeta() (commitSha string, err error)`: serialize the snapshot (§1) to one JSON blob → a tree `{ "meta.json": <blob> }` → a commit (message `cairn-meta v1`, the configured identity, no parent or parented on the prior meta for history) → return its sha. The push layer points `refs/cairn/meta` at it.
- Reuse `writeBlob`/`writeTreeRefs`/`writeCommit`. Read the catalogue rows (lines/changes/conflicts) directly from `e.db`.

## 3. Push (`push.go` — cairn remotes also push the meta)
In `PushToRemote`, when `remoteKind(remoteName) == "cairn"`:
1. `metaCommit, _ := ExportMeta()`; create/update the local ref `refs/cairn/meta` → metaCommit.
2. Add `refs/cairn/meta:refs/cairn/meta` to the push refspecs (alongside the existing `refs/heads/*` + `refs/tags/*`).
A `git` remote pushes exactly as today (no meta ref) — the conversion path is unchanged. (The meta ref's commit/tree/blob are pushed as ordinary git objects; a plain git server stores them harmlessly under `refs/cairn/*`.)

## 4. Import with fidelity (`importer.go` — `ImportFromRemote`)
After `fetchRemote` (which must now ALSO fetch `refs/cairn/*` — extend the refspec to `+refs/cairn/*:refs/cairn/*`):
- If `refs/cairn/meta` exists on the remote/after fetch → **full-fidelity reconstruct** (replace the flat projection): read `meta.json` from the meta commit's tree; in one tx, **clear** the init-created lines/changes and **insert the meta's lines/changes/conflicts verbatim** (origin IDs reused), then set the expressed default branch from the root line. The git objects are already fetched, so all referenced commits/trees/blobs resolve.
- Else (no meta — a plain git remote) → the current flat projection (unchanged).
- Reconstruction details: insert each `line` (preserving `parent_line`/`base_commit` — the REAL tree), each `change` (preserving `sealed`/`has_conflict`/`head_commit`/`status`), each `conflict` row. The default branch = the root line's name (parent_line IS NULL). The clone's `.cairn/wc.json` then expresses that root line (existing clone flow).
- **Validation**: the meta version must be `1` (else "unsupported cairn metadata version N; upgrade cairn"). Referenced commit shas must resolve (they were fetched) — a missing one is a clear error, not a silent drop.

## 5. Worktree/CLI
- No new CLI verb. `cairn clone <cairn-url>` automatically reconstructs full fidelity when the remote has `refs/cairn/meta`; `cairn push` to a `--cairn` remote automatically writes the meta. (The `--cairn` flag on `remote add` already records the kind; document that it now enables fidelity.)
- A clone from a cairn remote yields the exact line tree, change-ids, sealed history, and open conflicts — verifiable with `cairn tree`/`log`/`status`.

## 6. Out of scope (later)
- **Pull-meta-merge** (reconciling a remote meta snapshot into an existing local catalogue with its own changes) — v1 does fidelity on *clone* only; `cairn pull` from a cairn remote stays the git-level reconcile.
- op-log / stash / bisect round-trip (local by design).
- Incremental/delta meta (v1 ships a full snapshot each push); signed/encrypted meta; the privacy-redacted projection (Phase 3).
- Conflict-blob GC / orphan handling.

## 7. Testing / DoD
- **round-trip fidelity (the core)**: build a repo with a real line TREE (a branch off a branch — `a` off root, `b` off `a`), distinct change-ids, a sealed history, and an OPEN conflict on a line; `remote add --cairn` a bare repo + `push`; `clone` it into a fresh dir; assert the clone's `lines` reproduce the exact parent/base tree (b's parent is a, a's parent is root), the `changes` reproduce ids + sealed + has_conflict, and the conflict row is present (so `cairn status` on that line shows the conflict). Contrast: cloning the SAME repo as a plain `git` remote (no meta) → the flat projection (current behavior, unchanged).
- **export determinism**: `ExportMeta` twice on unchanged state → identical meta commit/tree hash.
- **git remote unaffected**: pushing to a non-cairn remote writes no `refs/cairn/*`; a normal `git clone` of a cairn-pushed repo still sees sane branches/tags (the meta ref is inert).
- **version guard**: a meta with `version: 2` → clone errors clearly.
- **op-log not exported**: the meta contains no operation rows; a clone has a fresh (empty/clone) op-log.
- Full gate + cross-compile; `skipOnWindows` on local-fixture push/clone e2e; all prior phases unaffected; atomic reconstruct (a failed import leaves the clone uninitialized, not half-built).
