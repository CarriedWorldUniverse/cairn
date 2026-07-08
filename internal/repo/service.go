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

// PullStateOpen is the only state this build writes; merged/closed are reserved.
const PullStateOpen = "open"

// ErrPullNotFound is returned when no pull request matches.
var ErrPullNotFound = errors.New("repo: pull request not found")

// ErrNotFastForward means source and target have diverged; a fast-forward merge
// is impossible (the caller should rebase). ErrAlreadyUpToDate means target
// already contains source (no ref change needed).
var (
	ErrNotFastForward  = errors.New("repo: not a fast-forward")
	ErrAlreadyUpToDate = errors.New("repo: already up to date")
)

// Pull is a row in the pull_request catalogue: a source→target proposal bound
// to a ledger tracking issue.
type Pull struct {
	ID             string
	RepoID         string
	Source         string
	Target         string
	Title          string
	LedgerIssueKey string
	State          string
	OpenedBy       string
	CreatedAt      time.Time
}

// PullCheck states. RecordPullCheck rejects anything else.
const (
	CheckStatePass    = "pass"
	CheckStateFail    = "fail"
	CheckStatePending = "pending"
)

// PullCheck is a row in the pull_check catalogue: a named check verdict on a
// pull, upserted by (pull, name).
type PullCheck struct {
	ID          string
	PullID      string
	Name        string
	State       string
	Summary     string
	EvidenceURL string
	RecordedBy  string
	RecordedAt  time.Time
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

// CreatePull inserts an open pull request. The partial-unique index rejects a
// second open PR for the same (repo, source, target). Populates p.ID/State.
func (s *Service) CreatePull(ctx context.Context, p *Pull) error {
	if p.RepoID == "" || p.Source == "" || p.Target == "" || p.Title == "" || p.LedgerIssueKey == "" {
		return errors.New("repo.CreatePull: repo, source, target, title, ledger_issue_key required")
	}
	p.ID = newID()
	p.State = PullStateOpen
	p.CreatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO pull_request(id, repo_id, source_ref, target_ref, title, ledger_issue_key, state, opened_by, created_at)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		p.ID, p.RepoID, p.Source, p.Target, p.Title, p.LedgerIssueKey, p.State, p.OpenedBy,
		p.CreatedAt.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("repo.CreatePull: %w", err)
	}
	return nil
}

const pullCols = `id, repo_id, source_ref, target_ref, title, ledger_issue_key, state, opened_by, created_at`

func scanPull(row interface{ Scan(...any) error }) (Pull, error) {
	var p Pull
	var created string
	if err := row.Scan(&p.ID, &p.RepoID, &p.Source, &p.Target, &p.Title,
		&p.LedgerIssueKey, &p.State, &p.OpenedBy, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Pull{}, ErrPullNotFound
		}
		return Pull{}, err
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return p, nil
}

// GetPull loads a pull request by (repo, id).
func (s *Service) GetPull(ctx context.Context, repoID, id string) (Pull, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+pullCols+` FROM pull_request WHERE repo_id=? AND id=?`, repoID, id)
	p, err := scanPull(row)
	if err != nil {
		return Pull{}, fmt.Errorf("repo.GetPull: %w", err)
	}
	return p, nil
}

