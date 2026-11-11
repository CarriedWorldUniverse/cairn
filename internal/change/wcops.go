package change

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Working-copy mutation verbs (git parity): restore, reset, revert, partial
// commit, amend, rm/mv/clean, apply/am, and the stash additions.
//
// Every verb here follows the shape cherry-pick established rather than Seal's:
//
//   - the line's OPEN working change keeps its change-id. A verb that adds a
//     sealed commit mints a fresh change-id for it and re-parents the working
//     commit on top. Because the pre-op line tip is therefore always "this
//     change's working commit", `cairn undo` puts the line back EXACTLY where it
//     was — un-sealed edits included — by restoring one tip.
//   - the catalogue writes and the op-log entry commit or roll back together in
//     ONE transaction; the go-git object writes stay outside it (content-
//     addressed and idempotent).
//   - each records a NON-coalescing op, and closes the change's open snapshot
//     burst (closeSnapshotBurst) so a later Seal cannot absorb a snapshot op
//     that predates this verb and take a stale view_before with it.

// Op-log types for the working-copy verbs. None coalesces.
const (
	OpRestore = "restore"
	OpReset   = "reset"
	OpRevert  = "revert"
	OpAmend   = "amend"
	OpApply   = "apply"
	OpAm      = "am"
	OpRm      = "rm"
	OpMv      = "mv"
	OpClean   = "clean"
	// OpCommitPaths is a partial commit. It is a seal, so it reads as one.
	OpCommitPaths = opCommit
	// OpPickNoCommit is `cherry-pick -n`: the delta lands in the working change.
	OpPickNoCommit = opCherryPick
)

// ErrNothingToCommit is returned when a sealing verb would produce a commit
// whose tree equals its parent's and empty commits were not asked for.
var ErrNothingToCommit = errors.New("change: nothing to commit")

// ErrWouldConflict is returned by AppendSealed when the three-way merge that
// builds the new commit does not apply cleanly. Nothing was written to the
// catalogue; the caller decides whether to land the delta in the working change
// instead (MergeIntoWorking), where the conflict can be resolved.
var ErrWouldConflict = errors.New("change: the change does not apply cleanly")

// Signature is an explicit commit identity. Empty fields (and a zero When) fall
// back to the engine's configured identity and clock.
type Signature struct {
	Name, Email string
	When        time.Time
}

// SetAuthorOverride makes every commit this engine writes from now on carry the
// given AUTHOR (git's `commit --author` / `--date`); the committer stays the
// configured identity. Pass nil to clear it. It is process-local state set
// around a single seal by the CLI, never persisted.
func (e *Engine) SetAuthorOverride(sig *Signature) { e.authorOverride = sig }

// writeCommitAs is writeCommit with an explicit author. The committer is always
// the engine identity at e.now(); a nil author (or its empty fields) falls back
// to the same.
func (e *Engine) writeCommitAs(treeSha, changeID, message string, parents []string, author *Signature) (string, error) {
	when := e.now()
	name, email := e.idName, e.idEmail
	if name == "" {
		name = "cairn"
	}
	if email == "" {
		email = name + "@users.noreply.cairn"
	}
	committer := object.Signature{Name: name, Email: email, When: when}
	auth := committer
	if author != nil {
		if author.Name != "" {
			auth.Name = author.Name
		}
		if author.Email != "" {
			auth.Email = author.Email
		}
		if !author.When.IsZero() {
			auth.When = author.When
		}
	}
	if message == "" {
		message = "snapshot"
	}
	fullMsg := message + "\n\nChange-Id: " + changeID + "\n"
	parentHashes := make([]plumbing.Hash, 0, len(parents))
	for _, p := range parents {
		parentHashes = append(parentHashes, plumbing.NewHash(p))
	}
	c := &object.Commit{
		Author:       auth,
		Committer:    committer,
		Message:      fullMsg,
		TreeHash:     plumbing.NewHash(treeSha),
		ParentHashes: parentHashes,
	}
	obj := e.git.Storer.NewEncodedObject()
	if err := c.Encode(obj); err != nil {
		return "", fmt.Errorf("change.writeCommit: encode: %w", err)
	}
	h, err := e.git.Storer.SetEncodedObject(obj)
	if err != nil {
		return "", fmt.Errorf("change.writeCommit: store: %w", err)
	}
	return h.String(), nil
}

// ── exported read helpers the worktree layer builds trees with ──────────────

// TreeOf returns the tree hash of a commit ("" maps to the empty tree).
func (e *Engine) TreeOf(commit string) (string, error) { return e.treeHashOf(commit) }

// ReadTree reads a tree into a path->TreeEntry map (SHA+mode, no content).
func (e *Engine) ReadTree(tree string) (map[string]TreeEntry, error) { return e.readTreeRefs(tree) }

