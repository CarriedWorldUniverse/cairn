# Working-copy-is-a-commit — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to execute this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** the expressed folder is always a real commit (the **working commit** of an open working change), continuously auto-snapshotted (amend-in-place) at the start of every command; `cairn commit -m` seals one unit and starts the next; `log` shows the working change `(working)`; `status`/`diff` show the working change vs its parent.

**Architecture:** Add a snapshot perf cache so auto-snapshot is O(changed files). Split today's `Engine.Commit` into `SnapshotWorking` (amend the working commit in place, no merge-forward) and `Seal` (set message + merge-forward + open a fresh working change). Auto-amend ops coalesce in the op-log. `writeTree` gains a known-blob-SHA fast path so unchanged files aren't re-encoded.

**Tech stack:** Go 1.26.3, go-git, modernc sqlite. Builds on the daily-driver slices (ignore/mode/diff/log/oplog all present).

**Spec:** `docs/cairn/specs/2026-06-24-working-copy-is-a-commit.md`.

**Conventions:** errors wrapped `pkg.Func: %w`; `skipOnWindows` on local-fixture/e2e tests; one tx per catalogue mutation; commit after each task.

---

## Task 1: Snapshot perf cache + blob-ref scan + writeTree known-SHA fast path

**Files:**
- Create: `internal/worktree/wccache.go`, `internal/worktree/wccache_test.go`
- Modify: `internal/change/gitobj.go` (writeTree/buildTree fast path), `internal/change/change.go` (Commit accepts refs — see Step 4)
- Modify: `internal/worktree/fs.go` (a `CachedScan`)

The cache lets an unchanged file skip read+hash+encode.

- [ ] **Step 1: shared entry type + cache model + test (write first)**

Use ONE entry type across the scan→tree boundary. Define it in `internal/change` (so the engine's `writeTreeRefs` and worktree's `CachedScan` share it; worktree already imports `change`):
```go
// internal/change (e.g. mode.go or a new entry.go)
// TreeEntry is one path's content for tree-building. For an UNCHANGED file
// (cache hit) Data is nil and SHA is the known git blob hash; for a changed/new
// file Data holds the bytes and SHA is "". Mode carries exec/symlink.
type TreeEntry struct {
	SHA  string
	Data []byte
	Mode EntryMode
}
```
`internal/worktree/wccache.go` — the on-disk per-path stat cache only:
```go
package worktree

// wcCacheEntry is the on-disk per-path stat cache: (mtimeNs,size) -> blobSHA+mode.
type wcCacheEntry struct {
	MtimeNs int64            `json:"m"`
	Size    int64            `json:"s"`
	BlobSHA string           `json:"b"`
	Mode    change.EntryMode `json:"k"`
}
```
Test `wccache_test.go`: round-trip the cache JSON (save/load), and a `cacheHit` helper that, given a stat (mtime,size) equal to an entry, returns the cached blobSHA; unequal → miss. `CachedScan` (Step 2) returns `map[string]change.TreeEntry` and `SnapshotWorking`/`writeTreeRefs` consume exactly that — no second type, no conversion.

- [ ] **Step 2: `CachedScan` in `fs.go` (test first)**

`CachedScan(dir string, tracked map[string]struct{}, cache map[string]wcCacheEntry, scanStartNs int64) (map[string]change.TreeEntry, map[string]wcCacheEntry, error)`: reuse the Slice-C `Scan` walk (ignore + tracked-set + symlink/exec), but per file: `Lstat`; if a cache entry matches `(mtimeNs,size)` AND `mtimeNs < scanStartNs` (racy-clean rule), emit `change.TreeEntry{SHA: cached, Mode: cached}` WITHOUT reading; else read (or `Readlink` for symlinks), hash the blob sha (`plumbing.ComputeHash(BlobObject, data)` or via the engine), emit `change.TreeEntry{Data, Mode}` and write the fresh cache entry. Return the entries + the new cache map (dropping vanished paths).

Test: a file unchanged between two CachedScans is NOT re-read (assert via a sentinel — e.g. wrap with a counter, or assert its `TreeEntry.Data` is nil and `SHA` set on the 2nd scan); a modified file (bump mtime+content) IS re-read (Data set); a file whose mtime == scanStart is re-read (racy); a deleted file drops from the cache.

- [ ] **Step 3: hash helper**

