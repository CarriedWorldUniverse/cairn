# cairn Phase 2 Slice A — Working-Copy CLI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** a local `cairn` CLI (`cmd/cairn`) + `internal/worktree` package that expresses branches as plain folders, lets agents edit with normal tools, and `commit`s to drive the Phase-1 convergence engine — proving two expressed branches converge with no merge step.

**Architecture:** `internal/worktree` bridges a directory ⇄ the `internal/change` engine: materialize a commit's tree to a folder, scan a folder back to a files map, track `wc.json` (branch → {path, change_id}), and orchestrate express/commit/fold/etc. `cmd/cairn` is a thin stdlib-`flag` subcommand dispatcher over `worktree.Repo`. No CoW (plain copy), no remotes — those are Slices B/C.

**Tech Stack:** Go 1.26.3 · `internal/change` engine (already on main) · stdlib `flag`/`os`/`path/filepath` · `encoding/json` for `wc.json`. No new dependencies.

**Spec:** `docs/cairn/specs/2026-06-22-cairn-phase2-sliceA-worktree-cli-design.md`.

**Engine API available (exported, package `change`):** `Open(dir)(*Engine,error)`, `(*Engine)`: `LineByName(name)`, `CreateLine(name,parent)`, `CreateChange(lineID,author)`, `Commit(changeID,files map[string][]byte)(CommitResult,error)`, `GetChange(id)`, `FoldLine(lineID)`, `AbandonLine(lineID)`, `Conflicts(changeID)`, `ResolveConflict(changeID,path,bytes)`, `GetLineage(lineID)`, `GetLineTree()`, `Files(commitSha)(map[string][]byte,error)`, `Export()`, `Close()`. Types: `Line{ID,Name,ParentLine,TipCommit,BaseCommit,Status}`, `Change{ID,LineID,Author,HeadCommit,Status,HasConflict}`, `CommitResult{HeadCommit,Conflicts}`, `Conflict{...,Path,Status}`, `LineNode{Line,Parent,Ahead}`. Errors: `change.ErrNotFound`, `change.ErrHasConflict`.

## File Structure

| File | Responsibility |
|---|---|
| `internal/worktree/state.go` | `wc.json`: `State{Expressed map[string]Entry}`, `Entry{Path,ChangeID}`, `LoadState`/`SaveState` |
| `internal/worktree/fs.go` | `Materialize(eng,commitSha,dir)`, `Scan(dir)` |
| `internal/worktree/worktree.go` | `Repo` wrapper: `Open`/`Close`, `Express`/`Unexpress`/`Commit`/`Fold`/`Abandon`/`Status`/`Tree`/`Ls`/`Resolve` |
| `cmd/cairn/main.go` | subcommand dispatch + flags → `worktree.Repo` |

Core types (defined in Task 1/3; keep names exact):
```go
// state.go
type Entry struct { Path, ChangeID string }
type State struct { Expressed map[string]Entry `json:"expressed"` }

// worktree.go
type Repo struct {
	root string         // repo root dir
	eng  *change.Engine
	st   *State
}
type StatusInfo struct {
	Branch    string
	Lineage   []string   // names root-first
	Ahead     int
	Conflicts []string   // conflicting paths
	Expressed []string   // expressed branch names
}
```

---

## Task 1: wc.json state (NEX-723)

**Files:** Create `internal/worktree/state.go`, `internal/worktree/state_test.go`

- [ ] **Step 1: Failing test** (`state_test.go`)

```go
package worktree

import (
	"path/filepath"
	"testing"
)

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "wc.json")
	st := &State{Expressed: map[string]Entry{
		"main": {Path: "main", ChangeID: "zxy1"},
		"exp":  {Path: "exp", ChangeID: "zab2"},
	}}
	if err := SaveState(p, st); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	got, err := LoadState(p)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if len(got.Expressed) != 2 || got.Expressed["exp"].ChangeID != "zab2" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestLoadStateMissingReturnsEmpty(t *testing.T) {
	got, err := LoadState(filepath.Join(t.TempDir(), "none.json"))
	if err != nil {
		t.Fatalf("LoadState missing: %v", err)
	}
	if got == nil || len(got.Expressed) != 0 {
		t.Fatalf("missing state should be empty, got %+v", got)
	}
}
```

- [ ] **Step 2: Run, verify fail.** `go test ./internal/worktree/ -run TestState -v` → FAIL (undefined).

