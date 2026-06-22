# Cairn Convergence Core — Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build cairn's multi-agent convergence engine — a `change`/`line` graph on go-git with continuous (commit-triggered) merge-forward, conflicts-as-data, an operation log, fast-forward fold, zero-blast-radius abandon, lineage, tags, and git-compat export — proven by a concurrency harness.

**Architecture:** A new in-process Go library package `internal/change` (sibling to `internal/repo`), engine-per-repo, local-first (no server). go-git owns the object store; a SQLite catalogue (embedded `schema.sql`, same pattern as `internal/repo`) owns lines/changes/conflicts/tags/op-log. The hard part — three-way merge — is a self-contained `internal/change/diff3` package built on line-matching blocks, unit-tested independently. Everything is exercised through the library API + a concurrency harness; no frontends, no porter, no ledger.

**Tech Stack:** Go 1.26.3 · `github.com/go-git/go-git/v5` v5.13.2 (objects/refs) · `modernc.org/sqlite` (catalogue) · `github.com/pmezard/go-difflib/difflib` (line-matching blocks for diff3, pinned) · standard `testing`.

**Spec:** `docs/cairn/specs/2026-06-22-cairn-convergence-core-spec.md`. **Jira:** epic NEX-705, stories NEX-706…716.

---

## File Structure

All under `internal/change/` unless noted. Each file = one responsibility.

| File | Responsibility | Story |
|---|---|---|
| `schema.sql` | Catalogue DDL: line, change, conflict, tag, operation | 706/707/708 |
| `engine.go` | `Engine` type, `Open`, root-line bootstrap, shared helpers (`newID`, time) | 706 |
| `gitobj.go` | go-git helpers: `writeTree(map)→hash`, `readTree(hash)→map`, `writeCommit`, `mergeBase` | 706 |
| `change.go` | `Change` type + `CreateChange`, change_id generation, `Change-Id` trailer | 706 |
| `line.go` | `Line` type + `CreateLine`, parent pointer, `GetLineage`, `GetLineTree` | 707 |
| `oplog.go` | `Operation` type, append, `OperationLog`, `Undo`, view-map snapshot/restore | 708 |
| `diff3/diff3.go` | `Merge3(base, ours, theirs []string) Result` — the three-way line merge | 709 |
| `merge.go` | `Commit` (snapshot + merge-forward), per-path tree classify, conflict materialise | 710/712 |
| `conflict.go` | `Conflict` type, `Conflicts`, `ResolveConflict` | 712 |
| `fold.go` | `FoldLine` (fast-forward), `AbandonLine` | 711 |
| `tag.go` | `Tag`, `ListTags` | 713 |
| `export.go` | git-compat ref projection (`refs/heads/*`, `refs/cairn/change/*`, `refs/tags/*`) + lineage trailer | 714 |
| `harness/harness.go` | Concurrency harness: simulated agents + seedable scheduler | 715 |
| `harness/properties_test.go` | The 9 Phase-1 properties | 715 |
| `grpcapi` (in `internal/grpcapi`) | Thin JSON-over-gRPC wrapper for the engine API | 716 |

**Core types (defined in Task 1/2; used everywhere — keep names exact):**

```go
type Engine struct {
	dir string
	git *git.Repository
	db  *sql.DB
}

type Line struct {
	ID         string
	Name       string
	ParentLine string // "" for the root line
	TipCommit  string // hex sha, "" if no commit yet
	BaseCommit string // parent tip last adopted
	Status     string // "open" | "folded" | "abandoned"
}

type Change struct {
	ID          string // stable change_id (reverse-hex)
	LineID      string
	Author      string
	HeadCommit  string // hex sha, moves with each commit
	Status      string // "open" | "folded" | "abandoned"
	HasConflict bool
}

type Conflict struct {
	ID         string
	ChangeID   string
	Path       string
	BaseBlob   string // hex sha
	ParentBlob string
	ChangeBlob string
	MarkedBlob string // hex sha of diff3-marked blob
	Status     string // "open" | "resolved"
}

type Operation struct {
	ID         string // ULID-ish, monotonic via injected clock
	OpType     string // branch|commit|rebase|fold|abandon|tag|resolve|undo
	Actor      string
	ParentOp   string
	ViewBefore map[string]string // ref-map: name → sha
	ViewAfter  map[string]string
	Detail     string // JSON
}

// CommitResult is returned by Commit.
type CommitResult struct {
	HeadCommit string
	Conflicts  []Conflict // newly-materialised this commit (may be empty)
}
```

---

## Task 1: Engine bootstrap + git object helpers (NEX-706, part 1)

**Files:**
- Create: `internal/change/schema.sql`
- Create: `internal/change/engine.go`
- Create: `internal/change/gitobj.go`
- Test: `internal/change/engine_test.go`, `internal/change/gitobj_test.go`

- [ ] **Step 1: Write `schema.sql`** (the full catalogue up front; later tasks only add rows)

```sql
-- cairn convergence-core catalogue. go-git owns objects/refs; this owns the
-- change-graph: lines, changes, conflicts, tags, and the operation log.
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS line (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  parent_line TEXT,                       -- NULL for the root line
  tip_commit  TEXT NOT NULL DEFAULT '',
  base_commit TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL DEFAULT 'open',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS change (
  id           TEXT PRIMARY KEY,          -- stable change_id (reverse-hex)
  line_id      TEXT NOT NULL REFERENCES line(id) ON DELETE CASCADE,
  author       TEXT NOT NULL,
  head_commit  TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT 'open',
  has_conflict INTEGER NOT NULL DEFAULT 0,
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS conflict (
  id          TEXT PRIMARY KEY,
  change_id   TEXT NOT NULL REFERENCES change(id) ON DELETE CASCADE,
  path        TEXT NOT NULL,
  base_blob   TEXT NOT NULL DEFAULT '',
  parent_blob TEXT NOT NULL DEFAULT '',
  change_blob TEXT NOT NULL DEFAULT '',
  marked_blob TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL DEFAULT 'open',
  created_at  TEXT NOT NULL,
  resolved_at TEXT
);

CREATE TABLE IF NOT EXISTS tag (
  name   TEXT PRIMARY KEY,
  commit_sha TEXT NOT NULL,
  tagger TEXT NOT NULL,
  at     TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS operation (
  id          TEXT PRIMARY KEY,
  op_type     TEXT NOT NULL,
  actor       TEXT NOT NULL,
  parent_op   TEXT NOT NULL DEFAULT '',
  view_before TEXT NOT NULL,              -- JSON {name: sha}
  view_after  TEXT NOT NULL,             -- JSON {name: sha}
  detail      TEXT NOT NULL DEFAULT '{}',
  at          TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_change_line ON change(line_id);
CREATE INDEX IF NOT EXISTS idx_conflict_change ON conflict(change_id);
```

- [ ] **Step 2: Write the failing test for `Open` + root line** (`engine_test.go`)

```go
package change

import (
	"path/filepath"
	"testing"
)

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func TestOpenCreatesRootLine(t *testing.T) {
	e := newTestEngine(t)
	root, err := e.LineByName("main")
	if err != nil {
		t.Fatalf("LineByName(main): %v", err)
	}
	if root.ParentLine != "" {
		t.Fatalf("root parent = %q, want empty", root.ParentLine)
	}
	if root.Status != "open" {
		t.Fatalf("root status = %q, want open", root.Status)
	}
}
```

- [ ] **Step 3: Run test, verify it fails**

Run: `go test ./internal/change/ -run TestOpenCreatesRootLine -v`
Expected: FAIL — undefined `Open`, `Engine`, `LineByName`.

- [ ] **Step 4: Implement `engine.go`**

```go
// Package change is cairn's convergence core: a change/line graph over go-git
// with commit-triggered merge-forward, conflicts-as-data, and an operation log.
// Engine is per-repo and local-first: no server required.
package change

import (
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/go-git/go-git/v5"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

var ErrNotFound = errors.New("change: not found")

// RootLineName is the name of the line every repo starts with.
const RootLineName = "main"

type Engine struct {
	dir string
	git *git.Repository
	db  *sql.DB
	now func() time.Time // injectable for deterministic tests
}

// Open opens (creating if absent) a convergence repo rooted at dir: a bare
// go-git object store at dir/objects.git and a SQLite catalogue at dir/cairn.db.
// On first creation it bootstraps the root line ("main").
func Open(dir string) (*Engine, error) {
	gitDir := filepath.Join(dir, "objects.git")
	g, err := git.PlainOpen(gitDir)
	if errors.Is(err, git.ErrRepositoryNotExists) {
		g, err = git.PlainInit(gitDir, true) // bare
	}
	if err != nil {
		return nil, fmt.Errorf("change.Open: git: %w", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "cairn.db"))
	if err != nil {
		return nil, fmt.Errorf("change.Open: sqlite: %w", err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("change.Open: schema: %w", err)
	}
	e := &Engine{dir: dir, git: g, db: db, now: time.Now}
	if err := e.ensureRootLine(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return e, nil
}

func (e *Engine) Close() error { return e.db.Close() }

func (e *Engine) ensureRootLine() error {
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM line WHERE name=?`, RootLineName).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	ts := e.now().UTC().Format(time.RFC3339Nano)
	_, err := e.db.Exec(
		`INSERT INTO line(id,name,parent_line,tip_commit,base_commit,status,created_at,updated_at)
		 VALUES(?,?,NULL,'','','open',?,?)`,
		newID(), RootLineName, ts, ts)
	return err
}

