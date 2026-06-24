# bisect — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** `cairn bisect start/good/bad/skip/reset/status` + `cairn bisect run <cmd>` — binary-search a line's sealed history for the first bad commit, materializing the midpoint into the expressed folder, with WCC auto-snapshot suspended during the session.

**Architecture:** a single-row `bisect` session table; the search runs over `sealedChain` (from history-editing); the midpoint is materialized into the folder; `SyncWorking` early-returns while a session is active (the critical WCC interaction). Reuses `sealedChain`/`firstParent`/`Materialize`/`Conflicts`.

**Tech:** Go 1.26.3, go-git, modernc sqlite. Spec: `docs/cairn/specs/2026-06-24-bisect.md`.

**Conventions:** errors `pkg.Func: %w`; `skipOnWindows` on e2e (esp. `run` with a shell); one tx; commit after each task.

---

## Task 1: bisect engine (table + algorithm)

**Files:** modify `internal/change/schema.sql`; create `internal/change/bisect.go`, `internal/change/bisect_test.go`.

- [ ] **Step 1: schema** — add the `bisect` table (single row, `id INTEGER PRIMARY KEY CHECK(id=1)`) per the spec §2. Additive `CREATE TABLE IF NOT EXISTS`.

- [ ] **Step 2: types + helpers (test first)**
```go
type BisectInfo struct {
	Active            bool
	Branch            string
	Good, Bad, Current string
	CandidatesLeft    int
}
type BisectStep struct {
	Done     bool
	Current  string // the commit to test (when !Done)
	FirstBad string // the answer (when Done)
}
func (e *Engine) BisectActive() (bool, error)         // a row exists
func (e *Engine) BisectInfo() (BisectInfo, error)
```

- [ ] **Step 3: `BisectStart` (test first)**
```go
// BisectStart begins a session searching lineID's sealed chain for the first bad
// commit between good (known-good ancestor) and bad (known-bad). It records the
// session (restore_tip = the line tip) and returns the first midpoint to test, or
// Done immediately if bad is the commit right after good.
func (e *Engine) BisectStart(lineID, branch, good, bad string) (BisectStep, error)
```
Logic: `chain := sealedChain(lineID)`; resolve full shas; `gi, bi := index(good), index(bad)` (by commit sha; require both present and `gi < bi`, else "good is not an ancestor of bad on this line"). `line := lineByID(lineID)`. INSERT the session `{id:1, line_id, branch, good_sha:good, bad_sha:bad, current_sha:<mid>, restore_tip:line.TipCommit, started_at}` — but compute the step first: if `bi == gi+1` → `BisectStep{Done:true, FirstBad: bad}` (still INSERT a session so reset is symmetric? — pin: if done immediately, do NOT create a session; just return Done). Else `mi := gi + (bi-gi)/2`; `current := chain[mi].Commit`; INSERT session with that current; return `BisectStep{Current: current}`. **Refuse if a session already exists** ("a bisect is already in progress; reset first").

- [ ] **Step 4: `BisectMark` / `BisectSkip` / `BisectReset` (test first)**
```go
func (e *Engine) BisectMark(verdict string) (BisectStep, error) // verdict: "good" | "bad"
func (e *Engine) BisectSkip() (BisectStep, error)
func (e *Engine) BisectReset() (restoreTip string, err error)
```
- `BisectMark`: load the session; `chain := sealedChain(line_id)`; `gi, bi := index(good_sha), index(bad_sha)`. `good` → `good_sha = current_sha` (gi = index(current)); `bad` → `bad_sha = current_sha`. Recompute `gi,bi`; if `bi == gi+1` → DELETE the session, return `BisectStep{Done:true, FirstBad: bad_sha}`. Else `mi := gi+(bi-gi)/2`; `current := chain[mi].Commit`; UPDATE session `good_sha/bad_sha/current_sha`; return `{Current: current}`. (All in one tx.)
- `BisectSkip`: load session; pick the candidate one index toward `good` from the current midpoint (`mi-1`, if `> gi`) as the new `current` WITHOUT changing bounds; if none available (`mi-1 <= gi`) → error "cannot narrow further — adjacent commits skipped". UPDATE `current_sha`; return `{Current}`.
- `BisectReset`: `SELECT restore_tip`; if no session → error "no bisect in progress"; `DELETE FROM bisect`; return restore_tip.
- `BisectInfo`/`BisectActive`: read the row.

