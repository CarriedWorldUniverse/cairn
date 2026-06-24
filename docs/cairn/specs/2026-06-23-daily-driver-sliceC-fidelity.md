# cairn daily-driver — Slice C: working-copy fidelity

**Status:** draft · 2026-06-23 · from the daily-driver audit (dimension 3)
**Goal:** stop cairn from committing junk, corrupting files, and destroying the working copy. The `map[string][]byte` model carries no file metadata and `Scan` walks everything — this slice adds an ignore stage, makes `Materialize` crash-safe, protects binary files in merges, and carries per-entry mode/kind (exec bit + symlinks).

Four tasks, sequenced so the riskiest (the model change) is last and isolated.

## C1 — ignore files (audit P0 #1)
`Scan` walks **every** file (commits `node_modules`/`.env`/build output; descends a developer's nested `.git`).
- In `internal/worktree/fs.go` `Scan`, apply gitignore semantics using go-git's built-in matcher (`github.com/go-git/go-git/v5/plumbing/format/gitignore` — **no new dependency**): parse patterns from `.gitignore` and `.cairnignore` at the scan root (and honor nested ignore files if cheap; root-level is the minimum), plus an **unconditional skip** of any `.git/` and `.cairn/` directory. Return `filepath.SkipDir` for ignored directories so subtrees aren't descended.
- Keep `Scan`'s return type `map[string][]byte` for C1 (modes come in C4).
- DoD: a folder containing `node_modules/x`, `.env`, and a `.gitignore` listing them → `cairn commit`/`status` ignore them; `.git/`/`.cairn/` never committed.

## C2 — atomic Materialize (audit P1 #5)
`Materialize` does `os.RemoveAll(dir)` then rebuilds file-by-file — a crash/Ctrl-C mid-rebuild leaves a truncated working copy.
- Rebuild into a sibling temp dir (`dir + ".cairn-tmp-<n>"` under the same parent) and `os.Rename` it over `dir` (atomic swap on the same filesystem). On any error, remove the temp dir and leave the original intact.
- Preserve the existing blob-cache + reflink behavior inside the temp build.
- DoD: a Materialize that fails partway leaves the original `dir` untouched; success swaps atomically; existing Materialize/cache tests still pass.

## C3 — binary-file conflict safety (audit P1 #4)
`mergeTrees`/`splitLines` line-merge **every** path and inject text conflict markers — corrupting binary files on a genuine 3-way divergence.
- In `internal/change/merge.go`, before diff3 on a path, detect binary (NUL byte in the first ~8KB of any of base/ours/theirs — reuse `isBinary` from `filediff.go`). For a binary path that diverges on both sides, **skip diff3**: record it as a whole-file conflict (keep one side verbatim — `ours`/local — and mark the change `has_conflict`), never emit text markers into the bytes.
- Text paths are unchanged. The equal-content short-circuit is unchanged.
- DoD: same binary asset modified on both sides → a recorded conflict, the on-disk bytes are one side verbatim (valid binary), no `<<<<<<<` markers inside it.

## C4 — per-entry mode/kind: executable bit + symlinks (audit P0/P1 #2,#3)
The model carries no mode, so the exec bit is lost (`buildTree` hardcodes `filemode.Regular`; Materialize writes `0o644`) and symlinks are dereferenced into regular files. Carry mode **alongside** content (a sparse side-map, not a full struct rewrite — keeps merge/diff/Files content-based and existing `Engine.Commit` callers working via `nil`).
- `internal/change`: `type EntryMode int` (`ModeRegular`/`ModeExecutable`/`ModeSymlink`); a sparse `map[string]EntryMode` (absent ⇒ regular).
- **Scan** (`fs.go`): return `(map[string][]byte, map[string]EntryMode)`. Use `Lstat`/`d.Type()`: a symlink → `os.Readlink`, store the **target** as the path's bytes + `ModeSymlink` (do **not** follow it); a regular file with any `0o111` bit → `ModeExecutable`; else regular (omit from the map).
- **Engine.Commit** gains `modes map[string]EntryMode` (place it before `message`; `nil` ⇒ all regular — existing callers pass `nil`, no behavior change). Thread to `writeTree`/`buildTree`, which emit `filemode.Executable`/`filemode.Symlink`/`filemode.Regular` per entry.
- **Engine.FileModes(commit) (map[string]EntryMode, error)**: read the git tree and return the non-regular modes (Executable/Symlink) per path. `readTree`/`Files` stay content-only (a symlink's content = its target).
- **Materialize** (`fs.go`): fetch `Files` + `FileModes`; for each path — `ModeSymlink` → `os.Symlink(string(content), path)` (remove any existing first); `ModeExecutable` → write/reflink then `os.Chmod(0o755)`; regular → as today. Keep C2's atomic temp-swap.
- **worktree.Commit**: pass Scan's modes to `eng.Commit`.
- DoD: an executable `build.sh` round-trips with `+x`; a symlink `link → target` round-trips as a symlink (not a copy); a symlink to a dir / a dangling symlink commits cleanly (stored as its target string, not an error). Existing `Engine.Commit(nil)` callers + tests unaffected.

## Out of scope
Empty-dir tracking (git-equivalent, use `.gitkeep`), `.gitattributes`/CRLF, cache-hit revalidation, per-commit perf cache → Slice E or later. Remote auth = Slice D.

## Testing / DoD
Per-task DoD above. Plus: full gate green + cross-compile; `skipOnWindows` on symlink/exec/local-fixture tests (Windows symlink/exec semantics differ — gate those). All prior phases unaffected. The model change (C4) must leave every existing `Engine.Commit` caller and merge/diff/Files behavior unchanged for regular files.
