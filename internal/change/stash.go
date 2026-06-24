package change

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
)

// opStash / opStashPop are the op-log types for shelving and restoring a working
// change's delta. Neither coalesces.
const (
	opStash    = "stash"
	opStashPop = "stash-pop"
)

// StashEntry is a row in the stash catalogue: one shelved working delta. The
// shelved delta lives in CommitSha (a commit whose tree is the stashed working
// tree) rooted at BaseSha (the working change's parent at push time).
type StashEntry struct {
	ID        int64
	Branch    string
	Message   string
	CommitSha string
	BaseSha   string
	CreatedAt string
}

// treeHashOf returns the hex tree hash of commit sha. The empty sha maps to the
// empty tree (so "reset to a root parent" compares equal to a no-content head),
// computed via writeTreeRefs(nil) which is content-addressed and idempotent.
func (e *Engine) treeHashOf(sha string) (string, error) {
	if sha == "" {
		h, err := e.writeTreeRefs(nil)
		if err != nil {
			return "", fmt.Errorf("change.treeHashOf: empty tree: %w", err)
		}
		return h.String(), nil
	}
	c, err := e.git.CommitObject(plumbing.NewHash(sha))
	if err != nil {
		return "", fmt.Errorf("change.treeHashOf: commit %s: %w", sha, err)
	}
	return c.TreeHash.String(), nil
}

// parentsSlice returns the writeCommit parents slice for a single parent: the
// one-element slice when parent is non-empty, or nil for a root commit.
func parentsSlice(parent string) []string {
	if parent == "" {
		return nil
	}
	return []string{parent}
}

