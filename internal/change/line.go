package change

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Line is a row in the line catalogue: a named history (like a branch) with a
// tip commit, a fork-point base commit, and an optional parent line.
type Line struct {
	ID         string
	Name       string
	ParentLine string
	TipCommit  string
	BaseCommit string
	Status     string
}

// LineNode is a line plus its place in the line tree.
type LineNode struct {
	Line   Line
	Parent string
	Ahead  int
}

// CreateLine forks a new open line named name off the parent line. The new line
// is based at, and starts at, the parent's current tip. Returns the wrapped
// lineByID error if the parent does not exist.
func (e *Engine) CreateLine(name, parentLineID string) (Line, error) {
	parent, err := e.lineByID(parentLineID)
	if err != nil {
		return Line{}, err
	}
	before, err := e.viewMap()
	if err != nil {
		return Line{}, fmt.Errorf("change.CreateLine: %w", err)
	}
	// Fork from the parent's SEALED tip, not its working snapshot. The parent's
	// expressed folder is always an auto-snapshot "(working)" commit (authored by
	// the placeholder identity); forking off that would make it a permanent
	// ancestor of the new line — and it would surface in a pushed PR. Like
	// `git branch`, fork from committed state. sealedTip strips a "(working)" head.
	forkPoint, err := e.sealedTip(parent)
	if err != nil {
		return Line{}, fmt.Errorf("change.CreateLine: %w", err)
	}
	l := Line{
		ID:         newID(),
		Name:       name,
		ParentLine: parent.ID,
		TipCommit:  forkPoint,
		BaseCommit: forkPoint,
		Status:     "open",
	}
	now := e.now().UTC().Format(time.RFC3339Nano)
	_, err = e.db.Exec(
		`INSERT INTO line(id, name, parent_line, tip_commit, base_commit, status, created_at, updated_at)
		 VALUES(?,?,?,?,?,?,?,?)`,
		l.ID, l.Name, l.ParentLine, l.TipCommit, l.BaseCommit, l.Status, now, now)
	if err != nil {
		return Line{}, fmt.Errorf("change.CreateLine: %w", err)
	}
	after, err := e.viewMap()
	if err != nil {
		return Line{}, fmt.Errorf("change.CreateLine: %w", err)
	}
	if err := e.recordOp("branch", "system", before, after); err != nil {
		return Line{}, err
	}
	return l, nil
}

// Reparent changes lineID's recorded parent to newParentID and recomputes its
// base as the merge-base of the two tips. Importing from a plain git remote
// flat-projects every branch as a child of the root (git records no branch
// parentage), so a stacked branch — e.g. base/5-0 forked from rc/4-1, not main —
// arrives rooted at trunk. Reparenting restores the real topology, which fixes the
// lineage, the fold destination, and the reconcile base in one move. It refuses to
// reparent the root, onto itself, or onto one of its own descendants (a cycle).
func (e *Engine) Reparent(lineID, newParentID string) error {
	line, err := e.lineByID(lineID)
	if err != nil {
		return err
	}
	if line.ParentLine == "" {
		return fmt.Errorf("change.Reparent: cannot reparent the root line")
	}
	if lineID == newParentID {
		return fmt.Errorf("change.Reparent: a line cannot be its own parent")
	}
	np, err := e.lineByID(newParentID)
	if err != nil {
		return err
	}
	// Cycle guard: walk up from newParent; lineID must not appear above it.
	for cur := np; cur.ParentLine != ""; {
		if cur.ParentLine == lineID {
			return fmt.Errorf("change.Reparent: %q is a descendant of %q (would create a cycle)", np.Name, line.Name)
		}
		cur, err = e.lineByID(cur.ParentLine)
		if err != nil {
			return err
		}
	}
	base := np.TipCommit
	if line.TipCommit != "" && np.TipCommit != "" {
		if mb, mberr := e.mergeBase(line.TipCommit, np.TipCommit); mberr == nil && mb != "" {
			base = mb
		}
	}
	before, err := e.viewMap()
	if err != nil {
		return fmt.Errorf("change.Reparent: %w", err)
	}
	now := e.now().UTC().Format(time.RFC3339Nano)
	if _, err := e.db.Exec(
		`UPDATE line SET parent_line=?, base_commit=?, updated_at=? WHERE id=?`,
		newParentID, base, now, lineID); err != nil {
		return fmt.Errorf("change.Reparent: %w", err)
	}
	after, err := e.viewMap()
	if err != nil {
		return fmt.Errorf("change.Reparent: %w", err)
	}
	return e.recordOp("reparent", "system", before, after)
}

