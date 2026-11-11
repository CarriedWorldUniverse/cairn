package change

import (
	"database/sql"
	"errors"
	"fmt"

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
	// Parent is the commit the step sits on: the first parent of the BOTTOM
	// commit of its run (the previous step's Commit, the line's base, or "" for
	// a root commit). A rewrite that starts at this step replays onto it.
	Parent string
	// Extra are the step's non-first parents — the parent-line tip a seal
	// adopted, or what a fold merged in. An edit carries them onto the rewritten
	// commit (see rewriteChainOnto's keepMerges), so the history reachable only
	// through them is not lost.
	Extra []string
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
	return e.sealedChainAbove(lineID, line.BaseCommit)
}

// sealedChainAbove is sealedChain generalized to stop at an arbitrary commit
// `stop` (exclusive) instead of the line's base — the sealed commits strictly
// above `stop`, base→top. reconcile uses it with the merge-base so a rebase
// replays only the LOCAL divergent commits onto the remote tip.
func (e *Engine) sealedChainAbove(lineID, stop string) ([]sealStep, error) {
	return e.sealedChainTo(lineID, stop, "")
}

// sealedChainTo is sealedChainAbove that may stop early: with a non-empty
// target change-id the walk ends once it has collected target's step and the
// ONE step below it (squash folds a commit into its parent), so an edit near
// the tip of a long line — the root, whose chain runs to its first commit —
// costs a walk to the edit point, not to the beginning of history. The result
// is then the top of the chain, still base→top.
func (e *Engine) sealedChainTo(lineID, stop, target string) ([]sealStep, error) {
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
	seenTarget := false
	for c := top; c != "" && c != stop; {
		runCID := e.changeIDOf(c)
		if runCID == "" {
			break // reached a non-cairn commit; the editable chain ends here
		}
		topCommit := c
		// Descend through every commit carrying the same change-id, noting the
		// non-first parents on the way.
		bottom := c
		var extra []string
		for {
			parents, err := e.Parents(bottom)
			if err != nil {
				return nil, fmt.Errorf("change.sealedChain: %w", err)
			}
			parent := ""
			if len(parents) > 0 {
				parent = parents[0]
				extra = append(extra, parents[1:]...)
			}
			if parent == "" || parent == stop || e.changeIDOf(parent) != runCID {
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
		// The step's message is the stamped commit's (the bottom of the run): the
		// merge-forward commit above it carries an "adopt <parent> into <line>"
		// message of its own.
		msg, err := e.commitMessage(bottom)
		if err != nil {
			return nil, fmt.Errorf("change.sealedChain: %w", err)
		}
		steps = append(steps, sealStep{
			ChangeID:   runCID,
			Commit:     topCommit,
			Tree:       topTree,
			ParentTree: parentTree,
			Message:    stripChangeID(msg),
			Parent:     runParent,
			Extra:      extra,
		})
		if target != "" {
			if seenTarget {
				break // target's step and the one below it are collected
			}
			seenTarget = runCID == target
		}
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

// guardEditable resolves commit's owning line and enforces the editable rule:
// the commit must be a sealed commit (above base) on an open line, and
// rewriting it — and with it every commit after it on the line — must pass
// guardRewrite. It returns the line id, the TOP of the line's sealed chain
// (base→top, from the step below commit upward — see sealedChainTo), and the
// index of commit within that slice.
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
	if !ch.Sealed {
		return "", nil, -1, errors.New("change.guardEditable: that is the open working change, not a sealed commit")
	}
	lineID = ch.LineID
	line, err := e.lineByID(lineID)
	if err != nil {
		return "", nil, 0, err
	}
	if line.Status != "open" {
		return "", nil, 0, errors.New("cannot edit history on a folded/abandoned line")
	}

	chain, err = e.sealedChainTo(lineID, line.BaseCommit, cid)
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
	if err := e.guardRewrite(line, chain[len(chain)-1].Commit, chain[idx].Parent, false); err != nil {
		return "", nil, 0, err
	}
	return lineID, chain, idx, nil
}

// guardRewrite is the ONE rule for every verb that rewrites or drops sealed
// commits from the tip of a line — reword, squash, drop, commit --amend and
// reset <rev>. The commits affected are those from top down to, but not
// including, keep (the edit point and everything after it on the line); with
// dropAll (reset) that is everything reachable from top and not from keep,
// since those commits leave the line altogether, merged-in ones included.
//
//  1. The line must be open.
//  2. No affected commit may belong to a FOLDED line: folded history is
//     immutable (the fold already handed it to the parent), and `cairn undo` /
//     `cairn revert` are the ways back.
//  3. No open child line may be based on an affected commit — its foundation
//     would be rewritten under it. A child forked from an older commit is
//     untouched and does not block. The blocking child is named.
//  4. On the ROOT line, no affected commit may already be on a remote
//     (reachable from the line's tracking ref, by cairn's own ancestry walk):
//     the default branch is not force-pushed, so published root history is
//     only ever added to. Un-pushed root commits are editable. On any other
//     line a pushed commit may be rewritten; the CLI warns that the next push
//     needs --force.
func (e *Engine) guardRewrite(line Line, top, keep string, dropAll bool) error {
	if line.Status != "open" {
		return errors.New("cannot edit history on a folded/abandoned line")
	}
	// The affected commits on the line's first-parent spine.
	var spine []string
	onSpine := map[string]struct{}{}
	for c := top; c != "" && c != keep; {
		if dropAll && keep != "" {
			// keep may be reachable only through a merged-in parent; the spine
			// ends where it re-joins what is kept.
			if kept, err := e.isAncestor(c, keep); err != nil {
				return fmt.Errorf("change.guardRewrite: %w", err)
			} else if kept {
				break
			}
		}
		spine = append(spine, c)
		onSpine[c] = struct{}{}
		next, err := e.firstParent(c)
		if err != nil {
			return fmt.Errorf("change.guardRewrite: %w", err)
		}
		c = next
	}
	affected := func(c string) (bool, error) {
		if !dropAll {
			_, ok := onSpine[c]
			return ok, nil
		}
		if c == "" {
			return false, nil
		}
		under, err := e.isAncestor(c, top)
		if err != nil || !under {
			return false, err
		}
		if keep == "" {
			return true, nil
		}
		kept, err := e.isAncestor(c, keep)
		return !kept, err
	}

	for _, c := range spine {
		cid := e.changeIDOf(c)
		if cid == "" {
			continue
		}
		owner, err := e.GetChange(cid)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("change.guardRewrite: %w", err)
		}
		if owner.LineID == line.ID {
			continue
		}
		if ol, lerr := e.lineByID(owner.LineID); lerr == nil && ol.Status == "folded" {
			return fmt.Errorf("commit %s came in with the fold of %q; folded history is not rewritten — 'cairn undo' rewinds your last operation, and 'cairn revert <commit>' takes a commit back out with a new one", shortSha(c), ol.Name)
		}
	}

	rows, err := e.db.Query(`SELECT name, base_commit FROM line WHERE parent_line=? AND status='open'`, line.ID)
	if err != nil {
		return fmt.Errorf("change.guardRewrite: probe child lines: %w", err)
	}
	type child struct{ name, base string }
	var children []child
	for rows.Next() {
		var c child
		if err := rows.Scan(&c.name, &c.base); err != nil {
			_ = rows.Close()
			return fmt.Errorf("change.guardRewrite: probe child lines: %w", err)
		}
		children = append(children, c)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("change.guardRewrite: probe child lines: %w", err)
	}
	for _, c := range children {
		hit, err := affected(c.base)
		if err != nil {
			return fmt.Errorf("change.guardRewrite: %w", err)
		}
		if hit {
			return fmt.Errorf("line %q has a child line %q based on commit %s, which this would rewrite; cannot edit its history from there (fold or abandon %q first, or use 'cairn revert <commit>')", line.Name, c.name, shortSha(c.base), c.name)
		}
	}

	if line.ParentLine != "" {
		return nil
	}
	remotes, err := e.ListRemotes()
	if err != nil {
		return fmt.Errorf("change.guardRewrite: %w", err)
	}
	for _, rem := range remotes {
		heads, err := e.remoteHeads(rem.Name)
		if err != nil {
			return fmt.Errorf("change.guardRewrite: %w", err)
		}
		head, ok := heads[line.Name]
		if !ok || head == "" {
			continue
		}
		pushed := ""
		if dropAll {
			// Something on the remote is affected iff a common ancestor of the
			// remote tip and our tip is not kept.
			bases, err := e.mergeBases(head, top)
			if err != nil {
				return fmt.Errorf("change.guardRewrite: %w", err)
			}
			for _, b := range bases {
				hit, err := affected(b)
				if err != nil {
					return fmt.Errorf("change.guardRewrite: %w", err)
				}
				if hit {
					pushed = b
					break
				}
			}
		} else {
			// Newest first: once a spine commit is on the remote, so is
			// everything below it — report the newest one that is.
			for _, c := range spine {
				on, err := e.isAncestor(c, head)
				if err != nil {
					return fmt.Errorf("change.guardRewrite: %w", err)
				}
				if on {
					pushed = c
					break
				}
			}
		}
		if pushed != "" {
			return fmt.Errorf("commit %s on the root line %q is already on %s; the root line is never force-pushed, so its published history is not rewritten — use 'cairn revert <commit>' to take a commit back out with a new one", shortSha(pushed), line.Name, rem.Name)
		}
	}
	return nil
}

// rewriteChain re-seals newSeq (base→top) in place of origChain — both the
// part of the line's chain FROM THE EDIT POINT UP — onto the commit origChain's
// first step sat on, 3-way-rebasing each step onto its rebuilt parent, then
// rebases the open working change onto the new top. Commits below the edit
// point are not touched (same hashes). Surviving change-ids keep their rows;
// sealed change rows in origChain NOT present in newSeq are deleted. All
// catalogue writes commit or roll back in ONE transaction; the go-git
// tree/commit writes stay outside it (content-addressed and idempotent).
// Returns any rebase conflicts as data.
func (e *Engine) rewriteChain(lineID string, origChain, newSeq []sealStep) ([]Conflict, error) {
	if len(origChain) == 0 {
		return nil, errors.New("change.rewriteChain: nothing to rewrite")
	}
	return e.rewriteChainOnto(lineID, origChain[0].Parent, origChain, newSeq, true)
}

// rewriteChainOnto is rewriteChain generalized to replay the chain onto an
// arbitrary `baseCommit` instead of the line's own base. It is the primitive
// reconcile uses to REBASE the local divergent commits onto the remote tip —
// producing linear history rather than a "merge remote-tracking" commit — while
// reusing rewriteChain's proven per-step 3-way replay, working-change rebase,
// conflict recording, and single-transaction catalogue update.
//
// keepMerges carries each step's non-first parents (sealStep.Extra) onto its
// rewritten commit. History edits set it: a step that adopted the parent line
// or came from a fold is a merge, and re-writing it single-parented would drop
// from the line's ancestry whatever is reachable only through the other parent
// (on the root line: the root's own commits from before a fold). A pull's
// rebase leaves it off — linear history is its purpose.
func (e *Engine) rewriteChainOnto(lineID, baseCommit string, origChain, newSeq []sealStep, keepMerges bool) ([]Conflict, error) {
	before, err := e.viewMap()
	if err != nil {
		return nil, fmt.Errorf("change.rewriteChain: %w", err)
	}

	// Re-seal each step onto the rebuilt parent. ours = the new parent's tree
	// (prevTree), theirs = this step's original tree, base = this step's original
	// parent tree (ParentTree). When the parent is unchanged (ours==base), the
	// 3-way merge yields theirs unchanged — exactly what reword needs.
	prevCommit := baseCommit
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
		parents := parentsSlice(prevCommit)
		if keepMerges {
			for _, x := range s.Extra {
				if x != prevCommit {
					parents = append(parents, x)
				}
			}
		}
		nc, err := e.writeCommit(merged, s.ChangeID, s.Message, parents)
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

	ts := stamp(e.now())
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

// Squash folds the sealed commit `commit` into its parent — combining the two
// into one sealed commit that keeps the PARENT's change-id, takes `commit`'s
// (cumulative) tree, and concatenates their messages. Descendants rebase cleanly.
func (e *Engine) Squash(commit string) ([]Conflict, error) {
	lineID, chain, idx, err := e.guardEditable(commit)
	if err != nil {
		return nil, err
	}
	if idx == 0 {
		return nil, errors.New("change.Squash: nothing to squash into (commit is the first on its line)")
	}
	// The commit squashed INTO is rewritten too: the rule applies from there.
	line, err := e.lineByID(lineID)
	if err != nil {
		return nil, err
	}
	if err := e.guardRewrite(line, chain[len(chain)-1].Commit, chain[idx-1].Parent, false); err != nil {
		return nil, err
	}
	// Combine chain[idx] INTO chain[idx-1]: keep idx-1's change-id, take idx's
	// tree (the cumulative state), idx-1's ParentTree, concatenated message.
	combined := sealStep{
		ChangeID:   chain[idx-1].ChangeID,
		Tree:       chain[idx].Tree,
		ParentTree: chain[idx-1].ParentTree,
		Message:    chain[idx-1].Message + "\n\n" + chain[idx].Message,
		Parent:     chain[idx-1].Parent,
		Extra:      append(append([]string(nil), chain[idx-1].Extra...), chain[idx].Extra...),
	}
	newSeq := make([]sealStep, 0, len(chain)-idx)
	newSeq = append(newSeq, combined)
	newSeq = append(newSeq, chain[idx+1:]...)
	return e.rewriteChain(lineID, chain[idx-1:], newSeq)
}

// Drop removes the sealed commit `commit` from its line, 3-way-rebasing every
// later commit onto the gap. A later commit that depended on the dropped one
// records a conflict (data); the rewrite still completes.
func (e *Engine) Drop(commit string) ([]Conflict, error) {
	lineID, chain, idx, err := e.guardEditable(commit)
	if err != nil {
		return nil, err
	}
	return e.rewriteChain(lineID, chain[idx:], chain[idx+1:])
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
	// origChain and newSeq are the same slice: reword removes no change-ids, so
	// the removed set (computed by change-id membership) is empty regardless, and
	// the in-place message mutation does not affect that computation.
	return e.rewriteChain(lineID, chain[idx:], chain[idx:])
}
