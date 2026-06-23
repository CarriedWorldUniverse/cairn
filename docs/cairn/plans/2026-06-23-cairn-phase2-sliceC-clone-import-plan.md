# cairn Phase 2 Slice C-clone — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** `cairn clone <git-url>` — fetch a standard git repo into the cairn engine, import it (branches→lines, tags→tags, commits stay git objects), and express the default branch as a working folder.

**Architecture:** a new engine capability `internal/change/importer.go` (`ImportFromRemote(url) → defaultBranch`) using go-git fetch + the catalogue; `worktree.Clone` wires engine import → express default; `cmd/cairn clone` is the CLI front. Plain-git import; push/sync and cairn-metadata round-trip deferred.

**Tech Stack:** Go 1.26.3 · go-git v5.13.2 (`CreateRemote`/`Remote.Fetch`/`Remote.List`, `config.RefSpec`) · the `internal/change` engine + `internal/worktree` (on main).

**Spec:** `docs/cairn/specs/2026-06-23-cairn-phase2-sliceC-clone-import-design.md`.

**Engine internals available:** `Engine{git *git.Repository, db *sql.DB, now func()time.Time}`, `RootLineName="main"`, `lineByID`, `LineByName`, `Files`, `Export`; tables `line(id,name,parent_line,tip_commit,base_commit,status,...)`, `tag(name,commit_sha,tagger,at)`. The root line is the unique `parent_line IS NULL` row.

## File Structure

| File | Responsibility |
|---|---|
| `internal/change/importer.go` | `fetchRemote`, `detectDefault`, `listHeads`/`listTags`, `ImportFromRemote`, catalogue helpers (rename root, set tip, upsert child line/tag) |
| `internal/change/importer_test.go` | engine import tests vs a local bare repo |
| `internal/worktree/clone.go` | `Clone(url, dir, author) (*Repo, error)` |
| `internal/worktree/clone_test.go` | clone → express → commit a branch |
| `internal/worktree/worktree.go` (modify) | `Express` uses any existing line (not only "main") |
| `cmd/cairn/main.go` (modify) | `clone` subcommand |
| `cmd/cairn/clone_e2e_test.go` | CLI clone smoke/e2e |

---

## Task 1: Engine fetch + ref enumeration (NEX-731)

**Files:** Create `internal/change/importer.go`, `internal/change/importer_test.go`

- [ ] **Step 1: Test helper + failing test.** Build a local bare git repo in-test using go-git, then fetch it into a fresh engine and assert the refs landed + default detected.

```go
package change

import (
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// makeBareRepoWithCommit builds a non-bare repo with one commit on "main",
// returns a file:// URL to it. (Worktree commit is the simplest way to get a
// real commit + HEAD symref for default-branch detection.)
func makeOriginRepo(t *testing.T) (url string) {
	t.Helper()
	dir := t.TempDir()
	r, err := git.PlainInit(dir, false)
	if err != nil { t.Fatalf("PlainInit: %v", err) }
	wt, _ := r.Worktree()
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hello\n"), 0o644); err != nil { t.Fatal(err) }
	wt.Add("readme.txt")
	_, err = wt.Commit("init", &git.CommitOptions{Author: &object.Signature{Name: "o", Email: "o@x"}})
	if err != nil { t.Fatalf("commit: %v", err) }
	return "file://" + dir
}

func TestFetchRemoteLandsHeadsAndDefault(t *testing.T) {
	url := makeOriginRepo(t)
	e, err := Open(t.TempDir())
	if err != nil { t.Fatalf("Open: %v", err) }
	t.Cleanup(func() { _ = e.Close() })
	if err := e.fetchRemote(url); err != nil { t.Fatalf("fetchRemote: %v", err) }
	def, err := e.detectDefault()
	if err != nil { t.Fatalf("detectDefault: %v", err) }
	// go-git PlainInit defaults HEAD to "master"
	if def != "master" && def != "main" { t.Fatalf("default = %q, want master/main", def) }
	heads, err := e.listHeads()
	if err != nil { t.Fatalf("listHeads: %v", err) }
	if _, ok := heads[def]; !ok { t.Fatalf("default head %q not in %v", def, heads) }
	// the fetched commit's tree is readable
	if _, err := e.Files(heads[def]); err != nil { t.Fatalf("Files(headTip): %v", err) }
	_ = plumbing.ZeroHash
}
```
(add `os` import.)

- [ ] **Step 2: Run, verify fail.**

- [ ] **Step 3: Implement `importer.go` fetch/list/detect**

