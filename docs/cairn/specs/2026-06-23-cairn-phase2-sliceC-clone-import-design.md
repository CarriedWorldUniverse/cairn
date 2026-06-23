# cairn Phase 2 — Slice C (part 1): clone / import ("the world is git")

**Status:** draft for approval · 2026-06-23
**Goal:** `cairn clone <git-url>` — fetch a standard git repo into the cairn engine and **import** it into the change-graph (branches → lines, commits stay as git objects, tags → tags), then express the default branch as a working folder. After this you can work an existing GitHub/GitLab repo with cairn's superpowers (Slices A+B). Push and origin-sync are the next sub-slices.
**Builds on:** the convergence engine `internal/change` + the working-copy layer `internal/worktree` + `cmd/cairn` (all on main). go-git provides clone/fetch.

**Framing:** a "cairn server" *is* a git remote (cairn-server speaks git over SSH/HTTP), so this targets **any git remote** — GitHub, GitLab, or a cairn server — through one mechanism.

---

## 1. Scope

**IN (Slice C-clone):**
1. **Engine import** (`internal/change/importer.go`, package `change` — needs both `e.git` and `e.db`): `ImportFromRemote(url string) (defaultBranch string, error)`:
   - add remote `origin = url`; **fetch** `+refs/heads/*:refs/heads/*` and `+refs/tags/*:refs/tags/*` into the bare store.
   - detect the remote's **default branch** (via `remote.List()` → the `HEAD` symref target; fall back to `main`, else the sole branch).
   - **map default branch → the root line**: rename the engine's root line to the default branch's name and set its `tip_commit` to that commit.
   - **map other branches → child lines off the root** (flat: `parent_line = root`, `tip = base = branch commit`).
   - **map tags → tag rows**.
   - return the default branch name.
2. **`worktree.Clone(url, dir, author) (*Repo, error)`**: `change.Open(dir/.cairn)` → `ImportFromRemote(url)` → build `Repo` → `Express(defaultBranch)` (materialize the default branch folder + record `wc.json`).
3. **`cairn clone <git-url> [dir]`** CLI subcommand (default dir = the repo name from the URL) → `worktree.Clone` → print the expressed path.
4. Tests: import from a local `file://` bare git repo (built in-test with go-git) — assert lines/tips/tags, the default branch expressed with files on disk, and that `express`/`commit`/`fold` then work on an imported line.