- [ ] **Step 3: Implement `state.go`**

```go
// Package worktree bridges expressed branch folders and the cairn change engine.
package worktree

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

type Entry struct {
	Path     string `json:"path"`
	ChangeID string `json:"change_id"`
}

type State struct {
	Expressed map[string]Entry `json:"expressed"`
}

// LoadState reads wc.json; a missing file yields an empty (non-nil) State.
func LoadState(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &State{Expressed: map[string]Entry{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("worktree.LoadState: %w", err)
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("worktree.LoadState: %w", err)
	}
	if s.Expressed == nil {
		s.Expressed = map[string]Entry{}
	}
	return &s, nil
}

// SaveState writes wc.json atomically (temp + rename).
func SaveState(path string, s *State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("worktree.SaveState: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("worktree.SaveState: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("worktree.SaveState: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run, verify pass.** `go test ./internal/worktree/ -run TestState -v` → PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/worktree/state.go internal/worktree/state_test.go
git commit -m "feat(worktree): wc.json state load/save (NEX-723)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 2: Materialize + Scan (NEX-724)

**Files:** Create `internal/worktree/fs.go`, `internal/worktree/fs_test.go`

- [ ] **Step 1: Failing test** (`fs_test.go`) — uses a real engine to produce a commit, then round-trips through the filesystem.

```go
package worktree

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

func TestMaterializeScanRoundTrip(t *testing.T) {
	eng, err := change.Open(t.TempDir())
	if err != nil { t.Fatalf("Open: %v", err) }
	t.Cleanup(func() { _ = eng.Close() })
	main, _ := eng.LineByName("main")
	ch, _ := eng.CreateChange(main.ID, "t")
	files := map[string][]byte{"a.txt": []byte("a\n"), "dir/b.txt": []byte("b\n")}
	r, err := eng.Commit(ch.ID, files)
	if err != nil { t.Fatalf("Commit: %v", err) }

	dir := filepath.Join(t.TempDir(), "wc")
	if err := Materialize(eng, r.HeadCommit, dir); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	got, err := Scan(dir)
	if err != nil { t.Fatalf("Scan: %v", err) }
	if string(got["a.txt"]) != "a\n" || string(got["dir/b.txt"]) != "b\n" || len(got) != 2 {
		t.Fatalf("round-trip mismatch: %v", got)
	}
}

func TestMaterializeClearsStaleFiles(t *testing.T) {
	eng, _ := change.Open(t.TempDir())
	t.Cleanup(func() { _ = eng.Close() })
	main, _ := eng.LineByName("main")
	ch, _ := eng.CreateChange(main.ID, "t")
	r1, _ := eng.Commit(ch.ID, map[string][]byte{"keep.txt": []byte("1\n"), "gone.txt": []byte("x\n")})

	dir := filepath.Join(t.TempDir(), "wc")
	if err := Materialize(eng, r1.HeadCommit, dir); err != nil { t.Fatalf("mat1: %v", err) }
	// new commit dropping gone.txt
	r2, _ := eng.Commit(ch.ID, map[string][]byte{"keep.txt": []byte("2\n")})
	if err := Materialize(eng, r2.HeadCommit, dir); err != nil { t.Fatalf("mat2: %v", err) }
	if _, err := os.Stat(filepath.Join(dir, "gone.txt")); !os.IsNotExist(err) {
		t.Fatal("stale gone.txt not removed after re-materialize")
	}
}
```

- [ ] **Step 2: Run, verify fail.**

- [ ] **Step 3: Implement `fs.go`**

```go
package worktree

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

// Materialize writes the tree at commitSha into dir, replacing dir's current
// contents (so a re-materialize reflects exactly the commit — stale files gone).
func Materialize(eng *change.Engine, commitSha, dir string) error {
	files, err := eng.Files(commitSha)
	if err != nil {
		return fmt.Errorf("worktree.Materialize: %w", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("worktree.Materialize: clear: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("worktree.Materialize: mkdir: %w", err)
	}
	for p, data := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("worktree.Materialize: %w", err)
		}
		if err := os.WriteFile(full, data, 0o644); err != nil {
			return fmt.Errorf("worktree.Materialize: %w", err)
		}
	}
	return nil
}

