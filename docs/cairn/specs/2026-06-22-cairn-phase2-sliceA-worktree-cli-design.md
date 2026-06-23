# cairn Phase 2 — Slice A: working-copy CLI (express/commit loop)

**Status:** draft for approval · 2026-06-22
**Goal:** a local `cairn` CLI that lets agents express branches as plain folders, edit them with normal tools, and `commit` to drive the Phase-1 convergence engine — so **two expressed branches converge with no merge step**, on the real filesystem. No CoW, no remotes (those are Slices B and C).
**Builds on:** the Phase-1 convergence engine `internal/change` (merged to main, PR #24) — `Engine`, lines, change_id, commit-triggered merge-forward, conflicts-as-data, fold/abandon, lineage, tags, export.

**Context decision (2026-06-22):** porter (the cloud-backup, read-mostly, *explicitly-not-for-git* storage product) is **not** cairn's working-copy CoW layer. The working-copy layer is a **cairn-local component**. porter stays as-is.

---

## 1. Scope

**IN (Slice A):**
1. A new `cmd/cairn` CLI (stdlib subcommand dispatch — no new deps; matches the module, which has no cobra).
2. A new `internal/worktree` package: materialize a tree to a directory, scan a directory to a files map, manage per-repo working-copy state (`wc.json`), and orchestrate express/commit/fold/abandon/status over the `internal/change` engine.
3. Commands: `init`, `express`, `unexpress`, `commit`, `fold`, `abandon`, `status`, `tree`, `ls`, `resolve`.
4. The commit flow: **scan folder → engine.Commit (merge-forward) → re-materialize folder** to the new head (so adopted-parent content + diff3 conflict markers appear on disk).
5. Tests: unit (materialize/scan round-trip, wc.json) + a CLI-level integration test of the two-branch converge demo.

**OUT (later slices/phases):**
- OverlayFS CoW space-saving (Slice B) — Slice A uses plain materialization (full file copy per expressed folder). **Slice-B design note (operator, 2026-06-22):** in the OverlayFS model, `commit` must **revert the committed files out of the per-branch upper layer** so the upper holds only *live, uncommitted* edits — committed content is then served from the (advanced) shared lower. Otherwise the upper accumulates committed content and the space-saving erodes. (Slice A's re-materialize-from-head is the plain-FS analog.)
- `clone`/`push`/`fetch`/`sync` + commit-time origin sync (Slice C).
- `version`/`release`/`private`/`disclose`/`tag` CLI (Phase 5 / privacy).
- Multi-repo, daemon/watcher (there is none — commit is the trigger).

---

## 2. On-disk layout

```
myrepo/
├── .cairn/             engine store — engine.Open("myrepo/.cairn") creates:
│   ├── objects.git/    bare go-git object store
│   ├── cairn.db        SQLite catalogue
│   └── wc.json         working-copy state (this slice owns it)
├── main/               default branch, materialized to a plain folder
└── exp/                another branch, flat sibling — NEVER nested (spec §11)
```

- The engine's `Open(dir)` already creates `dir/objects.git` + `dir/cairn.db`; the CLI points it at `myrepo/.cairn`.
- Branch folders are **flat siblings** of `.cairn`. A branch-of-a-branch is its own flat sibling (e.g. `exp-idea--idea2/`), never nested inside another branch folder.
- `wc.json` schema (owned by `internal/worktree`):
  ```json
  { "expressed": { "<branch-name>": { "path": "<abs-or-rel dir>", "change_id": "<z...>" } } }
  ```

---

## 3. Components

### `internal/worktree` (the working-copy package)
Single responsibility: bridge a directory ⇄ the engine. Pure-ish, testable without the CLI.

```
internal/worktree/
  state.go        wc.json load/save; State{Expressed map[string]Entry}; Entry{Path, ChangeID}
  materialize.go  Materialize(eng, commitSha, dir): clear dir, write the tree's files to dir
  scan.go         Scan(dir): walk dir → map[string][]byte (path→bytes), skipping nothing (folder holds only content)
  worktree.go     Repo wrapper: Open(repoRoot) (engine.Open(repoRoot/.cairn) + load wc.json);
                  Express/Unexpress/Commit/Fold/Abandon/Status/Tree/Ls/Resolve orchestration
```

- **Materialize(commitSha, dir):** read the engine tree at `commitSha` (`eng.Files(commitSha)`), remove the dir's current contents, write each path→bytes (creating subdirs). Empty/zero commit → empty dir.
- **Scan(dir):** walk `dir` recursively, return `map[string][]byte` of regular files with "/"-relative paths. (No `.git`/`.cairn` inside a branch folder — they're siblings — so nothing to skip in Slice A.)
- **wc.json** is the only state the worktree owns; the engine owns everything else.

### `cmd/cairn` (the CLI)
Thin: parse subcommand + flags, call `internal/worktree`, print results / map errors to exit codes. Stdlib `flag` per subcommand, `os.Args[1]` dispatch.

---

## 4. Commands (Slice A)

| Command | Behavior |
|---|---|
| `cairn init [dir]` | Create `dir` (default `.`); `engine.Open(dir/.cairn)` (bootstraps root line `main`); express `main` → `dir/main/`; write `wc.json`. |
| `cairn express <branch> [--from <parent>]` | If the line doesn't exist, `CreateLine(branch, parent)` (parent default = `main`); `CreateChange(line, author)` → change_id; `Materialize(lineTip, ./<branch>/)`; record in `wc.json`. If already expressed, no-op (report path). |
| `cairn unexpress <branch>` | Remove `./<branch>/`; drop from `wc.json`. Line + change stay in the store. |
| `cairn commit [<branch>] [-m <msg>]` | Resolve `<branch>` (default: infer from CWD or require arg); `Scan(./<branch>/)` → files; `engine.Commit(change_id, files)` (merge-forward); **re-`Materialize`** the new head into `./<branch>/`; print head + any conflicts (path list). Non-fatal on conflict. |
| `cairn fold <branch>` | `engine.FoldLine(line)`; `unexpress` the folded folder; if `main` (or the parent) is expressed, re-`Materialize` it to the new parent tip. |
| `cairn abandon <branch>` | `engine.AbandonLine(line)`; remove `./<branch>/`; drop from `wc.json`. |
| `cairn status [<branch>]` | Print: current branch, lineage chain (`GetLineage`), ahead (`GetLineTree`), open conflicts (`Conflicts`), and the expressed list. |
| `cairn tree` | Print `engine.GetLineTree()` as an indented forest. |
| `cairn ls` | Print expressed branches (from `wc.json`) + their change ids. |
| `cairn resolve <path> [<branch>]` | Read `./<branch>/<path>` from disk → `engine.ResolveConflict(change_id, path, bytes)`; re-`Materialize`. |

**Author identity:** Slice A reads it from `--author` flag or `$CAIRN_AUTHOR` (default a local username); Phase-2 Slice C / the server path will herald-stamp it. The engine never trusts a model-supplied author at a real boundary; for the local CLI the human/agent running it is the author.

---

## 5. The commit flow (precise)

```
cairn commit exp
  1. entry  = wc.json.Expressed["exp"]              (error if not expressed)
  2. files  = worktree.Scan(entry.Path)            (./exp/ → path→bytes)
  3. result = engine.Commit(entry.ChangeID, files)  (snapshot + merge-forward; atomic)
  4. ch     = engine.GetChange(entry.ChangeID)     (new head)
  5. worktree.Materialize(ch.HeadCommit, entry.Path) (folder now shows the converged/merged tree, incl. diff3 markers if conflicted)
  6. print head sha + result.Conflicts (paths), non-fatal
```

Re-materializing at step 5 is the key UX property: after every commit the folder reflects the line's *adopted-parent* state. If the parent advanced, the agent sees those changes pulled in; if there's an overlap, the agent sees diff3-marked files to resolve (then `cairn resolve` / edit + `commit` again).

---

## 6. Testing

- **Unit (`internal/worktree`):**
  - `Materialize`→`Scan` round-trips a nested tree (write tree to dir, scan back, equal).
  - `Materialize` clears stale files (a file removed from the tree is gone from the dir after re-materialize).
  - `wc.json` load/save round-trip; express records, unexpress drops.
- **Integration (CLI-level, the DoD demo):** drive the `worktree.Repo` API (and/or the built binary) through:
  1. `init` a temp repo (main expressed, seeded with a base file via a first commit).
  2. `express exp` (off main).
  3. Write a new file in `main/` and a different new file in `exp/`; `commit main`; `commit exp`.
  4. `fold exp`.
  5. Assert `main/` now contains BOTH new files — **no merge step invoked** — and no conflict.
  - A second integration test: overlapping edit on `exp/` after `main` advanced → `commit exp` leaves a diff3-marked file in `exp/`; `resolve` clears it; `fold` succeeds; `main/` has the resolved content.
- `go test ./... && go vet ./... && go build ./...` green; CLI integration tests use `t.TempDir()`.

**DoD:** the two-branch converge demo passes as an automated test; `cairn init/express/commit/fold/status/tree/ls/unexpress/abandon/resolve` all work end-to-end on real folders; no CoW, no network.

---

## 7. Error handling

- Map engine sentinels to clear CLI errors + non-zero exit: `change.ErrNotFound` → "no such line/change", `change.ErrHasConflict` → "resolve conflicts before folding".
- `commit` with conflicts: exit 0 (success) but print a clear "N conflicts in: …" notice (mirrors the engine's non-blocking model).
- `express` an already-expressed branch: no-op + report. `commit`/`resolve` on a non-expressed branch: clear error.
- All filesystem ops (materialize/scan) wrap errors with `worktree.<op>: %w`.

---

## 8. Build sequence (for the plan)

1. `internal/worktree` state (`wc.json` load/save) — TDD.
2. `Materialize` + `Scan` (+ round-trip / clear-stale tests) — TDD.
3. `worktree.Repo` orchestration: Open, Express, Unexpress, Commit (scan→engine→re-materialize), Fold, Abandon, Status, Tree, Ls, Resolve — TDD against a real engine in a temp dir.
4. `cmd/cairn` CLI dispatch + flags wiring each subcommand to `worktree.Repo`.
5. CLI-level integration test: the two-branch converge demo + the conflict/resolve demo.

---

## 9. Open questions (small, non-blocking)

- **Default branch inference for `commit`/`status` with no arg:** infer from CWD if it's inside an expressed folder, else require the branch arg. Pin in step 3.
- **`wc.json` path storage:** store paths relative to the repo root (portable) vs absolute. Lean relative.
- **Materialize of a conflicted tree:** the engine already writes diff3-marked blobs into the conflicted commit's tree, so `Materialize` writes them verbatim — no special handling. Confirm in step 2 tests.
- **CLI framework:** stdlib `flag` + manual subcommand dispatch (no cobra) to avoid a new dependency; revisit only if the surface grows past Slice C.
