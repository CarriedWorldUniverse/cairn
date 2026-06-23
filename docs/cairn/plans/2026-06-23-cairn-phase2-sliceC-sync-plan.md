# cairn Phase 2 Slice C-sync (part 1) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** `cairn pull`/`fetch` — fetch a remote into tracking refs and reconcile each local line against its remote branch (fast-forward, or 3-way merge with conflicts-as-data), so branches never go stale. Re-materialize expressed folders.

**Architecture:** engine `PullFromRemote` reuses `mergeBase`/`mergeTrees`/`commitTree`/`writeCommit`; fetch goes to `refs/remotes/<remote>/*` (not `refs/heads/*`) so local work isn't clobbered. `Repo.Pull` re-materializes. CLI `pull`/`fetch`. Commit-time auto-sync deferred to part 2.

**Tech Stack:** Go 1.26.3 · go-git v5.13.2 (`Remote.Fetch` with a tracking refspec, `plumbing`) · the engine's existing reconcile helpers.

**Spec:** `docs/cairn/specs/2026-06-23-cairn-phase2-sliceC-sync-design.md`.

**Reusable engine internals:** `mergeBase(a,b string)(string,error)`, `mergeTrees(changeID,baseTree,oursTree,theirsTree string)(string,[]Conflict,error)`, `commitTree(sha)(string,error)`, `writeCommit(treeSha,changeID,author string,parents []string)(string,error)`, `lineByID`, `LineByName`, `GetChange`, `CreateChange`, `Conflicts`, `fetchRemote`/`AddRemote`, `e.git`. Line/Change tables; conflicts attach to a change_id.

## File Structure

| File | Responsibility |
|---|---|
| `internal/change/sync.go` | `fetchTracking`, `remoteHeads`, `PullFromRemote`, `PullSummary`/`LineResult` |
| `internal/change/sync_test.go` | ff / clean-merge / conflict / up-to-date; two-parent merge commit |
| `internal/worktree/worktree.go` (modify) | `Repo.Pull(remote)`, `Repo.Fetch(remote)` (+ re-materialize) |
| `cmd/cairn/main.go` (modify) | `pull`/`fetch` subcommands |
| `cmd/cairn/sync_e2e_test.go` | collaboration loop (two clones) |

---

## Task 1: engine tracking-fetch + PullFromRemote (NEX-736)

**Files:** Create `internal/change/sync.go`, `internal/change/sync_test.go`

- [ ] **Step 1: Failing test** (`sync_test.go`). Helpers: build a bare remote, clone into engine A (import), advance the remote independently, then pull. Reuse the push_test/importer_test helper patterns (plain paths; `skipOnWindowsSync`).

```go
package change

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func skipOnWindowsSync(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" { t.Skip("go-git local-transport flakes under Windows file locking") }
}

// originWithCommit: a bare repo seeded (via a temp working clone) with readme on
// its default branch; returns (barePath, defaultBranchName).
func originWithCommit(t *testing.T) (string, string) { /* PlainInit bare; clone non-bare; commit readme; push; return */ }

// advanceOrigin: clone the bare, change `path` to `content`, commit, push back.
func advanceOrigin(t *testing.T, bare, path, content string) { /* ... */ }

func TestPullFastForward(t *testing.T) {
	skipOnWindowsSync(t)
	bare, def := originWithCommit(t)
	e, err := Open(t.TempDir())
	if err != nil { t.Fatalf("Open: %v", err) }
	t.Cleanup(func() { _ = e.Close() })
	if _, err := e.ImportFromRemote(bare); err != nil { t.Fatalf("import: %v", err) }
	// remote advances; local untouched
	advanceOrigin(t, bare, "added.txt", "X\n")
	sum, err := e.PullFromRemote("origin")
	if err != nil { t.Fatalf("Pull: %v", err) }
	root, _ := e.LineByName(def)
	files, _ := e.Files(root.TipCommit)
	if string(files["added.txt"]) != "X\n" { t.Fatalf("ff pull did not bring added.txt: %v", files) }
	_ = sum
}

func TestPullDivergentCleanMerge(t *testing.T) {
	skipOnWindowsSync(t)
	bare, def := originWithCommit(t)
	e, _ := Open(t.TempDir())
	t.Cleanup(func() { _ = e.Close() })
	e.ImportFromRemote(bare)
	root, _ := e.LineByName(def)
	// local edits a NEW file on the default line (via a change on it)
	ch, _ := e.CreateChange(root.ID, "local")
	e.Commit(ch.ID, mustFilesPlusLocal(t, e, root.TipCommit)) // existing files + local.txt
	// remote edits a DIFFERENT new file
	advanceOrigin(t, bare, "remote.txt", "R\n")
	sum, err := e.PullFromRemote("origin")
	if err != nil { t.Fatalf("Pull: %v", err) }
	root2, _ := e.LineByName(def)
	files, _ := e.Files(root2.TipCommit)
	if string(files["local.txt"]) == "" || string(files["remote.txt"]) != "R\n" {
		t.Fatalf("clean merge missing a side: %v", files)
	}
	// merged commit has 2 parents
	assertTwoParents(t, e, root2.TipCommit)
	_ = sum
}
```
(Write `mustFilesPlusLocal` = read tip files + add `local.txt`; `assertTwoParents` via `e.git.CommitObject`.) Run → confirm FAIL.