// Scan walks dir into a path->bytes map with "/"-relative keys (regular files only).
func Scan(dir string) (map[string][]byte, error) {
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("worktree.Scan: %w", err)
	}
	return out, nil
}
```

- [ ] **Step 4: Run, verify pass.**

- [ ] **Step 5: Commit**

```bash
git add internal/worktree/fs.go internal/worktree/fs_test.go
git commit -m "feat(worktree): Materialize + Scan (tree<->dir) (NEX-724)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 3: worktree.Repo orchestration (NEX-725)

**Files:** Create `internal/worktree/worktree.go`, `internal/worktree/worktree_test.go`

- [ ] **Step 1: Failing test — the converge demo at the package API level**

```go
package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRepoTwoBranchConverge(t *testing.T) {
	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil { t.Fatalf("Open: %v", err) }
	t.Cleanup(func() { _ = r.Close() })

	// seed main with a base file, commit it
	if err := os.WriteFile(filepath.Join(root, "main", "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write base: %v", err)
	}
	if _, err := r.Commit("main", ""); err != nil { t.Fatalf("commit main base: %v", err) }

	// express exp off main; edit different files in each
	if err := r.Express("exp", "main"); err != nil { t.Fatalf("Express: %v", err) }
	if err := os.WriteFile(filepath.Join(root, "main", "m.txt"), []byte("M\n"), 0o644); err != nil { t.Fatal(err) }
	if err := os.WriteFile(filepath.Join(root, "exp", "e.txt"), []byte("E\n"), 0o644); err != nil { t.Fatal(err) }

	if _, err := r.Commit("main", ""); err != nil { t.Fatalf("commit main: %v", err) }
	res, err := r.Commit("exp", "")
	if err != nil { t.Fatalf("commit exp: %v", err) }
	if len(res.Conflicts) != 0 { t.Fatalf("unexpected conflicts: %v", res.Conflicts) }

	if err := r.Fold("exp"); err != nil { t.Fatalf("Fold: %v", err) }

	// main folder now holds BOTH edits, no merge step
	got, err := Scan(filepath.Join(root, "main"))
	if err != nil { t.Fatalf("Scan main: %v", err) }
	if string(got["m.txt"]) != "M\n" || string(got["e.txt"]) != "E\n" || string(got["base.txt"]) != "base\n" {
		t.Fatalf("main did not converge: %v", got)
	}
}
```

- [ ] **Step 2: Run, verify fail.**

- [ ] **Step 3: Implement `worktree.go`** — full orchestration. Key methods:

