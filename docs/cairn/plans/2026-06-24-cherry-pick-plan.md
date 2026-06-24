# cherry-pick — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** `cairn cherry-pick <commit> [branch]` — apply a commit from another line onto your branch as a new sealed commit (original message, fresh change-id), rebasing your working change on top. Conflicts-as-data.

**Architecture:** `CherryPick` appends one sealed commit (the picked delta 3-way-merged onto the line's current top) + rebases the open working change, all atomic. No history-editing guard (nothing existing is re-hashed). Reuses `mergeTrees`/`writeCommit`/`treeHashOf`/`firstParent`/`stripChangeID`/`openWorkingChange`/`newChangeID` + the `Seal` inline-insert pattern.

**Tech:** Go 1.26.3, go-git, modernc sqlite. Spec: `docs/cairn/specs/2026-06-24-cherry-pick.md`. `mergeTrees(changeID, baseTree, oursTree, theirsTree) (mergedTree, []Conflict, error)`.

**Conventions:** errors `pkg.Func: %w`; `skipOnWindows` on e2e; one tx; commit after each task.

---

## Task 1: engine `CherryPick`

**Files:** create `internal/change/cherrypick.go`, `internal/change/cherrypick_test.go`.

- [ ] **Step 1: test first** — build TWO non-root lines (or a line A with a commit + a target line B). On A, seal a commit `C` that adds `f.txt`="F". On B, seal some history (or leave B with just its base). Then `CherryPick(Ccommit, Blines.ID)`:
  - `TestCherryPickCleanApply`: returns no conflicts; B gains a NEW sealed commit whose message == C's message and whose change-id is FRESH (≠ C's change-id, and a new `change` row exists, sealed=1); `f.txt`="F" present at B's tip; C on A is unchanged (same commit + change-id).
  - `TestCherryPickKeepsWorkingEdits`: before the pick, snapshot B's working change with an edit to `g.txt`="G"; after the pick, B's tip tree has BOTH `f.txt`="F" (picked) and `g.txt`="G" (your work) — the working edit survived on top.
  - `TestCherryPickConflictAsData`: C edits `x.txt` and B's history has `x.txt` differently → the pick returns conflicts (len>0), still completes (new sealed change exists, tip advanced), the conflict is recorded (queryable).
  - `TestCherryPickNonCairnCommit`: a bogus/zero sha → error "not a cairn commit".
  Build trees via `e.WriteBlob` + `change.TreeEntry` + `SnapshotWorking` + `Seal`; create lines via the same helper the edit tests use (`CreateLine` + seals). To assert the fresh change-id: capture C's change-id and assert B's new top sealed change-id differs and `GetChange(newID)` exists with `sealed=1`.

- [ ] **Step 2: implement `internal/change/cherrypick.go`**
```go
func (e *Engine) CherryPick(pickedCommit, targetLineID string) ([]Conflict, error) {
	// resolve picked
	cid, err := e.ChangeIDOf(pickedCommit)
	if err != nil || cid == "" { return nil, fmt.Errorf("change.CherryPick: %q is not a cairn commit", pickedCommit) }
	pickedTree, err := e.treeHashOf(pickedCommit); if err != nil { return nil, ... }
	pickedParent, err := e.firstParent(pickedCommit); if err != nil { return nil, ... }
	pickedParentTree, err := e.treeHashOf(pickedParent); if err != nil { return nil, ... }
	pmsg, err := e.commitMessage(pickedCommit); if err != nil { return nil, ... }
	pickedMsg := stripChangeID(pmsg)

	line, err := e.lineByID(targetLineID); if err != nil { return nil, ... }
	w, err := e.openWorkingChange(targetLineID); if err != nil { return nil, ... }
	var topSealed string
	if w.HeadCommit != "" { topSealed, err = e.firstParent(w.HeadCommit); if err != nil { return nil, ... } } else { topSealed = line.TipCommit }
	topTree, err := e.treeHashOf(topSealed); if err != nil { return nil, ... }

	before, err := e.viewMap(); if err != nil { return nil, ... }
	newID := newChangeID()
	merged, cf, err := e.mergeTrees(newID, pickedParentTree, topTree, pickedTree); if err != nil { return nil, ... }
	newSealed, err := e.writeCommit(merged, newID, pickedMsg, parentsSlice(topSealed)); if err != nil { return nil, ... }

	// rebase the working change W onto newSealed (apply W's delta onto the pick)
	var newW string
	wDesc := workingDescription
	if w.HeadCommit == "" {
		newW, err = e.writeCommit(merged, w.ID, wDesc, parentsSlice(newSealed)); if err != nil { return nil, ... } // empty delta → rides on the pick
	} else {
		wMsg, err := e.commitMessage(w.HeadCommit); if err != nil { return nil, ... }
		wDesc = stripChangeID(wMsg)
		wOld, err := e.treeHashOf(w.HeadCommit); if err != nil { return nil, ... }
		wParent, err := e.firstParent(w.HeadCommit); if err != nil { return nil, ... }
		wParentTree, err := e.treeHashOf(wParent); if err != nil { return nil, ... }
		mw, cfw, err := e.mergeTrees(w.ID, wParentTree, merged, wOld); if err != nil { return nil, ... }
		cf = append(cf, cfw...)
		newW, err = e.writeCommit(mw, w.ID, wDesc, parentsSlice(newSealed)); if err != nil { return nil, ... }
	}

	ts := e.now().UTC().Format(time.RFC3339Nano)
	tx, err := e.db.Begin(); if err != nil { return nil, ... }
	defer func() { _ = tx.Rollback() }()
	// INSERT new sealed change (match Seal's inline insert columns: id,line_id,author,head_commit,status,has_conflict,sealed,created_at,updated_at)
	hc := 0; if len(cf) > 0 { hc = 1 }
	if _, err := tx.Exec(`INSERT INTO change(id,line_id,author,head_commit,status,has_conflict,sealed,created_at,updated_at) VALUES(?,?,?,?, 'open', ?, 1, ?, ?)`,
		newID, targetLineID, e.identityName(), newSealed, hc, ts, ts); err != nil { return nil, ... }   // use the same author source Seal uses
	if _, err := tx.Exec(`UPDATE change SET head_commit=?, updated_at=? WHERE id=?`, newW, ts, w.ID); err != nil { return nil, ... }
	if _, err := tx.Exec(`UPDATE line SET tip_commit=? WHERE id=?`, newW, targetLineID); err != nil { return nil, ... }
	for _, c := range cf { if err := insertConflict(tx, c, ts); err != nil { return nil, ... } }
	if err := e.recordOpTx(tx, e.now().UTC(), "cherry-pick", e.identityName(), before, /*after*/ nil, ts); err != nil { return nil, ... } // match recordOpTx's real signature + viewMapTx(tx) for after
	if err := tx.Commit(); err != nil { return nil, ... }
	return cf, nil
}
```
IMPORTANT: match the REAL column set + author source that `Seal`'s inline `INSERT INTO change` uses (read seal.go — it may use `e.idName`/`ch.Author`/a helper, and may include different columns). Match `recordOpTx`'s real signature (it takes `view_after` via `viewMapTx(tx)` — mirror how Seal/rewriteChain call it). `workingDescription` is the existing constant.