// LineByName fetches a line by its unique name.
func (e *Engine) LineByName(name string) (Line, error) {
	var l Line
	var parent sql.NullString
	err := e.db.QueryRow(
		`SELECT id,name,parent_line,tip_commit,base_commit,status FROM line WHERE name=?`, name,
	).Scan(&l.ID, &l.Name, &parent, &l.TipCommit, &l.BaseCommit, &l.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return Line{}, ErrNotFound
	}
	if err != nil {
		return Line{}, err
	}
	l.ParentLine = parent.String
	return l, nil
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
```

Add the `Line` struct to `line.go` now (Task 2 fills the rest), or temporarily here. Place it in `line.go`:

```go
package change

type Line struct {
	ID, Name, ParentLine, TipCommit, BaseCommit, Status string
}
```

- [ ] **Step 5: Run test, verify it passes**

Run: `go test ./internal/change/ -run TestOpenCreatesRootLine -v`
Expected: PASS.

- [ ] **Step 6: Write failing tests for git helpers** (`gitobj_test.go`)

```go
package change

import (
	"reflect"
	"testing"
)

func TestWriteReadTreeRoundTrip(t *testing.T) {
	e := newTestEngine(t)
	files := map[string][]byte{
		"a.txt":       []byte("alpha\n"),
		"dir/b.txt":   []byte("beta\n"),
		"dir/c/d.txt": []byte("delta\n"),
	}
	h, err := e.writeTree(files)
	if err != nil {
		t.Fatalf("writeTree: %v", err)
	}
	got, err := e.readTree(h.String())
	if err != nil {
		t.Fatalf("readTree: %v", err)
	}
	if !reflect.DeepEqual(got, files) {
		t.Fatalf("round-trip mismatch:\n got %v\nwant %v", got, files)
	}
}
```

- [ ] **Step 7: Run, verify fail.** `go test ./internal/change/ -run TestWriteReadTreeRoundTrip -v` → FAIL (undefined writeTree/readTree).

- [ ] **Step 8: Implement `gitobj.go`** (the reused go-git plumbing)

```go
package change

import (
	"fmt"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// writeTree stores blobs + (recursively) tree objects for the given path→bytes
// map and returns the root tree hash. Paths use "/" separators.
func (e *Engine) writeTree(files map[string][]byte) (plumbing.Hash, error) {
	return e.buildTree("", files)
}

func (e *Engine) buildTree(prefix string, files map[string][]byte) (plumbing.Hash, error) {
	// Partition into this level's blobs and subdir groups.
	blobs := map[string][]byte{}          // name → bytes
	subdirs := map[string]map[string][]byte{} // dirname → (relpath → bytes)
	for path, data := range files {
		rel := strings.TrimPrefix(path, prefix)
		if i := strings.IndexByte(rel, '/'); i >= 0 {
			dir, rest := rel[:i], rel[i+1:]
			if subdirs[dir] == nil {
				subdirs[dir] = map[string][]byte{}
			}
			subdirs[dir][rest] = data
		} else {
			blobs[rel] = data
		}
	}
	var entries []object.TreeEntry
	for name, data := range blobs {
		h, err := e.writeBlob(data)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		entries = append(entries, object.TreeEntry{Name: name, Mode: filemode.Regular, Hash: h})
	}
	for name, sub := range subdirs {
		h, err := e.buildTree(prefix+name+"/", remap(sub, name))
		if err != nil {
			return plumbing.ZeroHash, err
		}
		entries = append(entries, object.TreeEntry{Name: name, Mode: filemode.Dir, Hash: h})
	}
	// git requires tree entries sorted by name.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	tree := &object.Tree{Entries: entries}
	obj := e.git.Storer.NewEncodedObject()
	if err := tree.Encode(obj); err != nil {
		return plumbing.ZeroHash, err
	}
	return e.git.Storer.SetEncodedObject(obj)
}

// remap re-keys a subdir group so paths are again relative to repo root for the
// recursive call (buildTree trims prefix on entry).
func remap(sub map[string][]byte, dir string) map[string][]byte {
	out := make(map[string][]byte, len(sub))
	for rel, data := range sub {
		out[dir+"/"+rel] = data
	}
	return out
}

func (e *Engine) writeBlob(data []byte) (plumbing.Hash, error) {
	obj := e.git.Storer.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	w, err := obj.Writer()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if _, err := w.Write(data); err != nil {
		return plumbing.ZeroHash, err
	}
	_ = w.Close()
	return e.git.Storer.SetEncodedObject(obj)
}

// readTree walks a tree hash into a path→bytes map.
func (e *Engine) readTree(treeHash string) (map[string][]byte, error) {
	t, err := e.git.TreeObject(plumbing.NewHash(treeHash))
	if err != nil {
		return nil, fmt.Errorf("readTree: %w", err)
	}
	out := map[string][]byte{}
	iter := t.Files()
	defer iter.Close()
	return out, iter.ForEach(func(f *object.File) error {
		s, err := f.Contents()
		if err != nil {
			return err
		}
		out[f.Name] = []byte(s)
		return nil
	})
}

// readBlob returns a blob's bytes by hex sha.
func (e *Engine) readBlob(sha string) ([]byte, error) {
	b, err := e.git.BlobObject(plumbing.NewHash(sha))
	if err != nil {
		return nil, err
	}
	r, err := b.Reader()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	buf := new(strings.Builder)
	if _, err := io.Copy(buf, r); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}
```

Add `"io"` to imports. (If `f.Name` returns the full path with `/` separators — it does — the round-trip matches the input map.)

- [ ] **Step 9: Run, verify pass.** `go test ./internal/change/ -run TestWriteReadTreeRoundTrip -v` → PASS.

- [ ] **Step 10: Commit**

```bash
git add internal/change/schema.sql internal/change/engine.go internal/change/line.go internal/change/gitobj.go internal/change/engine_test.go internal/change/gitobj_test.go
git commit -m "feat(change): engine bootstrap + go-git tree helpers (NEX-706)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 2: Change/Commit model + change_id + writeCommit (NEX-706, part 2)

**Files:**
- Modify: `internal/change/change.go` (create), `internal/change/gitobj.go` (add `writeCommit`)
- Test: `internal/change/change_test.go`

- [ ] **Step 1: Failing test** (`change_test.go`)

```go
package change

import (
	"strings"
	"testing"
)

func TestCommitAdvancesHeadStableChangeID(t *testing.T) {
	e := newTestEngine(t)
	root, _ := e.LineByName("main")
	ch, err := e.CreateChange(root.ID, "agent-a")
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	if !strings.HasPrefix(ch.ID, "z") {
		t.Fatalf("change_id %q, want reverse-hex (z-prefixed)", ch.ID)
	}
	r1, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("one\n")})
	if err != nil {
		t.Fatalf("Commit 1: %v", err)
	}
	r2, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("two\n")})
	if err != nil {
		t.Fatalf("Commit 2: %v", err)
	}
	if r1.HeadCommit == r2.HeadCommit {
		t.Fatal("head did not advance between commits")
	}
	got, _ := e.GetChange(ch.ID)
	if got.ID != ch.ID {
		t.Fatalf("change_id changed: %s != %s", got.ID, ch.ID)
	}
	if got.HeadCommit != r2.HeadCommit {
		t.Fatalf("stored head %s != last commit %s", got.HeadCommit, r2.HeadCommit)
	}
}
```

- [ ] **Step 2: Run, verify fail.** `go test ./internal/change/ -run TestCommitAdvancesHeadStableChangeID -v` → FAIL.

- [ ] **Step 3: Implement `change.go`**

```go
package change

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"time"
)

type Change struct {
	ID, LineID, Author, HeadCommit, Status string
	HasConflict                            bool
}

// newChangeID returns a 256-bit random id rendered in jj-style reverse-hex
// (alphabet z..k, so ids never collide with hex shas and read distinctly).
func newChangeID() string {
	const alphabet = "zyxwvutsrqponmlk" // 16 symbols = reverse hex
	var b [32]byte
	_, _ = rand.Read(b[:])
	out := make([]byte, 0, 64)
	for _, x := range b {
		out = append(out, alphabet[x>>4], alphabet[x&0x0f])
	}
	return string(out)
}

func (e *Engine) CreateChange(lineID, author string) (Change, error) {
	ts := e.now().UTC().Format(time.RFC3339Nano)
	c := Change{ID: newChangeID(), LineID: lineID, Author: author, Status: "open"}
	_, err := e.db.Exec(
		`INSERT INTO change(id,line_id,author,head_commit,status,has_conflict,created_at,updated_at)
		 VALUES(?,?,?,'','open',0,?,?)`, c.ID, lineID, author, ts, ts)
	return c, err
}

func (e *Engine) GetChange(id string) (Change, error) {
	var c Change
	var hc int
	err := e.db.QueryRow(
		`SELECT id,line_id,author,head_commit,status,has_conflict FROM change WHERE id=?`, id,
	).Scan(&c.ID, &c.LineID, &c.Author, &c.HeadCommit, &c.Status, &hc)
	if errors.Is(err, sql.ErrNoRows) {
		return Change{}, ErrNotFound
	}
	c.HasConflict = hc != 0
	return c, err
}

// Commit records a new snapshot on the change. Phase-1 plumbing version: write
// the tree, make a commit whose parent is the change's previous head (if any),
// advance the stored head. Merge-forward (Task 5) wraps this. Returns the new
// head. NOTE: full Commit (with merge-forward) is finished in Task 5; this is
// the snapshot half it builds on.
func (e *Engine) Commit(changeID string, files map[string][]byte) (CommitResult, error) {
	ch, err := e.GetChange(changeID)
	if err != nil {
		return CommitResult{}, err
	}
	tree, err := e.writeTree(files)
	if err != nil {
		return CommitResult{}, err
	}
	var parents []string
	if ch.HeadCommit != "" {
		parents = append(parents, ch.HeadCommit)
	}
	head, err := e.writeCommit(tree.String(), changeID, ch.Author, parents)
	if err != nil {
		return CommitResult{}, err
	}
	ts := e.now().UTC().Format(time.RFC3339Nano)
	if _, err := e.db.Exec(`UPDATE change SET head_commit=?, updated_at=? WHERE id=?`, head, ts, changeID); err != nil {
		return CommitResult{}, err
	}
	return CommitResult{HeadCommit: head}, nil
}

// CommitResult is the outcome of a commit.
type CommitResult struct {
	HeadCommit string
	Conflicts  []Conflict
}
```

- [ ] **Step 4: Add `writeCommit` to `gitobj.go`**

```go
// writeCommit stores a commit object pointing at treeSha with the given parents
// (hex shas) and a `Change-Id` trailer, and returns its hex sha. The committer
// time is fixed (engine clock) so identical inputs hash identically in tests.
func (e *Engine) writeCommit(treeSha, changeID, author string, parents []string) (string, error) {
	when := e.now().UTC()
	c := &object.Commit{
		Author:    object.Signature{Name: author, Email: author + "@cairn", When: when},
		Committer: object.Signature{Name: author, Email: author + "@cairn", When: when},
		Message:   "snapshot\n\nChange-Id: " + changeID + "\n",
		TreeHash:  plumbing.NewHash(treeSha),
	}
	for _, p := range parents {
		c.ParentHashes = append(c.ParentHashes, plumbing.NewHash(p))
	}
	obj := e.git.Storer.NewEncodedObject()
	if err := c.Encode(obj); err != nil {
		return "", err
	}
	h, err := e.git.Storer.SetEncodedObject(obj)
	if err != nil {
		return "", err
	}
	return h.String(), nil
}
```

- [ ] **Step 5: Run, verify pass.** `go test ./internal/change/ -run TestCommitAdvancesHeadStableChangeID -v` → PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/change/change.go internal/change/gitobj.go internal/change/change_test.go
git commit -m "feat(change): Change/Commit model + change_id + writeCommit (NEX-706)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 3: Line model — CreateLine, lineage, tree (NEX-707)

**Files:** Modify `internal/change/line.go`; Test `internal/change/line_test.go`

- [ ] **Step 1: Failing test**

```go
package change

import "testing"

func TestCreateLineLineage(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	exp, err := e.CreateLine("exp/idea", main.ID)
	if err != nil {
		t.Fatalf("CreateLine: %v", err)
	}
	idea2, err := e.CreateLine("exp/idea/idea2", exp.ID)
	if err != nil {
		t.Fatalf("CreateLine child: %v", err)
	}
	chain, err := e.GetLineage(idea2.ID)
	if err != nil {
		t.Fatalf("GetLineage: %v", err)
	}
	// Root-first: [main, exp/idea, exp/idea/idea2]
	if len(chain) != 3 || chain[0].Name != "main" || chain[2].Name != "exp/idea/idea2" {
		t.Fatalf("lineage = %v", names(chain))
	}
}

func names(ls []Line) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.Name
	}
	return out
}
```

- [ ] **Step 2: Run, verify fail.**

- [ ] **Step 3: Implement in `line.go`**

```go
import (
	"database/sql"
	"time"
)

// CreateLine creates a new line whose parent is parentLineID, based at the
// parent's current tip. Returns the new Line.
func (e *Engine) CreateLine(name, parentLineID string) (Line, error) {
	parent, err := e.lineByID(parentLineID)
	if err != nil {
		return Line{}, err
	}
	ts := e.now().UTC().Format(time.RFC3339Nano)
	l := Line{ID: newID(), Name: name, ParentLine: parentLineID,
		BaseCommit: parent.TipCommit, TipCommit: parent.TipCommit, Status: "open"}
	_, err = e.db.Exec(
		`INSERT INTO line(id,name,parent_line,tip_commit,base_commit,status,created_at,updated_at)
		 VALUES(?,?,?,?,?,'open',?,?)`,
		l.ID, name, parentLineID, l.TipCommit, l.BaseCommit, ts, ts)
	return l, err
}

func (e *Engine) lineByID(id string) (Line, error) {
	var l Line
	var parent sql.NullString
	err := e.db.QueryRow(
		`SELECT id,name,parent_line,tip_commit,base_commit,status FROM line WHERE id=?`, id,
	).Scan(&l.ID, &l.Name, &parent, &l.TipCommit, &l.BaseCommit, &l.Status)
	if err == sql.ErrNoRows {
		return Line{}, ErrNotFound
	}
	l.ParentLine = parent.String
	return l, err
}

// GetLineage returns the ancestry chain root-first, ending with the line itself.
func (e *Engine) GetLineage(lineID string) ([]Line, error) {
	var rev []Line
	cur := lineID
	for cur != "" {
		l, err := e.lineByID(cur)
		if err != nil {
			return nil, err
		}
		rev = append(rev, l)
		cur = l.ParentLine
	}
	// reverse into root-first
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev, nil
}
```

- [ ] **Step 4: Run, verify pass.**

- [ ] **Step 5: Add `GetLineTree` test + impl** (forest with ahead/behind). Test:

```go
func TestGetLineTreeAheadBehind(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	exp, _ := e.CreateLine("exp", main.ID)
	nodes, err := e.GetLineTree()
	if err != nil {
		t.Fatalf("GetLineTree: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(nodes))
	}
	// exp has main as parent, 0 ahead at creation.
	for _, n := range nodes {
		if n.Line.ID == exp.ID && n.Parent != main.ID {
			t.Fatalf("exp parent = %q, want %q", n.Parent, main.ID)
		}
	}
}
```

Impl:

```go
type LineNode struct {
	Line   Line
	Parent string
	Ahead  int // commits on this line since base_commit (Phase-1: 0 or 1 per commit)
}

func (e *Engine) GetLineTree() ([]LineNode, error) {
	rows, err := e.db.Query(`SELECT id,name,parent_line,tip_commit,base_commit,status FROM line WHERE status!='abandoned'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LineNode
	for rows.Next() {
		var l Line
		var parent sql.NullString
		if err := rows.Scan(&l.ID, &l.Name, &parent, &l.TipCommit, &l.BaseCommit, &l.Status); err != nil {
			return nil, err
		}
		l.ParentLine = parent.String
		ahead := 0
		if l.TipCommit != "" && l.TipCommit != l.BaseCommit {
			ahead = 1 // refined once commit-counting lands; sufficient for Phase-1 proof
		}
		out = append(out, LineNode{Line: l, Parent: parent.String, Ahead: ahead})
	}
	return out, rows.Err()
}
```

- [ ] **Step 6: Run all change tests, verify pass.** `go test ./internal/change/ -v`

- [ ] **Step 7: Commit**

```bash
git add internal/change/line.go internal/change/line_test.go
git commit -m "feat(change): line model — CreateLine, lineage, tree (NEX-707)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 4: Three-way merge (diff3) — THE RISK (NEX-709)

Self-contained package `internal/change/diff3`, no go-git dependency, pure functions. Built on `difflib.SequenceMatcher` matching blocks (pin `github.com/pmezard/go-difflib`).

**Files:** Create `internal/change/diff3/diff3.go`, `internal/change/diff3/diff3_test.go`

- [ ] **Step 1: Add the dependency**

Run: `go get github.com/pmezard/go-difflib@v1.0.0`
Expected: go.mod/go.sum updated.

- [ ] **Step 2: Failing tests** (`diff3_test.go`)

```go
package diff3

import (
	"strings"
	"testing"
)

func lines(s string) []string { return strings.SplitAfter(s, "\n") }

func TestMerge3CleanNonOverlap(t *testing.T) {
	base := lines("a\nb\nc\n")
	ours := lines("a\nB\nc\n")   // changed line 2
	theirs := lines("a\nb\nC\n") // changed line 3
	r := Merge3(base, ours, theirs)
	if r.Conflict {
		t.Fatalf("unexpected conflict: %q", strings.Join(r.Merged, ""))
	}
	if got := strings.Join(r.Merged, ""); got != "a\nB\nC\n" {
		t.Fatalf("merged = %q, want a/B/C", got)
	}
}

func TestMerge3ConflictBothEditSameLine(t *testing.T) {
	base := lines("a\nb\nc\n")
	ours := lines("a\nX\nc\n")
	theirs := lines("a\nY\nc\n")
	r := Merge3(base, ours, theirs)
	if !r.Conflict {
		t.Fatal("expected conflict")
	}
	out := strings.Join(r.Merged, "")
	if !strings.Contains(out, "<<<<<<<") || !strings.Contains(out, "=======") || !strings.Contains(out, ">>>>>>>") {
		t.Fatalf("missing diff3 markers: %q", out)
	}
}

func TestMerge3IdenticalEditsNoConflict(t *testing.T) {
	base := lines("a\nb\n")
	ours := lines("a\nZ\n")
	theirs := lines("a\nZ\n")
	r := Merge3(base, ours, theirs)
	if r.Conflict {
		t.Fatal("identical edits must not conflict")
	}
	if strings.Join(r.Merged, "") != "a\nZ\n" {
		t.Fatalf("merged = %q", strings.Join(r.Merged, ""))
	}
}
```

- [ ] **Step 3: Run, verify fail.** `go test ./internal/change/diff3/ -v` → FAIL.

- [ ] **Step 4: Implement `diff3.go`**

```go
// Package diff3 is a line-level three-way merge. Conflicts are emitted inline
// with git-style markers AND signalled via Result.Conflict so the caller can
// record a structured conflict object. Pure; no I/O.
package diff3

import "github.com/pmezard/go-difflib/difflib"

type Result struct {
	Merged   []string
	Conflict bool
}

// Merge3 merges ours and theirs against their common base.
func Merge3(base, ours, theirs []string) Result {
	oursOps := matchBlocks(base, ours)
	theirsOps := matchBlocks(base, theirs)
	// Build per-base-line change maps from each side.
	oChange := sideChanges(base, ours, oursOps)
	tChange := sideChanges(base, theirs, theirsOps)

	var out []string
	conflict := false
	i := 0
	for i < len(base) {
		o, oHas := oChange[i]
		tc, tHas := tChange[i]
		switch {
		case !oHas && !tHas:
			out = append(out, base[i])
			i++
		case oHas && !tHas:
			out = append(out, o.replacement...)
			i += o.span
		case !oHas && tHas:
			out = append(out, tc.replacement...)
			i += tc.span
		default: // both changed the same region
			if equal(o.replacement, tc.replacement) && o.span == tc.span {
				out = append(out, o.replacement...)
				i += o.span
			} else {
				conflict = true
				out = append(out, "<<<<<<< ours\n")
				out = append(out, o.replacement...)
				out = append(out, "||||||| base\n")
				out = append(out, base[i:i+max(o.span, tc.span)]...)
				out = append(out, "=======\n")
				out = append(out, tc.replacement...)
				out = append(out, ">>>>>>> theirs\n")
				i += max(o.span, tc.span)
			}
		}
	}
	// trailing inserts at EOF (base-index == len(base))
	if o, ok := oChange[len(base)]; ok {
		out = append(out, o.replacement...)
	}
	if tc, ok := tChange[len(base)]; ok {
		if _, dup := oChange[len(base)]; !dup {
			out = append(out, tc.replacement...)
		}
	}
	return Result{Merged: out, Conflict: conflict}
}

type seg struct {
	span        int      // base lines covered
	replacement []string // side lines that replace them
}

func matchBlocks(a, b []string) []difflib.OpCode {
	sm := difflib.NewMatcher(a, b)
	return sm.GetOpCodes()
}

// sideChanges maps a base line-index → the replacement that side applies there.
func sideChanges(base, side []string, ops []difflib.OpCode) map[int]seg {
	m := map[int]seg{}
	for _, op := range ops {
		switch op.Tag {
		case 'r': // replace base[I1:I2] with side[J1:J2]
			m[op.I1] = seg{span: op.I2 - op.I1, replacement: side[op.J1:op.J2]}
		case 'd': // delete base[I1:I2]
			m[op.I1] = seg{span: op.I2 - op.I1, replacement: nil}
		case 'i': // insert side[J1:J2] before base[I1]
			m[op.I1] = seg{span: 0, replacement: side[op.J1:op.J2]}
		}
	}
	return m
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func max(a, b int) int { if a > b { return a }; return b }
```

> Note for the implementer: insert-at-`I1` and span-0 entries can collide with a replace at the same index; the harness (Task 9) stresses these. If `go test` surfaces an overlap edge case, the fix is to merge span-0 inserts into the adjacent segment before the switch — keep the change inside `sideChanges`/the loop, do not push complexity to callers.

- [ ] **Step 5: Run, verify pass.** `go test ./internal/change/diff3/ -v` → PASS (all three).

- [ ] **Step 6: Add edge-case tests** (add/add at EOF, delete-vs-modify) and make them pass:

```go
func TestMerge3DeleteVsModifyConflicts(t *testing.T) {
	base := lines("a\nb\nc\n")
	ours := lines("a\nc\n")    // deleted b
	theirs := lines("a\nB\nc\n") // modified b
	r := Merge3(base, ours, theirs)
	if !r.Conflict {
		t.Fatal("delete-vs-modify must conflict")
	}
}
```

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/change/diff3/
git commit -m "feat(change): three-way diff3 line merge (NEX-709)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 5: Merge-forward on commit + tree classify (NEX-710)

Wire diff3 into `Commit`: after snapshotting, rebase the change onto its line's parent tip. Per-path three-way at the tree level (base = merge-base tree, ours = parent tip tree, theirs = change tip tree).

**Files:** Create `internal/change/merge.go`; add `mergeBase` to `gitobj.go`; modify `Commit` in `change.go`; Test `internal/change/merge_test.go`

- [ ] **Step 1: Failing test — non-overlapping converges, overlapping conflicts**

```go
package change

import "testing"

func TestCommitMergeForwardNonOverlap(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	// Seed main with a base file via a change folded into main (Task 6 provides Fold;
	// for this test use the lower-level seedLine helper).
	seedLineTip(t, e, main.ID, map[string][]byte{"a.txt": []byte("a\n"), "b.txt": []byte("b\n")})

	exp, _ := e.CreateLine("exp", main.ID)
	ch, _ := e.CreateChange(exp.ID, "agent-a")
	// exp edits b.txt only.
	r, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("a\n"), "b.txt": []byte("B\n")})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(r.Conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", r.Conflicts)
	}
	tree, _ := e.commitTree(r.HeadCommit)
	files, _ := e.readTree(tree)
	if string(files["b.txt"]) != "B\n" || string(files["a.txt"]) != "a\n" {
		t.Fatalf("merge-forward content wrong: %v", files)
	}
}
```

- [ ] **Step 2: Add test helpers** `seedLineTip` and `commitTree` (test file + `gitobj.go`).

`seedLineTip` (in `merge_test.go`): create a change on the line, commit the files, then set the line tip to that commit directly (bypasses Fold for setup):

```go
func seedLineTip(t *testing.T, e *Engine, lineID string, files map[string][]byte) string {
	t.Helper()
	ch, _ := e.CreateChange(lineID, "seed")
	r, err := e.Commit(ch.ID, files)
	if err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	if _, err := e.db.Exec(`UPDATE line SET tip_commit=?, base_commit=? WHERE id=?`, r.HeadCommit, r.HeadCommit, lineID); err != nil {
		t.Fatalf("seed tip: %v", err)
	}
	return r.HeadCommit
}
```

`commitTree` (in `gitobj.go`):

```go
func (e *Engine) commitTree(commitSha string) (string, error) {
	c, err := e.git.CommitObject(plumbing.NewHash(commitSha))
	if err != nil {
		return "", err
	}
	return c.TreeHash.String(), nil
}

// mergeBase returns the hex sha of the best common ancestor of a and b, or ""
// if none (independent roots).
func (e *Engine) mergeBase(a, b string) (string, error) {
	if a == "" || b == "" {
		return "", nil
	}
	ca, err := e.git.CommitObject(plumbing.NewHash(a))
	if err != nil {
		return "", err
	}
	cb, err := e.git.CommitObject(plumbing.NewHash(b))
	if err != nil {
		return "", err
	}
	bases, err := ca.MergeBase(cb)
	if err != nil || len(bases) == 0 {
		return "", err
	}
	return bases[0].Hash.String(), nil
}
```

- [ ] **Step 3: Run, verify fail.**

- [ ] **Step 4: Implement `merge.go` and rewire `Commit`**

```go
package change

import (
	"strings"
	"time"

	"github.com/CarriedWorldUniverse/cairn/internal/change/diff3"
)

// mergeForward rebases the change's just-written snapshot (theirs) onto its
// line's parent tip (ours), using the merge-base tree as base. Returns the new
// merged tree sha and any conflicts. If the line has no parent (root) or the
// parent has no tip, it's a no-op (returns the change's own tree).
func (e *Engine) mergeForward(changeID, snapshotCommit string) (mergedTree string, conflicts []Conflict, err error) {
	ch, err := e.GetChange(changeID)
	if err != nil {
		return "", nil, err
	}
	line, err := e.lineByID(ch.LineID)
	if err != nil {
		return "", nil, err
	}
	theirsTree, err := e.commitTree(snapshotCommit)
	if err != nil {
		return "", nil, err
	}
	if line.ParentLine == "" {
		return theirsTree, nil, nil // root line: nothing to adopt
	}
	parent, err := e.lineByID(line.ParentLine)
	if err != nil {
		return "", nil, err
	}
	if parent.TipCommit == "" {
		return theirsTree, nil, nil
	}
	oursTree, err := e.commitTree(parent.TipCommit)
	if err != nil {
		return "", nil, err
	}
	baseCommit, err := e.mergeBase(parent.TipCommit, snapshotCommit)
	if err != nil {
		return "", nil, err
	}
	baseTree := ""
	if baseCommit != "" {
		if baseTree, err = e.commitTree(baseCommit); err != nil {
			return "", nil, err
		}
	}
	return e.mergeTrees(changeID, baseTree, oursTree, theirsTree)
}

// mergeTrees does a per-path three-way merge of three tree shas and returns the
// merged tree sha + conflict objects (already persisted).
func (e *Engine) mergeTrees(changeID, baseTree, oursTree, theirsTree string) (string, []Conflict, error) {
	base, _ := e.maybeReadTree(baseTree)
	ours, err := e.readTree(oursTree)
	if err != nil {
		return "", nil, err
	}
	theirs, err := e.readTree(theirsTree)
	if err != nil {
		return "", nil, err
	}
	merged := map[string][]byte{}
	var conflicts []Conflict
	paths := unionKeys(base, ours, theirs)
	for _, p := range paths {
		b, bok := base[p]
		o, ook := ours[p]
		tt, tok := theirs[p]
		switch {
		case ook && tok && bytesEq(o, tt):
			merged[p] = o
		case ook && !tok && (!bok || bytesEq(b, o)):
			// only ours has it / only ours unchanged-vs-base → take ours
			if ook { merged[p] = o }
		case tok && !ook && (!bok || bytesEq(b, tt)):
			if tok { merged[p] = tt }
		case ook && !tok && bok && !bytesEq(b, o):
			merged[p] = o // theirs deleted, ours modified: keep ours (Phase-1 rule)
		case !ook && tok && bok && !bytesEq(b, tt):
			merged[p] = tt
		default:
			// both present & differ → diff3
			res := diff3.Merge3(splitLines(b), splitLines(o), splitLines(tt))
			merged[p] = []byte(strings.Join(res.Merged, ""))
			if res.Conflict {
				cf, err := e.recordConflict(changeID, p, b, o, tt, merged[p])
				if err != nil {
					return "", nil, err
				}
				conflicts = append(conflicts, cf)
			}
		}
	}
	h, err := e.writeTree(merged)
	if err != nil {
		return "", nil, err
	}
	return h.String(), conflicts, nil
}

func (e *Engine) maybeReadTree(sha string) (map[string][]byte, error) {
	if sha == "" {
		return map[string][]byte{}, nil
	}
	return e.readTree(sha)
}

func splitLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	return strings.SplitAfter(string(b), "\n")
}
```

Add small helpers to `merge.go`: `bytesEq`, `unionKeys` (sorted), and rewire `Commit` so after `writeCommit` it calls `mergeForward`, writes a second commit on the merged tree if the merge changed the tree, updates head + `has_conflict`, and records the op (op recording added in Task 6/8 — for now set head to the merged result):

```go
// In change.go Commit, replace the "set head" tail with:
merged, conflicts, err := e.mergeForward(changeID, head)
if err != nil { return CommitResult{}, err }
if merged != "" {
	// re-commit on the merged tree (parent = snapshot) so head reflects adoption
	head, err = e.writeCommit(merged, changeID, ch.Author, []string{head})
	if err != nil { return CommitResult{}, err }
}
hasConf := 0
if len(conflicts) > 0 { hasConf = 1 }
ts := e.now().UTC().Format(time.RFC3339Nano)
if _, err := e.db.Exec(`UPDATE change SET head_commit=?, has_conflict=?, updated_at=? WHERE id=?`, head, hasConf, ts, changeID); err != nil {
	return CommitResult{}, err
}
return CommitResult{HeadCommit: head, Conflicts: conflicts}, nil
```

(`recordConflict` is implemented in Task 6; until then stub it to return a `Conflict{}` with the fields set and no DB write so this task's test compiles — then Task 6 replaces the stub with persistence. Mark the stub with `// TODO(NEX-712): persist` and complete it in Task 6.)

- [ ] **Step 5: Run, verify pass.** `go test ./internal/change/ -run TestCommitMergeForward -v` → PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/change/merge.go internal/change/gitobj.go internal/change/change.go internal/change/merge_test.go
git commit -m "feat(change): merge-forward on commit via diff3 tree merge (NEX-710)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 6: Conflict object + ResolveConflict (NEX-712)

**Files:** Create `internal/change/conflict.go`; Test `internal/change/conflict_test.go`

- [ ] **Step 1: Failing test**

```go
package change

import (
	"strings"
	"testing"
)

func TestConflictRecordedAndResolved(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	seedLineTip(t, e, main.ID, map[string][]byte{"f.txt": []byte("base\n")})
	// main advances: change "X"
	mc, _ := e.CreateChange(main.ID, "m")
	e.Commit(mc.ID, map[string][]byte{"f.txt": []byte("X\n")})
	e.db.Exec(`UPDATE line SET tip_commit=(SELECT head_commit FROM change WHERE id=?) WHERE id=?`, mc.ID, main.ID)

	exp, _ := e.CreateLine("exp", main.ID)
	ec, _ := e.CreateChange(exp.ID, "e")
	r, _ := e.Commit(ec.ID, map[string][]byte{"f.txt": []byte("Y\n")})
	if len(r.Conflicts) == 0 {
		t.Fatal("expected a conflict")
	}
	open, _ := e.Conflicts(ec.ID)
	if len(open) != 1 || open[0].Status != "open" {
		t.Fatalf("open conflicts = %+v", open)
	}
	if err := e.ResolveConflict(ec.ID, "f.txt", []byte("resolved\n")); err != nil {
		t.Fatalf("ResolveConflict: %v", err)
	}
	stillOpen, _ := e.Conflicts(ec.ID)
	if len(stillOpen) != 0 {
		t.Fatalf("still open after resolve: %+v", stillOpen)
	}
	got, _ := e.GetChange(ec.ID)
	if got.HasConflict {
		t.Fatal("change still flagged has_conflict")
	}
	_ = strings.TrimSpace("") // keep import
}
```

- [ ] **Step 2: Run, verify fail.**

- [ ] **Step 3: Implement `conflict.go`** (replace the Task-5 stub)

```go
package change

import (
	"database/sql"
	"time"
)

func (e *Engine) recordConflict(changeID, path string, base, ours, theirs, marked []byte) (Conflict, error) {
	bb, _ := e.writeBlob(base)
	ob, _ := e.writeBlob(ours)
	tb, _ := e.writeBlob(theirs)
	mb, _ := e.writeBlob(marked)
	cf := Conflict{
		ID: newID(), ChangeID: changeID, Path: path,
		BaseBlob: bb.String(), ParentBlob: ob.String(), ChangeBlob: tb.String(),
		MarkedBlob: mb.String(), Status: "open",
	}
	ts := e.now().UTC().Format(time.RFC3339Nano)
	_, err := e.db.Exec(
		`INSERT INTO conflict(id,change_id,path,base_blob,parent_blob,change_blob,marked_blob,status,created_at)
		 VALUES(?,?,?,?,?,?,?, 'open', ?)`,
		cf.ID, changeID, path, cf.BaseBlob, cf.ParentBlob, cf.ChangeBlob, cf.MarkedBlob, ts)
	return cf, err
}

// Conflicts lists OPEN conflicts on a change.
func (e *Engine) Conflicts(changeID string) ([]Conflict, error) {
	rows, err := e.db.Query(
		`SELECT id,change_id,path,base_blob,parent_blob,change_blob,marked_blob,status
		 FROM conflict WHERE change_id=? AND status='open'`, changeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Conflict
	for rows.Next() {
		var c Conflict
		if err := rows.Scan(&c.ID, &c.ChangeID, &c.Path, &c.BaseBlob, &c.ParentBlob, &c.ChangeBlob, &c.MarkedBlob, &c.Status); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ResolveConflict marks the conflict on (change, path) resolved, writes the
// resolved blob into the change's tip tree, and clears has_conflict when no
// open conflicts remain.
func (e *Engine) ResolveConflict(changeID, path string, resolved []byte) error {
	ts := e.now().UTC().Format(time.RFC3339Nano)
	res, err := e.db.Exec(
		`UPDATE conflict SET status='resolved', resolved_at=? WHERE change_id=? AND path=? AND status='open'`,
		ts, changeID, path)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	// Patch the resolved file into the change tip's tree → new commit.
	ch, err := e.GetChange(changeID)
	if err != nil {
		return err
	}
	tree, err := e.commitTree(ch.HeadCommit)
	if err != nil {
		return err
	}
	files, err := e.readTree(tree)
	if err != nil {
		return err
	}
	files[path] = resolved
	nt, err := e.writeTree(files)
	if err != nil {
		return err
	}
	head, err := e.writeCommit(nt.String(), changeID, ch.Author, []string{ch.HeadCommit})
	if err != nil {
		return err
	}
	var remaining int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM conflict WHERE change_id=? AND status='open'`, changeID).Scan(&remaining); err != nil {
		return err
	}
	hc := 0
	if remaining > 0 {
		hc = 1
	}
	_, err = e.db.Exec(`UPDATE change SET head_commit=?, has_conflict=?, updated_at=? WHERE id=?`, head, hc, ts, changeID)
	return err
}

