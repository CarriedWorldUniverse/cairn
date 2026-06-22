package change

import (
	"database/sql"
	"fmt"
)

// Conflict is a row in the conflict catalogue: a single path that could not be
// merged cleanly when a change rebased forward onto its parent line. The four
// blob references capture the merge inputs (base/parent/change) plus the
// conflict-marked result, so the conflict can be re-presented or resolved later.
type Conflict struct {
	ID         string
	ChangeID   string
	Path       string
	BaseBlob   string
	ParentBlob string
	ChangeBlob string
	MarkedBlob string
	Status     string
}

// buildConflict assembles a single per-path merge conflict. It writes each of
// base/ours(parent)/theirs(change)/marked as git blobs (content-addressed and
// idempotent, so safe to do outside any transaction) and returns a fully
// populated open Conflict. It does NOT touch the SQLite catalogue; the caller
// inserts the row transactionally via insertConflict.
func (e *Engine) buildConflict(changeID, path string, base, ours, theirs, marked []byte) (Conflict, error) {
	baseBlob, err := e.writeBlob(base)
	if err != nil {
		return Conflict{}, fmt.Errorf("change.buildConflict: base blob: %w", err)
	}
	parentBlob, err := e.writeBlob(ours)
	if err != nil {
		return Conflict{}, fmt.Errorf("change.buildConflict: parent blob: %w", err)
	}
	changeBlob, err := e.writeBlob(theirs)
	if err != nil {
		return Conflict{}, fmt.Errorf("change.buildConflict: change blob: %w", err)
	}
	markedBlob, err := e.writeBlob(marked)
	if err != nil {
		return Conflict{}, fmt.Errorf("change.buildConflict: marked blob: %w", err)
	}

	return Conflict{
		ID:         newID(),
		ChangeID:   changeID,
		Path:       path,
		BaseBlob:   baseBlob.String(),
		ParentBlob: parentBlob.String(),
		ChangeBlob: changeBlob.String(),
		MarkedBlob: markedBlob.String(),
		Status:     "open",
	}, nil
}

// insertConflict inserts a single open conflict row within the given
// transaction. The blob references it carries must already have been written
// (see buildConflict).
func insertConflict(tx *sql.Tx, c Conflict, at string) error {
	if _, err := tx.Exec(
		`INSERT INTO conflict(id, change_id, path, base_blob, parent_blob, change_blob, marked_blob, status, created_at)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		c.ID, c.ChangeID, c.Path, c.BaseBlob, c.ParentBlob, c.ChangeBlob, c.MarkedBlob, c.Status, at); err != nil {
		return fmt.Errorf("change.insertConflict: %w", err)
	}
	return nil
}