- [ ] **Step 2: Implement `sync.go`**
- `type LineResult struct { Line, Status string; Conflicts int }` (Status: up-to-date|fast-forward|merged); `type PullSummary struct { Lines []LineResult }`.
- `fetchTracking(remoteName string) error`: `e.git.Remote(remoteName)` (ErrRemoteNotFound→clear err); `rem.Fetch(&git.FetchOptions{RefSpecs: ["+refs/heads/*:refs/remotes/"+remoteName+"/*"], Tags: git.AllTags})`; tolerate `NoErrAlreadyUpToDate`. Wrap `change.fetchTracking: %w`.
- `remoteHeads(remoteName) (map[string]string, error)`: iterate `e.git.Storer.IterReferences()`, collect `refs/remotes/<remoteName>/<name>` HashReferences → name→sha (skip `HEAD`).
- `PullFromRemote(remoteName string) (PullSummary, error)`:
  - `fetchTracking(remoteName)`; `rheads = remoteHeads(remoteName)`.
  - enumerate local **open** lines (`SELECT id,name,tip_commit FROM line WHERE status='open'`).
  - for each line whose `name` is in `rheads`: reconcile (below), append a LineResult.
  - return the summary.
- **reconcile(line, R) → LineResult** (one tx per line):
  - find/just-create the line's active change: `SELECT id,head_commit FROM change WHERE line_id=? AND status='open' ORDER BY updated_at DESC LIMIT 1`; if none, `CreateChange(line.ID, "sync")`. `L := change.head_commit (or line.tip_commit if empty)`.
  - if `L == R` → up-to-date.
  - `base = mergeBase(L, R)`.
  - if `base == R` → local ahead → up-to-date-ish (status "up-to-date"/"ahead"), no change.
  - if `base == L` → fast-forward: set change.head_commit=R, line.tip_commit=R → "fast-forward".
  - else diverged: `merged, conflicts = mergeTrees(changeID, tree(base or ""), tree(R), tree(L))`; `head = writeCommit(merged, changeID, syncAuthor, []string{L, R})`; set change.head_commit=head (+has_conflict), line.tip_commit=head → "merged" (+Conflicts count).
  - commit the tx.
  Wrap errors `change.PullFromRemote: %w`. (Conflicts persist via `mergeTrees` which already records them on the changeID — confirm it inserts; if `mergeTrees` only BUILDS conflicts and the caller persists (per the Commit path), replicate the Commit pattern: insert the returned conflicts in the same tx.)

- [ ] **Step 3: Run, verify pass** (ff + clean-merge). `go test ./internal/change/ -run TestPull -v`.

- [ ] **Step 4: Add conflict + up-to-date tests, make pass**
- `TestPullDivergentConflict`: local + remote edit the SAME file region → `PullFromRemote` returns a LineResult with Conflicts>0; `Conflicts(changeID)` lists it; the line is conflicted (resolvable).
- `TestPullUpToDate`: no remote advance → all lines "up-to-date", no new commits.

