# cairn Phase 2 Slice C-sync (part 2) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** `push` auto-reconciles a diverged remote and retries; opt-in `autosync` keeps a line current with origin after each commit (best-effort).

**Architecture:** worktree-layer convenience over part-1 primitives — `Repo.Push` retries via `Repo.Pull` on non-ff; a small `config` store drives `Repo.Commit`'s opt-in post-commit best-effort pull. No new transport, no daemon.

**Tech Stack:** Go 1.26.3 · the engine `PushToRemote`/`PullFromRemote`/`ListRemotes` + new `IsNonFastForward`/`GetConfig`/`SetConfig` · the worktree/CLI.

**Spec:** `docs/cairn/specs/2026-06-23-cairn-phase2-sliceC-sync2-design.md`.

**Hook points:** `internal/worktree/worktree.go` — `Repo.Commit` (line ~156), `Repo.Push` (~363), `Repo.Pull` (~380). `internal/change/push.go` — `isNonFastForward` (~177). CLI dispatch + `mapErr` + e2e helpers (`makeSeededBareRepo`, `soleExpressedDir`, `mustRun`, `skipOnWindows`) in `cmd/cairn`.

## File Structure

| File | Responsibility |
|---|---|
| `internal/change/push.go` (modify) | export `IsNonFastForward(err) bool` |
| `internal/worktree/worktree.go` (modify) | `Repo.Push` auto-pull-retry; `Repo.Commit` opt-in auto-sync |
| `internal/change/config.go` | `config` table helpers: `GetConfig`/`SetConfig` |
| `internal/change/config_test.go` | config round-trip |
| `internal/change/schema.sql` (modify) | add `config` table |
| `cmd/cairn/main.go` (modify) | `config` subcommand; push/commit notices |
| `cmd/cairn/sync2_e2e_test.go` | push-auto-retry + autosync e2e |

---

## Task 1: push auto-pull-retry (NEX-738)

**Files:** modify `internal/change/push.go`, `internal/worktree/worktree.go`; create `cmd/cairn/sync2_e2e_test.go`

- [ ] **Step 1: export the non-ff predicate.** In `push.go`, rename `isNonFastForward` → exported `IsNonFastForward(err error) bool` (update its one internal caller in `push`).

