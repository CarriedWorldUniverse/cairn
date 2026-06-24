# history editing (reword / squash / drop) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** `cairn reword <commit> -m`, `cairn squash <commit>`, `cairn drop <commit>` — rewrite your own un-folded line's sealed history, auto-rebasing descendants (conflicts-as-data), strictly guarded.

**Architecture:** One uniform engine primitive `rewriteChain` re-seals a line's sealed-commit sequence and rebases the open working change, **always via 3-way `mergeTrees`** — so reword/squash are conflict-free special cases and drop's real merges record conflicts-as-data. Each verb builds the new sequence and calls `rewriteChain`. Atomic catalogue writes (mirror `Seal`). Built on WCC (sealed chain + working change) + `firstParent`/`ChangeIDOf`/`treeHashOf`/`mergeTrees`.

**Tech:** Go 1.26.3, go-git, modernc sqlite. Spec: `docs/cairn/specs/2026-06-24-history-editing.md`. `mergeTrees(changeID, baseTree, oursTree, theirsTree) (mergedTree string, []Conflict, error)`.

**Conventions:** errors `pkg.Func: %w`; `skipOnWindows` on e2e; one tx per catalogue mutation; commit after each task.

---

## Task 1: sealedChain + guardEditable + rewriteChain + `Reword`

**Files:** create `internal/change/edit.go`, `internal/change/edit_test.go`.

This task builds the shared machinery and proves it with the simplest verb (reword).

- [ ] **Step 1: `sealStep` + `sealedChain` (test first)**

```go
// sealStep is one sealed commit on a line, in base→top order.
type sealStep struct {
	ChangeID   string
	Commit     string // the sealed commit
	Tree       string // its tree
	ParentTree string // the tree it was originally built on (its parent's tree)
	Message    string // its description (Change-Id trailer stripped)
}

// sealedChain returns the line's sealed commits ABOVE base, base→top order. It
// walks first-parent from the line's top sealed commit (the working commit's
// parent if an open working change sits at the tip, else the line tip) down to
// (exclusive) line.BaseCommit.
func (e *Engine) sealedChain(lineID string) ([]sealStep, error)
```
Implementation: `line := lineByID(lineID)`. Determine the top sealed commit: find the line's open change (sealed=0); `top := firstParent(openChange.HeadCommit)` if it has a head, else `line.TipCommit`. Walk first-parent from `top` to `line.BaseCommit` (exclusive), collecting commits; for each, `ChangeIDOf`, `treeHashOf`, `treeHashOf(firstParent)` (ParentTree), `stripChangeID(message)`. Reverse to base→top. (A commit with no change-id, or below base, stops the walk.)
Test: build a line with 3 sealed commits via Seal (mirror seal_test.go: SnapshotWorking + Seal repeatedly); `sealedChain(lineID)` returns 3 steps in base→top order with the right messages/change-ids and `ParentTree`s.

- [ ] **Step 2: `guardEditable` (test first)**

```go
// guardEditable resolves commit's line and enforces the strict editable rule;
// returns the line id, the sealed chain, and commit's index in it.
func (e *Engine) guardEditable(commit string) (lineID string, chain []sealStep, idx int, err error)
```
Checks (each a distinct error): commit resolves to a sealed change on a line; `line.ParentLine != ""` (non-root); line status is active (not folded/abandoned — check the `line.Status`/state); commit is in `sealedChain` above base (idx found); **no child lines** (`SELECT 1 FROM line WHERE parent_line = lineID LIMIT 1` → if present, refuse "line %q has a child line; cannot edit its history").
Test: refuse on root line; on a commit below base / not on the line; on a line WITH a child line (create a child via CreateLine/Express); succeed on a clean non-root line commit.

- [ ] **Step 3: `rewriteChain` (test via Reword)**