- [ ] **Step 3: verify + commit**
`go test ./internal/change/ -run CherryPick -v` + `go test ./...` + vet + cross. Commit:
```
git add internal/change/cherrypick.go internal/change/cherrypick_test.go
git commit -m "feat(change): CherryPick — append picked delta as a sealed commit + rebase working change (conflicts-as-data) (cherry-pick task 1)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 2: worktree + CLI

**Files:** modify `internal/worktree/worktree.go`, `cmd/cairn/main.go`; create `cmd/cairn/cherrypick_e2e_test.go`.

- [ ] **Step 1: worktree**
```go
func (r *Repo) CherryPick(branch, commit string) (change.CommitResult, error) {
	line, err := r.eng.LineByName(branch)
	if err != nil { return change.CommitResult{}, fmt.Errorf("worktree.CherryPick: %w", err) }
	conflicts, err := r.eng.CherryPick(commit, line.ID)
	if err != nil { return change.CommitResult{}, fmt.Errorf("worktree.CherryPick: %w", err) }
	if entry, ok := r.st.Expressed[branch]; ok { _ = r.rematerialize(branch, entry) }
	line, _ = r.eng.LineByName(branch)
	return change.CommitResult{HeadCommit: line.TipCommit, Conflicts: conflictPathsFrom(conflicts)}, nil
}
```
(Match the real `CommitResult` shape + how the existing `Repo.Commit`/`applyEdit` builds conflict paths from `[]change.Conflict` — reuse that exact mapping.)

- [ ] **Step 2: CLI** — dispatch `case "cherry-pick": return cmdCherryPick(rest)`; usage `cherry-pick <commit> [branch]   apply a commit from another line onto your branch`.
```go
func cmdCherryPick(args []string) error {
	fs := flag.NewFlagSet("cherry-pick", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := fs.Parse(args); err != nil { return err }
	if fs.NArg() < 1 { return errors.New("usage: cairn cherry-pick <commit> [branch]") }
	commit := fs.Arg(0)
	r, err := openRepoSynced(*repo, *author)
	if err != nil { return mapErr(err) }
	defer r.Close()
	branch := ""
	if fs.NArg() > 1 { branch = fs.Arg(1) } else if branch, err = r.DefaultBranch(); err != nil { return mapErr(err) }
	res, err := r.CherryPick(branch, commit)
	if err != nil { return mapErr(err) }
	if len(res.Conflicts) > 0 {
		fmt.Fprintf(os.Stderr, "cherry-pick: %d conflict(s) in: %s — resolve, then commit\n", len(res.Conflicts), strings.Join(res.Conflicts, ", "))
		return errConflicts
	}
	fmt.Fprintln(os.Stderr, "cairn: cherry-picked")
	return nil
}
```

- [ ] **Step 3: e2e `cmd/cairn/cherrypick_e2e_test.go`** (skipOnWindows): init; express `feat` from root; commit a change on `feat` adding `picked.txt` (capture its sha from stdout); express another branch `other` from root (or use root); `cairn cherry-pick <sha> --repo dir other` (or onto root) → `picked.txt` appears in the target folder + `cairn log <target>` shows the picked message. A bogus sha → error.

- [ ] **Step 4: verify + commit** — `go test ./...` + vet + cross. Commit `feat(worktree,cmd): cairn cherry-pick CLI (cherry-pick task 2)`.

---

## Task 3: final gate + usage
- [ ] Update `usage`; full `go test ./...` + `go vet ./...` + cross-compile darwin/windows. Manual smoke: commit on feat, cherry-pick onto root, check the file + log. Commit any usage tweak.

## Notes
- Cherry-pick **appends + rebases W** — it never re-hashes existing sealed commits, so no editable-guard is needed (unlike reword/squash/drop). Allowed on any line.
- The new sealed commit gets a FRESH change-id (a copy, like git's new sha); the origin commit is untouched.
- Conflicts-as-data via `mergeTrees` (pick delta onto your top, and W's delta onto the pick). Atomic tx mirrors `Seal`.
- DRY, YAGNI, TDD.