```go
package worktree

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

type Repo struct {
	root   string
	author string
	eng    *change.Engine
	st     *State
	stPath string
}

// Open opens/creates a repo at root: engine at root/.cairn, loads wc.json, and
// (on first init) expresses the default branch "main".
func Open(root, author string) (*Repo, error) {
	cairnDir := filepath.Join(root, ".cairn")
	eng, err := change.Open(cairnDir)
	if err != nil {
		return nil, fmt.Errorf("worktree.Open: %w", err)
	}
	stPath := filepath.Join(cairnDir, "wc.json")
	st, err := LoadState(stPath)
	if err != nil {
		_ = eng.Close()
		return nil, err
	}
	r := &Repo{root: root, author: author, eng: eng, st: st, stPath: stPath}
	// First-run: express main if not present.
	if _, ok := st.Expressed["main"]; !ok {
		if err := r.Express("main", ""); err != nil {
			_ = eng.Close()
			return nil, err
		}
	}
	return r, nil
}

func (r *Repo) Close() error { return r.eng.Close() }

func (r *Repo) save() error { return SaveState(r.stPath, r.st) }

// Express creates the line (if new, off parent — default "main") + an open
// change, materializes the line tip into root/<branch>/, records it.
func (r *Repo) Express(branch, parent string) error {
	if _, ok := r.st.Expressed[branch]; ok {
		return nil // already expressed
	}
	line, err := r.eng.LineByName(branch)
	if err != nil {
		// create it
		if parent == "" {
			parent = "main"
		}
		// root line "main" is created by engine.Open; only non-root lines are created here.
		if branch == change.RootLineName {
			line, err = r.eng.LineByName(change.RootLineName)
		} else {
			pl, perr := r.eng.LineByName(parent)
			if perr != nil {
				return fmt.Errorf("worktree.Express: parent %q: %w", parent, perr)
			}
			line, err = r.eng.CreateLine(branch, pl.ID)
		}
		if err != nil {
			return fmt.Errorf("worktree.Express: %w", err)
		}
	}
	ch, err := r.eng.CreateChange(line.ID, r.author)
	if err != nil {
		return fmt.Errorf("worktree.Express: %w", err)
	}
	dir := filepath.Join(r.root, branch)
	tip := line.TipCommit
	if tip != "" {
		if err := Materialize(r.eng, tip, dir); err != nil {
			return err
		}
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("worktree.Express: %w", err)
	}
	r.st.Expressed[branch] = Entry{Path: branch, ChangeID: ch.ID}
	return r.save()
}

func (r *Repo) dir(branch string) string { return filepath.Join(r.root, r.st.Expressed[branch].Path) }

// Commit scans the branch folder, commits to its change (merge-forward), then
// re-materializes the folder to the new head.
func (r *Repo) Commit(branch, _msg string) (change.CommitResult, error) {
	e, ok := r.st.Expressed[branch]
	if !ok {
		return change.CommitResult{}, fmt.Errorf("worktree.Commit: %q not expressed", branch)
	}
	files, err := Scan(r.dir(branch))
	if err != nil {
		return change.CommitResult{}, err
	}
	res, err := r.eng.Commit(e.ChangeID, files)
	if err != nil {
		return change.CommitResult{}, fmt.Errorf("worktree.Commit: %w", err)
	}
	ch, err := r.eng.GetChange(e.ChangeID)
	if err != nil {
		return change.CommitResult{}, fmt.Errorf("worktree.Commit: %w", err)
	}
	if err := Materialize(r.eng, ch.HeadCommit, r.dir(branch)); err != nil {
		return change.CommitResult{}, err
	}
	return res, nil
}

// Fold folds the branch into its parent, unexpresses it, and re-materializes the
// parent folder if it's expressed.
func (r *Repo) Fold(branch string) error {
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return fmt.Errorf("worktree.Fold: %w", err)
	}
	if err := r.eng.FoldLine(line.ID); err != nil {
		return fmt.Errorf("worktree.Fold: %w", err)
	}
	// find parent name for re-materialize
	if err := r.Unexpress(branch); err != nil {
		return err
	}
	// re-materialize parent if expressed
	for name, ent := range r.st.Expressed {
		pl, err := r.eng.LineByName(name)
		if err == nil && pl.ID == line.ParentLine {
			ch, gerr := r.eng.GetChange(ent.ChangeID)
			if gerr == nil && ch.HeadCommit != "" {
				_ = Materialize(r.eng, pl.TipCommit, r.dir(name))
			}
		}
	}
	return nil
}

func (r *Repo) Unexpress(branch string) error {
	if _, ok := r.st.Expressed[branch]; !ok {
		return nil
	}
	if err := os.RemoveAll(r.dir(branch)); err != nil {
		return fmt.Errorf("worktree.Unexpress: %w", err)
	}
	delete(r.st.Expressed, branch)
	return r.save()
}

func (r *Repo) Abandon(branch string) error {
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return fmt.Errorf("worktree.Abandon: %w", err)
	}
	if err := r.eng.AbandonLine(line.ID); err != nil {
		return fmt.Errorf("worktree.Abandon: %w", err)
	}
	return r.Unexpress(branch)
}

// Resolve writes the on-disk file as the resolution, then re-materializes.
func (r *Repo) Resolve(branch, path string) error {
	e, ok := r.st.Expressed[branch]
	if !ok {
		return fmt.Errorf("worktree.Resolve: %q not expressed", branch)
	}
	data, err := os.ReadFile(filepath.Join(r.dir(branch), filepath.FromSlash(path)))
	if err != nil {
		return fmt.Errorf("worktree.Resolve: %w", err)
	}
	if err := r.eng.ResolveConflict(e.ChangeID, path, data); err != nil {
		return fmt.Errorf("worktree.Resolve: %w", err)
	}
	ch, err := r.eng.GetChange(e.ChangeID)
	if err != nil {
		return err
	}
	return Materialize(r.eng, ch.HeadCommit, r.dir(branch))
}

// Status, Tree, Ls return data for the CLI to print.
func (r *Repo) Status(branch string) (StatusInfo, error) {
	e, ok := r.st.Expressed[branch]
	if !ok {
		return StatusInfo{}, fmt.Errorf("worktree.Status: %q not expressed", branch)
	}
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return StatusInfo{}, err
	}
	lin, _ := r.eng.GetLineage(line.ID)
	names := make([]string, len(lin))
	for i, l := range lin {
		names[i] = l.Name
	}
	confs, _ := r.eng.Conflicts(e.ChangeID)
	cpaths := make([]string, len(confs))
	for i, c := range confs {
		cpaths[i] = c.Path
	}
	ahead := 0
	tree, _ := r.eng.GetLineTree()
	for _, n := range tree {
		if n.Line.ID == line.ID {
			ahead = n.Ahead
		}
	}
	var expr []string
	for name := range r.st.Expressed {
		expr = append(expr, name)
	}
	return StatusInfo{Branch: branch, Lineage: names, Ahead: ahead, Conflicts: cpaths, Expressed: expr}, nil
}

func (r *Repo) Tree() ([]change.LineNode, error) { return r.eng.GetLineTree() }

func (r *Repo) Ls() map[string]Entry { return r.st.Expressed }

type StatusInfo struct {
	Branch    string
	Lineage   []string
	Ahead     int
	Conflicts []string
	Expressed []string
}
```

