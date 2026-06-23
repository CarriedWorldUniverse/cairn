# cairn Phase 2 — Slice B: reflink CoW (content-addressed blob cache)

**Status:** draft for approval · 2026-06-23
**Goal:** space-saving for expressed branch folders — unchanged file *content* shares disk blocks across branches and commits, editing a file copies only the changed blocks (CoW), and a `commit` re-shares the committed content. Achieved with **reflinks from a content-addressed blob cache**, transparently swapping Slice A's plain materialization. No daemon, no mount, no privilege, no new module dependency (uses the already-present `golang.org/x/sys`).
**Builds on:** Slice A (`internal/worktree` + `cmd/cairn`, on main). The `Repo`/CLI surface is **unchanged** — only `Materialize`'s internals change.

**Context decision (2026-06-23):** the spec's earlier OverlayFS pick predated the **daemon-less, commit-triggered** model locked in Slice A. An OverlayFS *mount* needs a daemon/privileged-persistent mount to survive between per-command CLI runs — fighting that model. **Reflink CoW** needs none of it and is confirmed working on Btrfs (dMon's FS). Decision: reflink, not OverlayFS.

---

## 1. Scope

**IN:**
1. `internal/worktree/reflink_linux.go` + `internal/worktree/reflink_other.go` — `reflinkOrCopy(src, dst string) error`: Linux **FICLONE** (`golang.org/x/sys/unix.IoctlFileClone`) for a block-sharing CoW clone; **plain-copy fallback** on `ENOTSUP`/`EXDEV`/`EOPNOTSUPP` (e.g. ext4) and on non-Linux (the `_other.go` build always copies). Build-tagged so macOS/Windows CI compiles + passes.
2. A **content-addressed blob cache** at `.cairn/cache/blobs/<sha>` — each unique file content written once (keyed by sha256 of the bytes).
3. **`Materialize` revision** (in `fs.go`): for each `(path, bytes)` of the commit's tree, ensure `cache/blobs/<sha>` exists (write once, atomically), then `reflinkOrCopy(cacheBlob, dir/path)`. RemoveAll the target dir first (unchanged from Slice A). Everything else in `worktree.Repo`/CLI is untouched.
4. Tests: `reflinkOrCopy` round-trip + fallback; cache dedup; **CoW isolation**; reflink-probe-gated block-sharing assertion; the existing Slice-A converge/e2e tests still pass unchanged.

**OUT (later / deferred):**
- **Cache GC** (pruning unreferenced blobs) — noted §6; the cache only grows by unique content. A `cairn gc` is a follow-up.
- Remotes / origin-sync (Slice C).
- OverlayFS (rejected, see context).

---

## 2. The blob cache

```
myrepo/.cairn/cache/blobs/<sha256-hex>     one file per unique content
```

- Key = lowercase hex sha256 of the file's bytes. (sha256, not the git blob sha, to keep the cache self-contained in `worktree` without an engine round-trip; collision-safe.)
- Writing a cache blob is **write-temp-then-rename** (atomic; concurrent/idempotent — a second writer of the same content is harmless).
- The cache is the **shared lower**: every materialized file is a reflink (or copy) of its cache blob, so identical content anywhere reflinks to one on-disk copy.

---

## 3. `reflinkOrCopy`

`reflink_linux.go` (`//go:build linux`):
```
reflinkOrCopy(src, dst):
  open src RDONLY; create/truncate dst (0o644)
  err = unix.IoctlFileClone(int(dst.Fd()), int(src.Fd()))   // FICLONE
  if err is ENOTSUP/EOPNOTSUPP/EXDEV/EINVAL:  // unsupported fs / cross-device
      fall back: io.Copy(dst, src)
  else if err != nil: return wrapped error
```
`reflink_other.go` (`//go:build !linux`): always `io.Copy` (macOS/Windows). Both wrap errors `worktree.reflinkOrCopy: %w`.

A small `reflinkSupported(dir) bool` probe (attempt a clone of a tmp file in `dir`, see if it succeeds) is exported for tests to gate the block-sharing assertion.

---

## 4. `Materialize` (revised)