- [ ] **Step 5: Commit**
```bash
git add internal/change/sync.go internal/change/sync_test.go
git commit -m "feat(change): PullFromRemote — tracking fetch + reconcile (ff / 3-way conflicts-as-data) (NEX-736)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 2: Repo.Pull/Fetch + CLI + collaboration e2e (NEX-737)

**Files:** modify `internal/worktree/worktree.go`, `cmd/cairn/main.go`; create `cmd/cairn/sync_e2e_test.go`

- [ ] **Step 1: `Repo` methods** in `worktree.go`:
```go
func (r *Repo) Fetch(remote string) error { return r.eng.FetchTrackingPublic(remote) } // or expose fetchTracking via a thin exported wrapper
func (r *Repo) Pull(remote string) (change.PullSummary, error) {
	sum, err := r.eng.PullFromRemote(remote)
	if err != nil { return sum, err }
	// re-materialize every expressed folder to its line's (possibly merged) tip
	for branch := range r.st.Expressed {
		line, lerr := r.eng.LineByName(branch)
		if lerr == nil && line.TipCommit != "" {
			_ = Materialize(r.eng, r.cacheDir(), line.TipCommit, r.dir(branch))
		}
	}
	return sum, r.save()
}
```
(If `fetchTracking` is unexported, add an exported `Engine.FetchTracking(remote) error` thin wrapper; or have `Repo.Fetch` call `PullFromRemote`'s fetch-only path. Keep it clean.)

- [ ] **Step 2: Failing collaboration-loop e2e** (`cmd/cairn/sync_e2e_test.go`):
```go
func TestE2E_CollaborationLoop(t *testing.T) {
	skipOnWindows(t)
	origin := makeSeededBareRepo(t) // reuse the push e2e helper (bare with default branch + readme)
	A := filepath.Join(t.TempDir(), "A"); B := filepath.Join(t.TempDir(), "B")
	mustRun(t, "clone", origin, A)
	mustRun(t, "clone", origin, B)
	def := soleExpressedDir(t, A)
	// A: add a file, commit, push
	os.WriteFile(filepath.Join(A, def, "fromA.txt"), []byte("A\n"), 0o644)
	mustRun(t, "commit", "--repo", A, def)
	mustRun(t, "push", "--repo", A)
	// B: add a different file, commit, then pull (gets A's work merged), then push
	os.WriteFile(filepath.Join(B, def, "fromB.txt"), []byte("B\n"), 0o644)
	mustRun(t, "commit", "--repo", B, def)
	mustRun(t, "pull", "--repo", B)
	// B's default folder now has BOTH files (A's merged in)
	if _, err := os.Stat(filepath.Join(B, def, "fromA.txt")); err != nil {
		t.Fatalf("pull did not bring A's work into B: %v", err)
	}
	if _, err := os.Stat(filepath.Join(B, def, "fromB.txt")); err != nil { t.Fatalf("B's own work lost: %v", err) }
	mustRun(t, "push", "--repo", B) // B can now push (it's a descendant via the merge)
}
```
(`makeSeededBareRepo`, `soleExpressedDir`, `mustRun`, `skipOnWindows` exist in cmd/cairn tests.) Run → confirm FAIL.

- [ ] **Step 3: `cairn fetch`/`pull` subcommands** in `main.go`: `fetch [remote]` (default origin) → `Repo.Fetch`; `pull [remote]` → `Repo.Pull`, print each LineResult (`<line>: up-to-date|fast-forward|merged[ with N conflicts]`); conflicts non-fatal with a "resolve + push" notice. `--repo` flag.

- [ ] **Step 4: Verify + commit.** `go test ./... && go vet ./... && go build ./...` + `GOOS=darwin/windows go build ./...`. Slice-A/B/C-clone/C-push green. Then:
```bash
git add internal/worktree/ cmd/cairn/
git commit -m "feat(worktree,cmd): cairn pull/fetch + collaboration loop (NEX-737)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Self-Review

**Spec coverage:** tracking-fetch + PullFromRemote reconcile (§2) → T1; Repo.Pull/Fetch + CLI + collaboration loop (§3) → T2. ✓
**Out of scope:** commit-time auto-sync + non-ff-push auto-pull (part 2), cairn→cairn fidelity, auth. ✓
**Consistency:** reuses `mergeBase`/`mergeTrees`/`commitTree`/`writeCommit` (the exact merge-forward machinery), the conflict-on-change model, the C-push/C-clone test-helper + `skipOnWindows` conventions. New engine API: `PullFromRemote`, `PullSummary`/`LineResult`, `FetchTracking`. New Repo: `Pull`, `Fetch`.
**Sharp edges:** (1) fetch to `refs/remotes/<remote>/*` (NOT refs/heads) so local lines aren't clobbered — the whole point. (2) the merge commit must have BOTH parents [L,R] for real git history + a pushable descendant. (3) conflicts attach to the line's active change (reuse the Commit conflict-persist pattern — verify mergeTrees persists vs caller-persists and match it). (4) re-materialize expressed folders after pull so disk reflects the merge. (5) base=="" unrelated histories → conflict-as-data (don't crash). (6) skipOnWindows on local-transport fixtures.
