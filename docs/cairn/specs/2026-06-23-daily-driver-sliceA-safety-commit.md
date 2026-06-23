# cairn daily-driver — Slice A: safety & commit correctness

**Status:** draft · 2026-06-23 · derived from the daily-driver audit (4-dimension)
**Goal:** stop cairn from (a) silently destroying uncommitted work, (b) writing every commit as `"snapshot"`, and (c) attributing commits to a fake `<name>@cairn` email. The write-side correctness + safety foundation for the rest of the daily-driver program.

## Scope (3 tasks)

### A1 — dirty-guard destructive ops (audit P0 #4)
`Unexpress`/`Abandon`/`Fold` call `os.RemoveAll` on the branch folder with **no check**, silently destroying uncommitted edits.
- Add `force bool` to `Repo.Unexpress`, `Repo.Abandon`, `Repo.Fold`.
- **Abandon/Fold** check dirtiness **before** the engine op (FoldLine/AbandonLine removes the line, after which `isDirty` can't resolve it): if `isDirty(branch)` && `!force` → refuse with a clear error naming the branch; else proceed and call `Unexpress(branch, true)`.
- **Unexpress(force=false)** checks `isDirty(branch)` before `RemoveAll`; refuse if dirty unless force.
- `isDirty` must tolerate a missing line (line already folded/abandoned → `ErrNotFound` → return `(false, nil)`; nothing to compare, removal is safe).
- CLI: `cairn abandon <branch> [--force]`, `cairn unexpress <branch> [--force]`, `cairn fold <branch> [--force]`. Refusal message: `branch %q has uncommitted changes; commit them or pass --force to discard`.

### A2 — commit messages (audit P0 #1)
`-m` is parsed then discarded; `writeCommit` hardcodes `Message: "snapshot..."`.
- `Engine.Commit(changeID, files, message string)` gains `message`; `Repo.Commit(branch, msg)` passes it.
- `writeCommit(treeSha, changeID, message string, parents)` — the `author` param is **replaced** by `message` (identity now comes from the engine, see A3). Builds `Message = (message or "snapshot") + "\n\nChange-Id: " + changeID + "\n"`.
- Call-site defaults: change.go:114 + the merge-forward re-commit (change.go:129) → the user's `message`; `conflict.go` ResolveConflict re-commit → `"resolve conflicts"`; `sync.go` merge synthesizer → `"merge remote-tracking"`. Empty user message → `"snapshot"` (preserves current behavior for callers that don't supply one, e.g. grpcapi facade).
- Update every `Engine.Commit` caller (worktree, grpcapi facade, tests) for the new arg.

### A3 — real author identity (audit P0/P1 #8)
`writeCommit` synthesizes `Email: author + "@cairn"`; `defaultAuthor` is name-only. Git/GitHub can't attribute these commits.
- `Engine` gains `idName, idEmail string` + `SetIdentity(name, email)`. `writeCommit` builds `object.Signature` from them: `name` falls back to `"cairn"`; `email` falls back to `name + "@users.noreply.cairn"` (a clear non-routable placeholder, **not** the broken `@cairn`).
- `worktree.Open(root, author)`: after the engine opens, resolve identity and `eng.SetIdentity(name, email)`:
  - `name` = `config["user.name"]` else the passed `author`.
  - `email` = `config["user.email"]` else `$CAIRN_EMAIL` else `$GIT_AUTHOR_EMAIL` else `""`.
  - set `r.author = name` (used for the change-row author via `CreateChange`).
- No new CLI: `cairn config user.name "X"` / `cairn config user.email "y@z"` already work via the generic `config` command. Document them in usage/help.
- Merge/sync/resolve commits now also carry the configured identity (instead of `sync@cairn`), which is correct — the person running the operation authors the merge.

## Out of scope (later slices)
Inspection (`log`/`diff`/`show`/`undo`/status-diff) = Slice B; ignore/symlink/exec-bit/atomic-materialize = Slice C; remote auth = Slice D.

## Testing / DoD
- A1: e2e — edit an expressed branch folder without committing, `cairn abandon`/`unexpress`/`fold` → refused with the dirty message; `--force` discards; a clean branch folds/abandons/unexpresses fine. Unit: `isDirty` returns `(false,nil)` for a missing line.
- A2: e2e — `cairn commit main -m "fix parser"` then read the commit object's message = `"fix parser"` (not `"snapshot"`); empty `-m` → `"snapshot"`. The `Change-Id` trailer is preserved.
- A3: e2e — `cairn config user.name "Jane Dev"` + `cairn config user.email "jane@x.io"`, commit, read the commit signature = `Jane Dev <jane@x.io>`; with no config, email is `<name>@users.noreply.cairn` (not `@cairn`).
- Full gate green + cross-compile; all prior phases unaffected (`skipOnWindows` on local-fixture/e2e tests as established).
