# stash + blame — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** `cairn blame <path>` (per-line who/when/why) + a minimal `cairn stash` shelve-stack (shelve the working delta → clean folder → pop to restore).

**Architecture:** blame wraps go-git's `Blame`, mapping each line's commit to its cairn change-id. stash captures the WCC working commit as a stack entry (a `stash` table), resets the working change to its parent, and pop re-applies. Builds on WCC (`SnapshotWorking`/`Seal`/`firstParent`/`ChangeIDOf`/`isWorkingHead`/working-vs-parent).

**Tech:** Go 1.26.3, go-git v5.13.2 (`git.Blame`), modernc sqlite. Spec: `docs/cairn/specs/2026-06-24-stash-blame.md`.

**Conventions:** errors `pkg.Func: %w`; `skipOnWindows` on e2e; one tx per catalogue mutation; commit after each task.

---

## Task 1: blame (engine + worktree + CLI)

**Files:** Create `internal/change/blame.go`, `internal/change/blame_test.go`; modify `internal/worktree/worktree.go`, `cmd/cairn/main.go`, `cmd/cairn/blame_e2e_test.go`.

- [ ] **Step 1: engine test (write first)**

`internal/change/blame_test.go` (real harness): on a line, commit `f.txt` with two lines via two separate changes/identities (use `SetIdentity` + `Seal` to make distinct authors/messages, or seedLineTip-style). Then `e.Blame(lineTip, "f.txt")` returns one `BlameLine` per line with the right `AuthorName`, a non-empty `Commit`, and `ChangeID` set to the change that last touched that line. A missing path → error.

- [ ] **Step 2: implement `internal/change/blame.go`**

```go
package change

import (
	"fmt"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// BlameLine is one line's provenance.
type BlameLine struct {
	Commit   string
	ChangeID string // the Change-Id trailer of Commit ("" if none)
	Author   string
	When     time.Time
	Text     string
}

// Blame returns per-line provenance for path at commit.
func (e *Engine) Blame(commit, path string) ([]BlameLine, error) {
	c, err := e.git.CommitObject(plumbing.NewHash(commit))
	if err != nil {
		return nil, fmt.Errorf("change.Blame: commit %s: %w", commit, err)
	}
	res, err := gogit.Blame(c, path)
	if err != nil {
		return nil, fmt.Errorf("change.Blame: %q: %w", path, err)
	}
	out := make([]BlameLine, 0, len(res.Lines))
	for _, ln := range res.Lines {
		sha := ln.Hash.String()
		cid, _ := e.ChangeIDOf(sha) // best-effort; "" if no trailer
		out = append(out, BlameLine{Commit: sha, ChangeID: cid, Author: ln.AuthorName, When: ln.Date, Text: ln.Text})
	}
	return out, nil
}
```
(Confirm the go-git alias used in the package is `git` — if so use `git.Blame`, not `gogit`. `ChangeIDOf` exists from WCC. `object.Line` fields: `AuthorName`, `Date`, `Hash`, `Text`.)

- [ ] **Step 3: worktree + a working-head check**

`internal/worktree/worktree.go`:
```go
func (r *Repo) Blame(branch, path string) ([]change.BlameLine, error) {
	line, err := r.eng.LineByName(branch)
	if err != nil { return nil, fmt.Errorf("worktree.Blame: %w", err) }
	if line.TipCommit == "" { return nil, fmt.Errorf("worktree.Blame: branch %q has no commits", branch) }
	return r.eng.Blame(line.TipCommit, path)
}
```

- [ ] **Step 4: CLI `cairn blame` + e2e**

Add dispatch `case "blame": return cmdBlame(rest)` (uses `openRepoSynced` so the tip reflects live edits), usage line `blame <path> [branch]   show per-line author/date/commit`. 
```go
func cmdBlame(args []string) error {
	fs := flag.NewFlagSet("blame", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := fs.Parse(args); err != nil { return err }
	if fs.NArg() < 1 { return errors.New("usage: cairn blame <path> [branch]") }
	path := fs.Arg(0)
	r, err := openRepoSynced(*repo, *author)
	if err != nil { return mapErr(err) }
	defer r.Close()
	branch := ""
	if fs.NArg() > 1 { branch = fs.Arg(1) } else if branch, err = r.DefaultBranch(); err != nil { return mapErr(err) }
	lines, err := r.Blame(branch, path)
	if err != nil { return mapErr(err) }
	// mark un-sealed working lines as (working)
	for _, ln := range lines {
		id := shortSha(ln.Commit)
		if working, _ := r.IsWorkingCommit(ln.Commit); working { id = "(working)" } // small Repo helper over eng.isWorkingHead
		fmt.Printf("%-10s %-12s %s  %s\n", id, ln.Author, ln.When.Format("2006-01-02"), strings.TrimRight(ln.Text, "\n"))
	}
	return nil
}
```
Add `Repo.IsWorkingCommit(sha)` over a small exported `Engine.IsWorkingHead(sha) (bool,error)` (wrap the existing unexported `isWorkingHead`). `shortSha` = first 8 chars. e2e `blame_e2e_test.go`: commit two lines, `cairn blame f.txt` stdout has both authors; edit a line (no commit) then blame → that line shows `(working)`.

