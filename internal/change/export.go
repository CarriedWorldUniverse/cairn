package change

import (
	"errors"
	"fmt"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
)

// Export projects the change-graph catalogue into real git refs so plain
// go-git or git tooling can read it directly. It is an idempotent projection:
// every open line with a tip becomes a branch ref, every open change becomes a
// refs/cairn/change/<id> ref, and every tag becomes a tag ref. Re-running
// Export simply overwrites each ref with the catalogue's current state.
//
// Export also prunes stale refs: after writing the current open entities it
// removes any refs/heads/<name> whose line is no longer open and any
// refs/cairn/change/<id> whose change is no longer open (folded or abandoned).
// This keeps the projection accurate across mutations, e.g. Export → FoldLine →
// Export leaves no orphaned refs/heads/<folded-line> or
// refs/cairn/change/<folded-change-id> behind. Tags, HEAD, and any other refs
// are left untouched.
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
	if err := e.pruneStaleRefs(); err != nil {
		return err
	}
	return nil
}

// pruneStaleRefs removes refs/heads/<name> and refs/cairn/change/<id> refs that
// no longer correspond to a currently-open line or change. The keep sets are
// built from the catalogue so refs just written for still-open entities survive.
// Tags, HEAD, and any non-heads/non-cairn-change refs are left alone.
func (e *Engine) pruneStaleRefs() error {
	keepLines, err := e.openLineNames()
	if err != nil {
		return err
	}
	keepChanges, err := e.openChangeIDs()
	if err != nil {
		return err
	}

	const cairnChangePrefix = "refs/cairn/change/"
	iter, err := e.git.Storer.IterReferences()
	if err != nil {
		return fmt.Errorf("change.Export: iter refs: %w", err)
	}
	defer iter.Close()

	var stale []plumbing.ReferenceName
	if err := iter.ForEach(func(ref *plumbing.Reference) error {
		name := ref.Name()
		switch {
		case name.IsBranch():
			if !keepLines[name.Short()] {
				stale = append(stale, name)
			}
		case strings.HasPrefix(name.String(), cairnChangePrefix):
			id := strings.TrimPrefix(name.String(), cairnChangePrefix)
			if !keepChanges[id] {
				stale = append(stale, name)
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("change.Export: iter refs: %w", err)
	}

	for _, name := range stale {
		if err := e.git.Storer.RemoveReference(name); err != nil {
			return fmt.Errorf("change.Export: prune %s: %w", name, err)
		}
	}
	return nil
}

// openLineNames returns the set of names of open lines with a non-empty tip:
// exactly the lines exportLines projects onto refs/heads/<name>.
func (e *Engine) openLineNames() (map[string]bool, error) {
	rows, err := e.db.Query(
		`SELECT name FROM line WHERE status = 'open' AND tip_commit != ''`)
	if err != nil {
		return nil, fmt.Errorf("change.Export: %w", err)
	}
	defer func() { _ = rows.Close() }()
	keep := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("change.Export: %w", err)
		}
		keep[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("change.Export: %w", err)
	}
	return keep, nil
}

// openChangeIDs returns the set of ids of open changes with a non-empty head:
// exactly the changes exportChanges projects onto refs/cairn/change/<id>.
func (e *Engine) openChangeIDs() (map[string]bool, error) {
	rows, err := e.db.Query(
		`SELECT id FROM change WHERE status='open' AND head_commit != ''`)
	if err != nil {
		return nil, fmt.Errorf("change.Export: %w", err)
	}
	defer func() { _ = rows.Close() }()
	keep := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("change.Export: %w", err)
		}
		keep[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("change.Export: %w", err)
	}
	return keep, nil
}

// exportLines projects open lines with a tip onto refs/heads/<name>. Folded and
// abandoned lines are not projected: a folded line's history lives in its parent.
func (e *Engine) exportLines() error {
	rows, err := e.db.Query(
		`SELECT id, name, tip_commit FROM line WHERE status = 'open' AND tip_commit != ''`)
	if err != nil {
		return fmt.Errorf("change.Export: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type lineRef struct{ id, name, tip string }
	var lines []lineRef
	for rows.Next() {
		var lr lineRef
		if err := rows.Scan(&lr.id, &lr.name, &lr.tip); err != nil {
			return fmt.Errorf("change.Export: %w", err)
		}
		lines = append(lines, lr)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("change.Export: %w", err)
	}
	// Project the SEALED tip, never the working snapshot. cairn's working change
	// is one auto-amended commit on top of the sealed tip (local-only, like git's
	// working tree); a branch ref a clone or push publishes must point at sealed
	// history, not a "(working)" snapshot. A line with only un-sealed work has no
	// sealed tip yet and is not projected.
	for _, lr := range lines {
		st, err := e.sealedTip(Line{ID: lr.id, TipCommit: lr.tip})
		if err != nil {
			return fmt.Errorf("change.Export: %w", err)
		}
		if st == "" {
			continue
		}
		if err := e.setRef(plumbing.NewBranchReferenceName(lr.name), st); err != nil {
			return err
		}
	}
	return nil
}

// sealedTip returns the line's most recent SEALED commit — what a push/export
// publishes. The working snapshot sits as one auto-amended commit on top of the
// sealed tip, so when line.TipCommit is that working commit its first parent is
// the sealed tip; otherwise the tip is itself sealed. Returns "" when the line
// has only un-sealed work (nothing to publish yet).
func (e *Engine) sealedTip(line Line) (string, error) {
	if line.TipCommit == "" {
		return "", nil
	}
	open, err := e.OpenChangeForLine(line.ID)
	if errors.Is(err, ErrNotFound) {
		return line.TipCommit, nil
	}
	if err != nil {
		return "", err
	}
	if open.HeadCommit != line.TipCommit {
		return line.TipCommit, nil
	}
	// The tip IS the open change's head. Publish it as-is unless it's an
	// auto-snapshot working commit (the "(working)" placeholder amended by
	// SnapshotWorking) — a real commit made directly on an open change (e.g. via
	// the engine Commit primitive or cherry-pick) is genuine history and stays.
	c, cerr := e.git.CommitObject(plumbing.NewHash(line.TipCommit))
	if cerr != nil {
		return "", fmt.Errorf("change.sealedTip: %w", cerr)
	}
	if strings.TrimSpace(stripChangeID(c.Message)) != workingDescription {
		return line.TipCommit, nil
	}
	return e.firstParent(line.TipCommit)
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