```go
package change

import (
	"errors"
	"fmt"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
)

const originRemote = "origin"

// fetchRemote ensures an "origin" remote at url and fetches all heads + tags
// into the bare store. Idempotent (re-fetch is fine).
func (e *Engine) fetchRemote(url string) error {
	rem, err := e.git.Remote(originRemote)
	if errors.Is(err, git.ErrRemoteNotFound) {
		rem, err = e.git.CreateRemote(&config.RemoteConfig{Name: originRemote, URLs: []string{url}})
	}
	if err != nil {
		return fmt.Errorf("change.fetchRemote: %w", err)
	}
	err = rem.Fetch(&git.FetchOptions{
		RefSpecs: []config.RefSpec{
			"+refs/heads/*:refs/heads/*",
			"+refs/tags/*:refs/tags/*",
		},
		Tags: git.AllTags,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("change.fetchRemote: %w", err)
	}
	return nil
}

// detectDefault returns the remote's default branch short name (HEAD symref
// target), falling back to "main", else the sole head, else an error.
func (e *Engine) detectDefault() (string, error) {
	rem, err := e.git.Remote(originRemote)
	if err != nil {
		return "", fmt.Errorf("change.detectDefault: %w", err)
	}
	refs, err := rem.List(&git.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("change.detectDefault: %w", err)
	}
	for _, ref := range refs {
		if ref.Name() == plumbing.HEAD && ref.Type() == plumbing.SymbolicReference {
			return ref.Target().Short(), nil
		}
	}
	heads, err := e.listHeads()
	if err != nil {
		return "", err
	}
	if _, ok := heads["main"]; ok {
		return "main", nil
	}
	if len(heads) == 1 {
		for name := range heads {
			return name, nil
		}
	}
	return "", fmt.Errorf("change.detectDefault: cannot determine default branch")
}

// listHeads returns short-name → commit-sha for refs/heads/* in the store.
func (e *Engine) listHeads() (map[string]string, error) {
	return e.listRefs("refs/heads/")
}
func (e *Engine) listTags() (map[string]string, error) {
	return e.listRefs("refs/tags/")
}
func (e *Engine) listRefs(prefix string) (map[string]string, error) {
	out := map[string]string{}
	iter, err := e.git.Storer.IterReferences()
	if err != nil {
		return nil, fmt.Errorf("change.listRefs: %w", err)
	}
	defer iter.Close()
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		n := ref.Name().String()
		if len(n) > len(prefix) && n[:len(prefix)] == prefix && ref.Type() == plumbing.HashReference {
			out[n[len(prefix):]] = ref.Hash().String()
		}
		return nil
	})
	return out, err
}
```

- [ ] **Step 4: Run, verify pass.** `go test ./internal/change/ -run 'TestFetchRemote' -v`. Note: `rem.List` over `file://` requires the URL form go-git accepts — if `file://` + List misbehaves, use a plain path URL (`dir`) or read the local `refs/heads/HEAD` after fetch instead; adjust `detectDefault` to also accept a fetched local `HEAD`. Make the test pass robustly.

- [ ] **Step 5: Commit**

```bash
git add internal/change/importer.go internal/change/importer_test.go
git commit -m "feat(change): remote fetch + ref enumeration for import (NEX-731)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 2: Engine import mapping — ImportFromRemote (NEX-732)

**Files:** Modify `internal/change/importer.go`, `internal/change/importer_test.go`

- [ ] **Step 1: Failing test**

```go
func TestImportFromRemoteMapsLinesAndTags(t *testing.T) {
	url := makeOriginRepoWithBranchAndTag(t) // main + feature + tag v1 (extend the helper)
	e, _ := Open(t.TempDir())
	t.Cleanup(func() { _ = e.Close() })
	def, err := e.ImportFromRemote(url)
	if err != nil { t.Fatalf("ImportFromRemote: %v", err) }
	if def != "main" && def != "master" { t.Fatalf("default = %q", def) }

	root, err := e.LineByName(def)
	if err != nil { t.Fatalf("root line %q: %v", def, err) }
	if root.ParentLine != "" { t.Fatalf("default line must be root, parent=%q", root.ParentLine) }
	if root.TipCommit == "" { t.Fatal("root tip not set from import") }

	feat, err := e.LineByName("feature")
	if err != nil { t.Fatalf("feature line: %v", err) }
	if feat.ParentLine != root.ID { t.Fatalf("feature parent = %q, want root %q", feat.ParentLine, root.ID)}
	if feat.TipCommit == "" { t.Fatal("feature tip not set") }

	tags, _ := e.ListTags()
	found := false
	for _, tg := range tags { if tg.Name == "v1" { found = true } }
	if !found { t.Fatalf("tag v1 not imported: %v", tags) }

	// idempotent re-import: no duplicate lines
	if _, err := e.ImportFromRemote(url); err != nil { t.Fatalf("re-import: %v", err) }
	tree, _ := e.GetLineTree()
	names := map[string]int{}
	for _, n := range tree { names[n.Line.Name]++ }
	if names[def] != 1 || names["feature"] != 1 { t.Fatalf("duplicate lines after re-import: %v", names) }
}
```
(Write `makeOriginRepoWithBranchAndTag` extending the Task-1 helper: after the main commit, create a `feature` branch with another commit, and a tag `v1` on main. Use go-git worktree checkout/commit + `r.CreateTag`.)

- [ ] **Step 2: Run, verify fail.**

- [ ] **Step 3: Implement `ImportFromRemote` + catalogue helpers** in `importer.go`

```go
import (
	"database/sql"
	"time"
)

