package change

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
)

// opRewrite is the op-log type for a history-editing rewrite (reword/squash/drop).
// Like a seal it never coalesces.
const opRewrite = "rewrite"

// sealStep is one sealed commit on a line: its stable change-id, the commit and
// its tree, the tree of its first parent (the merge base for a 3-way rebase), and
// its description (Change-Id trailer stripped).
type sealStep struct {
	ChangeID, Commit, Tree, ParentTree, Message string
}

// sealedChain returns the line's sealed commits ABOVE its base, in base→top
// order. The top sealed commit is the first parent of the open working change's
// head (or the line tip when the working change has no head yet); the walk
// follows first-parent ancestry down to — but excluding — the line's base commit.
func (e *Engine) sealedChain(lineID string) ([]sealStep, error) {
	line, err := e.lineByID(lineID)
	if err != nil {
		return nil, err
	}

	// Locate the open working change on the line and find the top sealed commit.
	var openHead string
	switch err := e.db.QueryRow(
		`SELECT head_commit FROM change WHERE line_id=? AND status='open' AND sealed=0 ORDER BY updated_at DESC LIMIT 1`,
		lineID).Scan(&openHead); {
	case errors.Is(err, sql.ErrNoRows):
		openHead = ""
	case err != nil:
		return nil, fmt.Errorf("change.sealedChain: find open change: %w", err)
	}

	var top string
	if openHead != "" {
		if top, err = e.firstParent(openHead); err != nil {
			return nil, fmt.Errorf("change.sealedChain: %w", err)
		}
	} else {
		top = line.TipCommit
	}

	// Walk first-parent from top down to (excluding) base, collecting one step per
	// sealed change. A single Seal can emit TWO commits sharing one change-id (the
	// stamped commit plus a merge-forward commit on top), so runs of consecutive
	// commits with the same change-id collapse into one logical step: the topmost
	// commit of the run is the step's Commit/Tree (the change's actual head, with
	// the adopted parent state), and the ParentTree is the tree of the first-parent
	// of the run's BOTTOM commit (the previous change's head, or the base).
	var steps []sealStep
	for c := top; c != "" && c != line.BaseCommit; {
		runCID := e.changeIDOf(c)
		topCommit := c
		// Descend through every commit carrying the same change-id.
		bottom := c
		for {
			parent, err := e.firstParent(bottom)
			if err != nil {
				return nil, fmt.Errorf("change.sealedChain: %w", err)
			}
			if parent == "" || parent == line.BaseCommit || e.changeIDOf(parent) != runCID {
				c = parent
				break
			}
			bottom = parent
		}
		topTree, err := e.treeHashOf(topCommit)
		if err != nil {
			return nil, fmt.Errorf("change.sealedChain: %w", err)
		}
		runParent, err := e.firstParent(bottom)
		if err != nil {
			return nil, fmt.Errorf("change.sealedChain: %w", err)
		}
		parentTree, err := e.treeHashOf(runParent)
		if err != nil {
			return nil, fmt.Errorf("change.sealedChain: %w", err)
		}
		msg, err := e.commitMessage(topCommit)
		if err != nil {
			return nil, fmt.Errorf("change.sealedChain: %w", err)
		}
		steps = append(steps, sealStep{
			ChangeID:   runCID,
			Commit:     topCommit,
			Tree:       topTree,
			ParentTree: parentTree,
			Message:    stripChangeID(msg),
		})
	}

	// Reverse to base→top order.
	for i, j := 0, len(steps)-1; i < j; i, j = i+1, j-1 {
		steps[i], steps[j] = steps[j], steps[i]
	}
	return steps, nil
}

// commitMessage returns the raw commit message (Change-Id trailer intact).
func (e *Engine) commitMessage(sha string) (string, error) {
	c, err := e.git.CommitObject(plumbing.NewHash(sha))
	if err != nil {
		return "", fmt.Errorf("change.commitMessage: commit %s: %w", sha, err)
	}
	return c.Message, nil
}

// changeIDOf returns the Change-Id trailer of commit sha, or "" if absent.
func (e *Engine) changeIDOf(sha string) string {
	c, err := e.git.CommitObject(plumbing.NewHash(sha))
	if err != nil {
		return ""
	}
	return parseChangeID(c.Message)
}