var _ = sql.ErrNoRows
```

- [ ] **Step 4: Run, verify pass.** `go test ./internal/change/ -run TestConflict -v` → PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/change/conflict.go internal/change/conflict_test.go internal/change/merge.go
git commit -m "feat(change): conflict object + ResolveConflict (NEX-712)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 7: Fold (fast-forward) + Abandon (NEX-711)

**Files:** Create `internal/change/fold.go`; Test `internal/change/fold_test.go`

- [ ] **Step 1: Failing tests** — fast-forward fold; fold rejected with open conflict; abandon leaves parent untouched.

```go
package change

import "testing"

func TestFoldFastForwardsParent(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	seedLineTip(t, e, main.ID, map[string][]byte{"a.txt": []byte("a\n")})
	exp, _ := e.CreateLine("exp", main.ID)
	ch, _ := e.CreateChange(exp.ID, "e")
	r, _ := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("a\n"), "n.txt": []byte("new\n")})
	// reflect change head onto its line tip (commit advances the change; fold reads line tip)
	e.db.Exec(`UPDATE line SET tip_commit=? WHERE id=?`, r.HeadCommit, exp.ID)

	if err := e.FoldLine(exp.ID); err != nil {
		t.Fatalf("FoldLine: %v", err)
	}
	main2, _ := e.LineByName("main")
	files, _ := e.readTree(must(e.commitTree(main2.TipCommit)))
	if string(files["n.txt"]) != "new\n" {
		t.Fatalf("fold did not bring n.txt into main: %v", files)
	}
}