- [ ] **Step 4: Run, verify pass.** `go test ./internal/worktree/ -v` → all PASS.

- [ ] **Step 5: Add conflict/resolve test** and confirm it passes:

```go
func TestRepoConflictThenResolve(t *testing.T) {
	root := t.TempDir()
	r, _ := Open(root, "t")
	t.Cleanup(func() { _ = r.Close() })
	os.WriteFile(filepath.Join(root, "main", "f.txt"), []byte("base\n"), 0o644)
	r.Commit("main", "")
	r.Express("exp", "main")            // forks at base
	os.WriteFile(filepath.Join(root, "main", "f.txt"), []byte("X\n"), 0o644)
	r.Commit("main", "")                // main advances
	os.WriteFile(filepath.Join(root, "exp", "f.txt"), []byte("Y\n"), 0o644)
	res, _ := r.Commit("exp", "")       // 3-way overlap -> conflict
	if len(res.Conflicts) == 0 { t.Fatal("expected conflict") }
	// resolve: write the resolution to disk, then Resolve
	os.WriteFile(filepath.Join(root, "exp", "f.txt"), []byte("resolved\n"), 0o644)
	if err := r.Resolve("exp", "f.txt"); err != nil { t.Fatalf("Resolve: %v", err) }
	if err := r.Fold("exp"); err != nil { t.Fatalf("Fold after resolve: %v", err) }
	got, _ := Scan(filepath.Join(root, "main"))
	if string(got["f.txt"]) != "resolved\n" { t.Fatalf("main f.txt = %q, want resolved", got["f.txt"]) }
}
```

- [ ] **Step 6: Commit**

```bash
git add internal/worktree/worktree.go internal/worktree/worktree_test.go
git commit -m "feat(worktree): Repo orchestration — express/commit/fold/resolve (NEX-725)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 4: cmd/cairn CLI (NEX-726)

**Files:** Create `cmd/cairn/main.go`, `cmd/cairn/main_test.go`

- [ ] **Step 1: Failing test** — a smoke test invoking the dispatch function (extract a `run(args []string, cwd string) error` so it's testable without `os.Exit`).

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCLIInitExpressCommit(t *testing.T) {
	root := t.TempDir()
	if err := run([]string{"init", root}); err != nil { t.Fatalf("init: %v", err) }
	if _, err := os.Stat(filepath.Join(root, ".cairn")); err != nil { t.Fatalf("no .cairn: %v", err) }
	if _, err := os.Stat(filepath.Join(root, "main")); err != nil { t.Fatalf("no main folder: %v", err) }
	// write + commit in main
	os.WriteFile(filepath.Join(root, "main", "x.txt"), []byte("x\n"), 0o644)
	if err := run([]string{"commit", "--repo", root, "main"}); err != nil { t.Fatalf("commit: %v", err) }
	// express + ls
	if err := run([]string{"express", "--repo", root, "exp"}); err != nil { t.Fatalf("express: %v", err) }
	if _, err := os.Stat(filepath.Join(root, "exp")); err != nil { t.Fatalf("no exp folder: %v", err) }
}
```

- [ ] **Step 2: Run, verify fail.**

