package change

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Change is a row in the change catalogue: a stable, named unit of work on a
// line. Its head_commit advances as work is snapshotted; the change_id never
// changes (jj-style stable identity).
type Change struct {
	ID          string
	LineID      string
	Author      string
	HeadCommit  string
	Status      string
	HasConflict bool
	// Sealed marks a change whose head is no longer the open working commit:
	// a fresh change is open (sealed=0); sealing freezes its head so later
	// snapshots no longer amend it.
	Sealed bool
}

// CommitResult is the outcome of a Commit: the final head commit (after merge-
// forward) and any per-path conflicts recorded while adopting the parent line.
type CommitResult struct {
	HeadCommit string
	Conflicts  []Conflict
}

// reverseHexAlphabet renders nibbles in jj-style reverse hex so change-ids are
// visually distinct from git hex shas.
const reverseHexAlphabet = "zyxwvutsrqponmlk"

// newChangeID returns a stable change-id: a constant 'z' marker followed by the
// reverse-hex rendering of 32 random bytes. The marker guarantees every id
// begins with 'z' and is unmistakably not a hex sha.
func newChangeID() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	out := make([]byte, 0, 1+len(b)*2)
	out = append(out, 'z')
	for _, by := range b {
		out = append(out, reverseHexAlphabet[by>>4], reverseHexAlphabet[by&0x0f])
	}
	return string(out)
}

// CreateChange opens a new change on the given line and returns it. The change
// starts open, with no head commit and no conflict.
func (e *Engine) CreateChange(lineID, author string) (Change, error) {
	ch := Change{
		ID:     newChangeID(),
		LineID: lineID,
		Author: author,
		Status: "open",
	}
	now := e.now().UTC().Format(time.RFC3339Nano)
	_, err := e.db.Exec(
		`INSERT INTO change(id, line_id, author, head_commit, status, has_conflict, sealed, created_at, updated_at)
		 VALUES(?,?,?,'',?,0,0,?,?)`,
		ch.ID, ch.LineID, ch.Author, ch.Status, now, now)
	if err != nil {
		return Change{}, fmt.Errorf("change.CreateChange: %w", err)
	}
	return ch, nil
}

// OpenChangeForLine returns the existing open (unsealed) change for lineID, or
// ErrNotFound if none exists. This allows callers to reuse a previously-imported
// open change (e.g. one restored from refs/cairn/meta on clone) rather than
// always creating a new one.
func (e *Engine) OpenChangeForLine(lineID string) (Change, error) {
	row := e.db.QueryRow(
		`SELECT id, line_id, author, head_commit, status, has_conflict, sealed
		 FROM change WHERE line_id=? AND status='open' AND sealed=0
		 ORDER BY updated_at DESC LIMIT 1`,
		lineID)
	var ch Change
	var hasConflict, sealed int
	if err := row.Scan(&ch.ID, &ch.LineID, &ch.Author, &ch.HeadCommit, &ch.Status, &hasConflict, &sealed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Change{}, ErrNotFound
		}
		return Change{}, fmt.Errorf("change.OpenChangeForLine: %w", err)
	}
	ch.HasConflict = hasConflict != 0
	ch.Sealed = sealed != 0
	return ch, nil
}

// GetChange loads a change by id, or returns ErrNotFound.
func (e *Engine) GetChange(id string) (Change, error) {
	row := e.db.QueryRow(
		`SELECT id, line_id, author, head_commit, status, has_conflict, sealed FROM change WHERE id=?`,
		id)
	var ch Change
	var hasConflict, sealed int
	if err := row.Scan(&ch.ID, &ch.LineID, &ch.Author, &ch.HeadCommit, &ch.Status, &hasConflict, &sealed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Change{}, ErrNotFound
		}
		return Change{}, fmt.Errorf("change.GetChange: %w", err)
	}
	ch.HasConflict = hasConflict != 0
	ch.Sealed = sealed != 0
	return ch, nil
}