- [ ] **Step 5: tests** `internal/change/bisect_test.go` — build a non-root line with 6 sealed commits S1..S6 where `flag.txt` is "ok" through S3 and "bad" from S4 (S4 = the first bad). Walk a manual bisect: `BisectStart(line, branch, S1, S6)` → returns a midpoint; simulate marking by checking that commit's `flag.txt` via `eng.Files(current)` ("ok"→good, "bad"→bad); loop `BisectMark` until Done; assert `FirstBad == S4`. Also: `BisectStart` with `good` after `bad` → error; second `BisectStart` while active → error; `BisectReset` returns the restore tip + clears; `BisectActive` true/false; immediate-done when good=S3,bad=S4. Build commits via `SnapshotWorking`+`Seal` on a child line (reuse the history-editing test fixture pattern).

- [ ] **Step 6: verify + commit** — `go test ./internal/change/ -run Bisect -v` + full + vet/cross. Commit `feat(change): bisect engine — session table + binary-search over sealed chain (bisect task 1)`.

---

## Task 2: worktree (suspend sync + materialize) + manual CLI

**Files:** modify `internal/worktree/worktree.go`, `cmd/cairn/main.go`; create `cmd/cairn/bisect_e2e_test.go`.

- [ ] **Step 1: suspend SyncWorking while bisecting (test first)**
In `Repo.SyncWorking`, early-return when a bisect session is active:
```go
func (r *Repo) SyncWorking() error {
	if active, err := r.eng.BisectActive(); err != nil { return err } else if active { return nil } // folder shows a historical commit; do not snapshot
	... existing loop ...
}
```
Test (worktree): start a bisect (via the engine + a Repo helper), then `r.SyncWorking()` is a no-op (the open working change's head is unchanged even after editing the folder).

- [ ] **Step 2: worktree methods**
```go
func (r *Repo) BisectActive() (bool, error) { return r.eng.BisectActive() }
func (r *Repo) BisectStatus() (change.BisectInfo, error) { return r.eng.BisectInfo() }
func (r *Repo) BisectStart(branch, good, bad string) (change.BisectStep, error) {
	// require clean working change
	dirty, err := r.isDirty(branch); if err != nil { return change.BisectStep{}, err }
	if dirty { return change.BisectStep{}, fmt.Errorf("worktree.BisectStart: stash or commit your work before bisecting") }
	line, err := r.eng.LineByName(branch); if err != nil { return change.BisectStep{}, fmt.Errorf("worktree.BisectStart: %w", err) }
	step, err := r.eng.BisectStart(line.ID, branch, good, bad); if err != nil { return change.BisectStep{}, fmt.Errorf("worktree.BisectStart: %w", err) }
	return step, r.materializeBisect(branch, step) // materialize Current (or restore tip if Done)
}
func (r *Repo) BisectMark(verdict string) (change.BisectStep, error) { /* engine.BisectMark + materializeBisect(activeBranch, step) */ }
func (r *Repo) BisectSkip() (change.BisectStep, error) { /* engine.BisectSkip + materialize */ }
func (r *Repo) BisectReset() error { /* tip := engine.BisectReset(); materialize tip into the session's branch folder */ }
```
`materializeBisect(branch, step)`: if `step.Done`, materialize the FirstBad (leave the user on it) — actually leave the materialized current; on Done the current is the first-bad already (or re-materialize FirstBad). If `!Done`, materialize `step.Current` into `filepath.Join(r.root, entry.Path)` via `Materialize`. The "active branch" for Mark/Skip comes from the session (`BisectInfo().Branch`). Add a helper to get the session's expressed entry.

- [ ] **Step 3: manual CLI** — `case "bisect": return cmdBisect(rest)`; `cmdBisect` sub-dispatches `start`/`good`/`bad`/`skip`/`reset`/`status`/`run`. All open the repo via `openRepoSynced` (which now skips sync while active; `start` runs the normal sync first to clean-check). 
  - `bisect start --bad <c> --good <c> [branch]` → `r.BisectStart`; print `testing <sha> — N candidates left` (or `first bad commit: <sha>` if immediate) to stderr.
  - `bisect good`/`bisect bad` → `r.BisectMark("good"/"bad")`; print next midpoint or `first bad commit: <sha>` + subject.
  - `bisect skip` → `r.BisectSkip()`.
  - `bisect reset` → `r.BisectReset()`; stderr `bisect: reset; working folder restored`.
  - `bisect status` → `r.BisectStatus()` → print good/bad/current/candidates to stdout.
  Usage lines for the bisect subcommands.

- [ ] **Step 4: e2e `cmd/cairn/bisect_e2e_test.go`** (skipOnWindows): init; express `feat`; make 6 commits on feat flipping `flag.txt` from "ok" to "bad" at commit 4 (capture shas via stdout); `cairn bisect start --good <s1> --bad <s6> --repo dir feat` → the feat folder shows the midpoint's `flag.txt`; loop: read `flag.txt` from the folder → `cairn bisect good`/`bad` accordingly → until `first bad commit:` printed == s4; `cairn bisect reset` → folder back to the working tip. Also: `bisect start` with a dirty working change → refused; `bisect status` mid-session shows candidates.

- [ ] **Step 5: verify + commit** — `go test ./...` + vet/cross. Commit `feat(worktree,cmd): cairn bisect manual (start/good/bad/skip/reset/status) + suspend auto-snapshot (bisect task 2)`.

---

## Task 3: `cairn bisect run <cmd>` + final gate

**Files:** modify `cmd/cairn/main.go`; `cmd/cairn/bisect_e2e_test.go`.

- [ ] **Step 1: `cmdBisectRun` (test first)** — requires an active session. Loop: get the current step (from `BisectStatus`/the last result); exec `<cmd...>` with `cmd.Dir = <session branch's expressed folder>` (resolve via `r` + the session branch); map exit code (0→good, 125→skip, else→bad — use `exec.ExitError.ExitCode()`); call `r.BisectMark`/`BisectSkip`; materialize the next midpoint (BisectMark already does); repeat until `step.Done`. Print each step's verdict to stderr; on Done print `first bad commit: <sha>` + subject to stdout; leave the session (user resets). 
  CLI: `cairn bisect run <cmd> [args...]` — everything after `run` is the command. (Parse: the command is `fs.Args()` after the `run` token; don't let `--repo` after `run` be eaten by the command — put repo flags before `run`, or document `cairn --repo dir bisect run <cmd>`… simplest: `cmdBisectRun` parses `--repo`/`--author` with flag, then the REST (after `--`) is the command; OR treat all args after `run` as the command and read `--repo` from a leading position. Pin: `cairn bisect run --repo dir -- <cmd> [args]` using `--` to separate; if no `--repo`, default ".".)

- [ ] **Step 2: e2e** — `TestBisectRunE2E`: the same 6-commit fixture; `cairn bisect run --repo dir -- sh -c 'grep -q ok flag.txt'` (exit 0 = good when flag is "ok") → drives to `first bad commit: <s4>`. (skipOnWindows — uses `sh`.)

- [ ] **Step 3: usage + final gate** — update `usage` (all bisect subcommands incl. `run`); full `go test ./...` + `go vet ./...` + cross-compile darwin/windows. Commit `feat(cmd): cairn bisect run <cmd> (automated bisect) (bisect task 3)`.

## Notes
- **The suspend-SyncWorking guard is load-bearing** — without it, every command during bisect would snapshot the historical midpoint into your working change. Test it explicitly.
- `start` requires a clean working change + records `restore_tip`; `reset` always restores it. The working change's catalogue state is NEVER mutated during bisect (only the folder display changes).
- The search is over the sealed chain (history-editing's `sealedChain`); good/bad are sealed commit shas from `cairn log`.
- `run` exit-code mapping mirrors git (0=good, 125=skip, else=bad).
- DRY, YAGNI, TDD.