func TestAbandonLeavesParentUntouched(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	baseTip := seedLineTip(t, e, main.ID, map[string][]byte{"a.txt": []byte("a\n")})
	exp, _ := e.CreateLine("exp", main.ID)
	ch, _ := e.CreateChange(exp.ID, "e")
	e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("WILD\n")})
	if err := e.AbandonLine(exp.ID); err != nil {
		t.Fatalf("AbandonLine: %v", err)
	}
	main2, _ := e.LineByName("main")
	if main2.TipCommit != baseTip {
		t.Fatalf("main tip moved: %s != %s", main2.TipCommit, baseTip)
	}
}

func must(s string, err error) string { if err != nil { panic(err) }; return s }
```

- [ ] **Step 2: Run, verify fail.**

- [ ] **Step 3: Implement `fold.go`**

```go
package change

import (
	"errors"
	"time"
)

var ErrHasConflict = errors.New("change: line has open conflicts; resolve before folding")

// FoldLine fast-forwards the line's parent to the line's tip. Rejected if any
// change on the line has an open conflict. The line is marked folded.
func (e *Engine) FoldLine(lineID string) error {
	line, err := e.lineByID(lineID)
	if err != nil {
		return err
	}
	var open int
	if err := e.db.QueryRow(
		`SELECT COUNT(*) FROM conflict c JOIN change ch ON c.change_id=ch.id
		 WHERE ch.line_id=? AND c.status='open'`, lineID).Scan(&open); err != nil {
		return err
	}
	if open > 0 {
		return ErrHasConflict
	}
	ts := e.now().UTC().Format(time.RFC3339Nano)
	if _, err := e.db.Exec(`UPDATE line SET tip_commit=?, updated_at=? WHERE id=?`, line.TipCommit, ts, line.ParentLine); err != nil {
		return err
	}
	_, err = e.db.Exec(`UPDATE line SET status='folded', updated_at=? WHERE id=?`, ts, lineID)
	return err
}