```go
// rewriteChain re-seals newSeq (base→top) onto the line base, 3-way-rebasing each
// step onto its rebuilt parent, then rebases the open working change onto the new
// top. Change-ids in newSeq survive; sealed change rows NOT in newSeq are deleted.
// All catalogue writes are atomic. Returns any rebase conflicts (data).
func (e *Engine) rewriteChain(lineID string, newSeq []sealStep) ([]Conflict, error)
```
Logic:
```
line := lineByID(lineID)
before := viewMap()
prevCommit := line.BaseCommit
prevTree := treeHashOf(prevCommit)        // empty-tree if base==""
var conflicts []Conflict
type upd struct{ changeID, head string }
var updates []upd
for _, s := range newSeq {
    merged, cf, err := mergeTrees(s.ChangeID, s.ParentTree, prevTree, s.Tree)  // base, ours(new parent), theirs(this step)
    conflicts = append(conflicts, cf...)
    nc := writeCommit(merged, s.ChangeID, s.Message, parentsSlice(prevCommit))
    updates = append(updates, upd{s.ChangeID, nc})
    prevCommit, prevTree = nc, merged
}
// rebase the open working change W onto prevCommit (new top sealed)
w := openChange(lineID)                    // sealed=0 change on the line
wOldTree := treeHashOf(w.HeadCommit)       // "" → empty
wParentTree := treeHashOf(firstParent(w.HeadCommit))
mw, cfw, _ := mergeTrees(w.ID, wParentTree, prevTree, wOldTree)
conflicts = append(conflicts, cfw...)
newW := writeCommit(mw, w.ID, "(working)", parentsSlice(prevCommit))
// ATOMIC tx: for each updates → UPDATE change SET head_commit=? WHERE id=? ;
//   DELETE change rows whose id is NOT in newSeq and was a sealed change on this line (squash/drop removals);
//   UPDATE working change head=newW ; advance line tip = newW ; insert conflicts ; record one "rewrite" op.
```
Helpers: `parentsSlice(p)` = nil if ""; `openChange(lineID)` = the sealed=0 change; `treeHashOf` (empty-tree for ""). The removed-change detection: the set of sealed change-ids originally on the line minus the set in newSeq. Mirror Seal's tx structure exactly (defer Rollback; writeCommit/mergeTrees git writes OUTSIDE the tx).

- [ ] **Step 4: `Reword` (test first)**

