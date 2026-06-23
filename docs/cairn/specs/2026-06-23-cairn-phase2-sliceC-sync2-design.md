# cairn Phase 2 — Slice C-sync (part 2): auto-sync ("push just works" + opt-in commit auto-sync)

**Status:** draft for approval · 2026-06-23
**Goal:** make staying-current automatic — a `push` that hits a moved remote **auto-reconciles and retries** (no manual pull dance), and an opt-in mode keeps a line current with origin **after every commit**. The convenience layer on top of the manual `pull` from part 1.
**Builds on:** `PushToRemote`/`isNonFastForward` (push), `PullFromRemote`/`Repo.Pull` (reconcile + re-materialize), `Repo.Commit` (all on main).

---

## 1. Scope

**IN:**
1. **Non-ff push auto-pull-retry** (default, in `worktree.Repo.Push`): try `eng.PushToRemote(remote, force)`. If rejected **non-fast-forward** and `!force`: run `r.Pull(remote)` (reconcile + re-materialize), then — if the pull surfaced conflicts on any line → return a clear "remote diverged; resolve conflicts then push" error (do **not** loop); else **retry the push once**. `--force` bypasses the whole dance. The CLI prints a "remote advanced — pulled & merged, retrying push" notice.
2. **Config store** (`internal/change/config.go`): a `config(key TEXT PRIMARY KEY, value TEXT)` catalogue table + `Engine.GetConfig(key) (string, bool, error)` / `SetConfig(key, value) error`. CLI `cairn config <key> [value]` (set if value given, else print).
3. **Opt-in commit-time auto-sync** (`worktree.Repo.Commit`): when `autosync` config is truthy AND an `origin` remote exists, after the normal commit (scan → engine.Commit → re-materialize), run a **best-effort** `r.Pull("origin")` — a fetch/reconcile error or unreachable origin **never blocks the commit**; print a notice and proceed. Net: post-commit the line is also current with origin. **Order matters: commit first, then pull** — pulling/re-materializing before committing would clobber uncommitted folder edits.
4. Tests per §4.

**OUT (still deferred):** cairn→cairn fidelity, private-remote auth, cross-line pull atomicity, auto-sync on a remote other than `origin`, auto-sync as a background daemon (it's commit-triggered + best-effort, no watcher — consistent with the daemon-less model).

---

## 2. Push auto-pull-retry (`Repo.Push`)

```
Repo.Push(remote, force):
  err = eng.PushToRemote(remote, force)
  if err == nil: return nil
  if force or not isNonFastForward-shaped(err): return err     // forced, or a real error
  // remote diverged → reconcile + retry
  notice("remote advanced — pulling & merging, then retrying push")
  sum, perr = r.Pull(remote)                                   // reconcile + re-materialize
  if perr != nil: return perr
  if sum has any conflicts: return error("remote diverged and merging produced conflicts; resolve, then push")
  return eng.PushToRemote(remote, force)                       // retry once
```
- Detecting a non-ff at the worktree layer: `PushToRemote` already wraps non-ff into a distinct error string ("non-fast-forward"); expose a small `change.IsNonFastForward(err) bool` (rename/export the existing `isNonFastForward`) so `Repo.Push` can branch on it without string-matching.
- The CLI `push` surface is unchanged; the auto-retry is the new default. `--force` still does a force push (no retry path).
- Retry **once** (not a loop) — if a concurrent third party advances the remote again between our pull and retry (rare), the second non-ff surfaces as an error the user re-runs. Avoids infinite loops.

## 3. Config + commit auto-sync

- **Config store:** `config(key,value)` table (`CREATE TABLE IF NOT EXISTS`). `GetConfig`→(value, found, err); `SetConfig` upserts. Truthiness for `autosync`: `"true"`/`"1"`/`"on"` (case-insensitive) = on.
- **CLI:** `cairn config <key>` prints the value (or empty); `cairn config <key> <value>` sets it. (`cairn config autosync true`.)
- **`Repo.Commit` change:** after the existing commit body (which already returns the CommitResult + re-materializes), if `eng.GetConfig("autosync")` is on and an `origin` remote exists (`ListRemotes` has it), call `r.Pull("origin")` **best-effort**: capture its error, do NOT fail the commit on it — return the original CommitResult and surface the sync outcome via a notice (the CLI prints "auto-synced with origin" / "auto-sync skipped: <reason>"). The commit's own success is independent of the sync.
- Offline / no origin / fetch error → commit succeeds, notice explains the skip.

---

## 4. Testing

- **Push auto-pull-retry (`push_retry_test.go` / e2e):** A and B clone one remote; A commits+pushes; B commits a *different* file, then **`cairn push` (no --force)** → B's push initially non-ff → auto-pull merges A's work → retry push succeeds; the remote ends with both. A second case: B and remote edit the **same** region → B's `push` auto-pulls, the merge conflicts → `push` stops with the resolve message (exit non-zero), B resolves + pushes.
- **Config:** `SetConfig`/`GetConfig` round-trip; `cairn config autosync true` then `cairn config autosync` prints `true`.
- **Commit auto-sync:** clone; set `autosync true`; advance the remote independently; `cairn commit` a local edit → after commit the line is current with origin (origin's change merged in, on disk); **offline/no-origin:** `cairn commit` with autosync on but origin unreachable (or a local `init` repo with no origin) → commit still succeeds, notice explains the skip.
- `skipOnWindows` on local-fixture tests; full + cross-compile gate; Slice-A/B/C-clone/C-push/C-sync(part1) all green.

**DoD:** `cairn push` auto-reconciles a diverged remote and retries (conflicts stop cleanly); `cairn config autosync true` makes commits keep the line current with origin best-effort, never blocking; offline commit still works; CI green cross-platform.

---

## 5. Build sequence (for the plan)

1. **Export `change.IsNonFastForward` + `Repo.Push` auto-pull-retry** + push-retry tests (e2e two-clone: retry-succeeds; conflict-stops).
2. **Config store** (`config.go` + table + GetConfig/SetConfig) + `cairn config` CLI + tests.
3. **`Repo.Commit` opt-in best-effort auto-sync** + tests (autosync-on → current; offline/no-origin → still commits).

---

## 6. Open questions (small, non-blocking)

- **non-ff detection at worktree layer:** export `IsNonFastForward(err)` from the change package (cleanest) vs re-match the string in worktree. Lean export.
- **which branch the commit auto-syncs:** pull reconciles all open lines with a remote counterpart (existing `PullFromRemote` behavior), so the committed line is covered; no per-line targeting needed.
- **autosync truthiness:** `true/1/on` = on; anything else = off. Pin in step 2.
- **notice channel:** CLI prints notices to stderr; the engine/worktree return enough info (the PullSummary + a sentinel skip reason) for the CLI to phrase them. Keep engine I/O-free.
