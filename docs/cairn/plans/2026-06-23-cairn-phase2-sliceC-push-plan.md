# cairn Phase 2 Slice C-push — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** `cairn push` — translate the local cairn change-graph into git and publish branches + tags to a git remote, completing the clone→work→push→re-clone round-trip. Plus `cairn remote add`/`remote` with a per-remote type seam (cairn|git, default git).

**Architecture:** engine `PushToRemote` = `Export()` (cairn→git refs) + go-git `Remote.Push` of `refs/heads/*` + `refs/tags/*` (not `refs/cairn/*`). `AddRemote`/`ListRemotes` wrap go-git + record a `kind`. CLI `remote`/`push` over a thin `Repo.Push`. cairn→cairn full fidelity deferred (seam recorded only).

**Tech Stack:** Go 1.26.3 · go-git v5.13.2 (`Remote.Push`, `git.PushOptions{RemoteName,RefSpecs,Force}`, `NoErrAlreadyUpToDate`, `CreateRemote`/`Remotes`) · the engine `Export`/`fetchRemote`/importer (on main).

**Spec:** `docs/cairn/specs/2026-06-23-cairn-phase2-sliceC-push-design.md`.

**Available:** `Engine.Export()`, `Engine.fetchRemote`, `Engine.ImportFromRemote`, `Engine.RootLine`; go-git `e.git.Remote/CreateRemote/DeleteRemote/Remotes`. The C-clone test helper pattern (`makeOriginRepo*` returning a plain local path) + `skipOnWindows(t)` exist in `internal/change`/`internal/worktree`/`cmd/cairn` test files.

## File Structure

| File | Responsibility |
|---|---|
| `internal/change/push.go` | `AddRemote`, `ListRemotes`/`RemoteInfo`, `PushToRemote`, `PushToRemoteBranch`, remote-kind store |
| `internal/change/push_test.go` | push to file:// bare → clone-back assert; non-ff + force; refs/cairn not pushed |
| `internal/worktree/worktree.go` (modify) | `Repo.Push(remote, force)` + `Repo.AddRemote`/`Repo.Remotes` thin pass-throughs |
| `cmd/cairn/main.go` (modify) | `remote` (add/list) + `push` subcommands |
| `cmd/cairn/push_e2e_test.go` | round-trip clone→work→push→re-clone |

The remote **kind** (cairn|git): store in a tiny catalogue table `remote_kind(name TEXT PRIMARY KEY, kind TEXT)` (add to `schema.sql`) — simplest, travels with the engine. Default `git` when unset.

---

## Task 1: engine push + remotes (NEX-734)

**Files:** Create `internal/change/push.go`, `internal/change/push_test.go`; modify `internal/change/schema.sql` (add `remote_kind`)

- [ ] **Step 1: schema** — add to `schema.sql`:
```sql
CREATE TABLE IF NOT EXISTS remote_kind (
  name TEXT PRIMARY KEY,
  kind TEXT NOT NULL DEFAULT 'git'   -- 'git' | 'cairn'
);
```

- [ ] **Step 2: Failing test** (`push_test.go`). Build a `file://`… no — use a **bare** local repo as the push target (plain path). Reuse the C-clone helper style (plain path, not `file://`).

```go
package change

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

func skipOnWindowsPush(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("go-git local-transport fixtures flake under Windows file locking")
	}
}

func TestPushToRemoteGitRefs(t *testing.T) {
	skipOnWindowsPush(t)
	// bare remote to receive the push
	bareDir := t.TempDir()
	if _, err := git.PlainInit(bareDir, true); err != nil { t.Fatalf("PlainInit bare: %v", err) }

	e, err := Open(t.TempDir())
	if err != nil { t.Fatalf("Open: %v", err) }
	t.Cleanup(func() { _ = e.Close() })
	// seed main with a commit + a tag
	main, _ := e.LineByName("main")
	ch, _ := e.CreateChange(main.ID, "a")
	r, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("a\n")})
	if err != nil { t.Fatalf("Commit: %v", err) }
	if err := e.Tag("v1", r.HeadCommit, "rel"); err != nil { t.Fatalf("Tag: %v", err) }

	if err := e.AddRemote("origin", bareDir, "git"); err != nil { t.Fatalf("AddRemote: %v", err) }
	if err := e.PushToRemote("origin", false); err != nil { t.Fatalf("PushToRemote: %v", err) }

	// open the bare remote and assert refs landed + content
	bare, err := git.PlainOpen(bareDir)
	if err != nil { t.Fatalf("PlainOpen bare: %v", err) }
	mref, err := bare.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil || mref.Hash().String() != r.HeadCommit {
		t.Fatalf("bare refs/heads/main = %v (%v), want %s", mref, err, r.HeadCommit)
	}
	if _, err := bare.Reference(plumbing.NewTagReferenceName("v1"), true); err != nil {
		t.Fatalf("bare tag v1 missing: %v", err)
	}
	// refs/cairn/* must NOT be pushed
	if _, err := bare.Reference(plumbing.ReferenceName("refs/cairn/change/"+ch.ID), true); err == nil {
		t.Fatal("refs/cairn/* must not be pushed to a git remote")
	}
	_ = os.Stat
}
```