```go
func (e *Engine) Reword(commit, message string) ([]Conflict, error) {
	lineID, chain, idx, err := e.guardEditable(commit)
	if err != nil { return nil, err }
	chain[idx].Message = message      // only the message changes
	return e.rewriteChain(lineID, chain)
}
```
Test: 3 sealed commits; `Reword(S2, "new msg")` → `sealedChain` now shows S2's message = "new msg"; S2's change-id preserved; S1/S3 change-ids preserved; trees unchanged (S3's content identical); ZERO conflicts; the line tip's working change rebased.

- [ ] **Step 5: verify + commit**

`go test ./internal/change/ -run 'SealedChain|GuardEditable|Reword|RewriteChain' -v` + `go test ./...` + vet + cross-compile. Commit:
```
git add internal/change/edit.go internal/change/edit_test.go
git commit -m "feat(change): sealedChain + guardEditable + rewriteChain (3-way) + Reword (history-editing task 1)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 2: `Squash`

**Files:** modify `internal/change/edit.go`, `internal/change/edit_test.go`.

- [ ] **Step 1: test first** — 3 sealed commits S1,S2,S3; `Squash(S2)` → combines S2 into S1: result chain has 2 sealed commits; the squashed step keeps S1's change-id, has tree==tree(S2), message == S1msg+"\n\n"+S2msg; S2's change-id is GONE; S3 rebased cleanly (no conflict), its content unchanged; `log` count drops by one. `Squash` on the FIRST sealed commit (parent is the base) → error "nothing to squash into".

- [ ] **Step 2: implement**
```go
func (e *Engine) Squash(commit string) ([]Conflict, error) {
	lineID, chain, idx, err := e.guardEditable(commit)
	if err != nil { return nil, err }
	if idx == 0 { return nil, errors.New("change.Squash: nothing to squash into (commit is first on its line)") }
	// merge chain[idx] INTO chain[idx-1]: combined step keeps idx-1's change-id,
	// takes idx's tree (cumulative), concatenated message, idx-1's ParentTree.
	combined := sealStep{
		ChangeID:   chain[idx-1].ChangeID,
		Tree:       chain[idx].Tree,
		ParentTree: chain[idx-1].ParentTree,
		Message:    chain[idx-1].Message + "\n\n" + chain[idx].Message,
	}
	newSeq := append(append([]sealStep{}, chain[:idx-1]...), combined)
	newSeq = append(newSeq, chain[idx+1:]...)
	return e.rewriteChain(lineID, newSeq)
}
```
(`rewriteChain` deletes the dropped change-id's row automatically via its removed-change detection. The 3-way for the combined step: base=chain[idx-1].ParentTree, ours=base-rebuilt(==same tree), theirs=tree(idx) → clean.)

- [ ] **Step 3: verify + commit** — `go test ./internal/change/ -run Squash -v` + full + vet/cross. Commit `feat(change): Squash — fold a commit into its parent (history-editing task 2)`.

---

## Task 3: `Drop` (the conflict-capable verb)

**Files:** modify `internal/change/edit.go`, `internal/change/edit_test.go`.

- [ ] **Step 1: test first** — (a) independent drop: S1 touches `a.txt`, S2 touches `b.txt`, S3 touches `c.txt`; `Drop(S2)` → S2 gone, S3 rebases CLEANLY (no conflict), `b.txt` no longer in the line tip, `a.txt`/`c.txt` present. (b) dependent drop: S1 creates `x.txt`="1", S2 edits `x.txt`="2", S3 edits `x.txt`="3"; `Drop(S2)` → S3's rebase 3-way-merges (base=tree after S2, ours=tree after S1, theirs=tree after S3) → records a CONFLICT (data), the rewrite still completes, the conflict is on S3's change.

- [ ] **Step 2: implement**
```go
func (e *Engine) Drop(commit string) ([]Conflict, error) {
	lineID, chain, idx, err := e.guardEditable(commit)
	if err != nil { return nil, err }
	newSeq := append(append([]sealStep{}, chain[:idx]...), chain[idx+1:]...) // omit idx
	return e.rewriteChain(lineID, newSeq)
}
```
(`rewriteChain` handles the 3-way rebase + conflicts-as-data for the steps after the dropped one, and deletes the dropped change-id's row.)

- [ ] **Step 3: verify + commit** — `go test ./internal/change/ -run Drop -v` + full + vet/cross. Commit `feat(change): Drop — remove a commit, 3-way rebase descendants (conflicts-as-data) (history-editing task 3)`.

---

## Task 4: worktree + CLI for reword/squash/drop

**Files:** modify `internal/worktree/worktree.go`, `cmd/cairn/main.go`; create `cmd/cairn/edit_e2e_test.go`.

- [ ] **Step 1: worktree (test first)**
```go
func (r *Repo) Reword(commit, message string) (change.CommitResult, error) // -> eng.Reword + rematerialize the line's expressed folder + return conflicts
func (r *Repo) Squash(commit string)  (change.CommitResult, error)
func (r *Repo) Drop(commit string)    (change.CommitResult, error)
```
Each: call the engine op; resolve the affected line (from the commit's change → LineID → line name); if that branch is expressed, `rematerialize(branch, entry)` to the new tip; build a `CommitResult` with the conflict paths (mirror `Repo.Commit`). A helper `lineNameOfCommit(commit) (string, error)` (commit → ChangeIDOf → change → LineID → line name).

- [ ] **Step 2: CLI**
Dispatch `case "reword"/"squash"/"drop"`. All via `openRepoSynced` (so W reflects live edits before the rewrite).
```
  reword <commit> -m "msg"   change a sealed commit's message (your own line)
  squash <commit>            fold a commit into its parent (your own line)
  drop <commit>              remove a commit, rebasing the rest (your own line)
```
- `cmdReword`: `-m` required; `r.Reword(commit, msg)`; on `res.Conflicts>0` → conflict notice + `errConflicts` (exit 2); else stderr note.
- `cmdSquash`/`cmdDrop`: `r.Squash/Drop(commit)`; same conflict handling. A guard refusal surfaces the clear `not editable` message via mapErr.

- [ ] **Step 3: e2e `cmd/cairn/edit_e2e_test.go`** (skipOnWindows): init; express a CHILD branch `feat` from root; commit 3 changes on `feat` (capture shas from `cairn commit` stdout); 
  - `cairn reword <sha2> --repo dir -m "reworded"` → `cairn log` on feat shows "reworded".
  - `cairn squash <sha3>` → `cairn log` count drops by one.
  - `cairn drop <sha1-independent>` → succeeds; `cairn log` no longer shows it.
  - `cairn reword <root-commit>` → refused "not editable" (root line).
  Match the real `express`/`commit`/`log` CLI spellings.

- [ ] **Step 4: verify + commit** — `go test ./...` + vet/cross. Commit `feat(worktree,cmd): cairn reword/squash/drop CLI (history-editing task 4)`.

---

## Task 5: final gate + usage

- [ ] Update `usage`; full `go test ./...` + `go vet ./...` + cross-compile darwin/windows. Manual smoke: make 3 commits on a feature line, reword → squash → drop, check `log`. Commit any usage tweak.

## Notes
- **The crux is `rewriteChain`** — uniform 3-way rebase makes reword/squash conflict-free and drop conflicts-as-data, all one code path. Get its atomic tx right (mirror `Seal`): update surviving change heads, delete removed change rows, rebase W, advance tip, insert conflicts, one op.
- **The strict guard is the safety boundary** — refuse on root / below base / folded / line-with-children. Test each refusal.
- **Change-ids are identity** — preserved except for the removed step (squash/drop). The working change is always rebased onto the new top.
- Atomicity: a failure mid-rewrite must leave the line unchanged (one tx; git writes are content-addressed/idempotent so orphaned objects are harmless).
- DRY, YAGNI, TDD. Each task green before the next.