// RootLine returns the repo's root line (the unique parent_line IS NULL row).
func (e *Engine) RootLine() (Line, error) {
	var id string
	if err := e.db.QueryRow(`SELECT id FROM line WHERE parent_line IS NULL`).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Line{}, ErrNotFound
		}
		return Line{}, fmt.Errorf("change.RootLine: %w", err)
	}
	return e.lineByID(id)
}

// lineByID loads a line by its id, or returns ErrNotFound.
func (e *Engine) lineByID(id string) (Line, error) {
	row := e.db.QueryRow(
		`SELECT id, name, parent_line, tip_commit, base_commit, status FROM line WHERE id=?`,
		id)
	var l Line
	var parent sql.NullString
	if err := row.Scan(&l.ID, &l.Name, &parent, &l.TipCommit, &l.BaseCommit, &l.Status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Line{}, ErrNotFound
		}
		return Line{}, fmt.Errorf("change.lineByID: %w", err)
	}
	l.ParentLine = parent.String
	return l, nil
}

// LineByID loads a line by its id (exported wrapper around lineByID), or returns
// ErrNotFound.
func (e *Engine) LineByID(id string) (Line, error) { return e.lineByID(id) }

// LineTracksRemote reports whether the line arrived from a remote (set on
// clone/import). The fold guard uses this to warn before folding a local change
// into an upstream branch (which would diverge from how the remote integrates
// it, e.g. via a PR). Lines created locally with `express` are not tracked.
func (e *Engine) LineTracksRemote(id string) (bool, error) {
	var v int
	err := e.db.QueryRow(`SELECT tracks_remote FROM line WHERE id=?`, id).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("change.LineTracksRemote: %w", err)
	}
	return v != 0, nil
}

// GetLineage walks parent_line from lineID up to the root and returns the chain
// root-first, ending with the line itself.
func (e *Engine) GetLineage(lineID string) ([]Line, error) {
	var chain []Line
	seen := map[string]bool{}
	id := lineID
	for id != "" {
		if seen[id] {
			return nil, fmt.Errorf("change.GetLineage: cycle detected at line %s", id)
		}
		seen[id] = true
		l, err := e.lineByID(id)
		if err != nil {
			return nil, err
		}
		chain = append(chain, l)
		id = l.ParentLine
	}
	// Reverse to root-first order.
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}

// GetLineTree returns all non-abandoned lines as tree nodes, each with its
// parent line and a Phase-1 ahead approximation.
func (e *Engine) GetLineTree() ([]LineNode, error) {
	rows, err := e.db.Query(
		`SELECT id, name, parent_line, tip_commit, base_commit, status
		 FROM line WHERE status != 'abandoned'`)
	if err != nil {
		return nil, fmt.Errorf("change.GetLineTree: %w", err)
	}
	defer rows.Close()

	var nodes []LineNode
	for rows.Next() {
		var l Line
		var parent sql.NullString
		if err := rows.Scan(&l.ID, &l.Name, &parent, &l.TipCommit, &l.BaseCommit, &l.Status); err != nil {
			return nil, fmt.Errorf("change.GetLineTree: %w", err)
		}
		l.ParentLine = parent.String
		// Phase-1: approximation (0/1), real commit-distance is a Phase-2 refinement.
		ahead := 0
		if l.TipCommit != "" && l.TipCommit != l.BaseCommit {
			ahead = 1
		}
		nodes = append(nodes, LineNode{Line: l, Parent: l.ParentLine, Ahead: ahead})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("change.GetLineTree: %w", err)
	}
	return nodes, nil
}