// WriteTree stores the tree for a path->TreeEntry map and returns its hash.
// Every entry's blob must already be in the object store.
func (e *Engine) WriteTree(entries map[string]TreeEntry) (string, error) {
	h, err := e.writeTreeRefs(entries)
	if err != nil {
		return "", err
	}
	return h.String(), nil
}

// CommitMeta returns a commit's displayable metadata.
func (e *Engine) CommitMeta(sha string) (CommitInfo, error) { return e.commitInfo(sha) }

// IsAncestor reports whether a is an ancestor of (or equal to) b, by cairn's
// own generation-ordered graph walk.
func (e *Engine) IsAncestor(a, b string) (bool, error) { return e.isAncestor(a, b) }

// SealedTip returns the most recent SEALED commit of a line — git's HEAD for
// that line — or "" when it has none yet.
func (e *Engine) SealedTip(lineID string) (string, error) {
	ws, err := e.workState(lineID)
	if err != nil {
		return "", err
	}
	return ws.top, nil
}

// DeltaBase returns the commit whose tree is the BASE of commit's own delta.
// A single Seal can emit two commits sharing one change-id (the stamped commit
// plus a merge-forward commit on top), so the literal first parent may belong
// to the same change; this descends through every consecutive same-change-id
// ancestor to the previous change's head (see CherryPick).
func (e *Engine) DeltaBase(commit string) (string, error) {
	cid := e.changeIDOf(commit)
	cur := commit
	for {
		next, err := e.firstParent(cur)
		if err != nil {
			return "", fmt.Errorf("change.DeltaBase: %w", err)
		}
		if next == "" || cid == "" || e.changeIDOf(next) != cid {
			return next, nil
		}
		cur = next
	}
}

// OwnCommit maps a commit to the one that carries the operator's own change.
// For a cairn merge-forward commit (a merge whose first parent shares its
// change-id) that is the first parent — the stamped snapshot BEFORE the parent
// line was adopted — so reverting it does not also revert what the adoption
// brought in. Any other commit is returned unchanged. merge reports whether the
// result is still a genuine multi-parent merge (which needs `-m`).
func (e *Engine) OwnCommit(commit string) (own string, merge bool, err error) {
	cur := commit
	for {
		parents, perr := e.Parents(cur)
		if perr != nil {
			return "", false, fmt.Errorf("change.OwnCommit: %w", perr)
		}
		if len(parents) < 2 {
			return cur, false, nil
		}
		cid := e.changeIDOf(cur)
		if cid == "" || e.changeIDOf(parents[0]) != cid {
			return cur, true, nil
		}
		cur = parents[0]
	}
}

// ── shared state / guards ────────────────────────────────────────────────────

// workState is a line's open working change as the verbs here need it.
type workState struct {
	line     Line
	w        Change
	desc     string   // the working commit's description ("(working)")
	wTree    string   // tree of the working commit (== topTree when never snapshotted)
	wParents []string // every parent of the working commit
	top      string   // top SEALED commit ("" on a line with no commits)
	topTree  string
}

func (e *Engine) workState(lineID string) (workState, error) {
	line, err := e.lineByID(lineID)
	if err != nil {
		return workState{}, err
	}
	w, err := e.openWorkingChange(lineID)
	if err != nil {
		return workState{}, err
	}
	ws := workState{line: line, w: w, desc: workingDescription}
	if w.HeadCommit == "" {
		ws.top = line.TipCommit
		ws.wParents = parentsSlice(ws.top)
	} else {
		if ws.wParents, err = e.Parents(w.HeadCommit); err != nil {
			return workState{}, fmt.Errorf("change.workState: %w", err)
		}
		if len(ws.wParents) > 0 {
			ws.top = ws.wParents[0]
		}
		msg, merr := e.commitMessage(w.HeadCommit)
		if merr != nil {
			return workState{}, fmt.Errorf("change.workState: %w", merr)
		}
		ws.desc = stripChangeID(msg)
	}
	if ws.topTree, err = e.treeHashOf(ws.top); err != nil {
		return workState{}, fmt.Errorf("change.workState: %w", err)
	}
	ws.wTree = ws.topTree
	if w.HeadCommit != "" {
		if ws.wTree, err = e.treeHashOf(w.HeadCommit); err != nil {
			return workState{}, fmt.Errorf("change.workState: %w", err)
		}
	}
	return ws, nil
}