// AbandonLine drops a line and its changes; the parent is never touched.
func (e *Engine) AbandonLine(lineID string) error {
	ts := e.now().UTC().Format(time.RFC3339Nano)
	if _, err := e.db.Exec(`UPDATE change SET status='abandoned', updated_at=? WHERE line_id=?`, ts, lineID); err != nil {
		return err
	}
	_, err := e.db.Exec(`UPDATE line SET status='abandoned', updated_at=? WHERE id=?`, ts, lineID)
	return err
}
```

- [ ] **Step 4: Run, verify pass.** `go test ./internal/change/ -run 'TestFold|TestAbandon' -v` → PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/change/fold.go internal/change/fold_test.go
git commit -m "feat(change): fold (fast-forward) + abandon (NEX-711)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 8: Operation log + Undo (NEX-708)

Record an `operation` row at each mutation (CreateLine/Commit/Fold/Abandon/Tag/Resolve) capturing the ref-map before/after; `Undo` restores the previous view.

**Files:** Create `internal/change/oplog.go`; modify the mutators to call `e.recordOp(...)`; Test `internal/change/oplog_test.go`

- [ ] **Step 1: Failing test**

```go
package change

import "testing"

func TestOpLogReplayAndUndo(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	seedLineTip(t, e, main.ID, map[string][]byte{"a.txt": []byte("a\n")})
	before, _ := e.LineByName("main")

	ch, _ := e.CreateChange(main.ID, "m")
	r, _ := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("a2\n")})
	e.db.Exec(`UPDATE line SET tip_commit=? WHERE id=?`, r.HeadCommit, main.ID)
	e.recordOp("commit", "m", map[string]string{"main": before.TipCommit}, e.viewMap())

	ops, _ := e.OperationLog()
	if len(ops) == 0 {
		t.Fatal("no ops recorded")
	}
	if err := e.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	after, _ := e.LineByName("main")
	if after.TipCommit != before.TipCommit {
		t.Fatalf("undo did not restore main tip: %s != %s", after.TipCommit, before.TipCommit)
	}
}
```

- [ ] **Step 2: Run, verify fail.**

- [ ] **Step 3: Implement `oplog.go`**

```go
package change