- [ ] **Step 2: failing e2e** (`cmd/cairn/sync2_e2e_test.go`) — push that auto-pulls then succeeds:
```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestE2E_PushAutoPullRetry(t *testing.T) {
	skipOnWindows(t)
	origin := makeSeededBareRepo(t)
	A := filepath.Join(t.TempDir(), "A"); B := filepath.Join(t.TempDir(), "B")
	mustRun(t, "clone", origin, A)
	mustRun(t, "clone", origin, B)
	def := soleExpressedDir(t, A)
	// A advances the remote
	os.WriteFile(filepath.Join(A, def, "fromA.txt"), []byte("A\n"), 0o644)
	mustRun(t, "commit", "--repo", A, def)
	mustRun(t, "push", "--repo", A)
	// B commits a different file and pushes WITHOUT --force → non-ff → auto-pull+retry succeeds
	os.WriteFile(filepath.Join(B, def, "fromB.txt"), []byte("B\n"), 0o644)
	mustRun(t, "commit", "--repo", B, def)
	mustRun(t, "push", "--repo", B) // must NOT error (auto-pull-retry)
	// B's folder now has both (pulled A's work), and the remote has both
	if _, err := os.Stat(filepath.Join(B, def, "fromA.txt")); err != nil { t.Fatalf("auto-pull didn't bring A's work: %v", err) }
	C := filepath.Join(t.TempDir(), "C")
	mustRun(t, "clone", origin, C)
	if _, err := os.Stat(filepath.Join(C, def, "fromB.txt")); err != nil { t.Fatalf("B's push didn't land on remote: %v", err) }
}
```
Run → confirm FAIL (B's push errors non-ff today).

- [ ] **Step 3: implement `Repo.Push` auto-pull-retry** (per spec §2): try `eng.PushToRemote`; on `change.IsNonFastForward(err) && !force`: print/return-info "remote advanced — pulling & merging, retrying"; `r.Pull(remote)`; if the summary has any `LineResult.Conflicts > 0` → return `fmt.Errorf("worktree.Push: remote diverged and merging produced conflicts; resolve then push")`; else retry `eng.PushToRemote(remote, force)` once. `--force` returns the original error path (no retry). (The "notice" can be a returned value or just rely on the CLI to print on the conflict-stop; keep the engine/worktree I/O-free — see step 4.)

- [ ] **Step 4: CLI notice.** In `cmdPush`, when `Repo.Push` performs a retry, surface a one-line stderr notice. Simplest: have `Repo.Push` return a small result or the CLI detect via a wrapper; OR keep it quiet and only print the conflict-stop error. Minimal acceptable: the conflict-stop returns the clear error (mapped to stderr); the successful auto-retry is silent. (Add a stderr "remote advanced; pulled & merged" line if easy via a `Repo.PushVerbose`-style return; otherwise silent success is fine for v1 — document the choice.)

- [ ] **Step 5: add the conflict-stop test:** B and the remote edit the SAME region → B `push` → auto-pull → conflict → `run(["push",...])` returns a non-nil error containing "resolve". Then B `resolve` + `push` succeeds. Make it pass.

- [ ] **Step 6: verify + commit.** `go test ./... && go vet ./... && go build ./...` + cross-compile. Then:
```bash
git add internal/change/push.go internal/worktree/worktree.go cmd/cairn/
git commit -m "feat(worktree,cmd): push auto-pull-retry on non-fast-forward (NEX-738)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 2: config store + cairn config (NEX-739)

**Files:** create `internal/change/config.go`, `internal/change/config_test.go`; modify `internal/change/schema.sql`, `cmd/cairn/main.go`

- [ ] **Step 1: schema** — add to `schema.sql`:
```sql
CREATE TABLE IF NOT EXISTS config (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
```

- [ ] **Step 2: failing test** (`config_test.go`):
```go
package change

import "testing"

func TestConfigRoundTrip(t *testing.T) {
	e, err := Open(t.TempDir())
	if err != nil { t.Fatalf("Open: %v", err) }
	t.Cleanup(func() { _ = e.Close() })
	if _, ok, _ := e.GetConfig("autosync"); ok { t.Fatal("autosync should be unset initially") }
	if err := e.SetConfig("autosync", "true"); err != nil { t.Fatalf("SetConfig: %v", err) }
	v, ok, err := e.GetConfig("autosync")
	if err != nil || !ok || v != "true" { t.Fatalf("GetConfig = %q,%v,%v", v, ok, err) }
	if err := e.SetConfig("autosync", "false"); err != nil { t.Fatalf("update: %v", err) }
	v, _, _ = e.GetConfig("autosync")
	if v != "false" { t.Fatalf("after update = %q", v) }
}
```

- [ ] **Step 3: implement `config.go`** — `GetConfig(key) (string, bool, error)` (SELECT; ok=false on no rows); `SetConfig(key, value) error` (`INSERT ... ON CONFLICT(key) DO UPDATE SET value=excluded.value`). Wrap `change.GetConfig:`/`change.SetConfig:`. Add an unexported `configTruthy(value) bool` (true/1/on case-insensitive) — or expose a helper the worktree uses.

- [ ] **Step 4: `cairn config` CLI** — `config <key>` prints the value (empty line if unset); `config <key> <value>` sets it. Uses `--repo`. Add to dispatch + usage.

- [ ] **Step 5: verify + commit.** `go test ./internal/change/ -run TestConfig -v`, `go test ./...`, vet, build. Then:
```bash
git add internal/change/config.go internal/change/config_test.go internal/change/schema.sql cmd/cairn/
git commit -m "feat(change,cmd): config store + cairn config (NEX-739)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 3: opt-in commit-time auto-sync (NEX-740)

**Files:** modify `internal/worktree/worktree.go`, `cmd/cairn/main.go`; add to `cmd/cairn/sync2_e2e_test.go`

- [ ] **Step 1: failing e2e** — autosync on → commit keeps the line current with origin:
```go
func TestE2E_CommitAutoSync(t *testing.T) {
	skipOnWindows(t)
	origin := makeSeededBareRepo(t)
	B := filepath.Join(t.TempDir(), "B")
	mustRun(t, "clone", origin, B)
	def := soleExpressedDir(t, B)
	mustRun(t, "config", "--repo", B, "autosync", "true")
	// remote advances independently (helper advanceSeededBare, or a second clone that pushes)
	advanceSeededBareRepo(t, origin, "remote.txt", "R\n")
	// B commits a local edit → autosync pulls origin after the commit
	os.WriteFile(filepath.Join(B, def, "local.txt"), []byte("L\n"), 0o644)
	mustRun(t, "commit", "--repo", B, def)
	// after the autosync, B's folder has BOTH local.txt and the remote's remote.txt
	if _, err := os.Stat(filepath.Join(B, def, "remote.txt")); err != nil { t.Fatalf("autosync didn't bring remote work: %v", err) }
	if _, err := os.Stat(filepath.Join(B, def, "local.txt")); err != nil { t.Fatalf("local work lost: %v", err) }
}

func TestE2E_CommitAutoSyncOfflineStillCommits(t *testing.T) {
	skipOnWindows(t)
	dir := filepath.Join(t.TempDir(), "local")
	mustRun(t, "init", dir)               // no origin at all
	mustRun(t, "config", "--repo", dir, "autosync", "true")
	def := soleExpressedDir(t, dir)
	os.WriteFile(filepath.Join(dir, def, "x.txt"), []byte("x\n"), 0o644)
	mustRun(t, "commit", "--repo", dir, def) // must succeed despite autosync on + no origin
}
```
(write `advanceSeededBareRepo` — clone the bare, write+commit `path`, push back; mirror existing helpers.) Run → confirm FAIL.

- [ ] **Step 2: implement `Repo.Commit` auto-sync** (per spec §3): after the existing commit body computes its `CommitResult` and re-materializes, check `eng.GetConfig("autosync")` truthy AND `origin` in `eng.ListRemotes()`; if so, call `r.Pull("origin")` **best-effort** — wrap in a way that a non-nil pull error does NOT change the returned `CommitResult`/error; capture the sync outcome (ok / skipped-reason) for the CLI. Order: commit FIRST (already done), then pull. (If exposing the sync outcome cleanly is awkward, log via a returned secondary value or a `Repo.LastSyncNote()` — keep engine I/O-free; CLI prints.)

- [ ] **Step 3: CLI notice in `cmdCommit`** — after a successful commit, if autosync ran, print "auto-synced with origin" or "auto-sync skipped: <reason>" to stderr. Non-fatal regardless.

- [ ] **Step 4: verify + commit.** `go test ./... && go vet ./... && go build ./...` + cross-compile; all prior slices green. Then:
```bash
git add internal/worktree/ cmd/cairn/
git commit -m "feat(worktree,cmd): opt-in commit-time auto-sync (best-effort) (NEX-740)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Self-Review

**Spec coverage:** push auto-pull-retry (§2) → T1; config store + CLI (§3) → T2; commit auto-sync (§3) → T3. ✓
**Out of scope:** cairn→cairn fidelity, auth, cross-line atomicity, background daemon. ✓
**Consistency:** reuses `PushToRemote`/`PullFromRemote`/`ListRemotes`; new exported `IsNonFastForward`, `GetConfig`/`SetConfig`. The `config` table mirrors `remote_kind`. Commit-then-pull ordering protects uncommitted edits.
**Sharp edges:** (1) push retry is **once**, not a loop (avoid infinite retry under a racing third writer). (2) auto-sync runs AFTER commit (never clobber uncommitted folder edits) and is **best-effort** (offline/no-origin/fetch-error never fails the commit). (3) keep engine/worktree I/O-free — CLI prints notices; thread the sync outcome out via return values, not prints in the engine. (4) `--force` push skips the retry entirely. (5) skipOnWindows on local-transport e2e.
