package change

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// Pull keeps every line written against its parent's LATEST state: after the
// lines are reconciled with their remotes, a line whose parent moved has its
// own commits replayed onto the parent's new tip — a rebase, so history stays
// linear instead of gaining a merge commit at the next seal.
//
// A commit that conflicts STOPS the replay there, git-rebase style: the
// commits below it are rebased, the conflicting one becomes the working
// change (conflicts recorded on it, as for any cairn conflict), and the rest
// wait in rebase_state. Resolve, then `cairn commit` — which seals it under its
// original message — and the rest replay, stopping again at the next conflict.
//
// Only UNPUBLISHED work is rebased. A line whose commits are on its remote
// branch is left alone: rewriting them would need a force-push and fight the
// next pull; such a line adopts its parent at the next seal, as before.

// ParentRebase reports what pull (or a continuing commit) did to one line.
type ParentRebase struct {
	Line string
	// Status: "rebased" (all replayed), "stopped" (a commit conflicted — see
	// Conflicts, Remaining), or "skipped: <reason>".
	Status    string
	Conflicts int
	// StoppedAt is the message of the commit the replay stopped at.
	StoppedAt string
	// Remaining counts the commits still to replay after it.
	Remaining int
}

// rebaseStep is one sealed commit waiting to be replayed.
type rebaseStep struct {
	ChangeID   string `json:"change_id"`
	Message    string `json:"message"`
	ParentTree string `json:"parent_tree"`
	Tree       string `json:"tree"`
}

// rebaseWork is the operator's un-sealed working change, replayed last.
type rebaseWork struct {
	ParentTree string `json:"parent_tree"`
	Tree       string `json:"tree"`
	Desc       string `json:"desc"`
}

// rebaseState is a stopped rebase: the commit being resolved, what follows.
type rebaseState struct {
	StopChangeID string       `json:"stop_change_id"`
	StopMessage  string       `json:"stop_message"`
	Onto         string       `json:"onto"`
	Pending      []rebaseStep `json:"pending"`
	Working      *rebaseWork  `json:"working,omitempty"`
}

// RebaseInProgress reports a stopped rebase on the line: the message of the
// commit being resolved and how many commits wait after it.
func (e *Engine) RebaseInProgress(lineID string) (message string, remaining int, ok bool, err error) {
	st, err := e.loadRebaseState(lineID)
	if err != nil || st == nil {
		return "", 0, false, err
	}
	return st.StopMessage, len(st.Pending), true, nil
}

func (e *Engine) loadRebaseState(lineID string) (*rebaseState, error) {
	var raw string
	err := e.db.QueryRow(`SELECT state FROM rebase_state WHERE line_id=?`, lineID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("change.rebaseState: %w", err)
	}
	var st rebaseState
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return nil, fmt.Errorf("change.rebaseState: %w", err)
	}
	return &st, nil
}

