package change

import (
	"encoding/json"
	"fmt"
	"time"
)

// Operation is a row in the append-only operation log: a single mutating action
// on the engine, with a before/after snapshot of the ref-map (line name → tip
// commit). The log never shrinks; Undo restores prior tips by appending a new
// "undo" operation rather than deleting history.
type Operation struct {
	ID         string
	OpType     string
	Actor      string
	ParentOp   string
	ViewBefore map[string]string
	ViewAfter  map[string]string
	Detail     string
}

// viewMap snapshots the ref-map: every non-abandoned line's name → tip_commit.
func (e *Engine) viewMap() (map[string]string, error) {
	view := map[string]string{}
	rows, err := e.db.Query(`SELECT name, tip_commit FROM line WHERE status != 'abandoned'`)
	if err != nil {
		return nil, fmt.Errorf("change.viewMap: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name, tip string
		if err := rows.Scan(&name, &tip); err != nil {
			return nil, fmt.Errorf("change.viewMap: %w", err)
		}
		view[name] = tip
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("change.viewMap: %w", err)
	}
	return view, nil
}

// recordOp appends a single operation to the log. The id carries an RFC3339Nano
// time prefix so ORDER BY id is chronological, plus a short random suffix to
// disambiguate ops minted within the same nanosecond.
func (e *Engine) recordOp(opType, actor string, before, after map[string]string) error {
	beforeJSON, err := json.Marshal(before)
	if err != nil {
		return fmt.Errorf("change.recordOp: marshal before: %w", err)
	}
	afterJSON, err := json.Marshal(after)
	if err != nil {
		return fmt.Errorf("change.recordOp: marshal after: %w", err)
	}
	now := e.now().UTC()
	id := now.Format(time.RFC3339Nano) + "-" + newID()[:8]
	// parent_op is the max existing op id, selected inline so the parent pick and
	// the insert are atomic under SQLite's serialized writes (no SELECT-then-INSERT
	// race). The new row's id is an RFC3339Nano "now" prefix, which sorts after all
	// existing ids, so MAX(id) over the current rows is the correct parent.
	if _, err := e.db.Exec(
		`INSERT INTO operation(id, op_type, actor, parent_op, view_before, view_after, detail, at)
		 VALUES(?,?,?, (SELECT COALESCE(MAX(id),'') FROM operation), ?,?,'{}',?)`,
		id, opType, actor, string(beforeJSON), string(afterJSON),
		now.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("change.recordOp: %w", err)
	}
	return nil
}

// OperationLog returns the full operation log in chronological order.
func (e *Engine) OperationLog() ([]Operation, error) {
	rows, err := e.db.Query(
		`SELECT id, op_type, actor, parent_op, view_before, view_after, detail
		 FROM operation ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("change.OperationLog: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var ops []Operation
	for rows.Next() {
		var op Operation
		var beforeJSON, afterJSON string
		if err := rows.Scan(&op.ID, &op.OpType, &op.Actor, &op.ParentOp, &beforeJSON, &afterJSON, &op.Detail); err != nil {
			return nil, fmt.Errorf("change.OperationLog: %w", err)
		}
		if err := json.Unmarshal([]byte(beforeJSON), &op.ViewBefore); err != nil {
			return nil, fmt.Errorf("change.OperationLog: unmarshal before: %w", err)
		}
		if err := json.Unmarshal([]byte(afterJSON), &op.ViewAfter); err != nil {
			return nil, fmt.Errorf("change.OperationLog: unmarshal after: %w", err)
		}
		ops = append(ops, op)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("change.OperationLog: %w", err)
	}
	return ops, nil
}

// Undo reverts the most recent operation by restoring each line's tip to the
// value it held in that operation's view_before, then records the undo itself as
// a new operation (append-only history). Returns ErrNotFound on an empty log.
//
// Phase-1 limitation: Undo restores line tips only. It does not delete lines
// created by the undone op (e.g. a branch); those remain, merely with their tips
// reset to the pre-op view. Full reversal is a later task.
func (e *Engine) Undo() error {
	ops, err := e.OperationLog()
	if err != nil {
		return err
	}
	if len(ops) == 0 {
		return ErrNotFound
	}
	last := ops[len(ops)-1]
	cur, err := e.viewMap()
	if err != nil {
		return err
	}

	ts := e.now().UTC().Format(time.RFC3339Nano)
	tx, err := e.db.Begin()
	if err != nil {
		return fmt.Errorf("change.Undo: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for name, sha := range last.ViewBefore {
		if _, err := tx.Exec(
			`UPDATE line SET tip_commit=?, updated_at=? WHERE name=?`,
			sha, ts, name); err != nil {
			return fmt.Errorf("change.Undo: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("change.Undo: commit tx: %w", err)
	}
	if err := e.recordOp("undo", "system", cur, last.ViewBefore); err != nil {
		return err
	}
	return nil
}
