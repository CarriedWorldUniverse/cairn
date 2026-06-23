# cairn Phase 2 — Slice C (part 2): push ("cairn-native locally, git-translated on push")

**Status:** draft for approval · 2026-06-23
**Goal:** `cairn push` — publish local cairn work to a git remote, **completing the round-trip** (clone → work → push back). Plus `cairn remote add`/`remote`.
**Builds on:** the engine `Export()` (cairn lines/tags → git refs) + the importer's remote helpers + `internal/worktree`/`cmd/cairn` (all on main).

## Guiding principle (operator)

**The cairn change-graph (lines/changes) is the local source of truth. git is purely the *wire language* for remotes.** Locally you always work in cairn branches; the git refs are a derived projection.

**Push behavior is remote-type-dependent:**
- **cairn → cairn = cairn** (full fidelity): pushing to another cairn remote keeps the cairn model — the change-graph rides along (`refs/cairn/*` + change-id/lineage trailers) so the receiving cairn reconstructs your exact lines/changes/tags.
- **cairn → git = cairn *converted* to git**: pushing to a plain git host translates down to ordinary branches/commits/tags (`Export()` is that translator); cairn-specific structure git can't hold is dropped (or rides harmlessly in commit trailers).

**This slice implements the cairn→git path** (the self-contained "world is git" round-trip). It records a per-remote **type** (`cairn`|`git`, default `git`) as the seam; **cairn→cairn full fidelity is deferred** — it's a paired feature needing both the push of `refs/cairn/*` *and* an import that reconstructs the change-graph from cairn metadata (C-clone currently imports as plain git), so it gets its own slice.

---

## 1. Scope

**IN:**
1. **Engine `PushToRemote(remoteName string, force bool) error`** (`internal/change/push.go`): call `Export()` (translate cairn → git refs, prune stale), then `e.git.Remote(remoteName).Push` with refspecs **`refs/heads/*:refs/heads/*`** and **`refs/tags/*:refs/tags/*`** only (NOT `refs/cairn/*`); `Force` set from the flag. Tolerate `git.NoErrAlreadyUpToDate`. On a non-fast-forward rejection, return a clear error.
2. **Engine `AddRemote(name, url, kind string) error`** + **`ListRemotes() ([]RemoteInfo, error)`** (name, URL, kind) — go-git `CreateRemote`/`Remotes` wrappers + a small per-remote **kind** record (`cairn`|`git`, default `git`) stored in the catalogue or a `.cairn` config (the type seam). v1 push reads it but only the `git` path is implemented; a `cairn`-kind remote pushes the same standard refs for now with a "cairn-fidelity push not yet implemented; pushing as git" notice.
3. **CLI**: `cairn remote add <name> <url>`, `cairn remote` (list), `cairn push [remote] [--force]` (default remote `origin`).
4. Tests: push to a `file://` bare remote → plain go-git clone it → assert branches/tags + content; the **full round-trip** (clone → express/commit/fold → push → re-clone → new work present); non-ff rejection + `--force` success.

**OUT (later / deferred):**
- **C-sync** (commit-time origin sync, `cairn pull`, fetch-and-rebase on non-ff) — the next sub-slice; v1 push just *reports* a non-ff and offers `--force`.
- **cairn→cairn full fidelity** — pushing `refs/cairn/*` + change-graph reconstruction on import. A paired push+import follow-up slice; v1's `git` path pushes standard refs only (open-change WIP stays local).
- private-remote auth (tokens/SSH keys) — `file://` + public `https` work via go-git's default transport; credential plumbing is a noted follow-up.
- per-branch push selection beyond an optional single-branch arg; push hooks; signed pushes.

---

## 2. Engine push (`PushToRemote`)

