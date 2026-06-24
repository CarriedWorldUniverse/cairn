# cairn — cherry-pick

**Status:** draft for approval · 2026-06-24
**Goal:** `cairn cherry-pick <commit>` — apply a commit from another line onto your branch as a new sealed commit (original message, fresh change-id), keeping your in-progress working change cleanly on top. Conflicts-as-data. Discrete CLI verb (no GUI).

**Operator decision (locked):** seal a new commit + keep your work (the picked change is a distinct sealed commit at your line's tip; your un-sealed working edits rebase on top, untouched).

---

## 1. Model

Target line `L = base → S1 → … → Sn → W` (sealed commits topped by the open working change `W`, WCC). Cherry-picking commit `C` (on any line):
- `pickedParentTree = tree(firstParent(C))`, `pickedTree = tree(C)`, `pickedMsg = stripChangeID(message(C))` — the picked **delta** is `pickedTree` vs `pickedParentTree`.
- **Apply the delta onto the current top sealed tree** (`tree(Sn)` = `W`'s parent): `merged = mergeTrees(newID, base=pickedParentTree, ours=tree(Sn), theirs=pickedTree)` → the picked change applied onto your history; conflicts-as-data.
- **Create a new sealed commit** `newSealed = writeCommit(merged, newID, pickedMsg, parent=Sn)` with a **fresh change-id** (it's a copy on your line, like git's new sha) and a **new sealed `change` row** (`sealed=1, status='open', head=newSealed`).
- **Rebase `W` on top:** `mergeTrees(W, base=W's old parent tree (=tree(Sn)), ours=merged, theirs=tree(W))` → `newW = writeCommit(..., W.id, <W's description>, parent=newSealed)`. Your un-sealed edits are re-applied on top of the cherry-picked commit (kept, separate).
- Advance the line tip to `newW`; all catalogue writes atomic (one tx, mirror `Seal`).

This **only appends** a sealed commit and rebases the open working change — `S1…Sn` are untouched (no re-hash), so **no shared/sealed history is rewritten**. Cherry-pick is therefore allowed on **any** line (including root) you're working on — no history-editing guard. The only checks: `C` resolves to a real cairn commit (`ChangeIDOf != ""`), and the target line is expressed/resolvable.

(If `W` has no head yet — never snapshotted — it rebases cleanly as an empty delta onto `newSealed`. If `W` had un-sealed edits, they survive on top.)

---

## 2. Engine (`internal/change/cherrypick.go`)
```go
// CherryPick applies the delta of pickedCommit (vs its first parent) onto the top
// of targetLineID's sealed history as a NEW sealed commit (fresh change-id, the
// picked message), then rebases the open working change on top. Conflicts are
// recorded as data (non-blocking). Returns them.
func (e *Engine) CherryPick(pickedCommit, targetLineID string) ([]Conflict, error)
```
- Resolve `pickedCommit` → full sha; if `ChangeIDOf == ""` → error "not a cairn commit". Read `pickedParentTree`/`pickedTree`/`pickedMsg`.
- `line := lineByID(targetLineID)`; `w := openWorkingChange(targetLineID)`; `topSealed := firstParent(w.HeadCommit)` if `w.HeadCommit != ""` else `line.TipCommit`; `topTree := treeHashOf(topSealed)`.
- `newID := newChangeID()`; `merged, cf, _ := mergeTrees(newID, pickedParentTree, topTree, pickedTree)`; `newSealed := writeCommit(merged, newID, pickedMsg, parentsSlice(topSealed))`.
- Rebase `W`: `wOld := treeHashOf(w.HeadCommit)`; `wParentTree := treeHashOf(firstParent(w.HeadCommit))` (when `w.HeadCommit==""`, treat `wOld`/`wParentTree` as the empty tree and skip the `firstParent` call — guard it like `rewriteChain` does); `mw, cfw, _ := mergeTrees(w.ID, wParentTree, merged, wOld)`; `wDesc := stripChangeID(message(w.HeadCommit))` (or `"(working)"` when headless); `newW := writeCommit(mw, w.ID, wDesc, parentsSlice(newSealed))`.
- conflicts = cf + cfw.
- **One tx:** INSERT the new sealed `change` row (`newID, line_id=targetLineID, author=<identity>, head_commit=newSealed, status='open', sealed=1, has_conflict=len(cf)>0, timestamps`); `UPDATE change SET head_commit=newW, has_conflict=?, updated_at=? WHERE id=w.ID`; `UPDATE line SET tip_commit=newW WHERE id=targetLineID`; `insertConflict` each; `recordOpTx(..., "cherry-pick", ...)`. Commit.

Reuse: `firstParent`, `ChangeIDOf`, `treeHashOf`, `commitMessage`/`stripChangeID`, `mergeTrees`, `writeCommit`, `parentsSlice`, `openWorkingChange`, `lineByID`, the new-sealed-change column set from `Seal`'s inline insert.

## 3. Worktree + CLI
- `Repo.CherryPick(branch, commit string) (change.CommitResult, error)` — resolve the target line from `branch` (the line you're applying ONTO); `eng.CherryPick(commit, line.ID)`; re-materialize the expressed folder to the new tip; return a `CommitResult` with conflict paths (mirror `Repo.Commit`).
- CLI: `cairn cherry-pick <commit> [branch]` (default branch = DefaultBranch) via `openRepoSynced` (so `W` captures live edits before the pick). On conflicts → conflict notice + `errConflicts` (exit 2; resolve then commit). Usage line. The `<commit>` is a sha from `cairn log <other-branch>`.

## 4. Conflict handling
If the picked delta conflicts with your history (or your working edits conflict with the pick), the conflict is recorded as data on the relevant change (the new sealed change and/or `W`), the pick still completes, and `cairn status`/`resolve` work on it — same model as merge-forward/pull/drop.

## 5. Out of scope (later)
- **Range cherry-pick** (`A..B`) — v1 is one commit.
- **`-x` provenance line** ("cherry picked from …") in the message.
- Picking a merge commit's specific parent. Cross-line full-fidelity provenance.

## 6. Testing / DoD
- **clean pick**: `C` on line A adds `f.txt`; cherry-pick onto line B (no overlap) → B gains a new sealed commit with `pickedMsg` + a fresh change-id; `f.txt` present at B's tip; A's `C` unchanged (same commit/change-id); B's prior sealed commits unchanged (same hashes).
- **keep your work**: B's working change has an un-sealed edit to `g.txt`; after cherry-pick, `g.txt` edit survives on top (the new working commit has both the picked `f.txt` and the edited `g.txt`).
- **conflict-as-data**: `C` edits the same line of `x.txt` that B's history has differently → the pick records a conflict (data), still completes; `resolve` works.
- **engine**: `CherryPick` inserts exactly one new sealed change row, rebases `W`, advances the tip, atomically; a non-cairn commit errors.
- Full gate + cross-compile; `skipOnWindows` on e2e; all prior phases unaffected.