// ListPulls returns the repo's pull requests, newest first. state "" or "all"
// returns every state; otherwise filters by exact state ("open"|"merged"|"closed").
func (s *Service) ListPulls(ctx context.Context, repoID, state string) ([]Pull, error) {
	q := `SELECT ` + pullCols + ` FROM pull_request WHERE repo_id=?`
	args := []any{repoID}
	if state != "" && state != "all" {
		q += ` AND state=?`
		args = append(args, state)
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("repo.ListPulls: %w", err)
	}
	defer rows.Close()
	var out []Pull
	for rows.Next() {
		p, err := scanPull(rows)
		if err != nil {
			return nil, fmt.Errorf("repo.ListPulls: scan: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// FindOpenPull returns the open pull request for (repo, source, target), or
// ErrPullNotFound. Used for idempotent open.
func (s *Service) FindOpenPull(ctx context.Context, repoID, source, target string) (Pull, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+pullCols+` FROM pull_request
		 WHERE repo_id=? AND source_ref=? AND target_ref=? AND state='open'`,
		repoID, source, target)
	p, err := scanPull(row)
	if err != nil {
		return Pull{}, fmt.Errorf("repo.FindOpenPull: %w", err)
	}
	return p, nil
}

// FastForward advances refs/heads/<target> to the tip of refs/heads/<source>
// when target is an ancestor of source. Returns the resulting target sha.
//   - target already contains source     → ErrAlreadyUpToDate (no change)
//   - target is a strict ancestor of src  → advance target, return src sha
//   - diverged                             → ErrNotFastForward
// A missing branch returns a wrapped ErrNotFound.
func (s *Service) FastForward(ctx context.Context, repoID, source, target string) (string, error) {
	g, err := s.openGit(ctx, repoID)
	if err != nil {
		return "", err
	}
	srcRef, err := g.Reference(plumbing.NewBranchReferenceName(source), true)
	if err != nil {
		return "", fmt.Errorf("repo.FastForward: source %q: %w: %w", source, ErrNotFound, err)
	}
	tgtRef, err := g.Reference(plumbing.NewBranchReferenceName(target), true)
	if err != nil {
		return "", fmt.Errorf("repo.FastForward: target %q: %w: %w", target, ErrNotFound, err)
	}
	if srcRef.Hash() == tgtRef.Hash() {
		return tgtRef.Hash().String(), ErrAlreadyUpToDate
	}
	srcCommit, err := g.CommitObject(srcRef.Hash())
	if err != nil {
		return "", fmt.Errorf("repo.FastForward: load source commit: %w", err)
	}
	tgtCommit, err := g.CommitObject(tgtRef.Hash())
	if err != nil {
		return "", fmt.Errorf("repo.FastForward: load target commit: %w", err)
	}
	if ok, _ := srcCommit.IsAncestor(tgtCommit); ok {
		return tgtRef.Hash().String(), ErrAlreadyUpToDate
	}
	if ok, _ := tgtCommit.IsAncestor(srcCommit); ok {
		newRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(target), srcRef.Hash())
		if err := g.Storer.SetReference(newRef); err != nil {
			return "", fmt.Errorf("repo.FastForward: advance target: %w", err)
		}
		return srcRef.Hash().String(), nil
	}
	return "", ErrNotFastForward
}

// SetPullState updates a pull request's state (e.g. to "merged").
func (s *Service) SetPullState(ctx context.Context, repoID, id, state string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE pull_request SET state=? WHERE repo_id=? AND id=?`, state, repoID, id)
	if err != nil {
		return fmt.Errorf("repo.SetPullState: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrPullNotFound
	}
	return nil
}

// RecordPullCheck upserts a named check verdict on a pull: a second call with
// the same (pull, name) replaces state/summary/evidence_url/recorder/time.
func (s *Service) RecordPullCheck(ctx context.Context, c *PullCheck) error {
	if c.PullID == "" || c.Name == "" {
		return errors.New("repo.RecordPullCheck: pull_id and name required")
	}
	switch c.State {
	case CheckStatePass, CheckStateFail, CheckStatePending:
	default:
		return fmt.Errorf("repo.RecordPullCheck: invalid state %q", c.State)
	}
	c.RecordedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO pull_check(id, pull_id, name, state, summary, evidence_url, recorded_by, recorded_at)
		 VALUES(?,?,?,?,?,?,?,?)
		 ON CONFLICT(pull_id, name) DO UPDATE SET
		   state=excluded.state, summary=excluded.summary,
		   evidence_url=excluded.evidence_url, recorded_by=excluded.recorded_by,
		   recorded_at=excluded.recorded_at`,
		newID(), c.PullID, c.Name, c.State, c.Summary, c.EvidenceURL, c.RecordedBy,
		c.RecordedAt.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("repo.RecordPullCheck: %w", err)
	}
	// The upsert keeps the pre-existing row's id on a repeat name; read it
	// back so the caller (and the RPC response) always reflects the true id.
	row := s.db.QueryRowContext(ctx,
		`SELECT id FROM pull_check WHERE pull_id=? AND name=?`, c.PullID, c.Name)
	if err := row.Scan(&c.ID); err != nil {
		return fmt.Errorf("repo.RecordPullCheck: reload id: %w", err)
	}
	return nil
}

const pullCheckCols = `id, pull_id, name, state, summary, evidence_url, recorded_by, recorded_at`

func scanPullCheck(row interface{ Scan(...any) error }) (PullCheck, error) {
	var c PullCheck
	var recordedAt string
	if err := row.Scan(&c.ID, &c.PullID, &c.Name, &c.State, &c.Summary,
		&c.EvidenceURL, &c.RecordedBy, &recordedAt); err != nil {
		return PullCheck{}, err
	}
	c.RecordedAt, _ = time.Parse(time.RFC3339, recordedAt)
	return c, nil
}

// ListPullChecks returns a pull's recorded checks, name-ordered.
func (s *Service) ListPullChecks(ctx context.Context, pullID string) ([]PullCheck, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+pullCheckCols+` FROM pull_check WHERE pull_id=? ORDER BY name`, pullID)
	if err != nil {
		return nil, fmt.Errorf("repo.ListPullChecks: %w", err)
	}
	defer rows.Close()
	var out []PullCheck
	for rows.Next() {
		c, err := scanPullCheck(rows)
		if err != nil {
			return nil, fmt.Errorf("repo.ListPullChecks: scan: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
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

// DeleteRepo removes all dependent rows (pull_request, push_event) and the
// catalogue row in a single transaction, then removes on-disk storage.
// Dependent rows are deleted explicitly rather than relying on ON DELETE CASCADE
// because the SQLite connection pool does not inherit the PRAGMA foreign_keys=ON
// that the schema DDL sets; cascade is unreliable across pool connections.
func (s *Service) DeleteRepo(ctx context.Context, id string) error {
	r, err := s.GetRepoByID(ctx, id)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("repo.DeleteRepo: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM pull_check WHERE pull_id IN (SELECT id FROM pull_request WHERE repo_id=?)`, id); err != nil {
		return fmt.Errorf("repo.DeleteRepo: pull_check: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM pull_request WHERE repo_id=?`, id); err != nil {
		return fmt.Errorf("repo.DeleteRepo: pull_request: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM push_event WHERE repo_id=?`, id); err != nil {
		return fmt.Errorf("repo.DeleteRepo: push_event: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM repo WHERE id=?`, id); err != nil {
		return fmt.Errorf("repo.DeleteRepo: delete row: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("repo.DeleteRepo: commit: %w", err)
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