// Commit snapshots files onto the change: it writes a tree and a commit whose
// parent is the change's previous head, advances head_commit, and returns the
// new head. This is the pure snapshot half; merge-forward is layered on later.
func (e *Engine) Commit(changeID string, files map[string][]byte, modes map[string]EntryMode, message string) (CommitResult, error) {
	ch, err := e.GetChange(changeID)
	if err != nil {
		return CommitResult{}, err
	}
	before, err := e.viewMap()
	if err != nil {
		return CommitResult{}, fmt.Errorf("change.Commit: %w", err)
	}
	tree, err := e.writeTree(files, modes)
	if err != nil {
		return CommitResult{}, err
	}
	var parents []string
	if ch.HeadCommit != "" {
		parents = []string{ch.HeadCommit}
	} else {
		// First snapshot on this change: parent it on the line's current tip so
		// the snapshot shares git ancestry with the fork point. This gives
		// merge-forward a real common ancestor (mergeBase) with the parent line,
		// so a change that didn't touch a file the parent kept unchanged adopts
		// cleanly instead of colliding against an empty base.
		if line, lerr := e.lineByID(ch.LineID); lerr == nil && line.TipCommit != "" {
			parents = []string{line.TipCommit}
		}
	}
	head, err := e.writeCommit(tree.String(), ch.ID, message, parents)
	if err != nil {
		return CommitResult{}, err
	}

	// Rebase the snapshot onto the line's parent-line tip (adopt the parent),
	// recording any conflicts as data. If the merge produced a tree different
	// from the snapshot's own, re-commit on it so the change's head reflects the
	// adopted state. (A root line, or one whose merge changed nothing, keeps the
	// snapshot as head.)
	merged, conflicts, err := e.mergeForward(ch.ID, head)
	if err != nil {
		return CommitResult{}, err
	}
	if merged != "" && merged != tree.String() {
		head, err = e.writeCommit(merged, ch.ID, message, []string{head})
		if err != nil {
			return CommitResult{}, err
		}
	}
	hasConflict := 0
	if len(conflicts) > 0 {
		hasConflict = 1
	}

	// All three catalogue writes — conflict rows, the change head, and the line
	// tip — commit or roll back together, so a failure can't orphan conflict
	// rows or leave a stale tip. The go-git blob/tree/commit writes above are
	// content-addressed and idempotent, so they stay outside the transaction.
	ts := e.now().UTC().Format(time.RFC3339Nano)
	tx, err := e.db.Begin()
	if err != nil {
		return CommitResult{}, fmt.Errorf("change.Commit: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, c := range conflicts {
		if err := insertConflict(tx, c, ts); err != nil {
			return CommitResult{}, fmt.Errorf("change.Commit: record conflict: %w", err)
		}
	}
	if _, err := tx.Exec(
		`UPDATE change SET head_commit=?, has_conflict=?, updated_at=? WHERE id=?`,
		head, hasConflict, ts, ch.ID); err != nil {
		return CommitResult{}, fmt.Errorf("change.Commit: advance change head: %w", err)
	}
	// advance the owning line's tip to this change's FINAL (post-merge) head.
	// Phase-1 simplification: with multiple concurrent changes on one line, the
	// tip simply reflects the most recent commit.
	if _, err := tx.Exec(`UPDATE line SET tip_commit=?, updated_at=? WHERE id=?`, head, ts, ch.LineID); err != nil {
		return CommitResult{}, fmt.Errorf("change.Commit: advance line tip: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return CommitResult{}, fmt.Errorf("change.Commit: commit tx: %w", err)
	}
	after, err := e.viewMap()
	if err != nil {
		return CommitResult{}, fmt.Errorf("change.Commit: %w", err)
	}
	if err := e.recordOp("commit", ch.Author, before, after); err != nil {
		return CommitResult{}, err
	}
	return CommitResult{HeadCommit: head, Conflicts: conflicts}, nil
}
