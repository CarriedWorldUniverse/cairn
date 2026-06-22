package change

import (
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"
)

// Export projects the change-graph catalogue into real git refs so plain
// go-git or git tooling can read it directly. It is an idempotent projection:
// every non-abandoned line becomes a branch ref, every open change becomes a
// refs/cairn/change/<id> ref, and every tag becomes a tag ref. Re-running
// Export simply overwrites each ref with the catalogue's current state.
func (e *Engine) Export() error {
	if err := e.exportLines(); err != nil {
		return err
	}
	if err := e.exportChanges(); err != nil {
		return err
	}
	if err := e.exportTags(); err != nil {
		return err
	}
	return nil
}

// exportLines projects open lines with a tip onto refs/heads/<name>. Folded and
// abandoned lines are not projected: a folded line's history lives in its parent.
func (e *Engine) exportLines() error {
	rows, err := e.db.Query(
		`SELECT name, tip_commit FROM line WHERE status = 'open' AND tip_commit != ''`)
	if err != nil {
		return fmt.Errorf("change.Export: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var name, tip string
		if err := rows.Scan(&name, &tip); err != nil {
			return fmt.Errorf("change.Export: %w", err)
		}
		if err := e.setRef(plumbing.NewBranchReferenceName(name), tip); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("change.Export: %w", err)
	}
	return nil
}

// exportChanges projects open changes with a head onto refs/cairn/change/<id>.
func (e *Engine) exportChanges() error {
	rows, err := e.db.Query(
		`SELECT id, head_commit FROM change WHERE status='open' AND head_commit != ''`)
	if err != nil {
		return fmt.Errorf("change.Export: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id, head string
		if err := rows.Scan(&id, &head); err != nil {
			return fmt.Errorf("change.Export: %w", err)
		}
		if err := e.setRef(plumbing.ReferenceName("refs/cairn/change/"+id), head); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("change.Export: %w", err)
	}
	return nil
}

// exportTags projects every tag onto refs/tags/<name>.
func (e *Engine) exportTags() error {
	rows, err := e.db.Query(`SELECT name, commit_sha FROM tag`)
	if err != nil {
		return fmt.Errorf("change.Export: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var name, sha string
		if err := rows.Scan(&name, &sha); err != nil {
			return fmt.Errorf("change.Export: %w", err)
		}
		if err := e.setRef(plumbing.NewTagReferenceName(name), sha); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("change.Export: %w", err)
	}
	return nil
}

// setRef writes a single hash reference into the bare object store.
func (e *Engine) setRef(name plumbing.ReferenceName, sha string) error {
	ref := plumbing.NewHashReference(name, plumbing.NewHash(sha))
	if err := e.git.Storer.SetReference(ref); err != nil {
		return fmt.Errorf("change.Export: set %s: %w", name, err)
	}
	return nil
}
