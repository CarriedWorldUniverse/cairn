package change

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const metaVersion = 1

type metaDoc struct {
	Version   int          `json:"version"`
	Lines     []metaLine   `json:"lines"`
	Changes   []metaChange `json:"changes"`
	Conflicts []metaConf   `json:"conflicts"`
}
type metaLine struct {
	ID, Name, ParentLine, TipCommit, BaseCommit, Status string
}
type metaChange struct {
	ID, LineID, Author, HeadCommit, Status string
	Sealed, HasConflict                    bool
}
type metaConf struct {
	ID, ChangeID, Path, BaseBlob, ParentBlob, ChangeBlob, MarkedBlob, Status string
}

// ExportMeta serializes the change-graph (lines, changes, conflicts) into one git
// commit and returns its sha. DETERMINISTIC: an unchanged catalogue yields the
// identical commit hash (fixed timestamp + fixed signature + no Change-Id trailer)
// so re-pushes are idempotent. The op-log/stash/bisect state is NOT included.
func (e *Engine) ExportMeta() (string, error) {
	doc := metaDoc{Version: metaVersion}
	// lines (deterministic order)
	rows, err := e.db.Query(`SELECT id, name, COALESCE(parent_line,''), tip_commit, base_commit, status FROM line ORDER BY id`)
	if err != nil {
		return "", fmt.Errorf("change.ExportMeta: lines: %w", err)
	}
	for rows.Next() {
		var l metaLine
		if err := rows.Scan(&l.ID, &l.Name, &l.ParentLine, &l.TipCommit, &l.BaseCommit, &l.Status); err != nil {
			_ = rows.Close()
			return "", err
		}
		doc.Lines = append(doc.Lines, l)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", err
	}
	_ = rows.Close()
	// changes
	crows, err := e.db.Query(`SELECT id, line_id, author, head_commit, status, sealed, has_conflict FROM change ORDER BY id`)
	if err != nil {
		return "", fmt.Errorf("change.ExportMeta: changes: %w", err)
	}
	for crows.Next() {
		var c metaChange
		var sealed, hc int
		if err := crows.Scan(&c.ID, &c.LineID, &c.Author, &c.HeadCommit, &c.Status, &sealed, &hc); err != nil {
			_ = crows.Close()
			return "", err
		}
		c.Sealed = sealed != 0
		c.HasConflict = hc != 0
		doc.Changes = append(doc.Changes, c)
	}
	if err := crows.Err(); err != nil {
		_ = crows.Close()
		return "", err
	}
	_ = crows.Close()
	// conflicts
	frows, err := e.db.Query(`SELECT id, change_id, path, base_blob, parent_blob, change_blob, marked_blob, status FROM conflict ORDER BY id`)
	if err != nil {
		return "", fmt.Errorf("change.ExportMeta: conflicts: %w", err)
	}
	for frows.Next() {
		var c metaConf
		if err := frows.Scan(&c.ID, &c.ChangeID, &c.Path, &c.BaseBlob, &c.ParentBlob, &c.ChangeBlob, &c.MarkedBlob, &c.Status); err != nil {
			_ = frows.Close()
			return "", err
		}
		doc.Conflicts = append(doc.Conflicts, c)
	}
	if err := frows.Err(); err != nil {
		_ = frows.Close()
		return "", err
	}
	_ = frows.Close()

	payload, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", fmt.Errorf("change.ExportMeta: marshal: %w", err)
	}
	blob, err := e.writeBlob(payload)
	if err != nil {
		return "", fmt.Errorf("change.ExportMeta: %w", err)
	}
	tree, err := e.writeTreeRefs(map[string]TreeEntry{"meta.json": {SHA: blob.String(), Mode: ModeRegular}})
	if err != nil {
		return "", fmt.Errorf("change.ExportMeta: %w", err)
	}

	// Deterministic commit: fixed epoch + fixed signature + NO Change-Id trailer.
	// This makes the commit hash a pure function of the serialized catalogue, so
	// an unchanged catalogue re-exports to the identical sha (idempotent push).
	sig := object.Signature{Name: "cairn", Email: "meta@cairn", When: time.Unix(0, 0).UTC()}
	c := &object.Commit{Author: sig, Committer: sig, Message: "cairn-meta v1\n", TreeHash: tree}
	obj := e.git.Storer.NewEncodedObject()
	if err := c.Encode(obj); err != nil {
		return "", fmt.Errorf("change.ExportMeta: encode: %w", err)
	}
	h, err := e.git.Storer.SetEncodedObject(obj)
	if err != nil {
		return "", fmt.Errorf("change.ExportMeta: store: %w", err)
	}
	return h.String(), nil
}