**OUT (later):**
- **C-push** (`cairn push` / `remote add`) and **C-sync** (commit-time origin sync, `pull`) — separate sub-slices.
- **cairn-metadata round-trip** — honoring a remote's existing `refs/cairn/*` + `Change-Id` trailers (cairn↔cairn fidelity). v1 imports as plain git.
- **merge-base parentage** — v1 maps non-default branches flat off root; inferring true parent via merge-base is deferred.
- shallow clone, auth (tokens/SSH keys beyond what go-git's default transport gives for `file://`/`https://` public), submodules, LFS.

---

## 2. Engine import (`ImportFromRemote`)

The novel capability: git → change-graph. Lives in `internal/change` because it needs the bare repo (`e.git`) and the catalogue (`e.db`).

```
ImportFromRemote(url) (defaultBranch string, err error):
  1. remote = e.git.CreateRemote({Name:"origin", URLs:[url]})   (or reuse if exists)
  2. remote.Fetch({RefSpecs: ["+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"], Tags: AllTags})
        (NoErrAlreadyUpToDate tolerated)
  3. default = detectDefault(remote)   // remote.List() → HEAD symref target short name; else "main"; else sole head
  4. tx:
       branches = enumerate refs/heads/* in the store (name → commit sha)
       // root line
       renameRootLine(default); setLineTip(rootID, branches[default])
       for name, sha in branches where name != default:
           upsertChildLine(name, parent=rootID, tip=sha, base=sha)
       for tagName, sha in refs/tags/*:
           upsertTag(tagName, sha)
     commit tx
  5. return default
```

New engine helpers (small, package-internal, all in `importer.go` except trivial catalogue ops which may live with their siblings):
- `renameRootLine(name)` — `UPDATE line SET name=? WHERE parent_line IS NULL` (atomic; the root is unique).
- `setLineTip(lineID, commit)` — `UPDATE line SET tip_commit=?, base_commit=? , updated_at=?`.
- `upsertChildLine(name, parentID, tip, base)` — insert a line (or update if a same-name line exists).
- The fetch/list/detect helpers wrap go-git.

**Invariants preserved:** the root line stays the unique `parent_line IS NULL` row; imported commits are real objects reachable from line tips; no `change` rows are created on import (history = git ancestry; new work creates a change via `express`+`commit`). `Export()` (already present) round-trips these lines/tags back to refs.

---

## 3. worktree.Clone + CLI

- `worktree.Clone(url, dir, author) (*Repo, error)`:
  1. `os.MkdirAll(dir)`; `change.Open(dir/.cairn)` (engine + root line).
  2. `def, err := eng.ImportFromRemote(url)`.
  3. `r := &Repo{root: dir, author: author, eng: eng, st: emptyState, stPath: dir/.cairn/wc.json}`.
  4. `r.Express(def, "")` — but `def` line already exists (it's the renamed root), so `Express` takes the existing-line path: create an open change on it + materialize its tip into `dir/<def>/` + record in `wc.json`. (Express already handles "line exists" — confirm/extend so it works for the root line under a non-"main" name.)
  5. `r.save()`; return `r`.
- `cairn clone <git-url> [dir]`: `dir` defaults to the URL's last path component minus `.git`. Calls `worktree.Clone`. `--author` as elsewhere. Prints `cloned <url> → <dir>/<default>`.

**`Express` adjustment:** today `Express("main", "")` special-cases the root by name `change.RootLineName`. After import the root may be named e.g. `master` or `trunk`. So `Express` must treat **any line that already exists** (root or not) by fetching it and creating a change + materializing — not only the literal "main". (It already has a `LineByName` path for existing non-root lines; ensure the root-by-its-actual-name resolves through that path. The `branch == RootLineName` special-case should become "line already exists → use it".)

---

## 4. Testing

- **Engine (`importer_test.go`):** build a `file://` bare git repo in-test (go-git `PlainInit(bare)` + write commits on `main` and a `feature` branch + a tag). `change.Open` a fresh engine; `ImportFromRemote("file://…")`; assert:
  - returns `main` (the default);
  - root line is named `main`, tip = main's commit; a `feature` line exists (parent = root, tip = feature's commit);
  - the tag row exists at the right commit;
  - `eng.Files(rootTip)` returns the repo's files (objects fetched correctly).
  - a second `ImportFromRemote` (idempotent re-fetch) doesn't duplicate lines.
  - a repo whose default branch is `master` → root line renamed to `master`.
- **worktree/CLI (`clone_test.go` / e2e):** `worktree.Clone(file://bare, dir, "t")` → assert `dir/main/` exists with the files; then `r.Express("feature","")` + edit + `r.Commit("feature","")` + `r.Fold("feature")` works (imported branch is fully usable). A CLI-level `run([]string{"clone", url, dir})` smoke test.
- Full gate: `go test ./... && go vet ./... && go build ./...` + `GOOS=darwin/windows go build ./...`.

**DoD:** `cairn clone <git-url>` imports a standard git repo into the change-graph and expresses the default branch as a working folder; imported branches/tags are real cairn lines/tags; you can immediately `express`/`commit`/`fold` them; round-trips back out via the existing `Export()`. CI green cross-platform.

---

## 5. Build sequence (for the plan)

1. **Engine fetch + ref enumeration** — `fetchRemote(url)` (add origin, fetch heads+tags) + `detectDefault` + helpers to list fetched branches/tags. TDD vs a local bare repo.
2. **Engine import mapping** — `ImportFromRemote` ties fetch + maps to lines/tags (rename root, child lines, tags) in one tx; idempotent re-import. TDD.
3. **`worktree.Clone` + `Express` root-rename adjustment + `cairn clone` CLI + e2e.** TDD.

---

## 6. Open questions (small, non-blocking)

- **default-branch detection when the remote gives no HEAD symref** (some bare repos): fall back to `main` if present, else the single branch, else error "cannot determine default branch". Pin in step 1.
- **branch name → line name** with slashes (e.g. `feature/x`): stored as the line name (engine treats names opaquely); the export already maps to `refs/heads/feature/x`. The Slice-A flat-folder note applies if expressed. Fine for import.
- **`origin` already exists** (re-clone into the same dir): reuse the remote, re-fetch (idempotent). Pin in step 1.
- **non-default branch base_commit**: v1 sets `base = tip` (treated as already-adopted); the first `commit` on it merges-forward against the (current) root tip normally. Confirm in step 2.
