# cairn daily-driver — Slice B: inspection

**Status:** draft · 2026-06-23 · from the daily-driver audit
**Goal:** make cairn's state and history *visible*. Today there is no `diff`, `log`, `show`, or real `status`, and the engine's `Undo`/`OperationLog` are implemented but unreachable. Almost all of this is *wiring* existing engine primitives (`Files`, `firstParent`, `CommitObject`, `OperationLog`, `Undo`) to CLI verbs.

## Scope (4 tasks)

### B1 — diff engine + real `status` + `cairn diff`
A shared diff over the existing path→bytes model.
- `internal/change` or `internal/worktree`: a `FileDiff` model — per-path status (`Added`/`Modified`/`Deleted`) + optional unified-text hunks (use `github.com/pmezard/go-difflib/difflib.GetUnifiedDiffString`).
- `Repo.WorkingDiff(branch) ([]FileDiff, error)` = `Scan(folder)` vs `eng.Files(line.TipCommit)`.
- `Engine.DiffCommits(a, b) ([]FileDiff, error)` = `Files(a)` vs `Files(b)`; `Repo.DiffCommits` passthrough.
- **`status`**: `StatusInfo` gains `Added/Modified/Deleted []string` (from `WorkingDiff`) and a **real `Ahead` count** (replace the 0/1 flag with `LineHeight(line)` — commits since branch point). `cmdStatus` prints a `git status`-style block (changed files) + `ahead N`.
- **`cairn diff [branch]`** = working-vs-tip unified diff; **`cairn diff <a> <b>`** = commit-vs-commit. Binary files (NUL in first 8KB) → print `Binary files differ`, no hunk. Stdout = the diff.

### B2 — `cairn log` + `cairn show`
- `Engine.Log(commit string, limit int) ([]CommitInfo, error)` walking `firstParent` from a tip, each `CommitInfo{SHA, AuthorName, AuthorEmail, When, Subject}` read via `e.git.CommitObject`. `Subject` = first line of the message with the `Change-Id:` trailer stripped. `limit<=0` = unbounded (cap via the existing `describeWalkCap`).
- `Repo.Log(branch, limit)` resolves the tip via `LineByName`.
- **`cairn log [branch] [-n N]`** prints `<short-sha> <date> <author>  <subject>` per commit (default `-n 20`). Stdout.
- `Engine.Show(commit string) (CommitInfo, []FileDiff, error)` = metadata + `Files(commit)` vs `Files(firstParent(commit))` (reuse B1's diff; first commit → diff vs empty).
- **`cairn show <commit>`** prints the metadata header + full message + the diff. Stdout.

### B3 — `cairn undo` + `cairn oplog`
- `Repo.Undo() error` = `eng.Undo()` then **re-materialize every expressed line to its (restored) tip** (mirror `Pull`'s re-materialize loop; without this, disk diverges from the catalogue). `cairn undo` + a stderr note describing the Phase-1 limitation (restores line tips; does not delete lines created by the undone op).
- `Repo.OperationLog() ([]change.Operation, error)` passthrough. **`cairn oplog`** prints each op: `<op-id> <op-type> <actor> <detail>` (most-recent first or chronological — pick chronological with newest last, matching `log`-style reading). Stdout.

### B4 — ergonomics: conflict exit code, error context, `-h`
- **Conflict exit code**: `cairn commit`/`cairn pull` currently exit 0 even when conflicts were recorded (unsafe for `commit && push`). Define a sentinel `errConflicts` returned (after the existing stderr notice) by `cmdCommit`/`cmdPull` when conflicts exist; `main` maps `errors.Is(err, errConflicts)` → `os.Exit(2)` (don't re-print — the notice already printed). Other errors stay exit 1.
- **`mapErr` context**: `ErrNotFound` currently collapses to the bare `"not found"`, discarding the wrapped `parent %q`/entity context. Return the original wrapped error for `ErrNotFound` (and `ErrHasConflict` keep a clear message but don't hide context). Net: `cairn express x --from nope` says *what* was not found.
- **`-h` per subcommand**: each `cmd*` uses `flag.ContinueOnError`, so `-h` returns `flag.ErrHelp` which currently surfaces as `cairn: flag: help requested` + exit 1. Treat `errors.Is(err, flag.ErrHelp)` as success (exit 0) in `run`/`main`. Optionally set `fs.Usage` with the positional synopsis per command.

## Out of scope
Behind-vs-remote count (needs tracking-ref walk — later), `cat <path>@<rev>`, rename detection. Working-copy fidelity = Slice C; auth = Slice D.

## Testing / DoD
- B1: e2e — edit a file + add a file + delete a file in an expressed folder, `cairn status` lists them as modified/added/deleted and `ahead` shows the real commit count; `cairn diff` shows unified hunks; `cairn diff <a> <b>` between two commits; a binary file → `Binary files differ`.
- B2: e2e — three commits with distinct `-m` messages, `cairn log` shows all three subjects + authors in order; `cairn show <sha>` shows the message + changed files.
- B3: e2e — commit, `cairn undo`, the line tip returns to the prior commit and the expressed folder re-materializes to match; `cairn oplog` lists the commit + undo ops.
- B4: e2e — `cairn commit` that produces a conflict exits 2; `cairn <cmd> -h` exits 0 and prints usage; a not-found error names the entity.
- Full gate green + cross-compile; `skipOnWindows` on local-fixture/e2e tests; all prior phases unaffected.