Compute a git blob SHA without storing: `plumbing.ComputeHash(plumbing.BlobObject, data).String()`. Verify it equals what `writeBlob` would store (write a blob via the engine, compare hashes) — add a tiny test in `internal/change` exposing a `BlobSHA(data) string` helper on the engine (or a package func) so worktree can pre-compute and the engine can store-by-need.

- [ ] **Step 4: writeTree known-SHA fast path (test first)**

In `internal/change/gitobj.go`, add a refs-aware tree builder over the `change.TreeEntry` defined in Step 1. Add:
```go
// writeTreeRefs builds a tree from entries that may carry a pre-computed blob SHA
// (skip encoding) or raw Data (encode via writeBlob). Symlinks/exec honored.
func (e *Engine) writeTreeRefs(entries map[string]TreeEntry) (plumbing.Hash, error)
```
For each entry: if `SHA != ""` use it directly as the blob hash (the blob is assumed already in the store OR will be ensured — see note); else `h := e.writeBlob(entry.Data)`. Reuse `buildTree`'s immediate/subdir split + mode emission. **Important:** a cache-hit blob SHA must actually exist in the object store. Guarantee it: the cache only stores a blobSHA after that blob was written (on the scan that first saw the file, `writeBlob` ran). So a cached SHA is always already stored. Add a test asserting `writeTreeRefs` produces the SAME tree hash as `writeTree(files,modes)` for the same logical content (one path via Data, one via SHA).

- [ ] **Step 5: commit + verify + commit**

Run: `go test ./internal/worktree/ ./internal/change/ -run 'Cache|WriteTreeRefs|BlobSHA' -v` → PASS. Full `go test ./...` green.
```bash
git add internal/worktree/wccache.go internal/worktree/wccache_test.go internal/worktree/fs.go internal/change/gitobj.go internal/change/*_test.go
git commit -m "feat(worktree,change): snapshot cache (mtime/size→blobSHA) + writeTree known-SHA fast path (WCC task 1)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 2: `SnapshotWorking` (amend-in-place) + `sealed` column + op coalescing

**Files:**
- Modify: `internal/change/schema.sql` (add `sealed` to `change`), `internal/change/change.go` (Change.Sealed, CreateChange default, SnapshotWorking), `internal/change/oplog.go` (coalesce), `internal/change/log.go` (CommitInfo.Working)
- Test: `internal/change/snapshot_test.go`

- [ ] **Step 1: schema + struct**

In `schema.sql`, add `sealed INTEGER NOT NULL DEFAULT 0` to the `change` table. In `change.go`, add `Sealed bool` to `Change`, SELECT it in `GetChange`, and have `CreateChange` insert `sealed=0`. (A fresh change is an open working change.)

- [ ] **Step 2: `SnapshotWorking` (test first)**

```go
// SnapshotWorking amends the open working change's head IN PLACE to reflect
// `entries` (the current folder). It keeps the working commit's parent and
// description; only the tree changes. No merge-forward. Returns changed=false
// (no-op) when the tree already matches. For a change with no head yet, it
// creates the working commit (parent = line tip) even if the tree is empty, so
// every open change always has a (working) commit at the line tip.
func (e *Engine) SnapshotWorking(changeID string, entries map[string]TreeEntry) (changed bool, head string, err error)
```
Logic:
- `ch := GetChange(changeID)`; `line := lineByID(ch.LineID)`.
- `tree := writeTreeRefs(entries)`.
- determine `parent`: if `ch.HeadCommit != ""` → `parent = firstParent(ch.HeadCommit)`; else `parent = line.TipCommit` (may be "").
- determine `desc`: if `ch.HeadCommit != ""` → reuse the current head's description (`stripChangeID(commitObject(head).Message)`); else `"(working)"`.
- **no-op check:** if `ch.HeadCommit != ""` and `treeHashOf(ch.HeadCommit) == tree` → return `(false, ch.HeadCommit, nil)`.
- `newHead := writeCommit(tree.String(), ch.ID, desc, parentsOrEmpty(parent))`.
- Transactionally: `UPDATE change SET head_commit=newHead, updated_at=? WHERE id=?` and advance the line tip to `newHead` (mirror Commit's tip update). Record a **coalesced** op (Step 4).
- return `(true, newHead, nil)`.

Test: create a line+open change, `SnapshotWorking` with files {a:1} → head set, change-id unchanged; snapshot again same content → `changed=false`, same head; snapshot {a:2} → `changed=true`, NEW head hash, SAME change-id; the line tip == the new head. Empty entries on a fresh change → still creates a `(working)` commit.

- [ ] **Step 3: amend keeps change-id, changes hash (test)**

Assert across two differing snapshots: `ch.ID` constant, `head` hash differs, and `firstParent(head)` is unchanged (parent preserved — it's an amend, not an append).

- [ ] **Step 4: op-log coalescing (test first)**

In `oplog.go`, add an op type `"snapshot"`. SnapshotWorking records its op via a coalescing path: if the **most recent** op is a `"snapshot"` for the **same change**, UPDATE that op's `view_after` (and timestamp) in place instead of INSERTing a new op; otherwise INSERT a new `"snapshot"` op. (So a burst of auto-amends collapses to one undo-able step; `undo` over it restores the pre-burst view.) Confirm `Undo` still walks ops correctly with coalesced snapshot ops.
Test: 3 consecutive `SnapshotWorking` calls on one change → `OperationLog()` grows by exactly 1 `snapshot` op (coalesced), whose `view_after` reflects the 3rd snapshot; a snapshot on a DIFFERENT change → a new op.

- [ ] **Step 5: CommitInfo.Working**

In `log.go`, add `Working bool` to `CommitInfo`; in `Log`, set `Working=true` for a commit that is the head of an open (`sealed=0`) change AND equals the line tip. (Query the change by head commit / track the open change for the line being logged.) Keep the Change-Id strip.

- [ ] **Step 6: verify + commit**

`go test ./internal/change/ -v` green; full `go test ./...` green.
```bash
git add internal/change/
git commit -m "feat(change): SnapshotWorking amend-in-place + sealed flag + op coalescing + CommitInfo.Working (WCC task 2)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 3: `Seal` (= the new commit) + fresh working change + merge-forward