// RebaseOntoParents replays every eligible line onto its parent's sealed
// tip, parents before children. remoteHeads (branch -> remote tip) decides
// which lines are published and so must not be rewritten.
func (e *Engine) RebaseOntoParents(remoteHeads map[string]string) ([]ParentRebase, error) {
	rows, err := e.db.Query(`SELECT id FROM line WHERE status='open'`)
	if err != nil {
		return nil, fmt.Errorf("change.RebaseOntoParents: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("change.RebaseOntoParents: %w", err)
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	children := map[string][]Line{}
	var root Line
	for _, id := range ids {
		l, err := e.lineByID(id)
		if err != nil {
			return nil, err
		}
		if l.ParentLine == "" {
			root = l
			continue
		}
		children[l.ParentLine] = append(children[l.ParentLine], l)
	}
	if root.ID == "" {
		return nil, nil
	}
	var out []ParentRebase
	// Breadth-first from the root, so a line is replayed onto its parent's
	// ALREADY-rebased tip. A line whose parent stopped mid-rebase waits.
	blocked := map[string]bool{}
	queue := []string{root.ID}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		kids := children[pid]
		sort.Slice(kids, func(i, j int) bool { return kids[i].Name < kids[j].Name })
		for _, kid := range kids {
			queue = append(queue, kid.ID)
			if blocked[pid] {
				blocked[kid.ID] = true
				continue
			}
			res, err := e.rebaseOntoParent(kid, remoteHeads[kid.Name])
			if err != nil {
				return out, fmt.Errorf("change.RebaseOntoParents: %s: %w", kid.Name, err)
			}
			if res.Status == "stopped" || res.Status == "skipped: rebase in progress" {
				blocked[kid.ID] = true
			}
			if res.Status != "" {
				out = append(out, res)
			}
		}
	}
	return out, nil
}

// rebaseOntoParent replays one line onto its parent's sealed tip. An empty
// Status means there was nothing to do.
func (e *Engine) rebaseOntoParent(line Line, remoteTip string) (ParentRebase, error) {
	res := ParentRebase{Line: line.Name}
	if st, err := e.loadRebaseState(line.ID); err != nil {
		return res, err
	} else if st != nil {
		res.Status = "skipped: rebase in progress"
		return res, nil
	}
	parent, err := e.lineByID(line.ParentLine)
	if err != nil {
		return res, err
	}
	onto, err := e.sealedTip(parent)
	if err != nil || onto == "" {
		return res, err
	}
	top, err := e.sealedTip(line)
	if err != nil {
		return res, err
	}
	if top != "" {
		if in, err := e.isAncestor(onto, top); err != nil || in {
			return res, err // already on the parent's latest
		}
	}
	w, err := e.openWorkingChange(line.ID)
	if err != nil {
		return res, nil // no working change: nothing that could be rebased
	}
	if w.HasConflict {
		res.Status = "skipped: open conflicts"
		return res, nil
	}

	// The line's own commits: first-parent from the sealed top down to the
	// first commit the parent already has.
	chain, below, err := e.ownChain(line.ID, top, onto)
	if err != nil {
		return res, err
	}
	// What the chain sits on must be the parent's history — or an older
	// version of an ancestor line's own commits, which that line's rebase just
	// replaced (the `rebase --onto` case). Commits cairn did not write, below
	// and outside the parent, are not ours to move.
	if below != "" {
		ok, err := e.isAncestor(below, onto)
		if err != nil {
			return res, err
		}
		if !ok {
			ok, err = e.ownedByAncestorLine(line.ID, below)
			if err != nil {
				return res, err
			}
		}
		if !ok {
			res.Status = "skipped: history below the line's commits is not the parent's"
			return res, nil
		}
	}
	// Published work stays put.
	if remoteTip != "" && len(chain) > 0 {
		if in, err := e.isAncestor(chain[0].commit, remoteTip); err != nil {
			return res, err
		} else if in {
			res.Status = "skipped: pushed (commit adopts the parent instead)"
			return res, nil
		}
	}

	steps := make([]rebaseStep, len(chain))
	for i, s := range chain {
		steps[i] = s.rebaseStep
	}
	var work *rebaseWork
	if w.HeadCommit != "" {
		if work, err = e.workOf(w.HeadCommit); err != nil {
			return res, err
		}
	}
	return e.replay(line, w, onto, onto, steps, work, "")
}

type chainStep struct {
	rebaseStep
	commit string // the step's bottom-most commit
}

// ownChain collects the line's own sealed steps above the parent:
// first-parent from top while the commit is not in onto's history and
// belongs to a change of THIS line, grouping consecutive commits that share a
// change-id (a seal, its adopt-merge, a resolution) into one step. It
// returns them base→top, plus the commit the chain sits on.
func (e *Engine) ownChain(lineID, top, onto string) ([]chainStep, string, error) {
	var steps []chainStep
	c := top
	for c != "" {
		if in, err := e.isAncestor(c, onto); err != nil {
			return nil, "", err
		} else if in {
			break
		}
		cid := e.changeIDOf(c)
		if cid == "" {
			break
		}
		var owner string
		if err := e.db.QueryRow(`SELECT line_id FROM change WHERE id=?`, cid).Scan(&owner); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, "", err
		}
		if owner != lineID {
			break // an ancestor line's commit (e.g. its pre-rebase copy)
		}
		stepTop, bottom := c, c
		for {
			p, err := e.firstParent(bottom)
			if err != nil {
				return nil, "", err
			}
			if p == "" || e.changeIDOf(p) != cid {
				c = p
				break
			}
			if in, err := e.isAncestor(p, onto); err != nil {
				return nil, "", err
			} else if in {
				c = p
				break
			}
			bottom = p
		}
		tree, err := e.treeHashOf(stepTop)
		if err != nil {
			return nil, "", err
		}
		below, err := e.firstParent(bottom)
		if err != nil {
			return nil, "", err
		}
		var ptree string
		if below != "" {
			if ptree, err = e.treeHashOf(below); err != nil {
				return nil, "", err
			}
		}
		msg, err := e.commitMessage(stepTop)
		if err != nil {
			return nil, "", err
		}
		steps = append(steps, chainStep{
			rebaseStep: rebaseStep{ChangeID: cid, Message: stripChangeID(msg), ParentTree: ptree, Tree: tree},
			commit:     bottom,
		})
	}
	for i, j := 0, len(steps)-1; i < j; i, j = i+1, j-1 {
		steps[i], steps[j] = steps[j], steps[i]
	}
	return steps, c, nil
}