- [ ] **Step 5: verify + commit**

`go build ./...` + `go test ./internal/change/ ./internal/worktree/ ./cmd/cairn/ -run 'Blame' -v` + `go test ./...` + vet + `GOOS=windows go build ./...`. Commit:
```
git add internal/change/blame.go internal/change/blame_test.go internal/worktree/worktree.go cmd/cairn/main.go cmd/cairn/blame_e2e_test.go
git commit -m "feat(change,worktree,cmd): cairn blame (per-line provenance via go-git, mapped to change-ids)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 2: stash storage + engine (push/list/apply/drop)

**Files:** modify `internal/change/schema.sql`; create `internal/change/stash.go`, `internal/change/stash_test.go`.

- [ ] **Step 1: schema**

Add to `schema.sql`:
```sql
CREATE TABLE IF NOT EXISTS stash (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  line_id TEXT NOT NULL,
  branch TEXT NOT NULL,
  commit_sha TEXT NOT NULL,
  base_sha TEXT NOT NULL,
  message TEXT NOT NULL,
  created_at TEXT NOT NULL
);
```
(CREATE IF NOT EXISTS is idempotent — existing repos get it on next Open. No ALTER needed.)

- [ ] **Step 2: engine test (write first)**

`stash_test.go`: open change with a working delta {a:v1 over empty parent}; `StashPush(id, "wip")` → returns an id, a `stash` row exists, and the working change is now CLEAN (head tree == parent tree). `StashList()` shows the entry. `StashApply(id, top, drop=true)` → the working change's head tree == the stashed tree again (v1 restored), the row is gone. `StashPush` on a clean change → "nothing to stash" error. `StashApply` onto a DIRTY working change → refused.

- [ ] **Step 3: implement `internal/change/stash.go`**

```go
type StashEntry struct {
	ID        int64
	Branch    string
	Message   string
	CommitSha string
	CreatedAt string
}

// StashPush shelves the working change's delta and resets the change to its parent.
func (e *Engine) StashPush(changeID, message string) (int64, error)
// StashList returns the stack newest-first.
func (e *Engine) StashList() ([]StashEntry, error)
// StashApply re-applies a stash entry's tree onto the (clean) working change.
// stashID==0 means the top entry. drop deletes the row after applying.
func (e *Engine) StashApply(changeID string, stashID int64, drop bool) error
// StashDrop deletes a stash entry (0 = top).
func (e *Engine) StashDrop(stashID int64) error
```
**StashPush logic:** `ch := GetChange(changeID)`; if `ch.HeadCommit == ""` → "nothing to stash". `parent := firstParent(ch.HeadCommit)`; `headTree := treeHashOf(ch.HeadCommit)`; `parentTree := treeHashOf(parent)` (empty-tree hash if parent==""); if `headTree == parentTree` → "nothing to stash" (clean). `line := lineByID(ch.LineID)`. In ONE tx: INSERT the stash row `{line_id, branch: line.Name, commit_sha: ch.HeadCommit, base_sha: parent, message, created_at}`; **reset working to parent**: `reset := writeCommit(parentTree, ch.ID, "(working)", parentsSlice(parent))` (git write OUTSIDE tx); `UPDATE change SET head_commit=reset` + advance line tip; record an op. Return `lastInsertId`.
**StashApply logic:** load the stash row (top if 0); **dirty check**: `ch := GetChange(changeID)`; `parent := firstParent(ch.HeadCommit)`; if `treeHashOf(ch.HeadCommit) != treeHashOf(parent)` → "working change has un-sealed work; commit or stash it before popping". `stashedTree := treeHashOf(row.commit_sha)`; `applied := writeCommit(stashedTree, ch.ID, "(working)", parentsSlice(firstParent(ch.HeadCommit)))`; tx: `UPDATE change SET head_commit=applied` + advance tip; if `drop` `DELETE FROM stash WHERE id=row.id`; record op. 
Helpers: reuse `treeHashOf` (read `CommitObject(sha).TreeHash` — add a small `e.treeHashOf(sha)(string,error)` if not present; empty-tree for sha==""), `firstParent`, `writeCommit`, `lineByID`. Confirm an empty-tree hash helper (`writeTreeRefs(nil)` gives it).

- [ ] **Step 4: verify + commit**

`go test ./internal/change/ -run Stash -v` + `go test ./...` + vet + cross-compile. Commit:
```
git add internal/change/schema.sql internal/change/stash.go internal/change/stash_test.go
git commit -m "feat(change): stash engine — push/list/apply/drop (shelve working delta, reset to parent)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 3: stash worktree + CLI