// StashPush shelves the open working change's delta onto the stash stack and
// resets the working change to its parent (working-copy-is-a-commit: the reset is
// an amend of the working commit onto its own parent tree). The shelved working
// commit is recorded verbatim in the stash row, so apply can restore its tree.
// Errors "nothing to stash" when the change has no head or its head tree already
// equals its parent's (a clean working change).
//
// The catalogue writes — inserting the stash row, resetting the change head,
// advancing the line tip, and the op-log entry — commit or roll back together in
// ONE transaction. The go-git reset-commit write stays outside the tx
// (content-addressed and idempotent).
func (e *Engine) StashPush(changeID, message string) (int64, error) {
	ch, err := e.GetChange(changeID)
	if err != nil {
		return 0, err
	}
	if ch.HeadCommit == "" {
		return 0, errors.New("nothing to stash")
	}
	parent, err := e.firstParent(ch.HeadCommit)
	if err != nil {
		return 0, fmt.Errorf("change.StashPush: %w", err)
	}
	headTree, err := e.treeHashOf(ch.HeadCommit)
	if err != nil {
		return 0, fmt.Errorf("change.StashPush: %w", err)
	}
	parentTree, err := e.treeHashOf(parent)
	if err != nil {
		return 0, fmt.Errorf("change.StashPush: %w", err)
	}
	if headTree == parentTree {
		return 0, errors.New("nothing to stash")
	}
	line, err := e.lineByID(ch.LineID)
	if err != nil {
		return 0, err
	}

	before, err := e.viewMap()
	if err != nil {
		return 0, fmt.Errorf("change.StashPush: %w", err)
	}

	// Write the reset commit (amend the working commit onto its parent's tree).
	reset, err := e.writeCommit(parentTree, ch.ID, workingDescription, parentsSlice(parent))
	if err != nil {
		return 0, fmt.Errorf("change.StashPush: write reset commit: %w", err)
	}

	ts := e.now().UTC().Format(time.RFC3339Nano)
	tx, err := e.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("change.StashPush: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.Exec(
		`INSERT INTO stash(line_id, branch, commit_sha, base_sha, message, created_at)
		 VALUES(?,?,?,?,?,?)`,
		ch.LineID, line.Name, ch.HeadCommit, parent, message, ts)
	if err != nil {
		return 0, fmt.Errorf("change.StashPush: insert stash: %w", err)
	}
	stashID, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("change.StashPush: last insert id: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE change SET head_commit=?, updated_at=? WHERE id=?`,
		reset, ts, ch.ID); err != nil {
		return 0, fmt.Errorf("change.StashPush: reset change head: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE line SET tip_commit=?, updated_at=? WHERE id=?`,
		reset, ts, ch.LineID); err != nil {
		return 0, fmt.Errorf("change.StashPush: advance line tip: %w", err)
	}
	after, err := viewMapTx(tx)
	if err != nil {
		return 0, fmt.Errorf("change.StashPush: %w", err)
	}
	if err := recordOpTx(tx, e.now().UTC(), opStash, ch.Author, before, after, ts); err != nil {
		return 0, fmt.Errorf("change.StashPush: record op: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("change.StashPush: commit tx: %w", err)
	}
	return stashID, nil
}

// StashList returns the stash stack newest-first.
func (e *Engine) StashList() ([]StashEntry, error) {
	rows, err := e.db.Query(
		`SELECT id, branch, message, commit_sha, base_sha, created_at FROM stash ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("change.StashList: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []StashEntry
	for rows.Next() {
		var s StashEntry
		if err := rows.Scan(&s.ID, &s.Branch, &s.Message, &s.CommitSha, &s.BaseSha, &s.CreatedAt); err != nil {
			return nil, fmt.Errorf("change.StashList: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("change.StashList: %w", err)
	}
	return out, nil
}

// StashApply applies a shelved delta onto the working change (an amend of the
// working commit onto the stashed tree, keeping the working change's own parent),
// and drops the stash row when drop is set (pop). stashID 0 selects the top of
// the stack. It refuses to apply over a dirty working change (head tree != parent
// tree) so un-sealed work is never silently overwritten.
//
// The catalogue writes — advancing the change head, advancing the line tip, the
// optional stash-row delete, and the op-log entry — commit or roll back together
// in ONE transaction. The go-git apply-commit write stays outside the tx.
func (e *Engine) StashApply(changeID string, stashID int64, drop bool) error {
	var (
		rowID     int64
		commitSha string
	)
	query := `SELECT id, commit_sha FROM stash WHERE id=?`
	args := []any{stashID}
	if stashID == 0 {
		query = `SELECT id, commit_sha FROM stash ORDER BY id DESC LIMIT 1`
		args = nil
	}
	switch err := e.db.QueryRow(query, args...).Scan(&rowID, &commitSha); {
	case errors.Is(err, sql.ErrNoRows):
		if stashID == 0 {
			return errors.New("no stash entries")
		}
		return fmt.Errorf("stash %d not found", stashID)
	case err != nil:
		return fmt.Errorf("change.StashApply: load stash: %w", err)
	}

	ch, err := e.GetChange(changeID)
	if err != nil {
		return err
	}
	if ch.HeadCommit == "" {
		return errors.New("change.StashApply: working change has no snapshot")
	}
	parent, err := e.firstParent(ch.HeadCommit)
	if err != nil {
		return fmt.Errorf("change.StashApply: %w", err)
	}
	headTree, err := e.treeHashOf(ch.HeadCommit)
	if err != nil {
		return fmt.Errorf("change.StashApply: %w", err)
	}
	parentTree, err := e.treeHashOf(parent)
	if err != nil {
		return fmt.Errorf("change.StashApply: %w", err)
	}
	if headTree != parentTree {
		return errors.New("working change has un-sealed work; commit or stash it before popping")
	}

	stashedTree, err := e.treeHashOf(commitSha)
	if err != nil {
		return fmt.Errorf("change.StashApply: %w", err)
	}
	applied, err := e.writeCommit(stashedTree, ch.ID, workingDescription, parentsSlice(parent))
	if err != nil {
		return fmt.Errorf("change.StashApply: write apply commit: %w", err)
	}

	before, err := e.viewMap()
	if err != nil {
		return fmt.Errorf("change.StashApply: %w", err)
	}

	ts := e.now().UTC().Format(time.RFC3339Nano)
	tx, err := e.db.Begin()
	if err != nil {
		return fmt.Errorf("change.StashApply: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(
		`UPDATE change SET head_commit=?, updated_at=? WHERE id=?`,
		applied, ts, ch.ID); err != nil {
		return fmt.Errorf("change.StashApply: advance change head: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE line SET tip_commit=?, updated_at=? WHERE id=?`,
		applied, ts, ch.LineID); err != nil {
		return fmt.Errorf("change.StashApply: advance line tip: %w", err)
	}
	if drop {
		if _, err := tx.Exec(`DELETE FROM stash WHERE id=?`, rowID); err != nil {
			return fmt.Errorf("change.StashApply: drop stash: %w", err)
		}
	}
	after, err := viewMapTx(tx)
	if err != nil {
		return fmt.Errorf("change.StashApply: %w", err)
	}
	if err := recordOpTx(tx, e.now().UTC(), opStashPop, ch.Author, before, after, ts); err != nil {
		return fmt.Errorf("change.StashApply: record op: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("change.StashApply: commit tx: %w", err)
	}
	return nil
}

// StashDrop deletes a stash entry. Drops are final — they record no operation,
// so they are not reversible by 'cairn undo' (matching git stash drop).
// stashID 0 drops the top of the stack. Errors "no stash entries" when there
// is nothing to drop.
func (e *Engine) StashDrop(stashID int64) error {
	rowID := stashID
	if stashID == 0 {
		switch err := e.db.QueryRow(
			`SELECT id FROM stash ORDER BY id DESC LIMIT 1`).Scan(&rowID); {
		case errors.Is(err, sql.ErrNoRows):
			return errors.New("no stash entries")
		case err != nil:
			return fmt.Errorf("change.StashDrop: resolve top: %w", err)
		}
	}
	res, err := e.db.Exec(`DELETE FROM stash WHERE id=?`, rowID)
	if err != nil {
		return fmt.Errorf("change.StashDrop: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("change.StashDrop: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("stash %d not found", stashID)
	}
	return nil
}
