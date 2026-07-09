// Package change is cairn's convergence-core: a per-repo, local-first change
// engine with no server. A bare go-git object store owns blobs/trees/commits;
// a SQLite catalogue owns lines, changes, conflicts, tags, and the operation
// log. The engine mirrors the conventions of the internal/repo package.
package change

import (
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/CarriedWorldUniverse/cairn/internal/winretry"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	gitcache "github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage/filesystem"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// ErrNotFound is returned when a catalogue row does not exist.
var ErrNotFound = errors.New("change: not found")

// RootLineName is the name of the line that every engine bootstraps with.
const RootLineName = "main"

// Engine is the per-repo change engine. The bare go-git repo and the SQLite
// catalogue live side by side under dir.
type Engine struct {
	dir      string
	git      *git.Repository
	db       *sql.DB
	now      func() time.Time
	idName   string
	idEmail  string
	progress io.Writer
}

// SetIdentity configures the author identity used for all commits this engine
// writes. Empty values fall back to safe placeholders in writeCommit.
func (e *Engine) SetIdentity(name, email string) { e.idName, e.idEmail = name, email }

// Identity returns the configured author name and email (either may be "" when
// unset).
func (e *Engine) Identity() (name, email string) { return e.idName, e.idEmail }

// SetProgress sets the writer that network fetch progress (the git sideband:
// counting/compressing/receiving objects) is streamed to during clone/fetch.
// nil (the default) disables progress output. The CLI points this at os.Stderr.
func (e *Engine) SetProgress(w io.Writer) { e.progress = w }

// Open opens (creating if needed) the change engine rooted at dir. It opens or
// initialises a bare go-git object store at dir/objects.git and the SQLite
// catalogue at dir/cairn.db, applies the schema, and ensures the root line.
func Open(dir string) (*Engine, error) {
	// Build the bare git object store on a filesystem whose Rename retries
	// transient Windows file locks. go-git writes every object to a temp file then
	// renames it into the store; on a large tree that rename races antivirus /
	// search-indexer handles and fails with "Access is denied" — near-certain at
	// scale on Windows. On non-Windows the wrapper is a no-op pass-through.
	gitDir := filepath.Join(dir, "objects.git")
	store := filesystem.NewStorage(winretry.FS(osfs.New(gitDir)), gitcache.NewObjectLRUDefault())
	g, err := git.Open(store, nil)
	if errors.Is(err, git.ErrRepositoryNotExists) {
		g, err = git.Init(store, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("change.Open: git: %w", err)
	}

	// _pragma=busy_timeout(5000) makes writers wait up to 5s for a held lock
	// instead of failing immediately with SQLITE_BUSY. _pragma=foreign_keys(1)
	// enforces foreign keys on EVERY pooled connection (a one-shot PRAGMA in
	// schema.sql only applies to the connection that ran it). These are the
	// query-param form modernc.org/sqlite documents (a bare _busy_timeout is not
	// parsed).
	db, err := sql.Open("sqlite", filepath.Join(dir, "cairn.db")+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("change.Open: sqlite: %w", err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("change.Open: schema: %w", err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	e := &Engine{dir: dir, git: g, db: db, now: time.Now}
	if err := e.ensureRootLine(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return e, nil
}

// Close releases the database handle.
func (e *Engine) Close() error { return e.db.Close() }

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// migrate applies idempotent ALTER TABLE migrations so repos created before a
// column was added pick it up. Each statement is run unconditionally; a
// "duplicate column name" error (the column already exists, e.g. a fresh repo
// where schema.sql already created it) is ignored. Add new column migrations to
// this list as the schema evolves.
func migrate(db *sql.DB) error {
	migrations := []string{
		`ALTER TABLE change ADD COLUMN sealed INTEGER NOT NULL DEFAULT 0`,
		// tracks_remote marks a line that ARRIVED from a remote (set on clone/
		// import, never on push). The fold guard warns before folding into one.
		`ALTER TABLE line ADD COLUMN tracks_remote INTEGER NOT NULL DEFAULT 0`,
	}
	for _, m := range migrations {
		if _, err := db.Exec(m); err != nil {
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			return fmt.Errorf("change.migrate: %w", err)
		}
	}
	return nil
}

// ensureRootLine inserts the root line ("main", no parent) if no root exists.
// It probes by STRUCTURE (the unique parent_line IS NULL row), not by name, so
// that a re-Open after ImportFromRemote renamed the root to the remote default
// (e.g. "master") does not insert a SECOND root and corrupt the unique-root
// invariant. Idempotent: opening an existing engine is a no-op.
func (e *Engine) ensureRootLine() error {
	var count int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM line WHERE parent_line IS NULL`).Scan(&count); err != nil {
		return fmt.Errorf("change.ensureRootLine: %w", err)
	}
	if count > 0 {
		return nil
	}
	now := e.now().UTC().Format(time.RFC3339Nano)
	if _, err := e.db.Exec(
		`INSERT INTO line(id, name, parent_line, tip_commit, base_commit, status, created_at, updated_at)
		 VALUES(?,?,NULL,'','','open',?,?)`,
		newID(), RootLineName, now, now); err != nil {
		return fmt.Errorf("change.ensureRootLine: %w", err)
	}
	return nil
}

// LineOfCommit resolves the line name that owns commit (by full or short sha).
// It looks up the commit's Change-Id trailer, finds the owning change row, then
// reads the line name. Returns ErrNotFound when the commit is not a cairn commit
// or its change row is absent.
func (e *Engine) LineOfCommit(commit string) (string, error) {
	full, err := e.git.ResolveRevision(plumbing.Revision(commit))
	if err != nil {
		return "", fmt.Errorf("change.LineOfCommit: resolve %q: %w", commit, err)
	}
	cid := e.changeIDOf(full.String())
	if cid == "" {
		return "", fmt.Errorf("change.LineOfCommit: %w: no Change-Id on commit %s", ErrNotFound, commit)
	}
	ch, err := e.GetChange(cid)
	if err != nil {
		return "", fmt.Errorf("change.LineOfCommit: %w", err)
	}
	line, err := e.lineByID(ch.LineID)
	if err != nil {
		return "", fmt.Errorf("change.LineOfCommit: %w", err)
	}
	return line.Name, nil
}

// ResolveCommit resolves a user-supplied commit revision — a full OR abbreviated
// SHA, or any git revision (e.g. a tag) — to its full 40-char hash. cairn's `log`
// prints short SHAs, so every command that takes a commit argument must accept
// them; callers normalize the user's argument through this before handing a SHA
// to the catalogue/object lookups, which expect full hashes.
func (e *Engine) ResolveCommit(rev string) (string, error) {
	h, err := e.git.ResolveRevision(plumbing.Revision(rev))
	if err != nil {
		return "", fmt.Errorf("change.ResolveCommit: %q: %w", rev, err)
	}
	return h.String(), nil
}

// ErrNoCommonAncestor is returned by MergeBase when a and b share no common
// history (genuinely unrelated histories) — there is no valid merge-base to
// diff from.
var ErrNoCommonAncestor = errors.New("change: no common ancestor")

// MergeBase resolves the best common ancestor of two revisions (any form
// ResolveCommit accepts) to its full hash — the merge-base git itself uses for
// a `target...source` three-dot diff. Returns ErrNoCommonAncestor if a and b
// share no history.
func (e *Engine) MergeBase(a, b string) (string, error) {
	ha, err := e.ResolveCommit(a)
	if err != nil {
		return "", fmt.Errorf("change.MergeBase: %w", err)
	}
	hb, err := e.ResolveCommit(b)
	if err != nil {
		return "", fmt.Errorf("change.MergeBase: %w", err)
	}
	ca, err := e.git.CommitObject(plumbing.NewHash(ha))
	if err != nil {
		return "", fmt.Errorf("change.MergeBase: load %s: %w", a, err)
	}
	cb, err := e.git.CommitObject(plumbing.NewHash(hb))
	if err != nil {
		return "", fmt.Errorf("change.MergeBase: load %s: %w", b, err)
	}
	bases, err := ca.MergeBase(cb)
	if err != nil {
		return "", fmt.Errorf("change.MergeBase: %s...%s: %w", a, b, err)
	}
	if len(bases) == 0 {
		return "", fmt.Errorf("change.MergeBase: %s and %s: %w", a, b, ErrNoCommonAncestor)
	}
	return bases[0].Hash.String(), nil
}

// DiffMergeBase returns the per-path diff introduced by source since it
// forked from target — target...source ("three-dot") semantics, resolving the
// merge-base of the two revisions and diffing FROM THERE to source's tip. This
// is what `gh pr diff`/GitHub's PR diff compute, and is deliberately distinct
// from DiffCommits' literal tip-to-tip diff: it stays correct as target
// advances with commits source never saw (those never appear as spurious
// revert hunks). Returns ErrNoCommonAncestor if target and source share no
// history.
func (e *Engine) DiffMergeBase(target, source string) ([]FileDiff, error) {
	mb, err := e.MergeBase(target, source)
	if err != nil {
		return nil, fmt.Errorf("change.DiffMergeBase: %w", err)
	}
	fullSource, err := e.ResolveCommit(source)
	if err != nil {
		return nil, fmt.Errorf("change.DiffMergeBase: %w", err)
	}
	diffs, err := e.DiffCommits(mb, fullSource)
	if err != nil {
		return nil, fmt.Errorf("change.DiffMergeBase: %w", err)
	}
	return diffs, nil
}

// LineByName loads a line by its unique name, or returns ErrNotFound.
func (e *Engine) LineByName(name string) (Line, error) {
	row := e.db.QueryRow(
		`SELECT id, name, parent_line, tip_commit, base_commit, status FROM line WHERE name=?`,
		name)
	var l Line
	var parent sql.NullString
	if err := row.Scan(&l.ID, &l.Name, &parent, &l.TipCommit, &l.BaseCommit, &l.Status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Line{}, ErrNotFound
		}
		return Line{}, fmt.Errorf("change.LineByName: %w", err)
	}
	l.ParentLine = parent.String
	return l, nil
}
