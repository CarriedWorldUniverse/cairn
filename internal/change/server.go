package change

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	gitcache "github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage/filesystem"

	"github.com/CarriedWorldUniverse/cairn/internal/winretry"
)

// OpenBare opens a server-side, read-mostly engine whose git object store IS an
// existing BARE repository at bareGitDir (standard bare layout — objects/ refs/
// HEAD at the top, as `git init --bare` and the cairn server's repo.Service
// produce). Unlike Open, it does NOT expect a nested dir/objects.git or create a
// dir/cairn.db: the catalogue is held IN MEMORY and (re)built from the pushed
// refs/cairn/meta via LoadFromMeta — the meta ref is the single source of truth
// on the server, so there is no second on-disk catalogue to keep coherent under
// concurrent pushes. Call Close to release it.
//
// This is the foundation for a convergence-AWARE server: it lets cairn-server
// read a hosted repo's change-graph (line tree, changes, conflicts) and, in
// later slices, serve a redacted projection and enforce the privacy tier.
func OpenBare(bareGitDir string) (*Engine, error) {
	store := filesystem.NewStorage(winretry.FS(osfs.New(bareGitDir)), gitcache.NewObjectLRUDefault())
	g, err := git.Open(store, nil)
	if err != nil {
		return nil, fmt.Errorf("change.OpenBare: open bare %q: %w", bareGitDir, err)
	}
	// In-memory catalogue pinned to a single long-lived connection so the DB
	// persists for the engine's lifetime and every read observes the same state
	// (a plain ":memory:" DB is per-connection, so the pool must not open more).
	db, err := sql.Open("sqlite", ":memory:?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("change.OpenBare: sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	if _, err := db.Exec(schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("change.OpenBare: schema: %w", err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	// No ensureRootLine: a server engine has no inherent root line — LoadFromMeta
	// installs the exact graph from the meta ref (a plain-git or not-yet-pushed
	// repo stays line-less).
	return &Engine{dir: bareGitDir, git: g, db: db, now: time.Now}, nil
}

// LoadFromMeta (re)builds the catalogue from refs/cairn/meta. Returns (false,nil)
// when the repo carries no cairn metadata (a plain git repo, or one not yet
// pushed by a cairn client) — the catalogue stays empty. Idempotent: importMeta
// clears and reinstalls, so it can be re-run after a fresh push to pick up the
// new graph.
func (e *Engine) LoadFromMeta() (bool, error) {
	ref, err := e.git.Reference(plumbing.ReferenceName("refs/cairn/meta"), false)
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("change.LoadFromMeta: %w", err)
	}
	tx, err := e.db.Begin()
	if err != nil {
		return false, fmt.Errorf("change.LoadFromMeta: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	ts := e.now().UTC().Format(time.RFC3339Nano)
	if _, err := e.importMeta(ref.Hash().String(), tx, ts); err != nil {
		return false, fmt.Errorf("change.LoadFromMeta: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("change.LoadFromMeta: %w", err)
	}
	return true, nil
}

// OpenConflictCount returns the number of open conflict objects across the repo —
// a cheap server-side health read of the change-graph.
func (e *Engine) OpenConflictCount() (int, error) {
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM conflict WHERE status='open'`).Scan(&n); err != nil {
		return 0, fmt.Errorf("change.OpenConflictCount: %w", err)
	}
	return n, nil
}
