# cairn daily-driver — Slice E: polish

**Status:** draft · 2026-06-23 · the P2 items from the audit, folded in where cheap
**Goal:** the small ergonomic/safety rough edges. No structural change.

## Scope (one coherent CLI-polish pass)

### E1 — "not a cairn repo" gate + init/clone guards (audit F5, F12)
- **Gate**: `worktree.Open` MkdirAll's `.cairn`, so running any command in an empty dir silently bootstraps a repo. In the CLI `openRepo` helper (used by every non-init/clone command), stat `<repo>/.cairn` FIRST; if absent, return `not a cairn repo (run 'cairn init' here first)`. `cmdInit`/`cmdClone` bypass `openRepo` so they still create. (Do NOT change `worktree.Open`'s create behavior — only gate at the CLI helper, so internal/test callers are unaffected.)
- **init re-init**: `cmdInit` should detect an existing `<dir>/.cairn` and print `already a cairn repo at <dir>` (to stderr) instead of `initialized...`. After a fresh init, print the expressed root branch folder so the user knows where to edit.
- **clone non-empty dest**: `cmdClone` should stat `dir`; if it exists and is non-empty, refuse with `destination <dir> already exists and is not empty` before calling `worktree.Clone`.

### E2 — output discipline (audit F6)
- Confirmation prose goes to **stderr**, leaving stdout clean/machine-parseable: `cmdInit` (`initialized...`), `cmdClone` (`cloned...`), `cmdConfig` set (`set k=v`) → `fmt.Fprintln(os.Stderr, ...)`. (`config get` keeps printing the bare value to stdout for `$(cairn config k)`.)
- `cmdExpress`'s printed new-folder path stays on stdout (it's useful output).

### E3 — usage completeness (audit F8)
- Audit the `usage` const against the dispatch switch: ensure every subcommand is listed (`diff`, `log`, `show`, `undo`, `oplog` from Slice B; `tag`, `version`, `release` from Phase 5) and note key flags (`version --release`, the `--force` on abandon/unexpress/fold, `diff <a> <b>`). Add a one-line "config keys: user.name, user.email, autosync" hint.

### E4 — friendlier file-vs-dir collision error (audit fidelity F8)
- `buildTree` returns `change.writeTree: name %q used as both file and directory` — reword to a clear user-facing message: `cannot commit: %q exists as both a file and a directory at the same level`. (Engine error string only; surfaces through `mapErr`.)

## Out of scope (explicit deferrals — optimizations, not holes)
- **Per-commit O(repo) perf** (mtime/size cache to skip unchanged files; stream blobs instead of buffering) — a real optimization but not a correctness hole; bigger change with staleness risk. Deferred as a dedicated perf pass.
- **Blob-cache hit revalidation** (verify-on-read) — low-probability corruption guard, adds I/O cost. Deferred.
- **`message` column on the change table** for fast `log` — `log` already reads from git objects correctly. Deferred optimization.
- **Annotated-tag fidelity**, **`--cairn` remote kind** activation — documented seams for later phases.

## Testing / DoD
- E1: e2e — `cairn status` (or any non-init cmd) in a fresh empty dir → errors `not a cairn repo`, and does NOT create `.cairn`; `cairn init` twice → second prints `already a cairn repo`; `cairn clone <url> <nonemptydir>` → refused. Existing init→work e2e unaffected.
- E2: e2e — `cairn init`/`clone`/`config set` write their confirmation to stderr (stdout empty for those); `config get` still emits the value on stdout.
- E3: the usage string lists every dispatch case (a test can assert each subcommand name appears in `usage`).
- E4: committing a tree with a file/dir name collision yields the reworded message.
- Full gate + cross-compile; all prior phases unaffected.
