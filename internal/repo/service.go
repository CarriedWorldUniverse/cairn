// Package repo is cairn's repo + ref core: the one engine both ingresses
// (SSH, HTTP) call. go-git owns on-disk object/ref storage for each bare
// repo; a SQLite catalogue owns repo discovery + the push audit log.
package repo

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// ErrNotFound is returned when a repo or ref does not exist.
var ErrNotFound = errors.New("repo: not found")

// Repo is a row in the repo catalogue.
type Repo struct {
	ID            string
	OrgID         string
	Slug          string
	DefaultBranch string
	Protection    string // JSON; minimal default-branch rule
	StoragePath   string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Ref is a single git reference (branch/tag) in a repo.
type Ref struct {
	Name string // full ref, e.g. refs/heads/main
	Hash string // 40-char hex sha
}

// PushEvent is one audit row recorded at receive-pack.
type PushEvent struct {
	RepoID        string
	Ref           string
	OldSHA        string
	NewSHA        string
	PusherAgentID string
	Forced        bool
}

// HookInstaller writes server-side hooks into a freshly created bare repo's
// hooks dir. The binary wires the pre-receive protection hook here; tests and
// the core itself stay independent of the protect package.
type HookInstaller func(repoID, hooksDir string) error

// Service is the repo + ref core. Safe for concurrent use (SQLite serialises
// writes; go-git opens each repo per call).
type Service struct {
	db            *sql.DB
	repoRoot      string // directory under which bare repos live
	hookInstaller HookInstaller
}

// Open opens (creating if needed) the SQLite catalogue at dbPath and ensures
// repoRoot exists. The on-disk layout is repoRoot/<repo-id>.git.
func Open(dbPath, repoRoot string) (*Service, error) {
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		return nil, fmt.Errorf("repo.Open: mkdir repoRoot: %w", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("repo.Open: sqlite: %w", err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("repo.Open: schema: %w", err)
	}
	return &Service{db: db, repoRoot: repoRoot}, nil
}

// Close releases the database handle.
func (s *Service) Close() error { return s.db.Close() }

// SetHookInstaller registers the hook installer used by CreateRepo. Optional;
// if unset, no server-side hooks are written.
func (s *Service) SetHookInstaller(h HookInstaller) { s.hookInstaller = h }

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// storagePath is the absolute on-disk path for a repo id.
func (s *Service) storagePath(id string) string {
	return filepath.Join(s.repoRoot, id+".git")
}

// CreateRepo creates the catalogue row and initialises an empty bare go-git
// repo on disk. Fails if (org, slug) already exists.
func (s *Service) CreateRepo(ctx context.Context, orgID, slug string) (Repo, error) {
	if orgID == "" || slug == "" {
		return Repo{}, errors.New("repo.CreateRepo: org and slug required")
	}
	now := time.Now().UTC()
	r := Repo{
		ID:            newID(),
		OrgID:         orgID,
		Slug:          slug,
		DefaultBranch: "main",
		Protection:    "{}",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	r.StoragePath = s.storagePath(r.ID)

	g, err := git.PlainInit(r.StoragePath, true)
	if err != nil {
		return Repo{}, fmt.Errorf("repo.CreateRepo: git init: %w", err)
	}
	// go-git's PlainInit points HEAD at refs/heads/master. cairn's default
	// branch is "main", so without this a client that pushes "main" leaves the
	// bare repo's HEAD dangling at the unborn "master" — and a fresh `git clone`
	// then checks out nothing. Point HEAD at the declared default branch.
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName(r.DefaultBranch))
	if err := g.Storer.SetReference(headRef); err != nil {
		_ = os.RemoveAll(r.StoragePath)
		return Repo{}, fmt.Errorf("repo.CreateRepo: set HEAD: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO repo(id, org_id, slug, default_branch, protection, storage_path, created_at, updated_at)
		 VALUES(?,?,?,?,?,?,?,?)`,
		r.ID, r.OrgID, r.Slug, r.DefaultBranch, r.Protection, r.StoragePath,
		now.Format(time.RFC3339), now.Format(time.RFC3339))
	if err != nil {
		_ = os.RemoveAll(r.StoragePath) // roll back the on-disk repo
		return Repo{}, fmt.Errorf("repo.CreateRepo: insert: %w", err)
	}

	if s.hookInstaller != nil {
		hooksDir := filepath.Join(r.StoragePath, "hooks")
		if err := s.hookInstaller(r.ID, hooksDir); err != nil {
			return Repo{}, fmt.Errorf("repo.CreateRepo: install hooks: %w", err)
		}
	}
	return r, nil
}

func scanRepo(row interface{ Scan(...any) error }) (Repo, error) {
	var r Repo
	var created, updated string
	if err := row.Scan(&r.ID, &r.OrgID, &r.Slug, &r.DefaultBranch,
		&r.Protection, &r.StoragePath, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Repo{}, ErrNotFound
		}
		return Repo{}, err
	}
	r.CreatedAt, _ = time.Parse(time.RFC3339, created)
	r.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return r, nil
}

const repoCols = `id, org_id, slug, default_branch, protection, storage_path, created_at, updated_at`

// GetRepo resolves a repo by (org, slug).
func (s *Service) GetRepo(ctx context.Context, orgID, slug string) (Repo, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+repoCols+` FROM repo WHERE org_id=? AND slug=?`, orgID, slug)
	r, err := scanRepo(row)
	if err != nil {
		return Repo{}, fmt.Errorf("repo.GetRepo: %w", err)
	}
	return r, nil
}

// GetRepoByID resolves a repo by its id (used by the ingresses after a path
// lookup).
func (s *Service) GetRepoByID(ctx context.Context, id string) (Repo, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+repoCols+` FROM repo WHERE id=?`, id)
	r, err := scanRepo(row)
	if err != nil {
		return Repo{}, fmt.Errorf("repo.GetRepoByID: %w", err)
	}
	return r, nil
}

// ListRepos lists all repos in an org, slug-ordered.
func (s *Service) ListRepos(ctx context.Context, orgID string) ([]Repo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+repoCols+` FROM repo WHERE org_id=? ORDER BY slug`, orgID)
	if err != nil {
		return nil, fmt.Errorf("repo.ListRepos: %w", err)
	}
	defer rows.Close()
	var out []Repo
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, fmt.Errorf("repo.ListRepos: scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteRepo removes the catalogue row (cascading push_events) and the on-disk
// bare repo.
func (s *Service) DeleteRepo(ctx context.Context, id string) error {
	r, err := s.GetRepoByID(ctx, id)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM repo WHERE id=?`, id); err != nil {
		return fmt.Errorf("repo.DeleteRepo: delete row: %w", err)
	}
	if err := os.RemoveAll(r.StoragePath); err != nil {
		return fmt.Errorf("repo.DeleteRepo: remove storage: %w", err)
	}
	return nil
}

// openGit opens the on-disk bare repo for a repo id.
func (s *Service) openGit(ctx context.Context, repoID string) (*git.Repository, error) {
	r, err := s.GetRepoByID(ctx, repoID)
	if err != nil {
		return nil, err
	}
	g, err := git.PlainOpen(r.StoragePath)
	if err != nil {
		return nil, fmt.Errorf("repo.openGit: %w", err)
	}
	return g, nil
}

// ListRefs lists every reference in the repo (heads + tags), excluding the
// symbolic HEAD.
func (s *Service) ListRefs(ctx context.Context, repoID string) ([]Ref, error) {
	g, err := s.openGit(ctx, repoID)
	if err != nil {
		return nil, err
	}
	iter, err := g.References()
	if err != nil {
		return nil, fmt.Errorf("repo.ListRefs: %w", err)
	}
	defer iter.Close()
	var out []Ref
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() == plumbing.SymbolicReference {
			return nil // skip HEAD
		}
		out = append(out, Ref{Name: ref.Name().String(), Hash: ref.Hash().String()})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("repo.ListRefs: iterate: %w", err)
	}
	return out, nil
}

// GetRef resolves a single full ref name (e.g. refs/heads/main).
func (s *Service) GetRef(ctx context.Context, repoID, name string) (Ref, error) {
	g, err := s.openGit(ctx, repoID)
	if err != nil {
		return Ref{}, err
	}
	ref, err := g.Reference(plumbing.ReferenceName(name), false)
	if err != nil {
		return Ref{}, fmt.Errorf("repo.GetRef: %w: %w", ErrNotFound, err)
	}
	return Ref{Name: ref.Name().String(), Hash: ref.Hash().String()}, nil
}

// StoragePathForID returns the on-disk path for a repo id. The ingresses use
// this to hand go-git's transport layer the right directory.
func (s *Service) StoragePathForID(ctx context.Context, id string) (string, error) {
	r, err := s.GetRepoByID(ctx, id)
	if err != nil {
		return "", err
	}
	return r.StoragePath, nil
}

// RecordPush appends a push_event audit row.
func (s *Service) RecordPush(ctx context.Context, e PushEvent) error {
	forced := 0
	if e.Forced {
		forced = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO push_event(id, repo_id, ref, old_sha, new_sha, pusher_agent_id, forced, at)
		 VALUES(?,?,?,?,?,?,?,?)`,
		newID(), e.RepoID, e.Ref, e.OldSHA, e.NewSHA, e.PusherAgentID, forced,
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("repo.RecordPush: %w", err)
	}
	return nil
}