- [ ] **Step 3: Run, verify fail.**

- [ ] **Step 4: Implement `push.go`**
- `type RemoteInfo struct { Name, URL, Kind string }`.
- `AddRemote(name, url, kind string) error`: if `kind==""` → "git". `e.git.Remote(name)`: if `ErrRemoteNotFound` → `CreateRemote`; else if URL differs → DeleteRemote+CreateRemote. Upsert `remote_kind(name,kind)` (`INSERT … ON CONFLICT(name) DO UPDATE SET kind=excluded.kind`). Wrap `change.AddRemote: %w`.
- `ListRemotes() ([]RemoteInfo, error)`: `e.git.Remotes()` → for each, name + first URL + kind (from remote_kind, default "git"). Sorted by name.
- `remoteKind(name) string`: SELECT kind; default "git".
- `PushToRemote(remoteName string, force bool) error`:
  - `if err := e.Export(); err != nil { return ... }`.
  - `rem, err := e.git.Remote(remoteName)` (ErrRemoteNotFound → `change.PushToRemote: no remote %q`).
  - refspecs (force → `+` prefix): `["refs/heads/*:refs/heads/*","refs/tags/*:refs/tags/*"]`.
  - `err = rem.Push(&git.PushOptions{RemoteName: remoteName, RefSpecs: <specs>, Force: force})`.
  - `if errors.Is(err, git.NoErrAlreadyUpToDate) { return nil }`.
  - if the error indicates non-fast-forward (inspect go-git's error — likely contains "non-fast-forward" or is `git.ErrNonFastForwardUpdate` if exported; match on the message if no typed error), return `fmt.Errorf("change.PushToRemote: remote %q diverged (non-fast-forward); fetch/sync first or push --force: %w", remoteName, err)`.
  - else wrap generic.
  - **kind note:** read `remoteKind(remoteName)`; if "cairn", log/return-info that cairn-fidelity push isn't implemented yet and proceed with the git path (push standard refs). (For v1 just proceed; a comment documents the seam. Do NOT push refs/cairn/* yet.)
- `PushToRemoteBranch(remoteName, branch string, force bool) error`: same but refspecs `refs/heads/<branch>:refs/heads/<branch>` + tags.

- [ ] **Step 5: Run, verify pass.** `go test ./internal/change/ -run TestPush -v`.

- [ ] **Step 6: Add non-ff + force test** and make it pass:
```go
func TestPushNonFastForwardThenForce(t *testing.T) {
	skipOnWindowsPush(t)
	bareDir := t.TempDir()
	git.PlainInit(bareDir, true)
	e, _ := Open(t.TempDir())
	t.Cleanup(func() { _ = e.Close() })
	main, _ := e.LineByName("main")
	ch, _ := e.CreateChange(main.ID, "a")
	r1, _ := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("1\n")})
	e.AddRemote("origin", bareDir, "git")
	if err := e.PushToRemote("origin", false); err != nil { t.Fatalf("push1: %v", err) }
	// advance the bare remote's main independently so our next push is non-ff
	// (push an unrelated commit via a second engine cloned from bare, OR write directly)
	advanceBareMainIndependently(t, bareDir) // helper: clone bare, commit, push back
	// our local main advances too
	r2, _ := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("2\n")})
	_ = r2
	if err := e.PushToRemote("origin", false); err == nil {
		t.Fatal("expected non-fast-forward rejection")
	}
	if err := e.PushToRemote("origin", true); err != nil {
		t.Fatalf("force push should succeed: %v", err)
	}
}
```
(Write `advanceBareMainIndependently` to make the remote diverge — clone the bare into a temp, add a commit on main, push back. If this is fiddly, an alternative is to push from a *second* cairn engine; pick whichever is robust.)

- [ ] **Step 7: Commit**
```bash
git add internal/change/push.go internal/change/push_test.go internal/change/schema.sql
git commit -m "feat(change): PushToRemote + remotes (cairn->git, type seam) (NEX-734)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 2: CLI remote/push + round-trip e2e (NEX-735)

**Files:** modify `internal/worktree/worktree.go`, `cmd/cairn/main.go`; create `cmd/cairn/push_e2e_test.go`

- [ ] **Step 1: `Repo` pass-throughs** in `worktree.go`: `func (r *Repo) Push(remote string, force bool) error { return r.eng.PushToRemote(remote, force) }`, `func (r *Repo) AddRemote(name, url, kind string) error { return r.eng.AddRemote(name, url, kind) }`, `func (r *Repo) Remotes() ([]change.RemoteInfo, error) { return r.eng.ListRemotes() }`.

- [ ] **Step 2: Failing round-trip e2e** (`cmd/cairn/push_e2e_test.go`):
```go
func TestE2E_CloneWorkPushReclone(t *testing.T) {
	skipOnWindows(t) // reuse the helper already in cmd/cairn tests
	// origin = a bare repo seeded with main+readme (helper: build non-bare, commit, then `git push` to a bare, OR init bare and push an initial commit via a temp clone)
	originBare := makeSeededBareRepo(t) // returns a path to a bare repo with main + readme.txt
	dirA := filepath.Join(t.TempDir(), "A")
	if err := run([]string{"clone", originBare, dirA}); err != nil { t.Fatalf("clone A: %v", err) }
	// work on the default branch folder, commit, (default branch is already expressed)
	def := defaultBranchName(t, dirA) // read the single expressed folder, or know it from the helper
	if err := os.WriteFile(filepath.Join(dirA, def, "new.txt"), []byte("NEW\n"), 0o644); err != nil { t.Fatal(err) }
	if err := run([]string{"commit", "--repo", dirA, def}); err != nil { t.Fatalf("commit: %v", err) }
	if err := run([]string{"push", "--repo", dirA}); err != nil { t.Fatalf("push: %v", err) }
	// re-clone into B and assert new.txt is present on the default branch
	dirB := filepath.Join(t.TempDir(), "B")
	if err := run([]string{"clone", originBare, dirB}); err != nil { t.Fatalf("clone B: %v", err) }
	if _, err := os.Stat(filepath.Join(dirB, def, "new.txt")); err != nil {
		t.Fatalf("pushed new.txt not present after re-clone: %v", err)
	}
}
```
(Write the helpers: `makeSeededBareRepo` — `PlainInit(bare)`, then create a temp working clone, commit readme on the default branch, push to the bare; return the bare path. `defaultBranchName` — list the dir's single non-".cairn" subfolder. Keep robust to go-git's default branch name.)

- [ ] **Step 3: `cairn remote` + `push` subcommands** in `main.go`:
  - `remote` (no args) → `Repo.Remotes()` → print `name  url  (kind)`. `remote add <name> <url> [--cairn]` → `Repo.AddRemote(name, url, kind)` (kind = "cairn" if `--cairn` else "git").
  - `push [remote] [--force]` → remote default "origin"; `Repo.Push(remote, force)`; print `pushed -> <remote>` or the (non-ff/no-remote) error. `--repo` flag as elsewhere.

- [ ] **Step 4: Verify + commit.** `go test ./... && go vet ./... && go build ./...` + `GOOS=darwin/windows go build ./...`. Slice-A/B/C-clone tests stay green. Then:
```bash
git add internal/worktree/ cmd/cairn/
git commit -m "feat(worktree,cmd): cairn remote + push; clone->work->push round-trip (NEX-735)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Self-Review

**Spec coverage:** AddRemote/ListRemotes+kind + PushToRemote/branch (§2) → T1; Repo.Push + CLI remote/push + round-trip (§3) → T2. ✓
**Out of scope:** cairn→cairn fidelity (refs/cairn push + import reconstruction), C-sync, private auth. The `remote_kind` table is the recorded seam; v1 pushes the git path for both kinds (with a note for cairn). ✓
**Consistency:** reuses `Export`, go-git `Remote.Push`/`PushOptions`, the C-clone plain-path-URL + `skipOnWindows` test conventions. New exported engine API: `AddRemote`, `ListRemotes`, `RemoteInfo`, `PushToRemote`, `PushToRemoteBranch`. New `Repo` methods: `Push`, `AddRemote`, `Remotes`.
**Sharp edges:** (1) non-ff detection — go-git may not export a typed error; match the message + wrap (read the actual error in step 4/6; don't assert a brittle exact string in the test — assert push returns non-nil on non-ff and nil on force). (2) Windows local-transport flake — `skipOnWindows` on push/round-trip tests. (3) `Export` before push so stale/folded refs don't get published. (4) the round-trip helper must seed the bare with an initial commit (an empty bare has no default branch to clone).
