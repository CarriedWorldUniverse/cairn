package change

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Change is a row in the change catalogue: a stable, named unit of work on a
// line. Its head_commit advances as work is snapshotted; the change_id never
// changes (jj-style stable identity).
type Change struct {
	ID          string
	LineID      string
	Author      string
	HeadCommit  string
	Status      string
	HasConflict bool
}

// CommitResult is the outcome of a Commit. A later task adds a Conflicts field
// when merge-forward lands; this snapshot-only result reports just the head.
type CommitResult struct {
	HeadCommit string
}

// reverseHexAlphabet renders nibbles in jj-style reverse hex so change-ids are
// visually distinct from git hex shas.
const reverseHexAlphabet = "zyxwvutsrqponmlk"

// newChangeID returns a stable change-id: a constant 'z' marker followed by the
// reverse-hex rendering of 32 random bytes. The marker guarantees every id
// begins with 'z' and is unmistakably not a hex sha.
func newChangeID() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	out := make([]byte, 0, 1+len(b)*2)
	out = append(out, 'z')
	for _, by := range b {
		out = append(out, reverseHexAlphabet[by>>4], reverseHexAlphabet[by&0x0f])
	}
	return string(out)
}

// CreateChange opens a new change on the given line and returns it. The change
// starts open, with no head commit and no conflict.
func (e *Engine) CreateChange(lineID, author string) (Change, error) {
	ch := Change{
		ID:     newChangeID(),
		LineID: lineID,
		Author: author,
		Status: "open",
	}
	now := e.now().UTC().Format(time.RFC3339Nano)
	_, err := e.db.Exec(
		`INSERT INTO change(id, line_id, author, head_commit, status, has_conflict, created_at, updated_at)
		 VALUES(?,?,?,'',?,0,?,?)`,
		ch.ID, ch.LineID, ch.Author, ch.Status, now, now)
	if err != nil {
		return Change{}, fmt.Errorf("change.CreateChange: %w", err)
	}
	return ch, nil
}

// GetChange loads a change by id, or returns ErrNotFound.
func (e *Engine) GetChange(id string) (Change, error) {
	row := e.db.QueryRow(
		`SELECT id, line_id, author, head_commit, status, has_conflict FROM change WHERE id=?`,
		id)
	var ch Change
	var hasConflict int
	if err := row.Scan(&ch.ID, &ch.LineID, &ch.Author, &ch.HeadCommit, &ch.Status, &hasConflict); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Change{}, ErrNotFound
		}
		return Change{}, fmt.Errorf("change.GetChange: %w", err)
	}
	ch.HasConflict = hasConflict != 0
	return ch, nil
}

// Commit snapshots files onto the change: it writes a tree and a commit whose
// parent is the change's previous head, advances head_commit, and returns the
// new head. This is the pure snapshot half; merge-forward is layered on later.
func (e *Engine) Commit(changeID string, files map[string][]byte) (CommitResult, error) {
	ch, err := e.GetChange(changeID)
	if err != nil {
		return CommitResult{}, err
	}
	tree, err := e.writeTree(files)
	if err != nil {
		return CommitResult{}, err
	}
	var parents []string
	if ch.HeadCommit != "" {
		parents = []string{ch.HeadCommit}
	}
	head, err := e.writeCommit(tree.String(), ch.ID, ch.Author, parents)
	if err != nil {
		return CommitResult{}, err
	}
	ts := e.now().UTC().Format(time.RFC3339Nano)
	if _, err := e.db.Exec(
		`UPDATE change SET head_commit=?, updated_at=? WHERE id=?`,
		head, ts, ch.ID); err != nil {
		return CommitResult{}, fmt.Errorf("change.Commit: %w", err)
	}
	// advance the owning line's tip to this change's new head.
	// Phase-1 simplification: with multiple concurrent changes on one line, the
	// tip simply reflects the most recent commit.
	if _, err := e.db.Exec(`UPDATE line SET tip_commit=?, updated_at=? WHERE id=?`, head, ts, ch.LineID); err != nil {
		return CommitResult{}, err
	}
	return CommitResult{HeadCommit: head}, nil
}
