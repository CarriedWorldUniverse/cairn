# cairn Phase 2 — Slice C (part 3): sync (`cairn pull`/`fetch`)

**Status:** draft for approval · 2026-06-23
**Goal:** `cairn pull` — bring a moved git remote *into* your local lines and **reconcile** without discarding either side, so branches never go stale: an advanced remote is adopted (fast-forward when possible, else a 3-way merge with conflicts-as-data), not left as a diverged branch to wrestle back. Plus `cairn fetch`. Completes the round-trip into a continuous loop (clone → work → pull → push).
**Builds on:** the engine's diff3 reconcile machinery (`mergeTrees`/`mergeBase`/`commitTree`/`writeCommit`), `fetchRemote`/import, push, and `internal/worktree`/`cmd/cairn` (all on main).

**Motivation:** stale/diverged branches are a divergence problem; cairn's model is *no silent divergence* — lines adopt their parent on every commit, and `pull` adopts a moved remote the same way (conflicts become discrete data, never a blocking merge-day).

---

## 1. Scope

**IN (C-sync part 1):**
1. **Tracking fetch** (`internal/change/sync.go`): fetch a remote into **`refs/remotes/<remote>/*`** (NOT `refs/heads/*` — pull must compare, not clobber local lines that hold work). A `fetchTracking(remoteName) error` helper + a `remoteHeads(remoteName) map[string]string` (short-name → commit from `refs/remotes/<remote>/*`).
2. **`PullFromRemote(remoteName string) (PullSummary, error)`**: fetch-tracking, then for each local **open** line whose name matches a remote branch, reconcile local vs remote:
   - resolve the line's **active change** (the open change on the line; if none, create one to carry the merge);
   - `L` = the change head (or line tip if no commits yet), `R` = the remote branch commit, `base = mergeBase(L, R)`;
   - `L == R` → up-to-date; `base == L` (local is ancestor) → **fast-forward** the change head + line tip to `R`; `base == R` (remote is ancestor) → nothing (local ahead); otherwise **diverged** → `mergeTrees(changeID, baseTree, R-tree, L-tree)` → write a merge commit (parents L + R) as the new change head + line tip; conflicts recorded on the change (never block);
   - return a per-line summary (up-to-date | fast-forwarded | merged | conflicted).
3. **`worktree.Repo.Pull(remote)`** — calls `PullFromRemote`, then **re-materializes every expressed folder** (so the merged result + any diff3 markers appear on disk), and saves.
4. **CLI**: `cairn fetch [remote]`, `cairn pull [remote]` (default `origin`) — print the per-line summary; conflicts reported, non-fatal.
5. Tests: ff pull (remote advanced, local clean → line fast-forwards); divergent non-overlap (clean 3-way merge, no conflict); divergent overlap (conflict object on the change, resolvable then push); up-to-date no-op; re-materialize reflects the pull; the **collaboration loop** (two clones of one remote: A commits+pushes, B pulls and sees A's work merged with B's).

**OUT (→ C-sync part 2, deferred):**
- **commit-time auto-sync** (a commit first fetches+reconciles origin) and **auto-pull-then-retry on a non-fast-forward `push`** — the convenience layer on top of explicit pull.
- cairn→cairn full fidelity, private-remote auth (still deferred).
- multi-remote tracking beyond the named remote; tag reconciliation conflicts (tags pulled as-is, last-writer).

---

## 2. Reconcile semantics (the heart)

A remote branch's advance is just another axis a line adopts — the **same conflicts-as-data 3-way merge** as merge-forward, with the incoming side = the remote tip instead of the parent line. Per local line that maps (by name) to a fetched remote branch:

```
L = change.HeadCommit (active change on the line) || line.TipCommit
R = remoteHeads[lineName]
if R == "" : skip (no remote counterpart)
base = mergeBase(L, R)
if L == R           : up-to-date
elif base == R      : local ahead → nothing (push later)
elif base == L      : fast-forward → change head & line tip := R
else (diverged):
    merged, conflicts = mergeTrees(changeID, tree(base), tree(R), tree(L))   // ours=remote, theirs=local
    head = writeCommit(merged, changeID, author, parents=[L, R])             // a real 2-parent merge commit
    change.head := head ; line.tip := head ; has_conflict := len(conflicts)>0
```

- The merge commit has **both parents** (L and R) — true git merge history, so a later push presents a normal merge to the remote.
- Conflicts attach to the line's active change (consistent with the existing conflict model); `cairn resolve` clears them; then `cairn push`.
- `base == ""` (unrelated histories — shouldn't happen for a tracked branch): treat as diverged with an empty base (add/add → conflict-as-data), or skip with a clear notice. Pin in the plan.
- All catalogue writes for a line's reconcile are atomic (one tx), mirroring Commit.

---

## 3. worktree + CLI

- `Repo.Pull(remote string) (change.PullSummary, error)`: `eng.PullFromRemote(remote)`; for each expressed branch, re-`Materialize` its line tip; `save()`; return the summary. `Repo.Fetch(remote)` → `eng` tracking-fetch.
- `cairn fetch [remote]` (default origin) → `Repo.Fetch`. `cairn pull [remote]` → `Repo.Pull`; print each line's result (`up-to-date` / `fast-forward → <sha>` / `merged` / `merged with N conflicts in <line>`). Conflicts non-fatal (exit 0) with a clear notice to resolve + push.

---

## 4. Testing

- **Engine (`sync_test.go`):** build a bare remote + clone it (import); advance the remote independently (helper: clone→commit→push-back); then `PullFromRemote`:
  - local clean → the line **fast-forwards** to the remote tip;
  - local commits a *different* file, remote a *different* file → `pull` produces a **clean 2-parent merge** (no conflict), line tip contains both;
  - local + remote edit the **same** region → `pull` records a **conflict** on the active change; `ResolveConflict` + (the line is then pushable);
  - up-to-date remote → no-op.
  - assert the merge commit has two parents (L and R).
- **worktree/e2e (collaboration loop):** clone the same remote into A and B; in A: edit+commit+push; in B: edit a different file+commit, then `cairn pull` → B's line now has A's change merged in (re-materialized on disk), then `cairn push` succeeds. The "no stale branches" proof.
- `skipOnWindows` on local-fixture tests; full + cross-compile gate green.

**DoD:** `cairn pull` reconciles a moved remote into local lines — fast-forward or conflicts-as-data 3-way merge, never a blocking diverge; the collaboration loop (clone→work→pull→push across two clones) works; expressed folders reflect the merged state; CI green cross-platform.

---

## 5. Build sequence (for the plan)

1. **Engine tracking-fetch + `PullFromRemote`** (`sync.go`): `fetchTracking`/`remoteHeads` + the per-line reconcile (ff / 3-way-merge-conflicts-as-data / up-to-date) + `PullSummary`. TDD: ff, clean-merge, conflict, up-to-date; two-parent merge commit.
2. **`Repo.Pull`/`Fetch` + `cairn pull`/`fetch` CLI + collaboration-loop e2e.** TDD.

---

## 6. Open questions (small, non-blocking)

- **active-change selection:** if a line has multiple open changes, pick the most-recent (or require one); pin in step 1. Common case is one active change per line.
- **line with no open change + diverged remote:** create a synthetic change to carry the merge, or ff-only and require an express+commit first. Lean: create a change so divergence always reconciles.
- **`base == ""`:** unrelated histories on a same-named branch — treat as diverged-with-empty-base (conflict-as-data) vs skip-with-notice. Pin in step 1.
- **tag reconciliation:** v1 pulls tags last-writer-wins (no tag-conflict handling).
- **fetch refspec:** `+refs/heads/*:refs/remotes/<remote>/*` for tracking; confirm go-git writes remote-tracking refs as expected.
