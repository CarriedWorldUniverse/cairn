package change

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// The stash stack and the open-conflict rows live outside the ref-map view an
// operation records, so Undo needs another way to put them back. SQL triggers
// (schema.sql) journal every stash/conflict row an operation inserts, deletes
// or changes into aux_journal; recordOp/recordOpTx claim the unclaimed journal
// rows for the operation they append, and Undo replays that operation's rows
// newest-first: an inserted row is removed, a deleted row re-inserted, a
// changed row restored. Whatever changes these tables outside an operation
// calls dropUnclaimedAux (nothing to undo) or attachUnclaimedAux (it is the
// tail of the preceding operation).

// auxExec is the Exec surface *sql.DB and *sql.Tx share.
type auxExec interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// claimAux assigns the unclaimed journal rows to opID.
func claimAux(x auxExec, opID string) error {
	if _, err := x.Exec(`UPDATE aux_journal SET op_id=? WHERE op_id=''`, opID); err != nil {
		return fmt.Errorf("change: journal claim: %w", err)
	}
	return nil
}

// dropUnclaimedAux forgets unclaimed journal rows: the change that made them is
// not an undoable operation.
func dropUnclaimedAux(x auxExec) error {
	if _, err := x.Exec(`DELETE FROM aux_journal WHERE op_id=''`); err != nil {
		return fmt.Errorf("change: journal drop: %w", err)
	}
	return nil
}

// attachUnclaimedAux folds unclaimed journal rows into the most recent
// operation, for follow-up writes that complete it.
func attachUnclaimedAux(x auxExec) error {
	if _, err := x.Exec(
		`UPDATE aux_journal SET op_id=(SELECT id FROM operation ORDER BY seq DESC LIMIT 1) WHERE op_id=''`); err != nil {
		return fmt.Errorf("change: journal attach: %w", err)
	}
	return nil
}

// restoreAux replays opID's journal backwards inside tx.
func restoreAux(tx *sql.Tx, opID string) error {
	type entry struct{ tbl, kind, rowID, old string }
	rows, err := tx.Query(`SELECT tbl, kind, row_id, old FROM aux_journal WHERE op_id=? ORDER BY seq DESC`, opID)
	if err != nil {
		return fmt.Errorf("change.Undo: journal: %w", err)
	}
	var all []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.tbl, &e.kind, &e.rowID, &e.old); err != nil {
			_ = rows.Close()
			return fmt.Errorf("change.Undo: journal: %w", err)
		}
		all = append(all, e)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("change.Undo: journal: %w", err)
	}
	touched := map[string]struct{}{} // change ids whose conflicts moved
	for _, e := range all {
		switch {
		case e.kind == "I":
			if e.tbl == "conflict" {
				var cid string
				if tx.QueryRow(`SELECT change_id FROM conflict WHERE id=?`, e.rowID).Scan(&cid) == nil {
					touched[cid] = struct{}{}
				}
			}
			if _, err := tx.Exec(`DELETE FROM `+e.tbl+` WHERE id=?`, e.rowID); err != nil {
				return fmt.Errorf("change.Undo: journal: %w", err)
			}
		case e.tbl == "stash":
			var s struct {
				ID        int64  `json:"id"`
				LineID    string `json:"line_id"`
				Branch    string `json:"branch"`
				CommitSha string `json:"commit_sha"`
				BaseSha   string `json:"base_sha"`
				Message   string `json:"message"`
				CreatedAt string `json:"created_at"`
			}
			if err := json.Unmarshal([]byte(e.old), &s); err != nil {
				return fmt.Errorf("change.Undo: journal: %w", err)
			}
			if _, err := tx.Exec(
				`INSERT OR REPLACE INTO stash(id, line_id, branch, commit_sha, base_sha, message, created_at) VALUES(?,?,?,?,?,?,?)`,
				s.ID, s.LineID, s.Branch, s.CommitSha, s.BaseSha, s.Message, s.CreatedAt); err != nil {
				return fmt.Errorf("change.Undo: journal: %w", err)
			}
		default: // a deleted or changed conflict row
			var c struct {
				ID         string  `json:"id"`
				ChangeID   string  `json:"change_id"`
				Path       string  `json:"path"`
				BaseBlob   string  `json:"base_blob"`
				ParentBlob string  `json:"parent_blob"`
				ChangeBlob string  `json:"change_blob"`
				MarkedBlob string  `json:"marked_blob"`
				Status     string  `json:"status"`
				CreatedAt  string  `json:"created_at"`
				ResolvedAt *string `json:"resolved_at"`
			}
			if err := json.Unmarshal([]byte(e.old), &c); err != nil {
				return fmt.Errorf("change.Undo: journal: %w", err)
			}
			// The change may have been deleted since (a rebase dropping a
			// sealed change); a conflict on nothing is not restorable.
			var one int
			if tx.QueryRow(`SELECT 1 FROM change WHERE id=?`, c.ChangeID).Scan(&one) != nil {
				continue
			}
			touched[c.ChangeID] = struct{}{}
			if _, err := tx.Exec(
				`INSERT OR REPLACE INTO conflict(id, change_id, path, base_blob, parent_blob, change_blob, marked_blob, status, created_at, resolved_at)
				 VALUES(?,?,?,?,?,?,?,?,?,?)`,
				c.ID, c.ChangeID, c.Path, c.BaseBlob, c.ParentBlob, c.ChangeBlob, c.MarkedBlob, c.Status, c.CreatedAt, c.ResolvedAt); err != nil {
				return fmt.Errorf("change.Undo: journal: %w", err)
			}
		}
	}
	// has_conflict follows the open rows.
	for cid := range touched {
		if _, err := tx.Exec(
			`UPDATE change SET has_conflict=EXISTS(SELECT 1 FROM conflict WHERE change_id=? AND status='open') WHERE id=?`,
			cid, cid); err != nil {
			return fmt.Errorf("change.Undo: journal: %w", err)
		}
	}
	return nil
}
