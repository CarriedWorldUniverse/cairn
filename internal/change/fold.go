package change

import (
	"errors"
	"fmt"
	"time"
)

// ErrHasConflict is returned by FoldLine when the line (any of its changes)
// still has open conflicts that must be resolved before it can be folded back.
var ErrHasConflict = errors.New("change: line has open conflicts; resolve before folding")

// FoldLine folds an open line back into its parent by fast-forwarding the
// parent's tip to this line's tip, then marks the line folded. The line has
// been continuously adopting its parent via merge-forward, so its tip already
// contains the parent's state plus this line's work; setting the parent tip to
// the line tip is therefore a clean fast-forward.
//
// Folding is refused with ErrHasConflict if any change on the line still has an
// open conflict, and refused if the line is the root (it has no parent). Both
// catalogue mutations (parent tip, line status) commit or roll back together.
func (e *Engine) FoldLine(lineID string) error {
	line, err := e.lineByID(lineID)
	if err != nil {
		return err
	}
	if line.ParentLine == "" {
		return fmt.Errorf("change.FoldLine: cannot fold the root line")
	}
	parent, err := e.lineByID(line.ParentLine)
	if err != nil {
		return err
	}
	if parent.Status != "open" {
		return fmt.Errorf("change.FoldLine: parent line %s is %s, cannot fold into it", parent.ID, parent.Status)
	}

	before := e.viewMap()
	ts := e.now().UTC().Format(time.RFC3339Nano)
	tx, err := e.db.Begin()
	if err != nil {
		return fmt.Errorf("change.FoldLine: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var open int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM conflict c JOIN change ch ON c.change_id=ch.id
		 WHERE ch.line_id=? AND c.status='open'`, lineID).Scan(&open); err != nil {
		return fmt.Errorf("change.FoldLine: %w", err)
	}
	if open > 0 {
		return ErrHasConflict
	}

	if _, err := tx.Exec(
		`UPDATE line SET tip_commit=?, updated_at=? WHERE id=?`,
		line.TipCommit, ts, line.ParentLine); err != nil {
		return fmt.Errorf("change.FoldLine: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE line SET status='folded', updated_at=? WHERE id=?`,
		ts, lineID); err != nil {
		return fmt.Errorf("change.FoldLine: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("change.FoldLine: commit tx: %w", err)
	}
	if err := e.recordOp("fold", "system", before, e.viewMap()); err != nil {
		return err
	}
	return nil
}

// AbandonLine throws a line away: it marks every change on the line abandoned
// and the line itself abandoned. The parent line is never touched, so nothing
// of the abandoned work reaches it. Both mutations commit or roll back together.
func (e *Engine) AbandonLine(lineID string) error {
	if _, err := e.lineByID(lineID); err != nil {
		return err
	}
	before := e.viewMap()
	ts := e.now().UTC().Format(time.RFC3339Nano)
	tx, err := e.db.Begin()
	if err != nil {
		return fmt.Errorf("change.AbandonLine: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(
		`UPDATE change SET status='abandoned', updated_at=? WHERE line_id=?`,
		ts, lineID); err != nil {
		return fmt.Errorf("change.AbandonLine: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE line SET status='abandoned', updated_at=? WHERE id=?`,
		ts, lineID); err != nil {
		return fmt.Errorf("change.AbandonLine: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("change.AbandonLine: commit tx: %w", err)
	}
	if err := e.recordOp("abandon", "system", before, e.viewMap()); err != nil {
		return err
	}
	return nil
}
