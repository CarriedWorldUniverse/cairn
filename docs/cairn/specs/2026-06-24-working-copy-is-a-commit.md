# cairn — working-copy-is-a-commit (+ snapshot perf cache)

**Status:** draft for approval · 2026-06-24
**Goal:** make the expressed folder's live state *always* a real commit, continuously auto-snapshotted, so work is never in an unsaved limbo and there is no staging step. Replace git's `add`→`commit`→"did I lose my changes?" model with: edit freely → your work is always captured → `cairn commit -m` seals one unit and starts the next.

**Why now:** it's the single biggest everyday-feel improvement over git, it removes git's two most confusing/scary concepts (the index and "uncommitted changes can be lost"), and it retires most of the Slice-A dirty-guard machinery. It requires a snapshot perf cache (auto-snapshotting on every command isn't viable as an O(repo) rescan), so that cache is built here.

**Operator decisions (locked):** keep the `cairn commit -m` verb (seal-and-advance, not jj `describe`/`new`); **show** the working change at the top of `cairn log` (jj-transparent), so `status`/`diff` become "working change vs its parent."

---

## 1. The model

Today: a line has an open change; you edit the folder; `cairn commit` *appends* a new commit to the change (parent = previous head). Edits before `commit` live only on disk (the "dirty" state Slice A had to guard).

New: each expressed line always has exactly one **open working change** whose head is the **working commit** — a real commit that represents the folder's current content.
- **Auto-snapshot (amend-in-place):** at the start of every `cairn` command, cairn scans each expressed folder (cache-accelerated) and, if the content changed, **amends** the working commit *in place* — rewrites its tree, keeping the same parent and the same stable **change-id**. The commit *hash* changes; the change-id does not (exactly jj). No new commit is appended, so history doesn't explode.
- **No daemon:** the snapshot fires on command invocation, not via a watcher — consistent with cairn's commit-triggered design. The perf cache (§4) makes an unchanged folder cost ≈ one `stat` per file.
- **Always saved:** because edits are captured into the working commit (a real object in the store) and every amend is an op-log entry, work is recoverable via `cairn undo` even after `abandon`/`unexpress`.

### `cairn commit -m "msg"` = seal + advance
1. Auto-snapshot the folder into the working commit (capture the latest edits).
2. Set the working commit's **description** to `msg` (it was the `(working)` placeholder).
3. **Merge-forward** (adopt the parent line) — same convergence trigger as today's commit; conflicts-as-data unchanged.
4. Start a **fresh working change** on top (parent = the just-sealed commit, empty tree-delta, `(working)` description). The folder now belongs to the new working change.

So the sealed commit becomes permanent history; a new `(working)` commit sits at the line tip. The verb and its effect ("advance history") are unchanged for the user; the difference is that nothing was ever at risk in between, and there was no staging step.

### `cairn log` (transparent)
`log` walks first-parent from the line tip, which *is* the working commit, so the working change shows at the top — labeled `(working)` (or `(no description)`), distinct from sealed commits below it. A commit is "working" iff it is the head of the line's open, un-sealed change.

### `cairn status` / `cairn diff` (redefined to jj semantics)
Because the folder always equals the working commit, "folder vs committed" is always empty — so these now show **the working change vs its parent** (the changes you're about to seal):
- `cairn status` — files changed in the working change (working-commit-tree vs its parent's tree) as `A`/`M`/`D`, plus lineage / ahead / conflicts (as today).
- `cairn diff` (no args) — the working change's diff against its parent. `cairn diff <a> <b>` (commit-vs-commit) is unchanged.
This reads from commits (no Scan-vs-tip), and is always current thanks to the pre-command snapshot.

---

## 2. Engine changes (`internal/change`)

- **`SnapshotWorking(changeID string, files map[string][]byte, modes map[string]EntryMode) (changed bool, newHead string, err error)`** — amend the working change's head **in place**: if `files/modes` differ from the current head's tree, write a new commit with the **same parent and same description** as the current head, set the change head + line tip to it (the old head becomes superseded — unreachable but recorded in the op-log for undo); return `changed=true`. If identical, no-op (`changed=false`). This is the auto-snapshot primitive; it does **not** merge-forward.
- **`Seal(changeID, message string) (newChangeID string, conflicts []Conflict, err error)`** — finalize the working change: set its head's description to `message` (amend message only), run `mergeForward` (adopt parent, record conflicts), then **create a fresh open change** on the line whose working commit's parent is the sealed commit (empty delta, `(working)` description). Returns the new working change-id. (This *is* the new `Repo.Commit`.)
- **Change description / "working" state:** a working change's description lives in its commit message; the `(working)` placeholder (e.g. literal `(working)`) marks an un-sealed change. Add an `is_open`/`sealed` flag to the `change` row (or derive: a change is "working" iff it is the line's current open change and its message is the placeholder). Pin: add a boolean `sealed` column to `change` (default 0; set 1 on `Seal`).
- **Express** creates the initial open working change (it already creates a change — adjust so its head is a `(working)` commit, or lazily create the working commit on first snapshot).
- `Log`/`commitInfo` (Slice B) gain a `Working bool` on `CommitInfo`, set when the commit is the head of an open (un-sealed) change, so `cairn log` can label it.
- Conflicts attach to the working change as today; `Seal` carries/clears them per the existing model.

## 3. Worktree changes (`internal/worktree`)

- **`SyncWorking() error`** — for each expressed branch, cached-scan its folder and `SnapshotWorking` the line's working change. Called at the **start of every command** (see §5). Cheap when nothing changed (§4).
- **`Repo.Commit(branch, message)`** becomes: `SyncWorking()` (or sync just this branch) → `eng.Seal(workingChangeID, message)` → re-materialize the folder to the new working tip. Same signature/return as today.
- **`Status`/`WorkingDiff`** redefined to read the working change vs its parent (`eng.DiffCommits(parent, workingHead)`), instead of `Scan` vs `Files(tip)`.
- **Dirty / destructive ops:** "dirty" now means *the open working change has content that differs from its parent and is un-sealed*. `abandon`/`unexpress`/`fold` keep their `--force` guards, but the refusal message changes to note recoverability: `branch %q has un-sealed work (recoverable with 'cairn undo'); commit it or pass --force to discard`. The Slice-A byte-comparison `isDirty` is replaced by a "working change has an un-sealed delta" check.

## 4. Snapshot perf cache (`internal/worktree`) — required for §1

Auto-snapshotting on every command must be O(changed files), not O(repo).
- A per-line cache `.cairn/wc-cache/<branch>.json` (or one keyed file) storing, per relative path: `{ mtimeNs int64, size int64, blobSHA string, mode EntryMode }`.
- **Cached scan:** walk the folder (honoring the Slice-C ignore + tracked-set + symlink/exec logic); for each file, `Lstat`; if `(mtimeNs, size)` match the cache entry, **reuse `blobSHA` + mode without reading or hashing the file**; otherwise read, hash, and update the entry. Drop cache entries for vanished paths. Produce the same `(files-or-blob-refs, modes)` the engine needs — i.e. the engine's `writeTree` should accept pre-hashed blob SHAs to avoid re-encoding unchanged blobs (add a fast path, or have the cache pre-write blobs to the object store once).
- **Racy-clean mitigation:** record the scan's start time; any file whose `mtimeNs >= scanStart` (could have changed within timer granularity) is treated as dirty and re-hashed regardless of the cache (git's "racy git" rule). Note this in the code.
- The cache also accelerates ordinary seals and `status`/`diff`. It is disposable: a missing/corrupt cache just means a full rescan (self-healing).
- **Scope:** v1 syncs *all* expressed folders at command start (cache-cheap when idle). A later optimization can scope the sync to the line(s) a command touches.

## 5. CLI changes (`cmd/cairn`)

- **Auto-snapshot hook:** in `run`/`openRepo`, after opening the repo and before dispatching a repo command, call `r.SyncWorking()` so every command sees live edits. (Skip for `init`/`clone`/`help`.) A snapshot failure is non-fatal to read commands but should surface clearly.
- `cairn commit -m` — unchanged surface; now seals + advances (the stderr note can say `sealed <change>; new working change started`).
- `cairn log` — label the working change `(working)`.
- `cairn status`/`diff` — now show the working-change-vs-parent delta (no user-visible flag change).
- Optional: `cairn describe -m "msg"` to set the working change's message *without* sealing (names your in-progress work so `log` shows it). Low-cost, nice-to-have; include if cheap, else defer.

## 6. What this retires / simplifies
- The Slice-A "uncommitted folder edits can be silently destroyed" hazard largely disappears (work is always in a commit + op-log). The dirty-guards relax to "un-sealed work, recoverable via undo."
- The mental model collapses to: **the folder is always a commit; `commit -m` just names it and starts the next.** No index, no "unsaved changes," no stash needed just to switch context (the working change holds your state).

## 7. Risks / open questions
- **Performance of "sync all expressed folders every command"** — mitigated by the cache; scope-to-touched-lines is the escape hatch if it bites.
- **`writeTree` re-encoding unchanged blobs** — the cache must feed pre-hashed/pre-stored blobs into tree-building to actually realize the speedup (else we still re-encode every blob each snapshot). The cache stores blobSHA; the tree-builder must accept known SHAs. Pin in the plan.
- **Op-log volume** — every amend is an operation; auto-snapshots could flood the op-log. Decide: coalesce consecutive working-amends into a single op (don't record a new op when the previous op on the same change was also an auto-amend), so `undo` steps over logical units, not keystrokes. Pin: coalesce auto-amend ops.
- **Conflicts during auto-snapshot** — auto-snapshot does NOT merge-forward, so it can't introduce conflicts; only `Seal` can. Good (keeps the cheap path conflict-free).
- **Multiple expressed folders sharing a line?** Not allowed (one folder per expressed branch) — unchanged.

## 8. Build sequence (for the plan)
1. **Perf cache + cached scan** (`internal/worktree`) — the `wc-cache` + a `CachedScan` that reuses blob SHAs; `writeTree` fast-path for known blob SHAs. TDD: unchanged file not re-hashed; changed file re-hashed; racy-clean re-hash; cache self-heals.
2. **`SnapshotWorking` (amend-in-place)** + `sealed` column + `(working)` placeholder + op coalescing. TDD: amend keeps change-id, changes hash; no-op when identical; op-log coalesces consecutive amends.
3. **`Seal`** (= new `Repo.Commit`) + fresh working change + merge-forward. TDD: seal sets message, advances, opens a clean working change; conflicts still recorded.
4. **`SyncWorking` + the command-start hook**; redefine `Status`/`Diff` to working-vs-parent; `log` labels `(working)`. TDD + e2e: edit folder, `cairn status`/`diff`/`log` reflect it WITHOUT an explicit commit; `cairn commit -m` seals; `cairn undo` recovers un-sealed work after `abandon --force`.
5. **Reconcile dirty-guards** with the always-saved model; update Slice-A guard messages/semantics; e2e.
6. Full gate + cross-compile; all prior phases unaffected.

## 9. Out of scope
History editing (squash/split/rebase of *sealed* lines), `stash`, `cherry-pick`, `blame`, `bisect` — separate later slices. (Note: WCC already delivers the "refine current work" slice of history-editing for free, and removes the main reason to `stash`.) Scope-to-touched-lines snapshot optimization — later.
