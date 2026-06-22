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
	before := e.viewMap()
	l := Line{
		ID:         newID(),
		Name:       name,
		ParentLine: parent.ID,
		TipCommit:  parent.TipCommit,
		BaseCommit: parent.TipCommit,
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
	if err := e.recordOp("branch", "system", before, e.viewMap()); err != nil {
		return Line{}, err
	}
	return l, nil
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
