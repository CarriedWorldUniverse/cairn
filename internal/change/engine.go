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
	"path/filepath"
	"time"

	"github.com/go-git/go-git/v5"
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
	dir     string
	git     *git.Repository
	db      *sql.DB
	now     func() time.Time
	idName  string
	idEmail string
}

// SetIdentity configures the author identity used for all commits this engine
// writes. Empty values fall back to safe placeholders in writeCommit.
func (e *Engine) SetIdentity(name, email string) { e.idName, e.idEmail = name, email }

// Open opens (creating if needed) the change engine rooted at dir. It opens or
// initialises a bare go-git object store at dir/objects.git and the SQLite
// catalogue at dir/cairn.db, applies the schema, and ensures the root line.
func Open(dir string) (*Engine, error) {
	gitDir := filepath.Join(dir, "objects.git")
	g, err := git.PlainOpen(gitDir)
	if errors.Is(err, git.ErrRepositoryNotExists) {
		g, err = git.PlainInit(gitDir, true)
	}
	if err != nil {
		return nil, fmt.Errorf("change.Open: git: %w", err)
	}

	// _pragma=busy_timeout(5000) makes writers wait up to 5s for a held lock
	// instead of failing immediately with SQLITE_BUSY. This is the query-param
	// form modernc.org/sqlite documents (a bare _busy_timeout is not parsed).
	db, err := sql.Open("sqlite", filepath.Join(dir, "cairn.db")+"?_pragma=busy_timeout(5000)")
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

// Close releases the database handle.
func (e *Engine) Close() error { return e.db.Close() }

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
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