import (
	"encoding/json"
	"time"
)

type Operation struct {
	ID, OpType, Actor, ParentOp string
	ViewBefore, ViewAfter       map[string]string
	Detail                      string
}

// viewMap snapshots the current ref-map: line-name → tip, plus change refs.
func (e *Engine) viewMap() map[string]string {
	m := map[string]string{}
	rows, _ := e.db.Query(`SELECT name,tip_commit FROM line WHERE status!='abandoned'`)
	defer rows.Close()
	for rows.Next() {
		var n, tip string
		_ = rows.Scan(&n, &tip)
		m[n] = tip
	}
	return m
}

func (e *Engine) lastOpID() string {
	var id string
	_ = e.db.QueryRow(`SELECT id FROM operation ORDER BY id DESC LIMIT 1`).Scan(&id)
	return id
}

func (e *Engine) recordOp(opType, actor string, before, after map[string]string) error {
	bj, _ := json.Marshal(before)
	aj, _ := json.Marshal(after)
	ts := e.now().UTC().Format(time.RFC3339Nano)
	// id is time-ordered (RFC3339Nano + random suffix) so ORDER BY id is chronological.
	id := ts + "-" + newID()[:8]
	_, err := e.db.Exec(
		`INSERT INTO operation(id,op_type,actor,parent_op,view_before,view_after,detail,at)
		 VALUES(?,?,?,?,?,?, '{}', ?)`,
		id, opType, actor, e.lastOpID(), string(bj), string(aj), ts)
	return err
}