```
Materialize(eng, commitSha, dir):
  files = eng.Files(commitSha)
  RemoveAll(dir); MkdirAll(dir)
  for path, bytes in files:
     sha = sha256hex(bytes)
     cacheBlob = <repoCacheDir>/blobs/<sha>
     if !exists(cacheBlob): atomically write bytes to cacheBlob   // once
     full = dir/<path>; MkdirAll(parent)
     reflinkOrCopy(cacheBlob, full)
```
- `Materialize` needs the repo's `.cairn/cache` path. Slice A's `Materialize(eng, commitSha, dir)` signature gains the cache root: **`Materialize(eng, cacheDir, commitSha, dir)`** — and `worktree.Repo` passes `filepath.Join(r.root, ".cairn", "cache")`. (All call sites are inside `worktree`, so this is a local signature change; update `Repo.Commit`/`Express`/`Fold`/`Resolve` call sites + the `fs_test.go` helpers.)
- Behavior preserved: re-materialize still clears stale files; a conflicted tree's diff3-marked blobs are just content, cached + reflinked like any other.

---

## 5. Behavior & degradation

- **Two branches at the same tree:** every file reflinks from the same cache blob → blocks shared, ~zero extra disk.
- **Edit a file in a branch folder:** the editor writes in place → reflink broken for that file's changed blocks only (CoW) — the cache blob and other branches are untouched.
- **Commit:** new content → new cache blob(s); re-materialize reflinks the branch folder to the (new) committed tree → committed files shared again. (This is the "committed files revert out of the per-branch upper" property, with the cache as the lower.)
- **ext4 / non-CoW FS / non-Linux:** `reflinkOrCopy` plain-copies — correct, just no block sharing. The cache still dedupes *writes* of identical content into one cache file, but branch copies are full.

---

## 6. Cache GC (deferred — design note)

The cache grows by unique content; abandoned/folded history's blobs linger. A future `cairn gc` prunes `cache/blobs/<sha>` not referenced by any reachable tree (open lines/changes + their history). Not in Slice B; the cache is bounded by total unique content, which is acceptable for now. Noted so it isn't forgotten.

---

## 7. Testing

- **`reflinkOrCopy`:** round-trips content (clone or copy) into a new file with equal bytes; works in a temp dir.
- **Cache dedup:** materialize two trees that share a file → only one `cache/blobs/<sha>` for that content; a changed file gets a distinct blob.
- **CoW isolation:** materialize tree into dir A and dir B (same content), then overwrite a file in A → B's file and the cache blob are unchanged (byte-identical to original).
- **Block-sharing (reflink-gated):** if `reflinkSupported(tmp)` — materialize the same content into two files and assert they share storage (compare `st_blocks` / a reflink-extent check); **`t.Skip`** if reflink unsupported (ext4 ubuntu CI, macOS, Windows) so CI stays green cross-platform.
- **Transparency:** the existing `internal/worktree` converge/resolve/abandon tests and the `cmd/cairn` e2e tests pass **unchanged** (Materialize swap is invisible to them).
- Full gate: `go test ./... && go vet ./... && go build ./...` green on all CI platforms.

**DoD:** reflink CoW materialization shipped behind the unchanged `Repo`/CLI surface; identical content shares one cache blob (and shares blocks where the FS supports reflink); edits CoW-isolate; all prior Slice-A tests still pass; CI green on linux/macOS/Windows.

---

## 8. Build sequence (for the plan)

1. `reflinkOrCopy` (`reflink_linux.go` + `reflink_other.go`) + `reflinkSupported` probe — TDD (round-trip + fallback).
2. Blob cache + `Materialize` signature/impl revision (ensure cache blob → reflinkOrCopy) + update all `worktree` call sites/tests — TDD (dedup + content correctness; existing tests stay green).
3. CoW-isolation test + reflink-gated block-sharing test; confirm whole-repo + e2e green.

---

## 9. Open questions (small, non-blocking)

- **Cache key:** sha256 of bytes (self-contained in worktree) vs reusing the engine's git blob sha (one round-trip, avoids re-hashing). Lean sha256 for independence; revisit if hashing cost shows up.
- **FICLONE error set for fallback:** start with `ENOTSUP`/`EOPNOTSUPP`/`EXDEV`/`EINVAL`; widen if a real FS surfaces another "unsupported" errno. Any unexpected errno is a hard error (don't silently mask real failures).
- **Cache location:** `.cairn/cache/blobs/` inside the repo (same FS as branch folders — required, since reflink can't cross filesystems). Confirm branch folders and `.cairn` are always co-located (they are: both under the repo root).
