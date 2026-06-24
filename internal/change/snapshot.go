package change

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
)

// opSnapshot is the op-log type for a working-copy snapshot. A burst of these
// on one change coalesces into a single op (see recordSnapshotOp) so an undo
// reverses the whole burst, not one auto-snapshot at a time.
const opSnapshot = "snapshot"

// workingDescription is the placeholder message carried by an open working
// commit until the change is described.
const workingDescription = "(working)"

// SnapshotWorking amends the open working change's head IN PLACE to reflect
// entries (the current folder), keeping the working commit's PARENT and
// description — only the tree changes. No merge-forward. Returns changed=false
// (no-op) when the tree already matches the current head. For a change with no
// head yet, it creates the working commit (parent = line tip) even if the tree
// is empty, so every open change always has a (working) commit at the line tip.
//
// This is an AMEND: the new commit's parent is the SAME as the old head's
// parent, NOT the old head. Appending (parent=oldHead) would explode history on
// every snapshot.
func (e *Engine) SnapshotWorking(changeID string, entries map[string]TreeEntry) (changed bool, head string, err error) {
	ch, err := e.GetChange(changeID)
	if err != nil {
		return false, "", err
	}
	line, err := e.lineByID(ch.LineID)
	if err != nil {
		return false, "", err
	}

	tree, err := e.writeTreeRefs(entries)
	if err != nil {
		return false, "", err
	}

	// Parent: preserve the existing working commit's parent (amend), or root the
	// first snapshot on the line's current tip.
	var parent string
	if ch.HeadCommit != "" {
		parent, err = e.firstParent(ch.HeadCommit)
		if err != nil {
			return false, "", fmt.Errorf("change.SnapshotWorking: %w", err)
		}
	} else {
		parent = line.TipCommit
	}

	// No-op check: the head already snapshots this exact tree.
	desc := workingDescription
	if ch.HeadCommit != "" {
		cur, err := e.git.CommitObject(plumbing.NewHash(ch.HeadCommit))
		if err != nil {
			return false, "", fmt.Errorf("change.SnapshotWorking: read head: %w", err)
		}
		if cur.TreeHash.String() == tree.String() {
			return false, ch.HeadCommit, nil
		}
		desc = stripChangeID(cur.Message)
	}

	var parents []string
	if parent != "" {
		parents = []string{parent}
	}
	newHead, err := e.writeCommit(tree.String(), ch.ID, desc, parents)
	if err != nil {
		return false, "", err
	}

	before, err := e.viewMap()
	if err != nil {
		return false, "", fmt.Errorf("change.SnapshotWorking: %w", err)
	}

	ts := e.now().UTC().Format(time.RFC3339Nano)
	tx, err := e.db.Begin()
	if err != nil {
		return false, "", fmt.Errorf("change.SnapshotWorking: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(
		`UPDATE change SET head_commit=?, updated_at=? WHERE id=?`,
		newHead, ts, ch.ID); err != nil {
		return false, "", fmt.Errorf("change.SnapshotWorking: advance change head: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE line SET tip_commit=?, updated_at=? WHERE id=?`,
		newHead, ts, ch.LineID); err != nil {
		return false, "", fmt.Errorf("change.SnapshotWorking: advance line tip: %w", err)
	}

	// after reflects the new tip (computed inside the tx so view_after captures
	// the just-written line tip).
	after, err := viewMapTx(tx)
	if err != nil {
		return false, "", fmt.Errorf("change.SnapshotWorking: %w", err)
	}
	if err := e.recordSnapshotOp(tx, ch.ID, ch.Author, before, after, ts); err != nil {
		return false, "", err
	}

	if err := tx.Commit(); err != nil {
		return false, "", fmt.Errorf("change.SnapshotWorking: commit tx: %w", err)
	}
	return true, newHead, nil
}

// recordSnapshotOp records (or coalesces) a snapshot op inside tx. If the most
// recent op is itself a snapshot for the SAME change (detail == changeID), it
// UPDATEs that row's view_after and timestamp in place — so a burst of
// auto-snapshots is a single undo step, with view_before still the pre-burst
// view. Otherwise it INSERTs a new snapshot op exactly as recordOp does
// (parent_op = the current MAX(id), so the op chain stays intact), storing the
// changeID in detail and the pre-snapshot view in view_before.
func (e *Engine) recordSnapshotOp(tx *sql.Tx, changeID, actor string, before, after map[string]string, ts string) error {
	afterJSON, err := json.Marshal(after)
	if err != nil {
		return fmt.Errorf("change.recordSnapshotOp: marshal after: %w", err)
	}

	// Inspect the most recent op for coalescing.
	var lastID, lastType, lastDetail string
	err = tx.QueryRow(
		`SELECT id, op_type, detail FROM operation ORDER BY id DESC LIMIT 1`).
		Scan(&lastID, &lastType, &lastDetail)
	switch {
	case err == nil && lastType == opSnapshot && lastDetail == changeID:
		// Coalesce: extend the existing burst op's view_after + timestamp.
		if _, err := tx.Exec(
			`UPDATE operation SET view_after=?, at=? WHERE id=?`,
			string(afterJSON), ts, lastID); err != nil {
			return fmt.Errorf("change.recordSnapshotOp: coalesce: %w", err)
		}
		return nil
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("change.recordSnapshotOp: probe last op: %w", err)
	}

	// Insert a fresh snapshot op (mirrors recordOp's insert, with detail set to
	// the changeID and parent_op selected inline as MAX(id)).
	beforeJSON, err := json.Marshal(before)
	if err != nil {
		return fmt.Errorf("change.recordSnapshotOp: marshal before: %w", err)
	}
	now := e.now().UTC()
	id := now.Format(time.RFC3339Nano) + "-" + newID()[:8]
	if _, err := tx.Exec(
		`INSERT INTO operation(id, op_type, actor, parent_op, view_before, view_after, detail, at)
		 VALUES(?,?,?, (SELECT COALESCE(MAX(id),'') FROM operation), ?,?,?,?)`,
		id, opSnapshot, actor, string(beforeJSON), string(afterJSON), changeID, ts); err != nil {
		return fmt.Errorf("change.recordSnapshotOp: %w", err)
	}
	return nil
}

// viewMapTx is viewMap evaluated against an open transaction, so a snapshot op's
// view_after sees the line tip just written in the same tx.
func viewMapTx(tx *sql.Tx) (map[string]string, error) {
	view := map[string]string{}
	rows, err := tx.Query(`SELECT name, tip_commit FROM line WHERE status != 'abandoned'`)
	if err != nil {
		return nil, fmt.Errorf("change.viewMapTx: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name, tip string
		if err := rows.Scan(&name, &tip); err != nil {
			return nil, fmt.Errorf("change.viewMapTx: %w", err)
		}
		view[name] = tip
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("change.viewMapTx: %w", err)
	}
	return view, nil
}