- [ ] **Step 3: Implement `main.go`** — stdlib dispatch. `run(args)` parses `args[0]` as the subcommand; each subcommand has a `flag.FlagSet` with a `--repo` (default `.`) and `--author` (default `$CAIRN_AUTHOR` or `os.Getenv("USER")`), opens `worktree.Open(repo, author)`, calls the method, prints results. `init` takes an optional positional dir. `main()` calls `run(os.Args[1:])` and `os.Exit(1)` on error. Provide a `usage()` for unknown/empty subcommand. Implement subcommands: `init`, `express` (`--from`), `unexpress`, `commit` (`-m`), `fold`, `abandon`, `status`, `tree`, `ls`, `resolve`. Print conflicts from `Commit` as a clear "N conflicts in: …" line (exit 0). Map `change.ErrHasConflict`/`ErrNotFound` to clear messages.

- [ ] **Step 4: Run, verify pass.** `go test ./cmd/cairn/ -v` and `go build ./cmd/cairn`.

- [ ] **Step 5: Commit**

```bash
git add cmd/cairn/
git commit -m "feat(cmd/cairn): CLI dispatch over the worktree package (NEX-726)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 5: End-to-end CLI integration test (NEX-727)

**Files:** Create `cmd/cairn/e2e_test.go`

- [ ] **Step 1: Write the DoD demo as a CLI-driven test** — build/run the `run()` dispatch through the full two-branch converge demo AND the conflict/resolve demo, asserting on the on-disk `main/` folder contents at the end. (Mirror the worktree_test demos but exercised purely through `run([]string{...})` calls, proving the CLI wiring.)

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestE2E_TwoBranchConvergeViaCLI(t *testing.T) {
	root := t.TempDir()
	mustRun(t, "init", root)
	os.WriteFile(filepath.Join(root, "main", "base.txt"), []byte("base\n"), 0o644)
	mustRun(t, "commit", "--repo", root, "main")
	mustRun(t, "express", "--repo", root, "exp")
	os.WriteFile(filepath.Join(root, "main", "m.txt"), []byte("M\n"), 0o644)
	os.WriteFile(filepath.Join(root, "exp", "e.txt"), []byte("E\n"), 0o644)
	mustRun(t, "commit", "--repo", root, "main")
	mustRun(t, "commit", "--repo", root, "exp")
	mustRun(t, "fold", "--repo", root, "exp")
	for _, f := range []string{"base.txt", "m.txt", "e.txt"} {
		if _, err := os.Stat(filepath.Join(root, "main", f)); err != nil {
			t.Fatalf("main missing %s after converge: %v", f, err)
		}
	}
}

func mustRun(t *testing.T, args ...string) {
	t.Helper()
	if err := run(args); err != nil { t.Fatalf("run %v: %v", args, err) }
}
```

- [ ] **Step 2: Run, verify pass.** `go test ./cmd/cairn/ -v`.

- [ ] **Step 3: Full gate.** `go test ./... && go vet ./... && go build ./...` → clean.

- [ ] **Step 4: Commit**

```bash
git add cmd/cairn/e2e_test.go
git commit -m "test(cmd/cairn): e2e two-branch converge + resolve via CLI (NEX-727)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Self-Review

**Spec coverage (§8 build sequence → tasks):** wc.json state → T1; Materialize+Scan → T2; Repo orchestration → T3; cmd/cairn → T4; integration demo → T5. ✓ All commands in spec §4 are implemented in T3 (Repo methods) + T4 (CLI). The DoD demo (§6) is T3 + T5.

**Out of scope (correctly absent):** OverlayFS CoW, clone/push/sync, version/privacy CLI — Slices B/C / later phases.

**Type consistency:** `Entry{Path,ChangeID}`, `State{Expressed}`, `Repo{root,author,eng,st,stPath}`, `StatusInfo` defined once; engine types/methods used match the merged Phase-1 API (`Files`, `Commit`, `CommitResult`, `LineByName`, `CreateLine`, `CreateChange`, `FoldLine`, `AbandonLine`, `ResolveConflict`, `GetChange`, `GetLineage`, `GetLineTree`, `RootLineName`).

**Sharp edges flagged:** (1) `Express` must not re-create the root line `main` (engine.Open already made it) — handled by the `branch == change.RootLineName` branch. (2) `commit`/`resolve` read from the expressed folder, so the branch must be expressed — guarded. (3) re-materialize after commit/resolve/fold is what surfaces adopted-parent content + diff3 markers on disk. (4) `Fold` re-materializes the parent folder so the human sees the folded result.