// guardEditable resolves commit's owning line and enforces the strict editable
// rule: the commit must be a sealed commit (above base) on a non-root, active
// line that has no child line. It returns the line id, the line's full sealed
// chain (base→top), and the index of commit within that chain.
func (e *Engine) guardEditable(commit string) (lineID string, chain []sealStep, idx int, err error) {
	// Accept a short sha by resolving to the full hash first.
	full, rerr := e.git.ResolveRevision(plumbing.Revision(commit))
	if rerr != nil {
		return "", nil, 0, fmt.Errorf("change.guardEditable: resolve %q: %w", commit, rerr)
	}
	commit = full.String()

	cid := e.changeIDOf(commit)
	if cid == "" {
		return "", nil, 0, errors.New("not a cairn commit")
	}
	ch, err := e.GetChange(cid)
	if err != nil {
		return "", nil, 0, err
	}
	lineID = ch.LineID
	line, err := e.lineByID(lineID)
	if err != nil {
		return "", nil, 0, err
	}
	if line.ParentLine == "" {
		return "", nil, 0, errors.New("cannot edit history on the root line")
	}
	if line.Status != "open" {
		return "", nil, 0, errors.New("cannot edit history on a folded/abandoned line")
	}

	chain, err = e.sealedChain(lineID)
	if err != nil {
		return "", nil, 0, err
	}
	idx = -1
	for i := range chain {
		if chain[i].ChangeID == cid {
			idx = i
			break
		}
	}
	if idx < 0 {
		return "", nil, -1, fmt.Errorf("change.guardEditable: commit is not an editable sealed commit on its line (it may be the base or below)")
	}

	// No child lines: editing this line's history would invalidate any line forked
	// off one of its commits.
	var one int
	switch err := e.db.QueryRow(`SELECT 1 FROM line WHERE parent_line=? LIMIT 1`, lineID).Scan(&one); {
	case err == nil:
		return "", nil, 0, fmt.Errorf("line %q has a child line; cannot edit its history", line.Name)
	case errors.Is(err, sql.ErrNoRows):
		// ok: no child lines.
	default:
		return "", nil, 0, fmt.Errorf("change.guardEditable: probe child lines: %w", err)
	}

	return lineID, chain, idx, nil
}