// importMeta reconstructs the cairn change-graph from a meta commit, REPLACING the
// init-created catalogue (clone fidelity). Runs inside the caller's tx. Returns the
// reconstructed default branch name (the root line: parent_line IS NULL).
func (e *Engine) importMeta(metaCommit string, tx *sql.Tx, ts string) (defaultBranch string, err error) {
	// read meta.json from the meta commit's tree
	c, err := e.git.CommitObject(plumbing.NewHash(metaCommit))
	if err != nil {
		return "", fmt.Errorf("change.importMeta: %w", err)
	}
	files, err := e.readTree(c.TreeHash.String())
	if err != nil {
		return "", fmt.Errorf("change.importMeta: %w", err)
	}
	raw, ok := files["meta.json"]
	if !ok {
		return "", errors.New("change.importMeta: meta.json missing from meta commit")
	}
	var doc metaDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("change.importMeta: parse: %w", err)
	}
	if doc.Version != metaVersion {
		return "", fmt.Errorf("change.importMeta: unsupported cairn metadata version %d; upgrade cairn", doc.Version)
	}

	// clear the init-created catalogue (the meta is authoritative for a fresh clone).
	// Order respects FK references (conflict -> change -> line).
	for _, q := range []string{`DELETE FROM conflict`, `DELETE FROM change`, `DELETE FROM line`} {
		if _, err := tx.Exec(q); err != nil {
			return "", fmt.Errorf("change.importMeta: clear: %w", err)
		}
	}
	// Identify the root first so an absent root is reported as "no root line"
	// rather than masked by a FK failure when a child is inserted before its parent.
	for _, l := range doc.Lines {
		if l.ParentLine == "" {
			defaultBranch = l.Name
		}
	}
	if defaultBranch == "" {
		return "", errors.New("change.importMeta: no root line in metadata")
	}
	// install lines (parent_line NULL when ""). Matches ensureRootLine's column set.
	// parent_line REFERENCES line(id), so insert parents before children: iterate in
	// passes, inserting any line whose parent is already present (or is the root).
	inserted := map[string]bool{}
	remaining := append([]metaLine(nil), doc.Lines...)
	for len(remaining) > 0 {
		progressed := false
		next := remaining[:0]
		for _, l := range remaining {
			if l.ParentLine != "" && !inserted[l.ParentLine] {
				next = append(next, l) // parent not yet installed; retry next pass
				continue
			}
			var parent interface{}
			if l.ParentLine != "" {
				parent = l.ParentLine
			}
			if _, err := tx.Exec(
				`INSERT INTO line(id, name, parent_line, tip_commit, base_commit, status, created_at, updated_at)
				 VALUES(?,?,?,?,?,?,?,?)`,
				l.ID, l.Name, parent, l.TipCommit, l.BaseCommit, l.Status, ts, ts); err != nil {
				return "", fmt.Errorf("change.importMeta: line: %w", err)
			}
			inserted[l.ID] = true
			progressed = true
		}
		remaining = next
		if !progressed {
			return "", fmt.Errorf("change.importMeta: line tree has %d unresolved parent reference(s)", len(remaining))
		}
	}
	// install changes. Matches CreateChange's column set.
	for _, ch := range doc.Changes {
		sealed, hc := 0, 0
		if ch.Sealed {
			sealed = 1
		}
		if ch.HasConflict {
			hc = 1
		}
		if _, err := tx.Exec(
			`INSERT INTO change(id, line_id, author, head_commit, status, has_conflict, sealed, created_at, updated_at)
			 VALUES(?,?,?,?,?,?,?,?,?)`,
			ch.ID, ch.LineID, ch.Author, ch.HeadCommit, ch.Status, hc, sealed, ts, ts); err != nil {
			return "", fmt.Errorf("change.importMeta: change: %w", err)
		}
	}
	// install conflicts. resolved_at is nullable (omitted); all NOT NULL columns set.
	for _, cf := range doc.Conflicts {
		if _, err := tx.Exec(
			`INSERT INTO conflict(id, change_id, path, base_blob, parent_blob, change_blob, marked_blob, status, created_at)
			 VALUES(?,?,?,?,?,?,?,?,?)`,
			cf.ID, cf.ChangeID, cf.Path, cf.BaseBlob, cf.ParentBlob, cf.ChangeBlob, cf.MarkedBlob, cf.Status, ts); err != nil {
			return "", fmt.Errorf("change.importMeta: conflict: %w", err)
		}
	}
	return defaultBranch, nil
}
