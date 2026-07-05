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
// been continuously adopting its parent via merge-forward on every seal, so
// its tip USUALLY already contains the parent's state plus this line's work —
// but the parent can advance after the line's last seal (a sibling line
// folding in between is the canonical case), so FoldLine verifies the
// fast-forward precondition and, when the parent has moved, adopts it first
// (a clean 3-way merge commit) rather than rewinding the parent to a stale
// base. A conflicted adoption refuses the fold with nothing mutated.
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

	// Fold is only a fast-forward when the line has already adopted the
	// parent's CURRENT tip. merge-forward keeps a line current on every
	// seal — but the parent can advance AFTER this line's last seal (the
	// canonical case: a SIBLING line folding in between; hit live
	// 2026-07-05, where the second of two sibling folds silently wiped the
	// first). Blindly setting parent.tip = line.tip then REWINDS the
	// parent, discarding everything that landed after this line's merge
	// base. So: detect the gap and adopt the parent here, exactly as Seal
	// would — a clean 3-way produces a true merge commit (parent tip
	// recorded as second parent) that the fold fast-forwards to; a
	// conflicted 3-way refuses the fold with nothing mutated, and the user
	// resolves through the normal seal path.
	foldTip := line.TipCommit
	if parent.TipCommit != "" && line.TipCommit != "" && parent.TipCommit != line.TipCommit {
		base, berr := e.mergeBase(parent.TipCommit, line.TipCommit)
		if berr != nil {
			return fmt.Errorf("change.FoldLine: merge base: %w", berr)
		}
		if base != parent.TipCommit {
			// The line's conflict-attribution id: its open change (one is
			// opened at CreateLine/Seal, so this exists in any normal flow).
			var adoptChangeID string
			if qerr := e.db.QueryRow(
				`SELECT id FROM change WHERE line_id=? AND status='open' AND sealed=0 ORDER BY updated_at DESC LIMIT 1`,
				lineID).Scan(&adoptChangeID); qerr != nil {
				return fmt.Errorf("change.FoldLine: open change lookup: %w", qerr)
			}
			merged, adoptedParent, conflicts, merr := e.mergeForward(adoptChangeID, line.TipCommit)
			if merr != nil {
				return fmt.Errorf("change.FoldLine: adopt parent: %w", merr)
			}
			if len(conflicts) > 0 {
				return fmt.Errorf("change.FoldLine: parent %q advanced since %q last adopted it, and auto-adoption hit %d conflict(s); seal the line ('cairn commit %s') to record them, resolve, then fold",
					parent.Name, line.Name, len(conflicts), line.Name)
			}
			parents := []string{line.TipCommit}
			if adoptedParent != "" {
				parents = append(parents, adoptedParent)
			}
			foldTip, merr = e.writeCommit(merged, adoptChangeID, "fold: adopt "+parent.Name, parents)
			if merr != nil {
				return fmt.Errorf("change.FoldLine: adopt commit: %w", merr)
			}
		}
	}

	before, err := e.viewMap()
	if err != nil {
		return fmt.Errorf("change.FoldLine: %w", err)
	}
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
		foldTip, ts, line.ParentLine); err != nil {
		return fmt.Errorf("change.FoldLine: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE line SET status='folded', tip_commit=?, updated_at=? WHERE id=?`,
		foldTip, ts, lineID); err != nil {
		return fmt.Errorf("change.FoldLine: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE change SET status='folded', updated_at=? WHERE line_id=? AND status='open'`,
		ts, lineID); err != nil {
		return fmt.Errorf("change.FoldLine: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("change.FoldLine: commit tx: %w", err)
	}
	after, err := e.viewMap()
	if err != nil {
		return fmt.Errorf("change.FoldLine: %w", err)
	}
	if err := e.recordOp("fold", "system", before, after); err != nil {
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
	before, err := e.viewMap()
	if err != nil {
		return fmt.Errorf("change.AbandonLine: %w", err)
	}
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
	after, err := e.viewMap()
	if err != nil {
		return fmt.Errorf("change.AbandonLine: %w", err)
	}
	if err := e.recordOp("abandon", "system", before, after); err != nil {
		return err
	}
	return nil
}
