package change

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	return e.FoldLineOpts(lineID, FoldOpts{})
}

// FoldOpts tunes a fold: git merge's --no-ff / --ff-only / -m.
type FoldOpts struct {
	// NoFF always records a merge commit on the parent, even when the fold
	// would fast-forward.
	NoFF bool
	// FFOnly refuses unless the fold is a pure fast-forward (the line already
	// contains the parent's sealed tip).
	FFOnly bool
	// Message is the merge commit's message ("" = the default).
	Message string
}

// ErrNotFastForward is returned by FoldLineOpts under FFOnly when the parent
// has moved since the line last adopted it.
var ErrNotFastForward = errors.New("change: not a fast-forward")

// FoldLineOpts is FoldLine with git merge's fold options.
func (e *Engine) FoldLineOpts(lineID string, opts FoldOpts) error {
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
	//
	// Everything here is computed on SEALED tips. Both lines' TipCommit is
	// normally an auto-snapshot "(working)" commit: folding the child's raw tip
	// put that placeholder into the parent's history, and comparing against the
	// parent's raw tip (never an ancestor of anything) forced an adoption merge
	// on every fold.
	childTop, err := e.foldSource(line)
	if err != nil {
		return err
	}
	parentSealed, err := e.sealedTip(parent)
	if err != nil {
		return fmt.Errorf("change.FoldLine: %w", err)
	}
	foldTip := childTop
	switch {
	case childTop == "":
		foldTip = parentSealed // an empty line has nothing to bring
	case parentSealed != "" && parentSealed != childTop:
		behind, aerr := e.isAncestor(childTop, parentSealed)
		if aerr != nil {
			return fmt.Errorf("change.FoldLine: %w", aerr)
		}
		if behind {
			foldTip = parentSealed // the parent already has all of it
			break
		}
		base, berr := e.mergeBase(parentSealed, childTop)
		if berr != nil {
			return fmt.Errorf("change.FoldLine: merge base: %w", berr)
		}
		if base != parentSealed {
			if opts.FFOnly {
				return fmt.Errorf("change.FoldLine: %w: %q has moved since %q last adopted it", ErrNotFastForward, parent.Name, line.Name)
			}
			// The line's conflict-attribution id: its open change (one is
			// opened at CreateLine/Seal, so this exists in any normal flow).
			var adoptChangeID string
			if qerr := e.db.QueryRow(
				`SELECT id FROM change WHERE line_id=? AND status='open' AND sealed=0 ORDER BY updated_at DESC LIMIT 1`,
				lineID).Scan(&adoptChangeID); qerr != nil {
				return fmt.Errorf("change.FoldLine: open change lookup: %w", qerr)
			}
			merged, adoptedParent, conflicts, merr := e.mergeForward(adoptChangeID, childTop)
			if merr != nil {
				return fmt.Errorf("change.FoldLine: adopt parent: %w", merr)
			}
			if len(conflicts) > 0 {
				return fmt.Errorf("change.FoldLine: parent %q advanced since %q last adopted it, and auto-adoption hit %d conflict(s); seal the line ('cairn commit %s') to record them, resolve, then fold",
					parent.Name, line.Name, len(conflicts), line.Name)
			}
			parents := []string{childTop}
			if adoptedParent != "" {
				parents = append(parents, adoptedParent)
			}
			adoptMsg := "fold: adopt " + parent.Name
			if opts.Message != "" {
				adoptMsg = opts.Message
			}
			foldTip, merr = e.writeCommit(merged, adoptChangeID, adoptMsg, parents)
			if merr != nil {
				return fmt.Errorf("change.FoldLine: adopt commit: %w", merr)
			}
		} else if opts.NoFF {
			// A true fast-forward (the line contains the parent's tip): --no-ff
			// still records a merge commit [parent, line] with the line's tree.
			var cid string
			if qerr := e.db.QueryRow(
				`SELECT id FROM change WHERE line_id=? AND status='open' AND sealed=0 ORDER BY updated_at DESC LIMIT 1`,
				lineID).Scan(&cid); qerr != nil {
				return fmt.Errorf("change.FoldLine: open change lookup: %w", qerr)
			}
			tree, terr := e.treeHashOf(childTop)
			if terr != nil {
				return fmt.Errorf("change.FoldLine: %w", terr)
			}
			msg := opts.Message
			if msg == "" {
				msg = "Merge " + line.Name + " into " + parent.Name
			}
			foldTip, err = e.writeCommit(tree, cid, msg, []string{parentSealed, childTop})
			if err != nil {
				return fmt.Errorf("change.FoldLine: merge commit: %w", err)
			}
		}
	}

	// The parent's open working change rides on top of the fold. Advancing only
	// the parent LINE tip left its working commit parented on the OLD sealed
	// tip; the very next sync amended that commit in place (same old parent)
	// and pointed the line back at it — so the fold's commits dropped out of
	// the parent's history and the folded files resurfaced as un-sealed adds.
	// Re-parent the working commit onto the fold tip here, carrying the
	// parent's un-sealed edits across with a 3-way merge; if those edits
	// overlap what the fold brings, refuse with nothing mutated.
	parentTip := foldTip
	var parentWork *Change
	if pw, perr := e.openWorkingChange(parent.ID); perr == nil && pw.HeadCommit != "" && pw.HeadCommit == parent.TipCommit && parentSealed != pw.HeadCommit {
		wParents, err := e.Parents(pw.HeadCommit)
		if err != nil {
			return fmt.Errorf("change.FoldLine: %w", err)
		}
		if len(wParents) > 1 {
			return fmt.Errorf("change.FoldLine: %q has a pull's merge in progress — resolve its conflicts and 'cairn commit %s' first, then fold", parent.Name, parent.Name)
		}
		wTree, err := e.treeHashOf(pw.HeadCommit)
		if err != nil {
			return fmt.Errorf("change.FoldLine: %w", err)
		}
		baseTree, err := e.treeHashOf(parentSealed)
		if err != nil {
			return fmt.Errorf("change.FoldLine: %w", err)
		}
		foldTree, err := e.treeHashOf(foldTip)
		if err != nil {
			return fmt.Errorf("change.FoldLine: %w", err)
		}
		mw, cf, err := e.mergeTrees(pw.ID, baseTree, foldTree, wTree)
		if err != nil {
			return fmt.Errorf("change.FoldLine: carry %q's working change: %w", parent.Name, err)
		}
		if len(cf) > 0 {
			paths := make([]string, len(cf))
			for i, c := range cf {
				paths[i] = c.Path
			}
			return fmt.Errorf("change.FoldLine: un-sealed edits on %q overlap what %q changes (%s); commit or stash them on %q first, then fold",
				parent.Name, line.Name, strings.Join(paths, ", "), parent.Name)
		}
		wMsg, err := e.commitMessage(pw.HeadCommit)
		if err != nil {
			return fmt.Errorf("change.FoldLine: %w", err)
		}
		parentTip, err = e.writeCommit(mw, pw.ID, stripChangeID(wMsg), parentsSlice(foldTip))
		if err != nil {
			return fmt.Errorf("change.FoldLine: %w", err)
		}
		parentWork = &pw
	}

	before, err := e.viewMap()
	if err != nil {
		return fmt.Errorf("change.FoldLine: %w", err)
	}
	ts := stamp(e.now())
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
		parentTip, ts, line.ParentLine); err != nil {
		return fmt.Errorf("change.FoldLine: %w", err)
	}
	if parentWork != nil {
		if _, err := tx.Exec(
			`UPDATE change SET head_commit=?, updated_at=? WHERE id=?`,
			parentTip, ts, parentWork.ID); err != nil {
			return fmt.Errorf("change.FoldLine: advance parent working head: %w", err)
		}
		// The parent's snapshot burst predates the fold: a later seal must not
		// absorb it and take a pre-fold view_before (see closeSnapshotBurst).
		if err := closeSnapshotBurst(tx, parentWork.ID); err != nil {
			return err
		}
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
	if err := e.recordOp(opFold, "system", before, after); err != nil {
		return err
	}
	// Name the folded line on the op, so undo can re-open exactly that line.
	detail, err := json.Marshal(map[string]string{"folded": line.Name})
	if err != nil {
		return fmt.Errorf("change.FoldLine: %w", err)
	}
	if _, err := e.db.Exec(
		`UPDATE operation SET detail=? WHERE op_type=? AND seq=(SELECT MAX(seq) FROM operation)`,
		string(detail), opFold); err != nil {
		return fmt.Errorf("change.FoldLine: record folded line: %w", err)
	}
	return nil
}