**Files:** modify `internal/worktree/worktree.go`, `cmd/cairn/main.go`; create `cmd/cairn/stash_e2e_test.go`.

- [ ] **Step 1: worktree (test first)**

`internal/worktree/stash_test.go` or fold into the e2e. Methods:
```go
func (r *Repo) Stash(branch, message string) error {
	entry, ok := r.st.Expressed[branch]; if !ok { return fmt.Errorf("worktree.Stash: branch %q is not expressed", branch) }
	if err := r.syncBranch(branch, entry); err != nil { return err }
	if _, err := r.eng.StashPush(entry.ChangeID, message); err != nil { return fmt.Errorf("worktree.Stash: %w", err) }
	return r.rematerialize(branch, entry) // Materialize to the new (clean) working tip
}
func (r *Repo) StashPop(branch string) error {
	entry, ok := r.st.Expressed[branch]; if !ok { return ... }
	if err := r.syncBranch(branch, entry); err != nil { return err }
	if err := r.eng.StashApply(entry.ChangeID, 0, true); err != nil { return fmt.Errorf("worktree.StashPop: %w", err) }
	return r.rematerialize(branch, entry)
}
func (r *Repo) StashList() ([]change.StashEntry, error) { return r.eng.StashList() }
func (r *Repo) StashDrop(id int64) error { return r.eng.StashDrop(id) }
```
(`rematerialize(branch, entry)` = resolve line tip + `Materialize(...)` to `filepath.Join(r.root, entry.Path)` — extract the existing re-materialize snippet used by Commit/Undo into a helper, or inline.)

- [ ] **Step 2: CLI**

Dispatch `case "stash": return cmdStash(rest)`. `cmdStash` parses a subcommand: bare/`push` (with `-m`), `pop`, `list`, `drop [id]`. All via `openRepoSynced`. Usage lines:
```
  stash [-m msg]           shelve the working change; reset the folder to the sealed state
  stash pop                restore the most recent stash
  stash list               list the stash stack
  stash drop [id]          discard a stash (default: most recent)
```
`stash` → `r.Stash(branch, *msg)` + stderr note (or "nothing to stash" surfaced from the engine). `stash pop` → `r.StashPop(branch)`. `stash list` → print `<id>  <branch>  <date>  <message>` to stdout. `stash drop` → `r.StashDrop(id)` (parse optional id; 0 = top).

- [ ] **Step 3: e2e `cmd/cairn/stash_e2e_test.go`**

`skipOnWindows`. init; write `a.txt`=wip into the root folder (no commit); `cairn stash --repo dir -m "wip"` → `cairn status` shows clean (folder reverted to sealed/empty), `a.txt` gone from disk; `cairn stash list` shows "wip"; `cairn stash pop --repo dir` → `a.txt`=wip back on disk, `cairn status` shows it; `cairn stash list` now empty. Also: `cairn stash` on a clean folder → error "nothing to stash".

- [ ] **Step 4: verify + commit**

`go test ./...` + vet + cross-compile. Commit:
```
git add internal/worktree/worktree.go cmd/cairn/main.go cmd/cairn/stash_e2e_test.go internal/worktree/stash_test.go
git commit -m "feat(worktree,cmd): cairn stash/pop/list/drop (shelve-and-restore the working delta)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 4: final gate + usage

- [ ] Update `usage` (blame + stash present), full `go test ./...` + `go vet ./...` + cross-compile darwin/windows. Manual smoke: blame a file; stash → status clean → pop → status dirty again. Commit any usage tweak.

## Notes
- blame is pure wiring of `git.Blame` — the only cairn-specific bit is mapping `line.Hash`→change-id and the `(working)` label for un-sealed lines.
- stash reuses the WCC amend mechanics: "reset to parent" and "apply tree" are both just amends of the working commit to a chosen tree. Keep the catalogue writes atomic.
- v1 stash refuses pop onto a dirty working change (no auto-merge) — pin and test it.
- DRY, YAGNI, TDD. Each task green before the next.