// ImportFromRemote fetches url's branches+tags and maps them into the change
// graph: default branch → the root line (renamed), other branches → flat child
// lines, tags → tag rows. Returns the default branch name. Idempotent.
func (e *Engine) ImportFromRemote(url string) (string, error) {
	if err := e.fetchRemote(url); err != nil {
		return "", err
	}
	def, err := e.detectDefault()
	if err != nil {
		return "", err
	}
	heads, err := e.listHeads()
	if err != nil {
		return "", err
	}
	tags, err := e.listTags()
	if err != nil {
		return "", err
	}
	ts := e.now().UTC().Format(time.RFC3339Nano)
	tx, err := e.db.Begin()
	if err != nil {
		return "", fmt.Errorf("change.ImportFromRemote: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// root line → default
	var rootID string
	if err := tx.QueryRow(`SELECT id FROM line WHERE parent_line IS NULL`).Scan(&rootID); err != nil {
		return "", fmt.Errorf("change.ImportFromRemote: root: %w", err)
	}
	if _, err := tx.Exec(`UPDATE line SET name=?, tip_commit=?, base_commit=?, updated_at=? WHERE id=?`,
		def, heads[def], heads[def], ts, rootID); err != nil {
		return "", fmt.Errorf("change.ImportFromRemote: rename root: %w", err)
	}
	for name, sha := range heads {
		if name == def {
			continue
		}
		if err := upsertLineTx(tx, name, rootID, sha, ts, e); err != nil {
			return "", err
		}
	}
	for name, sha := range tags {
		if _, err := tx.Exec(
			`INSERT INTO tag(name,commit_sha,tagger,at) VALUES(?,?,?,?)
			 ON CONFLICT(name) DO UPDATE SET commit_sha=excluded.commit_sha`,
			name, sha, "import", ts); err != nil {
			return "", fmt.Errorf("change.ImportFromRemote: tag %q: %w", name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("change.ImportFromRemote: %w", err)
	}
	return def, nil
}

// upsertLineTx creates a flat child line off parentID (or updates its tip if the
// name already exists), within tx.
func upsertLineTx(tx *sql.Tx, name, parentID, sha, ts string, e *Engine) error {
	var id string
	err := tx.QueryRow(`SELECT id FROM line WHERE name=?`, name).Scan(&id)
	if err == sql.ErrNoRows {
		_, err = tx.Exec(
			`INSERT INTO line(id,name,parent_line,tip_commit,base_commit,status,created_at,updated_at)
			 VALUES(?,?,?,?,?, 'open', ?, ?)`,
			newID(), name, parentID, sha, sha, ts, ts)
		return wrapImp(err)
	}
	if err != nil {
		return wrapImp(err)
	}
	_, err = tx.Exec(`UPDATE line SET tip_commit=?, base_commit=?, updated_at=? WHERE id=?`, sha, sha, ts, id)
	return wrapImp(err)
}
func wrapImp(err error) error {
	if err == nil { return nil }
	return fmt.Errorf("change.ImportFromRemote: %w", err)
}
```
(Verify the `tag` table has a UNIQUE/PK on `name` for the ON CONFLICT — it does, `name TEXT PRIMARY KEY`. The `ON CONFLICT(name)` upsert is for idempotent re-import.)

- [ ] **Step 4: Run, verify pass.** `go test ./internal/change/ -run 'TestImportFromRemote|TestFetchRemote' -v` + full `go test ./internal/change/` (no regressions).

- [ ] **Step 5: Commit**

```bash
git add internal/change/importer.go internal/change/importer_test.go
git commit -m "feat(change): ImportFromRemote — git refs to change-graph (NEX-732)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 3: worktree.Clone + Express adjustment + cairn clone CLI + e2e (NEX-733)

**Files:** Create `internal/worktree/clone.go`, `internal/worktree/clone_test.go`; modify `internal/worktree/worktree.go` (`Express`); modify `cmd/cairn/main.go`; create `cmd/cairn/clone_e2e_test.go`

- [ ] **Step 1: Adjust `Express` to use ANY existing line, not just "main".** Today `Express` special-cases `branch == change.RootLineName`. Change it so: if `LineByName(branch)` succeeds (line exists — root under any name, or any prior line), use it; only when `LineByName` returns `ErrNotFound` do the create-child path. (This makes `Express("master","")` work for an imported root named `master`.) Keep the existing converge/abandon tests green.

- [ ] **Step 2: Failing test** (`clone_test.go`)

```go
package worktree

import (
	"os"
	"path/filepath"
	"testing"
	// reuse a local bare-repo builder — either import a tiny helper or build inline with go-git
)

func TestCloneImportsAndExpresses(t *testing.T) {
	url := makeOriginRepoWT(t) // local file:// repo with main+readme (helper in this test file)
	dir := filepath.Join(t.TempDir(), "myrepo")
	r, err := Clone(url, dir, "tester")
	if err != nil { t.Fatalf("Clone: %v", err) }
	t.Cleanup(func() { _ = r.Close() })
	// default branch expressed with the repo's file on disk
	got, err := os.ReadFile(filepath.Join(dir, "main", "readme.txt"))
	if err != nil {
		// default might be "master" under go-git PlainInit; check both
		got, err = os.ReadFile(filepath.Join(dir, "master", "readme.txt"))
	}
	if err != nil { t.Fatalf("expressed default not found: %v", err) }
	if string(got) != "hello\n" { t.Fatalf("readme = %q", got) }
}
```

- [ ] **Step 3: Implement `clone.go`**

```go
package worktree

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

// Clone fetches a git remote into a new repo at dir, imports it into the change
// graph, and expresses the default branch as a working folder.
func Clone(url, dir, author string) (*Repo, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("worktree.Clone: %w", err)
	}
	eng, err := change.Open(filepath.Join(dir, ".cairn"))
	if err != nil {
		return nil, fmt.Errorf("worktree.Clone: %w", err)
	}
	def, err := eng.ImportFromRemote(url)
	if err != nil {
		_ = eng.Close()
		return nil, fmt.Errorf("worktree.Clone: %w", err)
	}
	stPath := filepath.Join(dir, ".cairn", "wc.json")
	st, err := LoadState(stPath)
	if err != nil {
		_ = eng.Close()
		return nil, err
	}
	r := &Repo{root: dir, author: author, eng: eng, st: st, stPath: stPath}
	if err := r.Express(def, ""); err != nil {
		_ = eng.Close()
		return nil, err
	}
	return r, nil
}
```

- [ ] **Step 4: `cairn clone` subcommand** in `main.go`: `run(["clone", url, dir?])` → if dir omitted, derive from url (last path component, strip trailing `.git`); `--author` as usual; `worktree.Clone(url, dir, author)`; print `cloned <url> -> <dir>`. (No `--repo` flag — clone creates the repo.)

- [ ] **Step 5: e2e** (`cmd/cairn/clone_e2e_test.go`): build a local bare repo, `run([]string{"clone", url, dir})`, assert the default folder + file exist; then `run(["express","--repo",dir,...])` on a branch if present.

- [ ] **Step 6: Verify + commit**

`go test ./... && go vet ./... && go build ./...` + `GOOS=darwin/windows go build ./...`. Confirm the Slice-A/B `worktree` + `cmd/cairn` tests stay green (the `Express` change must not break them).

```bash
git add internal/worktree/ cmd/cairn/
git commit -m "feat(worktree,cmd): cairn clone — import + express default (NEX-733)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Self-Review

**Spec coverage:** fetch+detect (§2) → T1; ImportFromRemote mapping (§2) → T2; worktree.Clone + Express adjust + CLI + e2e (§3) → T3. ✓
**Out of scope:** push/sync, cairn-metadata round-trip, merge-base parentage. ✓
**Type/consistency:** uses real engine internals (`e.git`, `e.db`, `RootLineName`, `newID`, `LineByName`, `Files`, `GetLineTree`, `ListTags`); `ImportFromRemote` is the one new exported engine method; `worktree.Clone` the one new exported worktree fn; `Express` generalized from "main"-special-case to "any existing line". Tag upsert relies on `tag.name` PK (present).
**Sharp edges:** (1) `file://` + go-git `Remote.List` for HEAD detection may need a fallback (read fetched local HEAD or accept master/main) — handled in T1 step 4. (2) `Express` change must keep Slice-A tests green (root is now matched by existing-line, not literal "main"). (3) import is atomic (one tx) + idempotent (re-fetch + upserts). (4) tests must tolerate go-git's default branch being `master` (PlainInit) — assert against the returned default, not a hardcoded "main".
