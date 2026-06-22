package change

import (
	"fmt"
	"time"
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

// recordConflict persists a single per-path merge conflict. It writes each of
// base/ours(parent)/theirs(change)/marked as git blobs, inserts an open conflict
// row, and returns the populated Conflict. (Listing and resolution land later.)
func (e *Engine) recordConflict(changeID, path string, base, ours, theirs, marked []byte) (Conflict, error) {
	baseBlob, err := e.writeBlob(base)
	if err != nil {
		return Conflict{}, fmt.Errorf("change.recordConflict: base blob: %w", err)
	}
	parentBlob, err := e.writeBlob(ours)
	if err != nil {
		return Conflict{}, fmt.Errorf("change.recordConflict: parent blob: %w", err)
	}
	changeBlob, err := e.writeBlob(theirs)
	if err != nil {
		return Conflict{}, fmt.Errorf("change.recordConflict: change blob: %w", err)
	}
	markedBlob, err := e.writeBlob(marked)
	if err != nil {
		return Conflict{}, fmt.Errorf("change.recordConflict: marked blob: %w", err)
	}

	c := Conflict{
		ID:         newID(),
		ChangeID:   changeID,
		Path:       path,
		BaseBlob:   baseBlob.String(),
		ParentBlob: parentBlob.String(),
		ChangeBlob: changeBlob.String(),
		MarkedBlob: markedBlob.String(),
		Status:     "open",
	}
	now := e.now().UTC().Format(time.RFC3339Nano)
	if _, err := e.db.Exec(
		`INSERT INTO conflict(id, change_id, path, base_blob, parent_blob, change_blob, marked_blob, status, created_at)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		c.ID, c.ChangeID, c.Path, c.BaseBlob, c.ParentBlob, c.ChangeBlob, c.MarkedBlob, c.Status, now); err != nil {
		return Conflict{}, fmt.Errorf("change.recordConflict: %w", err)
	}
	return c, nil
}