func (e *Engine) OperationLog() ([]Operation, error) {
	rows, err := e.db.Query(`SELECT id,op_type,actor,parent_op,view_before,view_after,detail FROM operation ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Operation
	for rows.Next() {
		var o Operation
		var bj, aj string
		if err := rows.Scan(&o.ID, &o.OpType, &o.Actor, &o.ParentOp, &bj, &aj, &o.Detail); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(bj), &o.ViewBefore)
		_ = json.Unmarshal([]byte(aj), &o.ViewAfter)
		out = append(out, o)
	}
	return out, rows.Err()
}

// Undo restores the ref-map to the view_before of the most recent op and records
// the undo as a new op (append-only history).
func (e *Engine) Undo() error {
	ops, err := e.OperationLog()
	if err != nil {
		return err
	}
	if len(ops) == 0 {
		return ErrNotFound
	}
	last := ops[len(ops)-1]
	cur := e.viewMap()
	ts := e.now().UTC().Format(time.RFC3339Nano)
	for name, sha := range last.ViewBefore {
		if _, err := e.db.Exec(`UPDATE line SET tip_commit=?, updated_at=? WHERE name=?`, sha, ts, name); err != nil {
			return err
		}
	}
	return e.recordOp("undo", "system", cur, last.ViewBefore)
}
```

- [ ] **Step 4: Wire `recordOp` into mutators.** In `CreateLine`, `Commit`, `FoldLine`, `AbandonLine`, `ResolveConflict`, `Tag`: capture `before := e.viewMap()` at entry and `e.recordOp(<type>, <actor>, before, e.viewMap())` before returning success. Add the call; do not change their signatures.

- [ ] **Step 5: Run, verify pass.** `go test ./internal/change/ -run TestOpLog -v` → PASS. Then `go test ./internal/change/ -v` (all green).

- [ ] **Step 6: Commit**

```bash
git add internal/change/oplog.go internal/change/*.go internal/change/oplog_test.go
git commit -m "feat(change): operation log + Undo (NEX-708)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 9: Tags (NEX-713)

**Files:** Create `internal/change/tag.go`; Test `internal/change/tag_test.go`

- [ ] **Step 1: Failing test**

```go
package change

import "testing"

func TestTagCreateList(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	tip := seedLineTip(t, e, main.ID, map[string][]byte{"a.txt": []byte("a\n")})
	if err := e.Tag("v1.0.0", tip, "rel"); err != nil {
		t.Fatalf("Tag: %v", err)
	}
	tags, _ := e.ListTags()
	if len(tags) != 1 || tags[0].Name != "v1.0.0" || tags[0].Commit != tip {
		t.Fatalf("tags = %+v", tags)
	}
}
```

- [ ] **Step 2: Run, verify fail.**

- [ ] **Step 3: Implement `tag.go`**

```go
package change

import "time"

type Tag struct {
	Name, Commit, Tagger string
}

func (e *Engine) Tag(name, commitSha, tagger string) error {
	ts := e.now().UTC().Format(time.RFC3339Nano)
	_, err := e.db.Exec(`INSERT INTO tag(name,commit_sha,tagger,at) VALUES(?,?,?,?)`, name, commitSha, tagger, ts)
	return err
}

func (e *Engine) ListTags() ([]Tag, error) {
	rows, err := e.db.Query(`SELECT name,commit_sha,tagger FROM tag ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tag
	for rows.Next() {
		var tg Tag
		if err := rows.Scan(&tg.Name, &tg.Commit, &tg.Tagger); err != nil {
			return nil, err
		}
		out = append(out, tg)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Run, verify pass.**

- [ ] **Step 5: Commit**

```bash
git add internal/change/tag.go internal/change/tag_test.go
git commit -m "feat(change): tags (NEX-713)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 10: Git-compat export (NEX-714)

Project the change-graph into real refs in the go-git store so plain `git` reads it.

**Files:** Create `internal/change/export.go`; Test `internal/change/export_test.go`

- [ ] **Step 1: Failing test**

```go
package change

import (
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

func TestExportRefs(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	tip := seedLineTip(t, e, main.ID, map[string][]byte{"a.txt": []byte("a\n")})
	e.Tag("v1.0.0", tip, "rel")
	exp, _ := e.CreateLine("exp", main.ID)
	ch, _ := e.CreateChange(exp.ID, "e")
	r, _ := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("a\n"), "b.txt": []byte("b\n")})

	if err := e.Export(); err != nil {
		t.Fatalf("Export: %v", err)
	}
	// refs/heads/main → tip
	ref, err := e.git.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil || ref.Hash().String() != tip {
		t.Fatalf("refs/heads/main = %v (%v), want %s", ref, err, tip)
	}
	// refs/tags/v1.0.0 present
	if _, err := e.git.Reference(plumbing.NewTagReferenceName("v1.0.0"), true); err != nil {
		t.Fatalf("tag ref missing: %v", err)
	}
	// refs/cairn/change/<id> → change head
	cref, err := e.git.Reference(plumbing.ReferenceName("refs/cairn/change/"+ch.ID), true)
	if err != nil || cref.Hash().String() != r.HeadCommit {
		t.Fatalf("change ref = %v (%v), want %s", cref, err, r.HeadCommit)
	}
}
```

- [ ] **Step 2: Run, verify fail.**

- [ ] **Step 3: Implement `export.go`**

```go
package change

import "github.com/go-git/go-git/v5/plumbing"

// Export projects the change-graph into the go-git ref store: lines→refs/heads,
// open changes→refs/cairn/change/*, tags→refs/tags. Idempotent.
func (e *Engine) Export() error {
	// lines
	rows, err := e.db.Query(`SELECT name,tip_commit FROM line WHERE status!='abandoned' AND tip_commit!=''`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name, tip string
		if err := rows.Scan(&name, &tip); err != nil {
			rows.Close()
			return err
		}
		ref := plumbing.NewHashReference(plumbing.NewBranchReferenceName(name), plumbing.NewHash(tip))
		if err := e.git.Storer.SetReference(ref); err != nil {
			rows.Close()
			return err
		}
	}
	rows.Close()
	// open changes
	crows, err := e.db.Query(`SELECT id,head_commit FROM change WHERE status='open' AND head_commit!=''`)
	if err != nil {
		return err
	}
	for crows.Next() {
		var id, head string
		if err := crows.Scan(&id, &head); err != nil {
			crows.Close()
			return err
		}
		ref := plumbing.NewHashReference(plumbing.ReferenceName("refs/cairn/change/"+id), plumbing.NewHash(head))
		if err := e.git.Storer.SetReference(ref); err != nil {
			crows.Close()
			return err
		}
	}
	crows.Close()
	// tags
	trows, err := e.db.Query(`SELECT name,commit_sha FROM tag`)
	if err != nil {
		return err
	}
	defer trows.Close()
	for trows.Next() {
		var name, sha string
		if err := trows.Scan(&name, &sha); err != nil {
			return err
		}
		ref := plumbing.NewHashReference(plumbing.NewTagReferenceName(name), plumbing.NewHash(sha))
		if err := e.git.Storer.SetReference(ref); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 4: Run, verify pass.**

- [ ] **Step 5: Commit**

```bash
git add internal/change/export.go internal/change/export_test.go
git commit -m "feat(change): git-compat ref export (NEX-714)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 11: Concurrency harness + the 9 properties (NEX-715)

The Phase-1 proof. A seedable scheduler interleaves N agents committing scripted edits across lines; assert the nine spec properties (§8).

**Files:** Create `internal/change/harness/harness.go`, `internal/change/harness/properties_test.go`

- [ ] **Step 1: Implement the harness driver** (`harness.go`)

```go
// Package harness drives N simulated agents against one change.Engine with a
// seedable, deterministic scheduler to prove the Phase-1 convergence properties.
package harness

import (
	"math/rand"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

// Step is one scripted action by an agent.
type Step struct {
	Line   string            // line name to work on (created off Base if new)
	Base   string            // parent line name when creating Line
	Author string
	Files  map[string][]byte // full tree snapshot to commit
}

// Run executes steps in a seed-shuffled order against e, committing each on a
// fresh change. Returns the change id created per step (index-aligned).
func Run(e *change.Engine, steps []Step, seed int64) ([]string, error) {
	order := rand.New(rand.NewSource(seed)).Perm(len(steps))
	lineID := map[string]string{}
	root, _ := e.LineByName("main")
	lineID["main"] = root.ID
	ids := make([]string, len(steps))
	for _, idx := range order {
		s := steps[idx]
		if _, ok := lineID[s.Line]; !ok {
			parent := lineID[s.Base]
			l, err := e.CreateLine(s.Line, parent)
			if err != nil {
				return nil, err
			}
			lineID[s.Line] = l.ID
		}
		ch, err := e.CreateChange(lineID[s.Line], s.Author)
		if err != nil {
			return nil, err
		}
		if _, err := e.Commit(ch.ID, s.Files); err != nil {
			return nil, err
		}
		ids[idx] = ch.ID
	}
	return ids, nil
}
```

- [ ] **Step 2: Write the property tests** (`properties_test.go`) — one test per property, each run across a fixed seed list `[]int64{1, 2, 7, 42, 1000}`. Convergence (non-overlap), deterministic conflicts (overlap), no-blocking, fast-forward fold, abandon isolation, lineage integrity, op-log replay+undo, resolution closes loop, git-compat. Example (property 1 + 5; write the remaining seven analogously, each asserting the spec property):

```go
package harness

import (
	"testing"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

var seeds = []int64{1, 2, 7, 42, 1000}

func newEngine(t *testing.T) *change.Engine {
	t.Helper()
	e, err := change.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

// Property 1: non-overlapping edits converge to the union, any order.
func TestProp_ConvergenceNonOverlap(t *testing.T) {
	for _, seed := range seeds {
		e := newEngine(t)
		// main has a.txt; two sibling lines each add a distinct file.
		main, _ := e.LineByName("main")
		seedTip(t, e, main.ID, map[string][]byte{"a.txt": []byte("a\n")})
		steps := []Step{
			{Line: "x", Base: "main", Author: "ax", Files: map[string][]byte{"a.txt": []byte("a\n"), "x.txt": []byte("x\n")}},
			{Line: "y", Base: "main", Author: "ay", Files: map[string][]byte{"a.txt": []byte("a\n"), "y.txt": []byte("y\n")}},
		}
		if _, err := Run(e, steps, seed); err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		// fold both into main; assert union present, no conflicts.
		foldAll(t, e)
		main2, _ := e.LineByName("main")
		files := readTip(t, e, main2.TipCommit)
		if string(files["x.txt"]) != "x\n" || string(files["y.txt"]) != "y\n" {
			t.Fatalf("seed %d: union missing: %v", seed, files)
		}
	}
}

// Property 5: abandoning a branch (even conflicted) leaves the parent unchanged.
func TestProp_AbandonIsolation(t *testing.T) {
	for _, seed := range seeds {
		e := newEngine(t)
		main, _ := e.LineByName("main")
		baseTip := seedTip(t, e, main.ID, map[string][]byte{"a.txt": []byte("a\n")})
		exp, _ := e.CreateLine("exp", main.ID)
		ch, _ := e.CreateChange(exp.ID, "e")
		e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("WILD\n")})
		if err := e.AbandonLine(exp.ID); err != nil {
			t.Fatalf("seed %d abandon: %v", seed, err)
		}
		main2, _ := e.LineByName("main")
		if main2.TipCommit != baseTip {
			t.Fatalf("seed %d: parent moved %s != %s", seed, main2.TipCommit, baseTip)
		}
	}
}
```

Add harness helpers `seedTip`, `readTip`, `foldAll` (thin wrappers over engine calls, mirroring `seedLineTip`/`commitTree`/`readTree` usage). Write the remaining seven property tests against §8.

- [ ] **Step 3: Run the full property suite, verify all pass.**

Run: `go test ./internal/change/... -v`
Expected: all properties PASS across all seeds. If property 2 (deterministic conflicts) flakes, the fix lives in `diff3.sideChanges` per the Task-4 note — do not weaken the assertion.

- [ ] **Step 4: Run vet + full module test.**

Run: `go vet ./... && go test ./...`
Expected: clean; no failures.

- [ ] **Step 5: Commit**

```bash
git add internal/change/harness/
git commit -m "test(change): concurrency harness + 9 Phase-1 properties (NEX-715)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 12: Thin gRPC wrapper (NEX-716)

Expose the engine API as JSON-over-gRPC in `internal/grpcapi`, matching the existing handler style (`internal/grpcapi/grpcapi.go`). Author/tagger is supplied by the caller for Phase 1 (herald-stamping is Phase 2). Keep the in-process API the primary path; the wrapper only forwards.

**Files:** Create `internal/grpcapi/change.go`; Test `internal/grpcapi/change_test.go`

- [ ] **Step 1: Read the existing handler pattern.** Open `internal/grpcapi/grpcapi.go` and `repo.go`; mirror the request/response decode + error mapping conventions exactly.

- [ ] **Step 2: Failing test** — a `ChangeService` that wraps a `*change.Engine`, with one round-trip (`CreateLine` then `GetLineage`) asserting JSON in/out. Write the test against the handler funcs directly (no live server), as `grpcapi_test.go` does.

```go
package grpcapi

import (
	"context"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

func TestChangeServiceCreateLineLineage(t *testing.T) {
	e, err := change.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	svc := NewChangeService(e)
	main, _ := e.LineByName("main")
	if _, err := svc.CreateLine(context.Background(), "exp", main.ID); err != nil {
		t.Fatalf("CreateLine: %v", err)
	}
	chain, err := svc.GetLineage(context.Background(), mustLine(t, e, "exp").ID)
	if err != nil {
		t.Fatalf("GetLineage: %v", err)
	}
	if len(chain) != 2 {
		t.Fatalf("lineage len = %d, want 2", len(chain))
	}
}

func mustLine(t *testing.T, e *change.Engine, name string) change.Line {
	l, err := e.LineByName(name)
	if err != nil {
		t.Fatal(err)
	}
	return l
}
```

- [ ] **Step 3: Run, verify fail.**

- [ ] **Step 4: Implement `internal/grpcapi/change.go`** — a `ChangeService` struct holding `*change.Engine`, with thin methods forwarding to the engine (`CreateLine`, `FoldLine`, `AbandonLine`, `ListLines`, `GetLineage`, `GetLineTree`, `CreateChange`, `Commit`, `GetChange`, `Conflicts`, `ResolveConflict`, `Tag`, `ListTags`, `OperationLog`, `Undo`). Each maps engine errors to the same gRPC status codes used in `repo.go` (`ErrNotFound`→`codes.NotFound`, `ErrHasConflict`→`codes.FailedPrecondition`).

- [ ] **Step 5: Run, verify pass.** `go test ./internal/grpcapi/ -run TestChangeService -v` → PASS.

- [ ] **Step 6: Final gate.** `go vet ./... && go test ./...` → clean.

- [ ] **Step 7: Commit**

```bash
git add internal/grpcapi/change.go internal/grpcapi/change_test.go
git commit -m "feat(grpcapi): thin gRPC wrapper for the change engine (NEX-716)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Self-Review

**Spec coverage (§9 build sequence → tasks):**
- §9.1 change_id/Commit/store → Tasks 1–2 ✓
- §9.2 line + lineage → Task 3 ✓
- §9.3 op-log + Undo → Task 8 ✓
- §9.4 diff3 → Task 4 ✓
- §9.5 merge-forward → Task 5 ✓
- §9.6 fold + abandon → Task 7 ✓
- §9.7 conflict + resolve → Task 6 ✓
- §9.8 tags → Task 9 ✓
- §9.9 git-export → Task 10 ✓
- §9.10 harness (9 props) → Task 11 ✓
- §9.11 gRPC seam → Task 12 ✓

**Out of Phase-1 scope (correctly absent):** porter/CoW mounts, the CLI, origin-sync, privacy redaction, version derivation, ledger — all later phases per spec §1 OUT.

**Type consistency:** `Engine`, `Line`, `Change`, `Conflict`, `Operation`, `CommitResult`, `Tag`, `LineNode` defined once and reused; method names (`Commit`, `mergeForward`, `mergeTrees`, `recordConflict`, `recordOp`, `viewMap`, `commitTree`, `writeTree`, `readTree`, `writeBlob`, `readBlob`, `writeCommit`, `mergeBase`) are consistent across tasks. The Task-5 `recordConflict` stub is explicitly replaced in Task 6.

**Ordering dependency:** Task 5 references `recordConflict` (Task 6) as a stub-then-complete; Task 8 wires `recordOp` into mutators from Tasks 3/5/6/7/9. Implement in numeric order; the stub note in Task 5 prevents a dangling reference.

**Known sharp edges flagged for the implementer:** diff3 insert/replace overlap at the same base index (Task 4 note); merge-base across independent roots returns `""` (handled in `mergeForward`); SQLite serialises writes so the harness must not share one `*sql.DB` across goroutines without the engine's mutexing — Phase-1 harness runs steps sequentially in seed order (no real parallel goroutines), which is sufficient for the convergence properties and sidesteps that.