// workOf captures a working commit's delta: its tree against its (first)
// parent's, and its description.
func (e *Engine) workOf(head string) (*rebaseWork, error) {
	tree, err := e.treeHashOf(head)
	if err != nil {
		return nil, err
	}
	p, err := e.firstParent(head)
	if err != nil {
		return nil, err
	}
	var ptree string
	if p != "" {
		if ptree, err = e.treeHashOf(p); err != nil {
			return nil, err
		}
	}
	msg, err := e.commitMessage(head)
	if err != nil {
		return nil, err
	}
	return &rebaseWork{ParentTree: ptree, Tree: tree, Desc: stripChangeID(msg)}, nil
}

// ContinueRebase replays what a stopped rebase left, after `commit` sealed the
// commit it stopped at. No-op (empty Status) when there is no rebase.
func (e *Engine) ContinueRebase(lineID string) (ParentRebase, error) {
	st, err := e.loadRebaseState(lineID)
	if err != nil || st == nil {
		return ParentRebase{}, err
	}
	line, err := e.lineByID(lineID)
	if err != nil {
		return ParentRebase{}, err
	}
	w, err := e.openWorkingChange(lineID)
	if err != nil {
		return ParentRebase{}, err
	}
	start, err := e.sealedTip(line)
	if err != nil {
		return ParentRebase{}, err
	}
	return e.replay(line, w, st.Onto, start, st.Pending, st.Working, st.StopChangeID)
}

