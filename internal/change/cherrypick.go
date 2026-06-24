package change

import (
	"fmt"
	"time"
)

// opCherryPick is the op-log type for a cherry-pick: it appends a sealed commit
// (the picked delta) and rebases the open working change on top. Like a seal it
// never coalesces.
const opCherryPick = "cherry-pick"

// CherryPick applies pickedCommit's delta onto the target line as a NEW sealed
// commit with a fresh change-id and the picked message, then rebases the open
// working change on top. It only APPENDS a sealed commit and rebases the open
// working change — it never re-hashes existing sealed commits — so no
// history-editing guard is needed.
//
// The merge is a three-way: base = the picked commit's first-parent tree (so the
// picked delta is isolated), ours = the target line's current top tree, theirs =
// the picked commit's tree. Conflicts are recorded as data; the pick still
// completes.
//
// All catalogue writes — the new sealed change row, the advanced working head,
// the advanced line tip, conflict rows, and the op-log entry — commit or roll
// back together in ONE transaction. The go-git tree/commit writes stay outside
// the tx (content-addressed and idempotent).
func (e *Engine) CherryPick(pickedCommit, targetLineID string) ([]Conflict, error) {
	cid, err := e.ChangeIDOf(pickedCommit)
	if err != nil || cid == "" {
		return nil, fmt.Errorf("change.CherryPick: %q is not a cairn commit", pickedCommit)
	}
	pickedTree, err := e.treeHashOf(pickedCommit)
	if err != nil {
		return nil, fmt.Errorf("change.CherryPick: %w", err)
	}
	// A single Seal can emit TWO commits sharing one change-id (the stamped commit
	// plus a merge-forward commit on top), so the picked commit's literal
	// first-parent may belong to the SAME change — its tree would equal the pick's
	// and the picked delta would vanish. Descend through every consecutive
	// same-change-id ancestor to the previous change's head: that is the true merge
	// base for isolating the picked delta.
	pickedParent := pickedCommit
	for {
		next, perr := e.firstParent(pickedParent)
		if perr != nil {
			return nil, fmt.Errorf("change.CherryPick: %w", perr)
		}
		if next == "" || e.changeIDOf(next) != cid {
			pickedParent = next
			break
		}
		pickedParent = next
	}
	pickedParentTree, err := e.treeHashOf(pickedParent)
	if err != nil {
		return nil, fmt.Errorf("change.CherryPick: %w", err)
	}
	pmsg, err := e.commitMessage(pickedCommit)
	if err != nil {
		return nil, fmt.Errorf("change.CherryPick: %w", err)
	}
	pickedMsg := stripChangeID(pmsg)

	line, err := e.lineByID(targetLineID)
	if err != nil {
		return nil, fmt.Errorf("change.CherryPick: %w", err)
	}
	w, err := e.openWorkingChange(targetLineID)
	if err != nil {
		return nil, fmt.Errorf("change.CherryPick: %w", err)
	}
	topSealed := line.TipCommit
	if w.HeadCommit != "" {
		topSealed, err = e.firstParent(w.HeadCommit)
		if err != nil {
			return nil, fmt.Errorf("change.CherryPick: %w", err)
		}
	}
	topTree, err := e.treeHashOf(topSealed)
	if err != nil {
		return nil, fmt.Errorf("change.CherryPick: %w", err)
	}

	before, err := e.viewMap()
	if err != nil {
		return nil, fmt.Errorf("change.CherryPick: %w", err)
	}

	newID := newChangeID()
	// base = picked parent, ours = our top, theirs = picked.
	merged, cf, err := e.mergeTrees(newID, pickedParentTree, topTree, pickedTree)
	if err != nil {
		return nil, fmt.Errorf("change.CherryPick: %w", err)
	}
	// Capture pick-only conflict flag BEFORE W-rebase appends its own conflicts.
	pickHC := 0
	if len(cf) > 0 {
		pickHC = 1
	}
	newSealed, err := e.writeCommit(merged, newID, pickedMsg, parentsSlice(topSealed))
	if err != nil {
		return nil, fmt.Errorf("change.CherryPick: %w", err)
	}

	// Rebase the open working change W onto newSealed (apply W's delta onto the pick).
	var newW string
	wHC := 0
	wDesc := workingDescription
	if w.HeadCommit == "" {
		// Never snapshotted: the working change has no delta — it rides cleanly on
		// the pick.
		newW, err = e.writeCommit(merged, w.ID, wDesc, parentsSlice(newSealed))
		if err != nil {
			return nil, fmt.Errorf("change.CherryPick: %w", err)
		}
	} else {
		wMsg, err := e.commitMessage(w.HeadCommit)
		if err != nil {
			return nil, fmt.Errorf("change.CherryPick: %w", err)
		}
		wDesc = stripChangeID(wMsg)
		wOld, err := e.treeHashOf(w.HeadCommit)
		if err != nil {
			return nil, fmt.Errorf("change.CherryPick: %w", err)
		}
		wParent, err := e.firstParent(w.HeadCommit)
		if err != nil {
			return nil, fmt.Errorf("change.CherryPick: %w", err)
		}
		wParentTree, err := e.treeHashOf(wParent)
		if err != nil {
			return nil, fmt.Errorf("change.CherryPick: %w", err)
		}
		// base = W's old parent, ours = the pick, theirs = W.
		mw, cfw, err := e.mergeTrees(w.ID, wParentTree, merged, wOld)
		if err != nil {
			return nil, fmt.Errorf("change.CherryPick: %w", err)
		}
		if len(cfw) > 0 {
			wHC = 1
		}
		cf = append(cf, cfw...)
		newW, err = e.writeCommit(mw, w.ID, wDesc, parentsSlice(newSealed))
		if err != nil {
			return nil, fmt.Errorf("change.CherryPick: %w", err)
		}
	}

	ts := e.now().UTC().Format(time.RFC3339Nano)
	tx, err := e.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("change.CherryPick: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// New SEALED change row for the picked commit. Mirrors Seal's column set —
	// Seal inserts an OPEN fresh change with the literals 'open',0,0; here the row
	// is the picked commit itself, so head=newSealed, has_conflict=pickHC, sealed=1.
	// pickHC reflects only pick-level conflicts, not W-rebase conflicts.
	if _, err := tx.Exec(
		`INSERT INTO change(id, line_id, author, head_commit, status, has_conflict, sealed, created_at, updated_at)
		 VALUES(?,?,?,?,'open',?,1,?,?)`,
		newID, targetLineID, w.Author, newSealed, pickHC, ts, ts); err != nil {
		return nil, fmt.Errorf("change.CherryPick: insert change: %w", err)
	}
	if _, err := tx.Exec(`UPDATE change SET head_commit=?, has_conflict=?, updated_at=? WHERE id=?`, newW, wHC, ts, w.ID); err != nil {
		return nil, fmt.Errorf("change.CherryPick: advance working: %w", err)
	}
	if _, err := tx.Exec(`UPDATE line SET tip_commit=? WHERE id=?`, newW, targetLineID); err != nil {
		return nil, fmt.Errorf("change.CherryPick: advance tip: %w", err)
	}
	for _, c := range cf {
		if err := insertConflict(tx, c, ts); err != nil {
			return nil, fmt.Errorf("change.CherryPick: conflict: %w", err)
		}
	}
	after, err := viewMapTx(tx)
	if err != nil {
		return nil, fmt.Errorf("change.CherryPick: %w", err)
	}
	if err := recordOpTx(tx, e.now().UTC(), opCherryPick, w.Author, before, after, ts); err != nil {
		return nil, fmt.Errorf("change.CherryPick: record op: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("change.CherryPick: commit: %w", err)
	}
	return cf, nil
}
