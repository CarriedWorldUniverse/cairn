package change

import (
	"encoding/json"
	"fmt"
	"time"

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