// guardSealable refuses the states a sealing verb must not run over: a merge
// left as the working commit by a conflicted pull, and a stopped rebase. Both
// are finished with resolve + commit (or rewound with undo) first.
func (e *Engine) guardSealable(ws workState, verb string) error {
	if len(ws.wParents) > 1 {
		return fmt.Errorf("%s: %q has a pull's merge in progress — resolve its conflicts and 'cairn commit' (or 'cairn undo') first", verb, ws.line.Name)
	}
	if _, _, ok, err := e.RebaseInProgress(ws.line.ID); err != nil {
		return fmt.Errorf("%s: %w", verb, err)
	} else if ok {
		return fmt.Errorf("%s: %q has a rebase onto its parent in progress — resolve and 'cairn commit' (or 'cairn undo') first", verb, ws.line.Name)
	}
	return nil
}

// closeSnapshotBurst retires the change's open working-snapshot op inside tx:
// it stays in the log (and stays undoable in sequence), but can no longer be
// coalesced into by the next snapshot nor absorbed by the next Seal. Without
// this a Seal after one of the verbs here would take the view_before of a
// snapshot burst that PREDATES the verb, so undoing that commit would silently
// rewind the verb as well.
func closeSnapshotBurst(tx *sql.Tx, changeID string) error {
	if _, err := tx.Exec(
		`UPDATE operation SET detail = detail || ' (closed)' WHERE op_type=? AND detail=?`,
		opSnapshot, changeID); err != nil {
		return fmt.Errorf("change.closeSnapshotBurst: %w", err)
	}
	return nil
}

