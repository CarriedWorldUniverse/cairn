package change

import (
	"database/sql"
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

// Conflicts lists the open (unresolved) conflicts on a change, ordered by path.
func (e *Engine) Conflicts(changeID string) ([]Conflict, error) {
	rows, err := e.db.Query(
		`SELECT id, change_id, path, base_blob, parent_blob, change_blob, marked_blob, status
		 FROM conflict WHERE change_id=? AND status='open' ORDER BY path`,
		changeID)
	if err != nil {
		return nil, fmt.Errorf("change.Conflicts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Conflict
	for rows.Next() {
		var c Conflict
		if err := rows.Scan(&c.ID, &c.ChangeID, &c.Path, &c.BaseBlob, &c.ParentBlob, &c.ChangeBlob, &c.MarkedBlob, &c.Status); err != nil {
			return nil, fmt.Errorf("change.Conflicts: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("change.Conflicts: %w", err)
	}
	return out, nil
}

// ResolveConflict resolves a single open conflict on a change by replacing the
// conflicting path's content with resolved. It writes a new tip commit carrying
// the resolved content, marks the conflict row resolved, advances the change
// head and owning line tip, and clears has_conflict once no open conflicts
// remain. All catalogue writes commit or roll back together. Resolving a
// non-existent open conflict (or unknown change) returns ErrNotFound with no
// head advance.
func (e *Engine) ResolveConflict(changeID, path string, resolved []byte) error {
	ch, err := e.GetChange(changeID)
	if err != nil {
		return err
	}

	before, err := e.viewMap()
	if err != nil {
		return fmt.Errorf("change.ResolveConflict: %w", err)
	}
	ts := e.now().UTC().Format(time.RFC3339Nano)
	tx, err := e.db.Begin()
	if err != nil {
		return fmt.Errorf("change.ResolveConflict: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Gate everything on the conflict existing: mark it resolved first, and bail
	// out (rolling back, writing no git objects) if there was nothing open to
	// resolve. This keeps the ErrNotFound path side-effect-free.
	res, err := tx.Exec(
		`UPDATE conflict SET status='resolved', resolved_at=? WHERE change_id=? AND path=? AND status='open'`,
		ts, changeID, path)
	if err != nil {
		return fmt.Errorf("change.ResolveConflict: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("change.ResolveConflict: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}

	// Build the new tip tree/commit with the resolved content. These go-git
	// writes are content-addressed and idempotent, so they are safe to do
	// mid-transaction (they are not DB ops); doing them after the existence
	// check means no dangling objects on the ErrNotFound path above.
	tree, err := e.commitTree(ch.HeadCommit)
	if err != nil {
		return fmt.Errorf("change.ResolveConflict: %w", err)
	}
	files, err := e.readTree(tree)
	if err != nil {
		return fmt.Errorf("change.ResolveConflict: %w", err)
	}
	// Preserve exec/symlink on every OTHER file in the head tree. The resolved
	// path gets user-merged regular bytes, so it must be regular now — drop any
	// prior non-regular mode for it.
	modes, err := e.FileModes(ch.HeadCommit)
	if err != nil {
		return fmt.Errorf("change.ResolveConflict: %w", err)
	}
	delete(modes, path)
	files[path] = resolved
	newTree, err := e.writeTree(files, modes)
	if err != nil {
		return fmt.Errorf("change.ResolveConflict: %w", err)
	}
	newHead, err := e.writeCommit(newTree.String(), changeID, "resolve conflicts", []string{ch.HeadCommit})
	if err != nil {
		return fmt.Errorf("change.ResolveConflict: %w", err)
	}

	var remaining int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM conflict WHERE change_id=? AND status='open'`,
		changeID).Scan(&remaining); err != nil {
		return fmt.Errorf("change.ResolveConflict: %w", err)
	}
	hasConflict := 0
	if remaining > 0 {
		hasConflict = 1
	}

	if _, err := tx.Exec(
		`UPDATE change SET head_commit=?, has_conflict=?, updated_at=? WHERE id=?`,
		newHead, hasConflict, ts, changeID); err != nil {
		return fmt.Errorf("change.ResolveConflict: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE line SET tip_commit=?, updated_at=? WHERE id=?`,
		newHead, ts, ch.LineID); err != nil {
		return fmt.Errorf("change.ResolveConflict: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("change.ResolveConflict: commit tx: %w", err)
	}
	after, err := e.viewMap()
	if err != nil {
		return fmt.Errorf("change.ResolveConflict: %w", err)
	}
	if err := e.recordOp("resolve", ch.Author, before, after); err != nil {
		return err
	}
	return nil
}