// rewriteChain re-seals newSeq (base→top) onto the line's base, 3-way-rebasing
// each step onto its rebuilt parent, then rebases the open working change onto the
// new top. Surviving change-ids keep their rows; sealed change rows on the line
// NOT present in newSeq are deleted. All catalogue writes commit or roll back in
// ONE transaction; the go-git tree/commit writes stay outside it (content-
// addressed and idempotent). Returns any rebase conflicts as data.
func (e *Engine) rewriteChain(lineID string, newSeq []sealStep) ([]Conflict, error) {
	line, err := e.lineByID(lineID)
	if err != nil {
		return nil, err
	}
	before, err := e.viewMap()
	if err != nil {
		return nil, fmt.Errorf("change.rewriteChain: %w", err)
	}
	origChain, err := e.sealedChain(lineID)
	if err != nil {
		return nil, err
	}

	// Re-seal each step onto the rebuilt parent. ours = the new parent's tree
	// (prevTree), theirs = this step's original tree, base = this step's original
	// parent tree (ParentTree). When the parent is unchanged (ours==base), the
	// 3-way merge yields theirs unchanged — exactly what reword needs.
	prevCommit := line.BaseCommit
	prevTree, err := e.treeHashOf(prevCommit)
	if err != nil {
		return nil, fmt.Errorf("change.rewriteChain: %w", err)
	}

	type headUpd struct{ changeID, head string }
	var heads []headUpd
	var conflicts []Conflict
	for _, s := range newSeq {
		merged, cf, err := e.mergeTrees(s.ChangeID, s.ParentTree, prevTree, s.Tree)
		if err != nil {
			return nil, fmt.Errorf("change.rewriteChain: merge step %s: %w", s.ChangeID, err)
		}
		conflicts = append(conflicts, cf...)
		nc, err := e.writeCommit(merged, s.ChangeID, s.Message, parentsSlice(prevCommit))
		if err != nil {
			return nil, fmt.Errorf("change.rewriteChain: write step %s: %w", s.ChangeID, err)
		}
		heads = append(heads, headUpd{s.ChangeID, nc})
		prevCommit, prevTree = nc, merged
	}

	// Rebase the open working change W onto the new top.
	w, err := e.openWorkingChange(lineID)
	if err != nil {
		return nil, err
	}
	var newW string
	wDesc := workingDescription
	if w.HeadCommit == "" {
		// Never snapshotted: the working change has no delta — it rides cleanly
		// on the new top without any tree merge needed.
		newW, err = e.writeCommit(prevTree, w.ID, wDesc, parentsSlice(prevCommit))
		if err != nil {
			return nil, fmt.Errorf("change.rewriteChain: write working (no head): %w", err)
		}
	} else {
		wMsg, err := e.commitMessage(w.HeadCommit)
		if err != nil {
			return nil, fmt.Errorf("change.rewriteChain: %w", err)
		}
		wDesc = stripChangeID(wMsg) // Fix 2: preserve the working description
		wOld, err := e.treeHashOf(w.HeadCommit)
		if err != nil {
			return nil, fmt.Errorf("change.rewriteChain: %w", err)
		}
		wParentCommit, err := e.firstParent(w.HeadCommit)
		if err != nil {
			return nil, fmt.Errorf("change.rewriteChain: %w", err)
		}
		wParentTree, err := e.treeHashOf(wParentCommit)
		if err != nil {
			return nil, fmt.Errorf("change.rewriteChain: %w", err)
		}
		mw, cfw, err := e.mergeTrees(w.ID, wParentTree, prevTree, wOld)
		if err != nil {
			return nil, fmt.Errorf("change.rewriteChain: merge working: %w", err)
		}
		conflicts = append(conflicts, cfw...)
		newW, err = e.writeCommit(mw, w.ID, wDesc, parentsSlice(prevCommit))
		if err != nil {
			return nil, fmt.Errorf("change.rewriteChain: write working: %w", err)
		}
	}

	// Change-ids present after the rewrite, to know which old rows to delete.
	kept := make(map[string]struct{}, len(newSeq))
	for _, s := range newSeq {
		kept[s.ChangeID] = struct{}{}
	}
	var removed []string
	for _, s := range origChain {
		if _, ok := kept[s.ChangeID]; !ok {
			removed = append(removed, s.ChangeID)
		}
	}

	// Which changes received conflicts, so has_conflict can be set on each.
	conflicted := map[string]struct{}{}
	for _, c := range conflicts {
		conflicted[c.ChangeID] = struct{}{}
	}

	ts := e.now().UTC().Format(time.RFC3339Nano)
	tx, err := e.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("change.rewriteChain: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Advance each surviving sealed change to its rewritten head.
	for _, h := range heads {
		hc := 0
		if _, ok := conflicted[h.changeID]; ok {
			hc = 1
		}
		if _, err := tx.Exec(
			`UPDATE change SET head_commit=?, has_conflict=?, updated_at=? WHERE id=?`,
			h.head, hc, ts, h.changeID); err != nil {
			return nil, fmt.Errorf("change.rewriteChain: advance sealed head: %w", err)
		}
	}
	// Delete sealed change rows that dropped out of the chain (and their conflicts).
	for _, cid := range removed {
		if _, err := tx.Exec(`DELETE FROM conflict WHERE change_id=?`, cid); err != nil {
			return nil, fmt.Errorf("change.rewriteChain: delete conflicts: %w", err)
		}
		if _, err := tx.Exec(`DELETE FROM change WHERE id=?`, cid); err != nil {
			return nil, fmt.Errorf("change.rewriteChain: delete change: %w", err)
		}
	}
	// Advance the open working change onto the new top.
	wHasConflict := 0
	if _, ok := conflicted[w.ID]; ok {
		wHasConflict = 1
	}
	if _, err := tx.Exec(
		`UPDATE change SET head_commit=?, has_conflict=?, updated_at=? WHERE id=?`,
		newW, wHasConflict, ts, w.ID); err != nil {
		return nil, fmt.Errorf("change.rewriteChain: advance working head: %w", err)
	}
	// Advance the line tip to the rebuilt working commit.
	if _, err := tx.Exec(
		`UPDATE line SET tip_commit=?, updated_at=? WHERE id=?`,
		newW, ts, lineID); err != nil {
		return nil, fmt.Errorf("change.rewriteChain: advance line tip: %w", err)
	}
	// Persist any conflicts produced by the rebase.
	for _, c := range conflicts {
		if err := insertConflict(tx, c, ts); err != nil {
			return nil, fmt.Errorf("change.rewriteChain: record conflict: %w", err)
		}
	}
	after, err := viewMapTx(tx)
	if err != nil {
		return nil, fmt.Errorf("change.rewriteChain: %w", err)
	}
	if err := recordOpTx(tx, e.now().UTC(), opRewrite, w.Author, before, after, ts); err != nil {
		return nil, fmt.Errorf("change.rewriteChain: record op: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("change.rewriteChain: commit tx: %w", err)
	}
	return conflicts, nil
}

// openWorkingChange returns the single open (sealed=0, status='open') working change on the line.
func (e *Engine) openWorkingChange(lineID string) (Change, error) {
	var id string
	switch err := e.db.QueryRow(
		`SELECT id FROM change WHERE line_id=? AND status='open' AND sealed=0 ORDER BY updated_at DESC LIMIT 1`, lineID).Scan(&id); {
	case errors.Is(err, sql.ErrNoRows):
		return Change{}, fmt.Errorf("change.openWorkingChange: no open change on line %s", lineID)
	case err != nil:
		return Change{}, fmt.Errorf("change.openWorkingChange: %w", err)
	}
	return e.GetChange(id)
}

// Reword changes the description of a sealed commit on its line, preserving its
// change-id and rebasing the commits above it (and the open working change) onto
// the rewritten history. It returns any rebase conflicts as data.
func (e *Engine) Reword(commit, message string) ([]Conflict, error) {
	lineID, chain, idx, err := e.guardEditable(commit)
	if err != nil {
		return nil, err
	}
	chain[idx].Message = message
	return e.rewriteChain(lineID, chain)
}