// opFold is the op-log type for a fold.
const opFold = "fold"

// foldSource returns the commit a line contributes to a fold: its sealed tip.
// When the line's tip is an auto-snapshot "(working)" commit, that placeholder
// is never what gets folded — a clean one is simply skipped, and one that
// holds un-sealed work (`fold --force` carries it) is re-written as a real
// commit with a message of its own, so no "(working)" entry reaches the
// parent's history.
func (e *Engine) foldSource(line Line) (string, error) {
	if line.TipCommit == "" {
		return "", nil
	}
	sealed, err := e.sealedTip(line)
	if err != nil {
		return "", fmt.Errorf("change.FoldLine: %w", err)
	}
	if sealed == line.TipCommit {
		return sealed, nil
	}
	tipTree, err := e.treeHashOf(line.TipCommit)
	if err != nil {
		return "", fmt.Errorf("change.FoldLine: %w", err)
	}
	sealedTree, err := e.treeHashOf(sealed)
	if err != nil {
		return "", fmt.Errorf("change.FoldLine: %w", err)
	}
	if tipTree == sealedTree {
		return sealed, nil
	}
	parents, err := e.Parents(line.TipCommit)
	if err != nil {
		return "", fmt.Errorf("change.FoldLine: %w", err)
	}
	cid := e.changeIDOf(line.TipCommit)
	if cid == "" {
		cid = newChangeID()
	}
	carried, err := e.writeCommit(tipTree, cid, "fold "+line.Name+": un-sealed work", parents)
	if err != nil {
		return "", fmt.Errorf("change.FoldLine: %w", err)
	}
	return carried, nil
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
	ts := stamp(e.now())
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