**Files:**
- Modify: `internal/change/change.go` (Seal), `internal/change/merge.go` (reuse mergeForward)
- Test: `internal/change/seal_test.go`

- [ ] **Step 1: `Seal` (test first)**

```go
// Seal finalizes the open working change `changeID`: it stamps `message` onto the
// working commit, adopts the parent line (mergeForward, conflicts-as-data), marks
// the change sealed, then opens a FRESH working change on the same line whose
// working commit will sit on top of the sealed commit. Returns the new working
// change-id and any conflicts. Assumes the caller already snapshotted the folder
// into the working commit (Repo.Commit does SyncWorking first).
func (e *Engine) Seal(changeID, message string) (newChangeID string, conflicts []Conflict, err error)
```
Logic (mirror today's `Commit` tail, but split):
- `ch := GetChange(changeID)`; `line := lineByID(ch.LineID)`.
- the working commit `ch.HeadCommit` already holds the current tree (post-SyncWorking). Stamp the message: `sealed := writeCommit(treeHashOf(ch.HeadCommit), ch.ID, message, parentsOf(ch.HeadCommit))` (same tree+parent, new message). If `ch.HeadCommit == ""` (nothing ever snapshotted) → snapshot empty / treat tree as empty.
- `merged, conflicts := mergeForward(ch.ID, sealed)`; if `merged != "" && merged != treeHashOf(sealed)` → `sealed = writeCommit(merged, ch.ID, message, []string{sealed})`.
- `newCh := CreateChange(line.ID, author)` (sealed=0, head="" → its working commit is created on the next SnapshotWorking with parent = line tip = sealed).
- Transactionally (one tx): insert conflicts; `UPDATE change SET head_commit=sealed, has_conflict=?, sealed=1, updated_at=? WHERE id=ch.ID`; advance line tip to `sealed`; insert the new change row; record a `"commit"`/`"seal"` op (NOT coalesced). 
- return `(newCh.ID, conflicts, nil)`.

Note: `CreateChange` may need to run inside the same tx, or be called then the tx adjusts — keep all catalogue writes atomic; if `CreateChange` is its own tx today, refactor so Seal's writes are atomic together (mirror how Commit keeps conflict+head+tip atomic).

Test: open change with a snapshot {a:1}; `Seal("first")` → the old change is `sealed=1` with message "first"; a NEW change-id is returned (open, sealed=0); the line tip == the sealed commit; `Log(tip)` shows "first" (no "(working)" label on it once a working commit exists above it — but right after seal the new change has no head yet, so tip==sealed; after the next SnapshotWorking the working commit sits above). Add a conflict case mirroring the existing merge-forward-conflict test: a child line whose seal conflicts records the conflict and still seals.

- [ ] **Step 2: verify + commit**

`go test ./internal/change/ -v` green; full suite green.
```bash
git add internal/change/
git commit -m "feat(change): Seal — stamp message + merge-forward + open fresh working change (WCC task 3)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 4: `SyncWorking` + command-start hook + redefine Commit/Status/Diff

**Files:**
- Modify: `internal/worktree/worktree.go` (SyncWorking, Commit→Seal, Status/WorkingDiff redefine, cache plumbing), `internal/worktree/state.go` (per-branch cache path; update ChangeID on seal)
- Modify: `cmd/cairn/main.go` (call SyncWorking at command start; `log` labels working; commit note)
- Test: `internal/worktree/syncworking_test.go`, `cmd/cairn/wcc_e2e_test.go`

- [ ] **Step 1: `SyncWorking` (test first)**

`Repo.SyncWorking() error`: for each expressed branch — load that branch's wc-cache (`.cairn/wc-cache/<branch>.json`), resolve `tracked` from `eng.Files(line.TipCommit)`, `CachedScan(folder, tracked, cache, scanStartNs)`, `eng.SnapshotWorking(entry.ChangeID, entries)`, save the updated cache. Non-fatal aggregation: a per-branch error is wrapped and returned (the command layer decides severity).

Test: express a branch, write files in its folder, `r.SyncWorking()`, then assert `eng.GetChange(changeID).HeadCommit`'s tree matches the folder (via `eng.Files(head)`), with NO explicit commit. A second `SyncWorking` with no edits is a no-op (head unchanged). Edit a file → head advances (amend). Confirm the cache file exists and a no-edit sync doesn't re-hash (counter or timing-independent assertion: the cache entries are unchanged).

- [ ] **Step 2: `Repo.Commit` becomes seal**

Rewrite `Repo.Commit(branch, message)`:
```go
func (r *Repo) Commit(branch, message string) (change.CommitResult, error) {
	entry, ok := r.st.Expressed[branch]
	if !ok { return change.CommitResult{}, fmt.Errorf("worktree.Commit: branch %q is not expressed", branch) }
	if err := r.syncBranch(branch); err != nil { return change.CommitResult{}, err } // snapshot latest edits
	newChangeID, conflicts, err := r.eng.Seal(entry.ChangeID, message)
	if err != nil { return change.CommitResult{}, fmt.Errorf("worktree.Commit: %w", err) }
	// update wc state: this branch now tracks the fresh working change
	e := r.st.Expressed[branch]; e.ChangeID = newChangeID; r.st.Expressed[branch] = e
	if err := SaveState(r.stPath, r.st); err != nil { return change.CommitResult{}, err }
	// re-materialize the folder to the new working tip (== sealed tree; folder unchanged in content)
	line, _ := r.eng.LineByName(branch)
	if line.TipCommit != "" { _ = Materialize(r.eng, r.cacheDir(), line.TipCommit, filepath.Join(r.root, e.Path)) }
	return change.CommitResult{HeadCommit: line.TipCommit, Conflicts: conflictPaths(conflicts)}, nil
}
```
(Match the real `CommitResult` shape + how conflicts are returned. `syncBranch` = the single-branch SyncWorking helper. Keep the auto-sync-on-commit behavior from C-sync2 if present — call it after seal.)

- [ ] **Step 3: redefine `Status`/`WorkingDiff` to working-vs-parent**

`WorkingDiff(branch)`: resolve the open change's head (the working commit) and its parent; `eng.DiffCommits(parent, workingHead)`. `Status`: same source for Added/Modified/Deleted; keep lineage/ahead/conflicts. (These no longer call `Scan` — the pre-command SyncWorking already captured the folder into the working commit.) Remove the now-dead `isDirty`-via-Scan content path or repoint it (see Task 5).

- [ ] **Step 4: command-start hook + `log` label (test first via e2e)**

In `cmd/cairn/main.go`: after `openRepo` succeeds for a repo command (NOT init/clone/help), call `r.SyncWorking()`. On a read command a sync error is surfaced but the command may proceed best-effort; on commit it must succeed. In `cmdLog`, prefix the working commit line with `(working)` when `CommitInfo.Working`.

e2e `cmd/cairn/wcc_e2e_test.go` (real helpers, `skipOnWindows`):
- `TestWCCEditsAutoCaptured`: init, write `a.txt` in the root folder, run `cairn status --repo dir` (NO commit) → status shows `A a.txt`; `cairn diff` shows the addition; `cairn log` shows a top entry labeled `(working)`.
- `TestWCCCommitSealsAndAdvances`: after editing, `cairn commit --repo dir -m "add a"` → `cairn log` shows "add a" as a sealed entry and a fresh `(working)` on top; `status` is now empty (no un-sealed delta) except the empty working change.
- `TestWCCUndoRecoversUnsealed`: edit (auto-captured) → `cairn undo` → the edit is reverted in the working commit and re-materialized on disk.

- [ ] **Step 5: verify + commit**

`go test ./...` green incl. the e2e; cross-compile.
```bash
git add internal/worktree/ cmd/cairn/main.go cmd/cairn/wcc_e2e_test.go
git commit -m "feat(worktree,cmd): SyncWorking on every command; commit=seal; status/diff=working-vs-parent; log labels (working) (WCC task 4)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 5: Reconcile dirty-guards with always-saved

**Files:**
- Modify: `internal/worktree/worktree.go` (isDirty → "un-sealed delta" check; guard messages)
- Test: `internal/worktree/safety_test.go` (update)

- [ ] **Step 1: redefine "dirty" (test first)**

Replace the Slice-A `isDirty` (Scan-vs-tip byte compare) with: a branch is "dirty" iff its **open working change has a non-empty delta against its parent** (i.e., `WorkingDiff(branch)` is non-empty) AND the change is un-sealed. Since SyncWorking runs at command start, the working commit is current. `abandon`/`unexpress`/`fold` keep `--force`; the refusal message becomes: `branch %q has un-sealed work (recoverable with 'cairn undo'); commit it or pass --force to discard`.

Update `TestUnexpressDirtyRefused`/`TestAbandonDirtyRefused`/`TestFoldDirtyRefused`: the setup now writes+syncs (no explicit commit needed to be "dirty"); assert the new message; `--force` discards; a clean (sealed, no new edits) branch proceeds.

- [ ] **Step 2: verify + commit**

`go test ./...` green.
```bash
git add internal/worktree/
git commit -m "feat(worktree): dirty = un-sealed working delta (recoverable via undo); update guards (WCC task 5)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 6: Final gate, docs, optional `describe`

**Files:**
- Modify: `cmd/cairn/main.go` (usage note for the new commit semantics; optional `cairn describe`)

- [ ] **Step 1: optional `cairn describe -m` (if cheap)**

`cairn describe -m "msg"` = set the working change's message without sealing (so `log`'s `(working)` shows a name). Implement as `eng.SnapshotWorking` variant that only rewrites the head's message, or a small `eng.Describe(changeID, message)`. Skip if it complicates Task 2's amend; defer to a follow-up.

- [ ] **Step 2: usage + full gate**

Update `usage`: note `commit -m` seals + advances; `status`/`diff` show the working change. Run `go test ./...` + `go vet ./...` + `GOOS=darwin go build ./...` + `GOOS=windows go build ./...` — all green. Manual smoke: init → edit → `status`/`log` reflect it with no commit → `commit -m` → `undo`.
```bash
git add -A
git commit -m "docs(cmd): usage for working-copy-is-a-commit semantics (WCC task 6)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Notes for the implementer
- **The hard part is Task 1's perf path:** a cache-hit file must NOT be re-read or re-encoded, and `writeTreeRefs` must reuse its stored blob SHA — otherwise auto-snapshot stays O(repo) and the whole feature is too slow to run on every command. Verify with a test that a no-edit `SyncWorking` does zero blob writes.
- **Amend vs append is the core invariant:** `SnapshotWorking` keeps the working commit's PARENT (amend); `Seal` is the only thing that advances the parent chain. Get this wrong and history either explodes (append every snapshot) or collapses (lose sealed commits).
- **Atomicity:** Seal's catalogue writes (seal old change, advance tip, insert new change, conflicts, op) must be one tx, mirroring today's `Commit`.
- **Op coalescing** keeps `undo` meaningful — without it, undo steps over individual keystroke-snapshots.
- Reuse everything from the daily-driver slices: ignore + tracked-set + mode model in the scan, `DiffTrees`/`Files`/`FileModes`, `Log`/`commitInfo`, `mergeForward`, the op-log.
- DRY, YAGNI, TDD, frequent commits. Each task green before the next.