// replay puts steps (then work) on top of start, recording everything in one
// transaction with one op. dropID is a sealed change whose content now lives
// under another change (the one a stopped rebase sealed), deleted here.
func (e *Engine) replay(line Line, w Change, onto, start string, steps []rebaseStep, work *rebaseWork, dropID string) (ParentRebase, error) {
	res := ParentRebase{Line: line.Name}
	prevCommit := start
	prevTree, err := e.treeHashOf(prevCommit)
	if err != nil {
		return res, err
	}
	type upd struct{ id, head string }
	var sealed []upd
	var stop *rebaseState
	var conflicts []Conflict
	var head string
	for i, s := range steps {
		merged, cf, err := e.mergeTrees(w.ID, s.ParentTree, prevTree, s.Tree)
		if err != nil {
			return res, err
		}
		if len(cf) == 0 {
			nc, err := e.writeCommit(merged, s.ChangeID, s.Message, parentsSlice(prevCommit))
			if err != nil {
				return res, err
			}
			sealed = append(sealed, upd{s.ChangeID, nc})
			prevCommit, prevTree = nc, merged
			continue
		}
		// Stop here: this commit's merged content, markers and all, becomes
		// the working change. It keeps the "(working)" description — the
		// original message waits in rebase_state for the sealing commit — so
		// nothing treats a half-resolved commit as published history.
		if head, err = e.writeCommit(merged, w.ID, workingDescription, parentsSlice(prevCommit)); err != nil {
			return res, err
		}
		conflicts = cf
		stop = &rebaseState{StopChangeID: s.ChangeID, StopMessage: s.Message, Onto: onto, Pending: steps[i+1:], Working: work}
		break
	}
	if stop == nil {
		if work != nil {
			merged, cf, err := e.mergeTrees(w.ID, work.ParentTree, prevTree, work.Tree)
			if err != nil {
				return res, err
			}
			conflicts = cf
			if head, err = e.writeCommit(merged, w.ID, work.Desc, parentsSlice(prevCommit)); err != nil {
				return res, err
			}
		} else if head, err = e.writeCommit(prevTree, w.ID, workingDescription, parentsSlice(prevCommit)); err != nil {
			return res, err
		}
	}

	before, err := e.viewMap()
	if err != nil {
		return res, err
	}
	prevState, err := e.rawRebaseState(line.ID)
	if err != nil {
		return res, err
	}
	ts := stamp(e.now())
	tx, err := e.db.Begin()
	if err != nil {
		return res, fmt.Errorf("change.rebase: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, u := range sealed {
		if _, err := tx.Exec(`UPDATE change SET head_commit=?, has_conflict=0, updated_at=? WHERE id=?`, u.head, ts, u.id); err != nil {
			return res, fmt.Errorf("change.rebase: %w", err)
		}
	}
	if dropID != "" {
		if _, err := tx.Exec(`DELETE FROM conflict WHERE change_id=?`, dropID); err != nil {
			return res, fmt.Errorf("change.rebase: %w", err)
		}
		if _, err := tx.Exec(`DELETE FROM change WHERE id=? AND sealed=1`, dropID); err != nil {
			return res, fmt.Errorf("change.rebase: %w", err)
		}
	}
	hc := 0
	if len(conflicts) > 0 {
		hc = 1
	}
	if _, err := tx.Exec(`UPDATE change SET head_commit=?, has_conflict=?, updated_at=? WHERE id=?`, head, hc, ts, w.ID); err != nil {
		return res, fmt.Errorf("change.rebase: %w", err)
	}
	if _, err := tx.Exec(`UPDATE line SET tip_commit=?, base_commit=?, updated_at=? WHERE id=?`, head, onto, ts, line.ID); err != nil {
		return res, fmt.Errorf("change.rebase: %w", err)
	}
	for _, c := range conflicts {
		if err := insertConflict(tx, c, ts); err != nil {
			return res, fmt.Errorf("change.rebase: %w", err)
		}
	}
	newState := ""
	if stop != nil {
		raw, err := json.Marshal(stop)
		if err != nil {
			return res, err
		}
		newState = string(raw)
		if _, err := tx.Exec(`INSERT INTO rebase_state(line_id, state) VALUES(?,?) ON CONFLICT(line_id) DO UPDATE SET state=excluded.state`, line.ID, newState); err != nil {
			return res, fmt.Errorf("change.rebase: %w", err)
		}
	} else if _, err := tx.Exec(`DELETE FROM rebase_state WHERE line_id=?`, line.ID); err != nil {
		return res, fmt.Errorf("change.rebase: %w", err)
	}
	after, err := viewMapTx(tx)
	if err != nil {
		return res, err
	}
	if err := recordOpTx(tx, e.now().UTC(), "rebase", w.Author, before, after, ts); err != nil {
		return res, err
	}
	if prevState != newState {
		var opID string
		if err := tx.QueryRow(`SELECT id FROM operation ORDER BY seq DESC LIMIT 1`).Scan(&opID); err != nil {
			return res, fmt.Errorf("change.rebase: %w", err)
		}
		if _, err := tx.Exec(`INSERT INTO rebase_history(op_id, line_id, before) VALUES(?,?,?)`, opID, line.ID, prevState); err != nil {
			return res, fmt.Errorf("change.rebase: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("change.rebase: commit: %w", err)
	}
	res.Conflicts = len(conflicts)
	if stop != nil {
		res.Status = "stopped"
		res.StoppedAt = stop.StopMessage
		res.Remaining = len(stop.Pending)
	} else {
		res.Status = "rebased"
	}
	return res, nil
}

func (e *Engine) rawRebaseState(lineID string) (string, error) {
	var raw string
	err := e.db.QueryRow(`SELECT state FROM rebase_state WHERE line_id=?`, lineID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("change.rebaseState: %w", err)
	}
	return raw, nil
}

// restoreRebaseStates puts back the rebase state an operation changed; Undo
// calls it for the operation it reverts.
func restoreRebaseStates(tx *sql.Tx, opID string) error {
	rows, err := tx.Query(`SELECT line_id, before FROM rebase_history WHERE op_id=?`, opID)
	if err != nil {
		return fmt.Errorf("change.Undo: rebase state: %w", err)
	}
	type rb struct{ line, before string }
	var all []rb
	for rows.Next() {
		var r rb
		if err := rows.Scan(&r.line, &r.before); err != nil {
			_ = rows.Close()
			return fmt.Errorf("change.Undo: rebase state: %w", err)
		}
		all = append(all, r)
	}
	_ = rows.Close()
	for _, r := range all {
		if r.before == "" {
			if _, err := tx.Exec(`DELETE FROM rebase_state WHERE line_id=?`, r.line); err != nil {
				return fmt.Errorf("change.Undo: rebase state: %w", err)
			}
			continue
		}
		if _, err := tx.Exec(`INSERT INTO rebase_state(line_id, state) VALUES(?,?) ON CONFLICT(line_id) DO UPDATE SET state=excluded.state`, r.line, r.before); err != nil {
			return fmt.Errorf("change.Undo: rebase state: %w", err)
		}
	}
	return nil
}

// ownedByAncestorLine reports whether commit belongs to a change of one of
// line's ancestor lines — an older copy of the parent's own work, which a
// rebase may leave behind. A commit with no change row here (another clone's
// cairn commit, or git's) is not.
func (e *Engine) ownedByAncestorLine(lineID, commit string) (bool, error) {
	cid := e.changeIDOf(commit)
	if cid == "" {
		return false, nil
	}
	var owner string
	err := e.db.QueryRow(`SELECT line_id FROM change WHERE id=?`, cid).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	lineage, err := e.GetLineage(lineID)
	if err != nil {
		return false, err
	}
	for _, l := range lineage[:len(lineage)-1] {
		if l.ID == owner {
			return true, nil
		}
	}
	return false, nil
}