// advanceWorking is the shared catalogue tail of every verb here: inside one
// transaction it runs extra (sealed-change rows etc.), records conflicts,
// repoints the working change and the line tip at newW, closes the snapshot
// burst and appends the op.
func (e *Engine) advanceWorking(ws workState, newW, opType string, before map[string]string, conflicts []Conflict, dropConflicts bool, extra func(tx *sql.Tx, ts string) error) error {
	ts := stamp(e.now())
	tx, err := e.db.Begin()
	if err != nil {
		return fmt.Errorf("change.%s: begin tx: %w", opType, err)
	}
	defer func() { _ = tx.Rollback() }()
	if extra != nil {
		if err := extra(tx, ts); err != nil {
			return err
		}
	}
	if dropConflicts {
		if _, err := tx.Exec(
			`UPDATE conflict SET status='resolved', resolved_at=? WHERE change_id=? AND status='open'`,
			ts, ws.w.ID); err != nil {
			return fmt.Errorf("change.%s: drop conflicts: %w", opType, err)
		}
	}
	wConflicts := 0
	for _, c := range conflicts {
		if err := insertConflict(tx, c, ts); err != nil {
			return fmt.Errorf("change.%s: record conflict: %w", opType, err)
		}
		if c.ChangeID == ws.w.ID {
			wConflicts++
		}
	}
	// has_conflict tracks OPEN rows: recompute rather than guess.
	var open int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM conflict WHERE change_id=? AND status='open'`, ws.w.ID).Scan(&open); err != nil {
		return fmt.Errorf("change.%s: count conflicts: %w", opType, err)
	}
	hc := 0
	if open > 0 || wConflicts > 0 {
		hc = 1
	}
	if _, err := tx.Exec(
		`UPDATE change SET head_commit=?, has_conflict=?, updated_at=? WHERE id=?`,
		newW, hc, ts, ws.w.ID); err != nil {
		return fmt.Errorf("change.%s: advance working head: %w", opType, err)
	}
	if _, err := tx.Exec(
		`UPDATE line SET tip_commit=?, updated_at=? WHERE id=?`,
		newW, ts, ws.line.ID); err != nil {
		return fmt.Errorf("change.%s: advance line tip: %w", opType, err)
	}
	if err := closeSnapshotBurst(tx, ws.w.ID); err != nil {
		return err
	}
	after, err := viewMapTx(tx)
	if err != nil {
		return fmt.Errorf("change.%s: %w", opType, err)
	}
	if err := recordOpTx(tx, e.now().UTC(), opType, ws.w.Author, before, after, ts); err != nil {
		return fmt.Errorf("change.%s: record op: %w", opType, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("change.%s: commit tx: %w", opType, err)
	}
	return nil
}

// ── working-change verbs (no sealed commit) ──────────────────────────────────

// RewriteWorking amends the line's open working commit to exactly tree,
// keeping its parents and description, and records a non-coalescing op of
// opType — the primitive behind restore, `reset --hard`, rm, mv, clean and
// apply. dropConflicts also settles every open conflict on the working change
// (`reset --hard` discards them with the work). changed is false, and nothing
// is recorded, when the working commit already has that tree.
func (e *Engine) RewriteWorking(lineID, tree, opType string, dropConflicts bool) (changed bool, err error) {
	ws, err := e.workState(lineID)
	if err != nil {
		return false, err
	}
	if ws.w.HeadCommit != "" && ws.wTree == tree {
		if !dropConflicts {
			return false, nil
		}
		open, cerr := e.Conflicts(ws.w.ID)
		if cerr != nil {
			return false, cerr
		}
		if len(open) == 0 {
			return false, nil
		}
	}
	before, err := e.viewMap()
	if err != nil {
		return false, fmt.Errorf("change.RewriteWorking: %w", err)
	}
	newW, err := e.writeCommit(tree, ws.w.ID, ws.desc, ws.wParents)
	if err != nil {
		return false, err
	}
	if err := e.advanceWorking(ws, newW, opType, before, nil, dropConflicts, nil); err != nil {
		return false, err
	}
	return true, nil
}

// MergeIntoWorking lands the delta baseTree→theirsTree in the line's open
// working change with a three-way merge (ours = the working tree), recording
// any conflicts on the working change as data — `cherry-pick -n`, `revert -n`,
// a revert that does not apply cleanly, and `stash apply`.
func (e *Engine) MergeIntoWorking(lineID, baseTree, theirsTree, opType string) (conflicts []Conflict, changed bool, err error) {
	return e.mergeIntoWorking(lineID, baseTree, theirsTree, opType, nil)
}

func (e *Engine) mergeIntoWorking(lineID, baseTree, theirsTree, opType string, extra func(tx *sql.Tx, ts string, conflicts []Conflict) error) ([]Conflict, bool, error) {
	ws, err := e.workState(lineID)
	if err != nil {
		return nil, false, err
	}
	merged, cf, err := e.mergeTrees(ws.w.ID, baseTree, ws.wTree, theirsTree)
	if err != nil {
		return nil, false, fmt.Errorf("change.MergeIntoWorking: %w", err)
	}
	if merged == ws.wTree && len(cf) == 0 && ws.w.HeadCommit != "" && extra == nil {
		return nil, false, nil
	}
	before, err := e.viewMap()
	if err != nil {
		return nil, false, fmt.Errorf("change.MergeIntoWorking: %w", err)
	}
	newW, err := e.writeCommit(merged, ws.w.ID, ws.desc, ws.wParents)
	if err != nil {
		return nil, false, err
	}
	var ex func(tx *sql.Tx, ts string) error
	if extra != nil {
		ex = func(tx *sql.Tx, ts string) error { return extra(tx, ts, cf) }
	}
	if err := e.advanceWorking(ws, newW, opType, before, cf, false, ex); err != nil {
		return nil, false, err
	}
	return cf, merged != ws.wTree, nil
}

// ── sealing verbs ────────────────────────────────────────────────────────────

// AppendSpec describes one sealed commit to append to a line.
type AppendSpec struct {
	Message string
	// Tree, when set, is the new commit's tree verbatim (a partial commit, an
	// applied mail patch). Otherwise the tree is the three-way merge of the
	// delta Base→Theirs onto the line's current sealed tip (revert).
	Tree         string
	Base, Theirs string
	// Author overrides the commit author (am, commit --author/--date).
	Author *Signature
	// AllowEmpty permits a commit whose tree equals its parent's.
	AllowEmpty bool
}

// AppendResult is the outcome of AppendSealed.
type AppendResult struct {
	Commit   string // the new sealed commit
	ChangeID string // its fresh change-id
	// Conflicts are those of re-parenting the open working change on top of the
	// new commit (the operator's un-sealed edits overlapping it), recorded on
	// the working change. The sealed commit itself is always clean.
	Conflicts []Conflict
}

// AppendSealed appends one sealed commit to the line and re-parents the open
// working change on top of it — the shape of cherry-pick, generalized. It never
// re-hashes an existing sealed commit, so it needs no history-edit guard and
// runs on any line, the root included.
//
// A delta that does not merge cleanly onto the sealed tip returns
// ErrWouldConflict with NOTHING written; a commit that would be empty returns
// ErrNothingToCommit unless spec.AllowEmpty.
func (e *Engine) AppendSealed(lineID, opType string, spec AppendSpec) (AppendResult, error) {
	ws, err := e.workState(lineID)
	if err != nil {
		return AppendResult{}, err
	}
	if err := e.guardSealable(ws, opType); err != nil {
		return AppendResult{}, err
	}
	newID := newChangeID()
	newTree := spec.Tree
	if newTree == "" {
		merged, cf, merr := e.mergeTrees(newID, spec.Base, ws.topTree, spec.Theirs)
		if merr != nil {
			return AppendResult{}, fmt.Errorf("change.AppendSealed: %w", merr)
		}
		if len(cf) > 0 {
			paths := make([]string, len(cf))
			for i, c := range cf {
				paths[i] = c.Path
			}
			return AppendResult{}, fmt.Errorf("%w: %s", ErrWouldConflict, strings.Join(paths, ", "))
		}
		newTree = merged
	}
	if newTree == ws.topTree && !spec.AllowEmpty {
		return AppendResult{}, ErrNothingToCommit
	}
	before, err := e.viewMap()
	if err != nil {
		return AppendResult{}, fmt.Errorf("change.AppendSealed: %w", err)
	}
	author := spec.Author
	if author == nil {
		author = e.authorOverride
	}
	sealed, err := e.writeCommitAs(newTree, newID, spec.Message, parentsSlice(ws.top), author)
	if err != nil {
		return AppendResult{}, err
	}
	// Re-parent the working change: base = its old parent, ours = the new
	// commit, theirs = the working tree.
	mw, cfw, err := e.mergeTrees(ws.w.ID, ws.topTree, newTree, ws.wTree)
	if err != nil {
		return AppendResult{}, fmt.Errorf("change.AppendSealed: rebase working: %w", err)
	}
	newW, err := e.writeCommit(mw, ws.w.ID, ws.desc, parentsSlice(sealed))
	if err != nil {
		return AppendResult{}, err
	}
	err = e.advanceWorking(ws, newW, opType, before, cfw, false, func(tx *sql.Tx, ts string) error {
		// The new SEALED change row, exactly as cherry-pick inserts it.
		if _, err := tx.Exec(
			`INSERT INTO change(id, line_id, author, head_commit, status, has_conflict, sealed, created_at, updated_at)
			 VALUES(?,?,?,?,'open',0,1,?,?)`,
			newID, lineID, ws.w.Author, sealed, ts, ts); err != nil {
			return fmt.Errorf("change.AppendSealed: insert change: %w", err)
		}
		return nil
	})
	if err != nil {
		return AppendResult{}, err
	}
	return AppendResult{Commit: sealed, ChangeID: newID, Conflicts: cfw}, nil
}

// AmendTop folds the open working change into the line's top sealed commit
// (git's `commit --amend`): the commit is rewritten with the working tree, its
// change-id and author kept, its message replaced when message is non-nil, and
// the working change left clean on top. It rewrites sealed history, so it is
// held to the same rule as reword (guardRewrite, edit.go).
func (e *Engine) AmendTop(lineID string, message *string, author *Signature, resetAuthor bool) (string, error) {
	ws, err := e.workState(lineID)
	if err != nil {
		return "", err
	}
	if err := e.guardSealable(ws, "commit --amend"); err != nil {
		return "", err
	}
	// Only the top commit itself is replaced (a merge-forward commit keeps the
	// stamped commit under it), so that is all the rule has to clear.
	cid := e.changeIDOf(ws.top)
	if ws.top == "" || ws.top == ws.line.BaseCommit || cid == "" {
		return "", fmt.Errorf("commit --amend: %q has no sealed commit of its own to amend", ws.line.Name)
	}
	parents, err := e.Parents(ws.top)
	if err != nil {
		return "", fmt.Errorf("change.AmendTop: %w", err)
	}
	below := ""
	if len(parents) > 0 {
		below = parents[0]
	}
	if err := e.guardRewrite(ws.line, ws.top, below, false); err != nil {
		return "", err
	}
	old, err := e.git.CommitObject(plumbing.NewHash(ws.top))
	if err != nil {
		return "", fmt.Errorf("change.AmendTop: %w", err)
	}
	step := sealStep{ChangeID: cid, Commit: ws.top, Message: stripChangeID(old.Message)}
	msg := step.Message
	if message != nil {
		msg = *message
	}
	var auth *Signature
	if !resetAuthor {
		auth = &Signature{Name: old.Author.Name, Email: old.Author.Email, When: old.Author.When}
	}
	if author != nil {
		if auth == nil {
			auth = &Signature{}
		}
		if author.Name != "" {
			auth.Name, auth.Email = author.Name, author.Email
		}
		if !author.When.IsZero() {
			auth.When = author.When
		}
	}
	before, err := e.viewMap()
	if err != nil {
		return "", fmt.Errorf("change.AmendTop: %w", err)
	}
	sealed, err := e.writeCommitAs(ws.wTree, step.ChangeID, msg, parents, auth)
	if err != nil {
		return "", err
	}
	newW, err := e.writeCommit(ws.wTree, ws.w.ID, ws.desc, parentsSlice(sealed))
	if err != nil {
		return "", err
	}
	err = e.advanceWorking(ws, newW, OpAmend, before, nil, false, func(tx *sql.Tx, ts string) error {
		if _, err := tx.Exec(`UPDATE change SET head_commit=?, updated_at=? WHERE id=? AND sealed=1`,
			sealed, ts, step.ChangeID); err != nil {
			return fmt.Errorf("change.AmendTop: advance sealed head: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return sealed, nil
}

// ResetResult reports what ResetLine did.
type ResetResult struct {
	OldTip, NewTip string
	Dropped        int // sealed commits the line no longer has
}

// ResetLine moves the line's sealed tip back to target (git's `reset <rev>`).
// hard rewrites the working change to target's tree, discarding un-sealed edits
// and open conflicts; otherwise (--soft/--mixed) the working tree is kept, so
// everything between target and the folder becomes the working change.
//
// What is safe: target must be an ancestor of the line's current sealed tip
// and must not lie below the line's base — reset only ever moves a line back
// along its OWN history. Moving a line onto an unrelated commit would detach it
// from its fork point (which every later merge-forward keys on), so that is
// refused; cherry-pick or `express --from` say it explicitly. Because it drops
// sealed commits it is held to the same rule as reword/drop (guardRewrite,
// edit.go). Resetting to the current tip is always allowed (it rewrites nothing).
func (e *Engine) ResetLine(lineID, target string, hard bool) (ResetResult, error) {
	ws, err := e.workState(lineID)
	if err != nil {
		return ResetResult{}, err
	}
	// A hard reset discards a conflicted pull's merge (and its conflicts) like
	// git's `reset --hard` over a merge in progress; everything else refuses it.
	guard := ws
	if hard {
		guard.wParents = nil
	}
	if err := e.guardSealable(guard, "reset"); err != nil {
		return ResetResult{}, err
	}
	res := ResetResult{OldTip: ws.top, NewTip: target}
	if target == ws.top {
		if !hard {
			return res, nil
		}
		if len(ws.wParents) > 1 {
			before, err := e.viewMap()
			if err != nil {
				return ResetResult{}, fmt.Errorf("change.ResetLine: %w", err)
			}
			newW, err := e.writeCommit(ws.topTree, ws.w.ID, ws.desc, parentsSlice(ws.top))
			if err != nil {
				return ResetResult{}, err
			}
			if err := e.advanceWorking(ws, newW, OpReset, before, nil, true, nil); err != nil {
				return ResetResult{}, err
			}
			return res, nil
		}
		if _, err := e.RewriteWorking(lineID, ws.topTree, OpReset, true); err != nil {
			return ResetResult{}, err
		}
		return res, nil
	}
	if ws.top == "" {
		return ResetResult{}, fmt.Errorf("reset: %q has no commits", ws.line.Name)
	}
	anc, err := e.isAncestor(target, ws.top)
	if err != nil {
		return ResetResult{}, fmt.Errorf("change.ResetLine: %w", err)
	}
	if !anc {
		return ResetResult{}, fmt.Errorf("reset: %s is not an ancestor of %q's tip — reset only moves a line back along its own history (to bring a commit in, use 'cairn cherry-pick'; to start from it, 'cairn express <new> --from <line>')", shortSha(target), ws.line.Name)
	}
	if ws.line.BaseCommit != "" {
		above, err := e.isAncestor(ws.line.BaseCommit, target)
		if err != nil {
			return ResetResult{}, fmt.Errorf("change.ResetLine: %w", err)
		}
		if !above {
			return ResetResult{}, fmt.Errorf("reset: %s is below %q's base (the commit it forked from) — that history belongs to its parent line", shortSha(target), ws.line.Name)
		}
	}
	if err := e.guardRewrite(ws.line, ws.top, target, true); err != nil {
		return ResetResult{}, err
	}
	tree := ws.wTree
	if hard {
		if tree, err = e.treeHashOf(target); err != nil {
			return ResetResult{}, fmt.Errorf("change.ResetLine: %w", err)
		}
	}
	if res.Dropped, _, err = e.divergence(ws.top, target); err != nil {
		return ResetResult{}, fmt.Errorf("change.ResetLine: %w", err)
	}
	before, err := e.viewMap()
	if err != nil {
		return ResetResult{}, fmt.Errorf("change.ResetLine: %w", err)
	}
	newW, err := e.writeCommit(tree, ws.w.ID, ws.desc, parentsSlice(target))
	if err != nil {
		return ResetResult{}, err
	}
	if err := e.advanceWorking(ws, newW, OpReset, before, nil, hard, nil); err != nil {
		return ResetResult{}, err
	}
	return res, nil
}

// CherryPickWith is CherryPick with the picked message passed through
// transform first (`cherry-pick -x` appends the origin line).
func (e *Engine) CherryPickWith(pickedCommit, targetLineID string, transform func(string) string) (string, []Conflict, error) {
	return e.cherryPick(pickedCommit, targetLineID, transform)
}

// AdoptWorkingTip re-stamps tip — a "(working)" placeholder commit that
// belongs to a DIFFERENT (since-sealed) change — as changeID's own working
// commit, same tree and parents, and points the change and its line at it.
// adopted is false (and nothing changes) when tip is not such a placeholder.
//
// Undo needs this: restoring a line to the tip it had before a seal lands on
// the sealed change's old working commit. Leaving it as the line tip and
// starting a fresh working commit ON TOP of it would bake an empty-messaged
// "(working)" commit into the line's history; adopting it instead makes it the
// open change's amendable working commit again, edits intact.
func (e *Engine) AdoptWorkingTip(changeID, tip string) (head string, adopted bool, err error) {
	c, err := e.git.CommitObject(plumbing.NewHash(tip))
	if err != nil {
		return "", false, fmt.Errorf("change.AdoptWorkingTip: %w", err)
	}
	cid := parseChangeID(c.Message)
	if cid == "" || cid == changeID || strings.TrimSpace(stripChangeID(c.Message)) != workingDescription {
		return "", false, nil
	}
	// Only a commit whose own change has moved on (sealed) is a stale working
	// commit; a sealed commit that merely happens to be described "(working)"
	// is real history.
	if owner, gerr := e.GetChange(cid); gerr == nil && owner.HeadCommit == tip {
		return "", false, nil
	}
	ch, err := e.GetChange(changeID)
	if err != nil {
		return "", false, err
	}
	parents, err := e.Parents(tip)
	if err != nil {
		return "", false, fmt.Errorf("change.AdoptWorkingTip: %w", err)
	}
	head, err = e.writeCommit(c.TreeHash.String(), changeID, workingDescription, parents)
	if err != nil {
		return "", false, err
	}
	ts := stamp(e.now())
	tx, err := e.db.Begin()
	if err != nil {
		return "", false, fmt.Errorf("change.AdoptWorkingTip: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`UPDATE change SET head_commit=?, updated_at=? WHERE id=?`, head, ts, changeID); err != nil {
		return "", false, fmt.Errorf("change.AdoptWorkingTip: %w", err)
	}
	if _, err := tx.Exec(`UPDATE line SET tip_commit=?, updated_at=? WHERE id=?`, head, ts, ch.LineID); err != nil {
		return "", false, fmt.Errorf("change.AdoptWorkingTip: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", false, fmt.Errorf("change.AdoptWorkingTip: commit tx: %w", err)
	}
	return head, true, nil
}

// ── stash additions ──────────────────────────────────────────────────────────

// StashGet loads one stash entry; id 0 is the top of the stack.
func (e *Engine) StashGet(id int64) (StashEntry, error) {
	query := `SELECT id, branch, message, commit_sha, base_sha, created_at FROM stash WHERE id=?`
	args := []any{id}
	if id == 0 {
		query = `SELECT id, branch, message, commit_sha, base_sha, created_at FROM stash ORDER BY id DESC LIMIT 1`
		args = nil
	}
	var s StashEntry
	switch err := e.db.QueryRow(query, args...).Scan(&s.ID, &s.Branch, &s.Message, &s.CommitSha, &s.BaseSha, &s.CreatedAt); {
	case errors.Is(err, sql.ErrNoRows):
		if id == 0 {
			return StashEntry{}, errors.New("no stash entries")
		}
		return StashEntry{}, fmt.Errorf("stash %d not found", id)
	case err != nil:
		return StashEntry{}, fmt.Errorf("change.StashGet: %w", err)
	}
	return s, nil
}

// StashClear discards every stash entry and reports how many there were. It is
// recorded as an operation, so `cairn undo` restores every entry.
func (e *Engine) StashClear() (int, error) {
	res, err := e.db.Exec(`DELETE FROM stash`)
	if err != nil {
		return 0, fmt.Errorf("change.StashClear: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("change.StashClear: %w", err)
	}
	if n > 0 {
		if err := e.recordStashShape("stash-clear"); err != nil {
			return 0, err
		}
	}
	return int(n), nil
}

// StashPushTrees is the partial form of StashPush (`stash push -- <paths>`):
// stashTree is what gets shelved (the sealed tip plus the selected paths'
// edits) and keepTree is what the working change is left with (the working
// tree minus those edits). Both are tree hashes the caller built.
func (e *Engine) StashPushTrees(lineID, message, stashTree, keepTree string) (int64, error) {
	ws, err := e.workState(lineID)
	if err != nil {
		return 0, err
	}
	if stashTree == ws.topTree {
		return 0, ErrNothingToStash
	}
	before, err := e.viewMap()
	if err != nil {
		return 0, fmt.Errorf("change.StashPushTrees: %w", err)
	}
	shelved, err := e.writeCommit(stashTree, ws.w.ID, workingDescription, parentsSlice(ws.top))
	if err != nil {
		return 0, err
	}
	newW, err := e.writeCommit(keepTree, ws.w.ID, ws.desc, ws.wParents)
	if err != nil {
		return 0, err
	}
	var stashID int64
	err = e.advanceWorking(ws, newW, opStash, before, nil, false, func(tx *sql.Tx, ts string) error {
		res, err := tx.Exec(
			`INSERT INTO stash(line_id, branch, commit_sha, base_sha, message, created_at) VALUES(?,?,?,?,?,?)`,
			lineID, ws.line.Name, shelved, ws.top, message, ts)
		if err != nil {
			return fmt.Errorf("change.StashPushTrees: insert stash: %w", err)
		}
		stashID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return 0, err
	}
	return stashID, nil
}

// StashApplyMerge restores a stash onto the line's working change with a
// three-way merge (base = the commit the stash was taken on, ours = the working
// tree, theirs = the stashed tree), so it applies over un-sealed work and onto
// a line that has moved since — where StashApply replaces the tree outright and
// therefore refuses a dirty change. Conflicts are recorded on the working
// change as data. With drop, the entry is removed only when the apply was
// clean (git keeps the stash when `stash pop` conflicts).
func (e *Engine) StashApplyMerge(lineID string, stashID int64, drop bool) ([]Conflict, error) {
	s, err := e.StashGet(stashID)
	if err != nil {
		return nil, err
	}
	baseTree, err := e.treeHashOf(s.BaseSha)
	if err != nil {
		return nil, fmt.Errorf("change.StashApplyMerge: %w", err)
	}
	stashTree, err := e.treeHashOf(s.CommitSha)
	if err != nil {
		return nil, fmt.Errorf("change.StashApplyMerge: %w", err)
	}
	cf, _, err := e.mergeIntoWorking(lineID, baseTree, stashTree, opStashPop, func(tx *sql.Tx, ts string, conflicts []Conflict) error {
		if drop && len(conflicts) == 0 {
			if _, err := tx.Exec(`DELETE FROM stash WHERE id=?`, s.ID); err != nil {
				return fmt.Errorf("change.StashApplyMerge: drop stash: %w", err)
			}
		}
		return nil
	})
	return cf, err
}

// LastOp returns the most recent operation, or ErrNotFound on an empty log.
func (e *Engine) LastOp() (Operation, error) {
	var op Operation
	switch err := e.db.QueryRow(
		`SELECT id, op_type, actor, detail FROM operation ORDER BY seq DESC LIMIT 1`).
		Scan(&op.ID, &op.OpType, &op.Actor, &op.Detail); {
	case errors.Is(err, sql.ErrNoRows):
		return Operation{}, ErrNotFound
	case err != nil:
		return Operation{}, fmt.Errorf("change.LastOp: %w", err)
	}
	return op, nil
}

// RewindLine puts ONE line's tip back to an earlier value and records it as an
// "undo" operation — what `cherry-pick --abort` / `revert --abort` need, where
// Undo would rewind whatever happened last on any line. The open conflicts on
// changeID (the line's working change) are dropped with the state that held
// them.
func (e *Engine) RewindLine(lineName, tip, changeID string) error {
	before, err := e.viewMap()
	if err != nil {
		return fmt.Errorf("change.RewindLine: %w", err)
	}
	ts := stamp(e.now())
	tx, err := e.db.Begin()
	if err != nil {
		return fmt.Errorf("change.RewindLine: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`UPDATE line SET tip_commit=?, updated_at=? WHERE name=?`, tip, ts, lineName); err != nil {
		return fmt.Errorf("change.RewindLine: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE conflict SET status='resolved', resolved_at=? WHERE change_id=? AND status='open'`,
		ts, changeID); err != nil {
		return fmt.Errorf("change.RewindLine: %w", err)
	}
	if _, err := tx.Exec(`UPDATE change SET has_conflict=0, updated_at=? WHERE id=?`, ts, changeID); err != nil {
		return fmt.Errorf("change.RewindLine: %w", err)
	}
	if err := closeSnapshotBurst(tx, changeID); err != nil {
		return err
	}
	after, err := viewMapTx(tx)
	if err != nil {
		return fmt.Errorf("change.RewindLine: %w", err)
	}
	if err := recordOpTx(tx, e.now().UTC(), "undo", "system", before, after, ts); err != nil {
		return fmt.Errorf("change.RewindLine: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("change.RewindLine: commit tx: %w", err)
	}
	return nil
}
