package change

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
)

const originRemote = "origin"

// ImportFromRemote fetches url's refs into the bare store and maps them onto the
// change-graph: the remote default branch becomes this engine's root line (the
// unique parent_line IS NULL row, renamed in place), every other head becomes a
// flat child line off the root, and every tag is recorded. It is idempotent —
// re-importing the same remote re-fetches and upserts without creating
// duplicate lines or tags. Returns the default branch short name.
func (e *Engine) ImportFromRemote(url string) (string, error) {
	if err := e.fetchRemote(url); err != nil {
		return "", fmt.Errorf("change.ImportFromRemote: %w", err)
	}
	def, err := e.detectDefault()
	if err != nil {
		return "", fmt.Errorf("change.ImportFromRemote: %w", err)
	}
	heads, err := e.listHeads()
	if err != nil {
		return "", fmt.Errorf("change.ImportFromRemote: %w", err)
	}
	tags, err := e.listTags()
	if err != nil {
		return "", fmt.Errorf("change.ImportFromRemote: %w", err)
	}

	tx, err := e.db.Begin()
	if err != nil {
		return "", fmt.Errorf("change.ImportFromRemote: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	ts := e.now().UTC().Format(time.RFC3339Nano)

	// The root line is the unique parent_line IS NULL row.
	var rootID string
	if err := tx.QueryRow(`SELECT id FROM line WHERE parent_line IS NULL`).Scan(&rootID); err != nil {
		return "", fmt.Errorf("change.ImportFromRemote: %w", err)
	}

	// Rename the root to the default branch and set it to that head's commit.
	defTip, ok := heads[def]
	if !ok {
		return "", fmt.Errorf("change.ImportFromRemote: default branch %q not in fetched heads", def)
	}
	if _, err := tx.Exec(
		`UPDATE line SET name=?, tip_commit=?, base_commit=?, updated_at=? WHERE id=?`,
		def, defTip, defTip, ts, rootID); err != nil {
		return "", fmt.Errorf("change.ImportFromRemote: %w", err)
	}

	// Every non-default head becomes a flat child line off the root.
	for name, sha := range heads {
		if name == def {
			continue
		}
		var existingID string
		err := tx.QueryRow(`SELECT id FROM line WHERE name=?`, name).Scan(&existingID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if _, err := tx.Exec(
				`INSERT INTO line(id, name, parent_line, tip_commit, base_commit, status, created_at, updated_at)
				 VALUES(?,?,?,?,?,'open',?,?)`,
				newID(), name, rootID, sha, sha, ts, ts); err != nil {
				return "", fmt.Errorf("change.ImportFromRemote: %w", err)
			}
		case err != nil:
			return "", fmt.Errorf("change.ImportFromRemote: %w", err)
		default:
			if _, err := tx.Exec(
				`UPDATE line SET tip_commit=?, base_commit=?, updated_at=? WHERE id=?`,
				sha, sha, ts, existingID); err != nil {
				return "", fmt.Errorf("change.ImportFromRemote: %w", err)
			}
		}
	}

	// Record each tag (name is PRIMARY KEY; upsert the commit on re-import).
	for name, sha := range tags {
		if _, err := tx.Exec(
			`INSERT INTO tag(name, commit_sha, tagger, at) VALUES(?,?,?,?)
			 ON CONFLICT(name) DO UPDATE SET commit_sha=excluded.commit_sha`,
			name, sha, "import", ts); err != nil {
			return "", fmt.Errorf("change.ImportFromRemote: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("change.ImportFromRemote: %w", err)
	}
	return def, nil
}

// fetchRemote ensures an "origin" remote at url and fetches all heads + tags
// into the bare store. Idempotent (re-fetch is fine).
func (e *Engine) fetchRemote(url string) error {
	rem, err := e.git.Remote(originRemote)
	if errors.Is(err, git.ErrRemoteNotFound) {
		rem, err = e.git.CreateRemote(&config.RemoteConfig{Name: originRemote, URLs: []string{url}})
	} else if err == nil {
		// origin already exists. If its configured URL differs from url, re-point
		// it at the new URL (delete + recreate) so a re-fetch with a changed URL
		// does not silently keep using the old one.
		cfg := rem.Config()
		if len(cfg.URLs) == 0 || cfg.URLs[0] != url {
			if err = e.git.DeleteRemote(originRemote); err != nil {
				return fmt.Errorf("change.fetchRemote: %w", err)
			}
			rem, err = e.git.CreateRemote(&config.RemoteConfig{Name: originRemote, URLs: []string{url}})
		}
	}
	if err != nil {
		return fmt.Errorf("change.fetchRemote: %w", err)
	}
	auth, err := e.authForRemote(rem)
	if err != nil {
		return fmt.Errorf("change.fetchRemote: %w", err)
	}
	err = rem.Fetch(&git.FetchOptions{
		RefSpecs: []config.RefSpec{
			"+refs/heads/*:refs/heads/*",
			"+refs/tags/*:refs/tags/*",
			"+refs/cairn/*:refs/cairn/*",
		},
		Tags: git.AllTags,
		Auth: auth,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("change.fetchRemote: %w", err)
	}
	return nil
}

// detectDefault returns the remote's default branch short name.
//
// It first asks the remote for its advertised refs and looks for the HEAD
// symbolic reference, which names the default branch directly. Over file://
// transports go-git's Remote.List does not reliably surface a symbolic HEAD
// (the local transport advertises HEAD as a plain hash, not a symref), so we
// also fall back to the fetched heads: "main" if present, else a sole head,
// else an error. A freshly-Open'd cairn bare repo has its own HEAD (pointing at
// the local root line), so we never read e.git's local HEAD here — only the
// remote's advertised HEAD and the fetched remote heads are trusted.
func (e *Engine) detectDefault() (string, error) {
	rem, err := e.git.Remote(originRemote)
	if err != nil {
		return "", fmt.Errorf("change.detectDefault: %w", err)
	}
	auth, err := e.authForRemote(rem)
	if err != nil {
		return "", fmt.Errorf("change.detectDefault: %w", err)
	}
	refs, err := rem.List(&git.ListOptions{Auth: auth})
	if err == nil {
		for _, ref := range refs {
			if ref.Name() == plumbing.HEAD && ref.Type() == plumbing.SymbolicReference {
				return ref.Target().Short(), nil
			}
		}
	}
	// Fallback: ANY rem.List error (not only the file:// no-symbolic-HEAD case)
	// drops us here, as does a List that returned but advertised no symbolic
	// HEAD. In all of these we determine the default from the fetched heads.
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

// listTags returns short-name → commit-sha for refs/tags/* in the store.
func (e *Engine) listTags() (map[string]string, error) {
	return e.listRefs("refs/tags/")
}

// listRefs returns short-name → sha for every hash reference whose full name
// begins with prefix. It mirrors export.go's IterReferences iteration style.
func (e *Engine) listRefs(prefix string) (map[string]string, error) {
	iter, err := e.git.Storer.IterReferences()
	if err != nil {
		return nil, fmt.Errorf("change.listRefs: %w", err)
	}
	defer iter.Close()
	out := map[string]string{}
	if err := iter.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() != plumbing.HashReference {
			return nil
		}
		n := ref.Name().String()
		if len(n) > len(prefix) && n[:len(prefix)] == prefix {
			out[n[len(prefix):]] = ref.Hash().String()
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("change.listRefs: %w", err)
	}
	return out, nil
}