```
PushToRemote(remoteName, force) error:
  Export()                                  // translate cairn change-graph → git refs (+prune)
  rem, err := e.git.Remote(remoteName)      // ErrRemoteNotFound → wrapped "no such remote"
  refspecs := ["refs/heads/*:refs/heads/*", "refs/tags/*:refs/tags/*"]
  if force: refspecs = ["+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"]
  err = rem.Push(&git.PushOptions{RemoteName: remoteName, RefSpecs: refspecs, Force: force})
  if err == NoErrAlreadyUpToDate: return nil
  if err is a non-fast-forward rejection: return a clear "remote diverged; sync first or --force" error
  return wrapped err
```
- **Why Export first:** the local git refs are a derived projection; `Export()` re-derives them from the current change-graph (the truth) and prunes folded/abandoned refs, so push always reflects the live cairn state, not stale refs.
- **Optional single-branch push** (`PushToRemoteBranch(remoteName, branch, force)`): refspecs `refs/heads/<branch>:refs/heads/<branch>` + tags. (The CLI exposes this when a branch arg is given.)
- `AddRemote(name,url)`: `CreateRemote`; if the name exists with a different URL, delete+recreate (mirrors `fetchRemote`'s URL-update). `ListRemotes()`: iterate `e.git.Remotes()`.

---

## 3. CLI

- `cairn remote add <name> <url>` → `AddRemote`. `cairn remote` (no args) → list `name → url`.
- `cairn push [remote] [--force]`: remote defaults to `origin`; `--force` for a force push. Open the repo (`worktree.Open` — needs the repo's engine; add a thin `Repo.Push(remote, force)` that calls `eng.PushToRemote`). Print `pushed → <remote>` or the non-ff error. (`cairn push <remote> <branch>` optional: push one line.)
- All map engine errors to clear messages; non-ff → "remote has diverged; run `cairn sync` (coming) or push --force".

---

## 4. Testing

- **Engine (`push_test.go`):** create a `file://` **bare** remote (go-git `PlainInit(bare)`); open a cairn engine, seed `main` + a second line folded in + a tag; `AddRemote("origin", bareURL)`; `PushToRemote("origin", false)`; then **plain go-git clone** the bare remote into a fresh dir and assert `refs/heads/main` (+ the line) and the tag resolve with the expected tree content. Assert `refs/cairn/*` were NOT pushed.
- **Non-ff:** push; then advance the bare remote's `main` independently (a direct commit); `PushToRemote("origin", false)` → returns the non-ff error; `PushToRemote("origin", true)` → succeeds.
- **Round-trip (worktree/e2e):** `worktree.Clone(srcBare)` → `Express`/edit/`Commit`/`Fold` a branch → `Repo.Push("origin", false)` → `worktree.Clone` the same remote into a *new* dir → assert the folded work is present on the default branch. The headline "the world is git" proof, through cairn end to end.
- Full gate: `go test ./... && go vet ./... && go build ./...` + `GOOS=darwin/windows go build ./...`. Local-git-fixture tests `skipOnWindows` (same go-git-local-transport quirk as C-clone).

**DoD:** `cairn push` translates the local cairn change-graph into git and publishes branches + tags to a git remote; the full clone→work→push→re-clone round-trip works; non-ff is reported with a `--force` escape; `refs/cairn/*` never leave; CI green cross-platform.

---

## 5. Build sequence (for the plan)

1. **Engine `AddRemote`/`ListRemotes` + `PushToRemote` (+ branch variant)** — `internal/change/push.go`; TDD: push to a file:// bare → clone-back assert; non-ff + force.
2. **`worktree.Repo.Push` + `cairn remote`/`push` CLI + round-trip e2e** — TDD: the clone→work→push→re-clone proof; `skipOnWindows` on local-fixture tests.

---

## 6. Open questions (small, non-blocking)

- **Non-ff detection:** go-git surfaces a non-fast-forward as a specific error/string; match on it to give the friendly message, else wrap generically. Pin in step 1 (read go-git's push error).
- **`cairn push` with no `origin`** (a local `cairn init` repo never `remote add`'d): clear "no remote 'origin'; add one with `cairn remote add`". Pin in step 2.
- **default push = all open lines + tags** vs current-branch-only: v1 default = all open lines + tags (publish the whole live state); optional single-branch arg. Confirm in step 1.
- **tags that point at unpushed commits:** Export only emits tags from the catalogue; their commits are pushed as part of `refs/heads/*` history if reachable; a tag on an orphan commit is an edge — acceptable for v1.
