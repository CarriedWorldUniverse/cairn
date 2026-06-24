# cairn — stash + blame

**Status:** draft for approval · 2026-06-24
**Goal:** two git-parity power-tools, in cairn's WCC model. **blame** = per-line "who/when/why" (mostly wiring go-git's blame). **stash** = a minimal shelve stack — set your in-progress working delta aside to get a clean folder at the sealed parent, then bring it back. (Operator left the stash shape to judgment; scoped minimal because WCC's always-save + multiple expressed folders already cover git-stash's other uses.)

---

## A. blame (`cairn blame <path> [branch]`)

go-git provides `git.Blame(commit, path) (*BlameResult, error)` with `[]object.Line{AuthorName, Date, Hash, Text}`. We wrap it and map each line's commit to its cairn change-id.

- **Engine** (`internal/change/blame.go`): `Blame(commit, path string) ([]BlameLine, error)` where `BlameLine{Commit, ChangeID, Author string; When time.Time; Text string}`. Resolve `commit`→`object.Commit` via `e.git.CommitObject`, call `git.Blame`, and for each line set `ChangeID = ChangeIDOf(line.Hash)` (the Change-Id trailer; "" if none) so blame speaks cairn identities, plus the short commit sha as a fallback.
- **Worktree:** `Repo.Blame(branch, path) ([]change.BlameLine, error)` — resolve the line tip (the WORKING commit, so blame reflects current synced content, including un-sealed lines attributed to the working change), call `eng.Blame(tip, path)`.
- **CLI:** `cairn blame <path> [branch]` (default branch = root) — print per line: `<short-sha-or-change> <author> <YYYY-MM-DD>  <line text>`. Lines from the un-sealed working change show `(working)` in place of a sha. Stdout. Command runs via `openRepoSynced` so the tip reflects live edits.
- **DoD:** `cairn blame f.txt` attributes each line to the commit/author/date that last touched it; a freshly-edited (un-sealed) line shows `(working)`; a missing path errors cleanly.

---

## B. stash — minimal shelve stack

The working change has a delta vs its parent (the sealed base). **stash** captures that delta as a stash entry, resets the working change to its parent (clean folder), and **pop** re-applies it.

### Storage
A `stash` catalogue table (global stack): `id INTEGER PRIMARY KEY AUTOINCREMENT, line_id TEXT, branch TEXT, commit_sha TEXT, base_sha TEXT, message TEXT, created_at TEXT`. The stack is ordered `id DESC` (newest = top). `commit_sha` = the shelved working commit (already in the object store; cairn doesn't GC, so it persists). Add to `schema.sql` + an idempotent migration (mirror the WCC `sealed` migration: `CREATE TABLE IF NOT EXISTS stash (...)` is itself idempotent, so no ALTER needed).

### Engine (`internal/change/stash.go`)
- `StashPush(changeID, message string) (stashID int64, err error)`: read `ch := GetChange(changeID)`; if `ch.HeadCommit == ""` or the working delta is empty (head tree == parent tree) → return a clear "nothing to stash" error. `parent := firstParent(ch.HeadCommit)`; insert a stash row `{line_id: ch.LineID, branch: <line name>, commit_sha: ch.HeadCommit, base_sha: parent, message, created_at}`; then **reset the working change to its parent**: amend the working commit to the parent's tree (`writeCommit(parentTree, ch.ID, "(working)", [parent])`), update change head + line tip. (One tx for the stash insert + the reset's catalogue writes.) Records an op.
- `StashList() ([]StashEntry, error)`: the stack, newest first, with `{ID, Branch, Message, CreatedAt, CommitSha}`.
- `StashApply(changeID string, stashID int64, drop bool) error`: load the stash row (default = top); read its `commit_sha`'s tree (the shelved content); **apply onto the working change**: amend the working commit to the stashed tree (`writeCommit(stashedTree, changeID, "(working)", [currentParent])`), update head + tip. If `drop`, delete the stash row. (One tx.) Records an op. **v1 requires the target working change to be clean** (delta empty) — else return "working change has un-sealed work; commit or stash it before popping" (no auto-merge in v1).
- `StashDrop(stashID int64) error`: delete a stash row (default top).

### Worktree
- `Repo.Stash(branch, message string) error`: `syncBranch` (capture edits) → `eng.StashPush(changeID, message)` → re-materialize the folder to the new (clean) working tip.
- `Repo.StashPop(branch string) error`: `eng.StashApply(changeID, 0 (top), drop=true)` → re-materialize the folder to the new working tip (now holding the shelved content). `Repo.StashList()` / `Repo.StashDrop()` pass-throughs.

### CLI
- `cairn stash [-m msg]` — shelve the current working delta; stderr note `shelved <n> change(s); folder reset to <branch>'s sealed state`. Refuse with "nothing to stash" if clean.
- `cairn stash pop` — re-apply + drop the top entry; stderr note.
- `cairn stash list` — the stack: `<id>  <branch>  <date>  <message>` (stdout).
- `cairn stash drop [id]` — discard the top (or a given id).
All via `openRepoSynced` (so the working change reflects live edits before stashing). Update `usage`.

### DoD
- `cairn stash` on a dirty working change → folder reverts to the sealed parent content, `status` shows clean, the delta is recoverable; `cairn stash list` shows the entry; `cairn stash pop` restores the content and `status` shows it again; `cairn stash drop` discards. Stashing a clean change → "nothing to stash". Pop onto a dirty change → refused (v1).

---

## Out of scope (later)
- **stash merge-on-pop** (apply onto a dirty working change via 3-way merge) — v1 requires clean.
- **partial stash** (specific paths), **stash branch**, **apply-without-drop as a flag** (we have list/drop; `apply` keeping the entry is a trivial later add).
- blame: `-L <range>`, follow-renames, ignore-revs.
- The pre-existing `ResolveConflict`-appends and pull+FF-working-head edges (noted in WCC) remain follow-ups.

## Testing
- blame: engine + e2e — 3 lines committed across 2 changes with distinct authors/messages → blame attributes each line correctly; an un-sealed edit shows `(working)`.
- stash: engine + e2e — push/list/pop/drop round-trip; reset-to-parent leaves a clean folder; "nothing to stash" on clean; refuse pop onto dirty; the shelved commit persists (referenced by the stash row).
- Migration: a legacy db gains the `stash` table on open (CREATE IF NOT EXISTS via the schema; the migrate() step covers any future ALTERs).
- Full gate + cross-compile; `skipOnWindows` on local-fixture/e2e; all prior phases unaffected.
