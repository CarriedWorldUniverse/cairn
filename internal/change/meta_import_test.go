package change

import (
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// TestImportMetaReconstructsTree exercises importMeta via a same-engine round-trip:
// build a line tree (root main -> a -> b) with a sealed change and a conflict,
// ExportMeta to a meta commit, DELETE the whole catalogue, then importMeta from the
// meta commit. The catalogue must be restored identically.
func TestImportMetaReconstructsTree(t *testing.T) {
	e := newTestEngine(t)
	rootID, aID, bID := buildMetaFixture(t, e)

	// Snapshot the pre-export catalogue for comparison.
	wantRoot, err := e.lineByID(rootID)
	if err != nil {
		t.Fatalf("lineByID(root): %v", err)
	}
	wantA, err := e.lineByID(aID)
	if err != nil {
		t.Fatalf("lineByID(a): %v", err)
	}
	wantB, err := e.lineByID(bID)
	if err != nil {
		t.Fatalf("lineByID(b): %v", err)
	}
	wantChanges := map[string]Change{}
	crows, err := e.db.Query(`SELECT id, line_id, author, head_commit, status, has_conflict, sealed FROM change`)
	if err != nil {
		t.Fatalf("query changes: %v", err)
	}
	for crows.Next() {
		var c Change
		var hc, sealed int
		if err := crows.Scan(&c.ID, &c.LineID, &c.Author, &c.HeadCommit, &c.Status, &hc, &sealed); err != nil {
			t.Fatalf("scan change: %v", err)
		}
		c.HasConflict = hc != 0
		c.Sealed = sealed != 0
		wantChanges[c.ID] = c
	}
	_ = crows.Close()
	if len(wantChanges) == 0 {
		t.Fatalf("fixture produced no changes")
	}
	var wantConflictPath, wantConflictChange string
	if err := e.db.QueryRow(`SELECT path, change_id FROM conflict LIMIT 1`).Scan(&wantConflictPath, &wantConflictChange); err != nil {
		t.Fatalf("query conflict: %v", err)
	}

	metaSha, err := e.ExportMeta()
	if err != nil {
		t.Fatalf("ExportMeta: %v", err)
	}

	// Wipe the catalogue, then reconstruct it from the meta commit in one tx.
	for _, q := range []string{`DELETE FROM conflict`, `DELETE FROM change`, `DELETE FROM line`} {
		if _, err := e.db.Exec(q); err != nil {
			t.Fatalf("clear %q: %v", q, err)
		}
	}
	tx, err := e.db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	ts := e.now().UTC().Format(time.RFC3339Nano)
	def, err := e.importMeta(metaSha, tx, ts)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("importMeta: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if def != wantRoot.Name {
		t.Fatalf("default branch = %q, want %q", def, wantRoot.Name)
	}

	// Line tree restored: parents and tip/base preserved.
	gotRoot, err := e.lineByID(rootID)
	if err != nil {
		t.Fatalf("lineByID(root) after import: %v", err)
	}
	if gotRoot.ParentLine != "" {
		t.Fatalf("root parent = %q, want empty (root)", gotRoot.ParentLine)
	}
	if gotRoot.Name != wantRoot.Name || gotRoot.TipCommit != wantRoot.TipCommit || gotRoot.BaseCommit != wantRoot.BaseCommit {
		t.Fatalf("root mismatch: got %+v want %+v", gotRoot, wantRoot)
	}
	gotA, err := e.lineByID(aID)
	if err != nil {
		t.Fatalf("lineByID(a) after import: %v", err)
	}
	if gotA.ParentLine != rootID {
		t.Fatalf("line a parent = %q, want root %q", gotA.ParentLine, rootID)
	}
	if gotA.TipCommit != wantA.TipCommit || gotA.BaseCommit != wantA.BaseCommit {
		t.Fatalf("line a tip/base mismatch: got %q/%q want %q/%q", gotA.TipCommit, gotA.BaseCommit, wantA.TipCommit, wantA.BaseCommit)
	}
	gotB, err := e.lineByID(bID)
	if err != nil {
		t.Fatalf("lineByID(b) after import: %v", err)
	}
	if gotB.ParentLine != aID {
		t.Fatalf("line b parent = %q, want a %q", gotB.ParentLine, aID)
	}
	if gotB.TipCommit != wantB.TipCommit || gotB.BaseCommit != wantB.BaseCommit {
		t.Fatalf("line b tip/base mismatch: got %q/%q want %q/%q", gotB.TipCommit, gotB.BaseCommit, wantB.TipCommit, wantB.BaseCommit)
	}

	// Changes restored with ids, sealed and has_conflict flags.
	for id, want := range wantChanges {
		got, err := e.GetChange(id)
		if err != nil {
			t.Fatalf("GetChange(%s) after import: %v", id, err)
		}
		if got.LineID != want.LineID || got.Author != want.Author || got.HeadCommit != want.HeadCommit ||
			got.Status != want.Status || got.Sealed != want.Sealed || got.HasConflict != want.HasConflict {
			t.Fatalf("change %s mismatch: got %+v want %+v", id, got, want)
		}
	}

	// Conflict restored with its path and change id.
	var gotPath, gotChange string
	if err := e.db.QueryRow(`SELECT path, change_id FROM conflict LIMIT 1`).Scan(&gotPath, &gotChange); err != nil {
		t.Fatalf("query conflict after import: %v", err)
	}
	if gotPath != wantConflictPath || gotChange != wantConflictChange {
		t.Fatalf("conflict mismatch: got %q/%q want %q/%q", gotPath, gotChange, wantConflictPath, wantConflictChange)
	}
}

// writeMetaJSON encodes raw JSON into a meta commit (one tree entry "meta.json")
// and returns the commit sha, mirroring ExportMeta's commit shape.
func writeMetaJSON(t *testing.T, e *Engine, payload []byte) string {
	t.Helper()
	blob, err := e.writeBlob(payload)
	if err != nil {
		t.Fatalf("writeBlob: %v", err)
	}
	tree, err := e.writeTreeRefs(map[string]TreeEntry{"meta.json": {SHA: blob.String(), Mode: ModeRegular}})
	if err != nil {
		t.Fatalf("writeTreeRefs: %v", err)
	}
	sig := object.Signature{Name: "cairn", Email: "meta@cairn", When: time.Unix(0, 0).UTC()}
	c := &object.Commit{Author: sig, Committer: sig, Message: "test-meta\n", TreeHash: tree}
	obj := e.git.Storer.NewEncodedObject()
	if err := c.Encode(obj); err != nil {
		t.Fatalf("encode: %v", err)
	}
	h, err := e.git.Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return h.String()
}

// TestImportMetaVersionGuard: a meta.json with a future version is rejected.
func TestImportMetaVersionGuard(t *testing.T) {
	e := newTestEngine(t)
	sha := writeMetaJSON(t, e, []byte(`{"version":2,"lines":[{"ID":"r","Name":"main","ParentLine":""}]}`))
	tx, err := e.db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = e.importMeta(sha, tx, e.now().UTC().Format(time.RFC3339Nano))
	if err == nil {
		t.Fatalf("expected version error, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported cairn metadata version") {
		t.Fatalf("error = %v, want 'unsupported cairn metadata version'", err)
	}
}

// TestImportMetaNoRoot: a meta with no parent_line-NULL line is rejected.
func TestImportMetaNoRoot(t *testing.T) {
	e := newTestEngine(t)
	// Every line has a non-empty ParentLine, so none is the root.
	sha := writeMetaJSON(t, e, []byte(`{"version":1,"lines":[{"ID":"x","Name":"a","ParentLine":"y"},{"ID":"y","Name":"b","ParentLine":"x"}]}`))
	tx, err := e.db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = e.importMeta(sha, tx, e.now().UTC().Format(time.RFC3339Nano))
	if err == nil {
		t.Fatalf("expected no-root error, got nil")
	}
	if !strings.Contains(err.Error(), "no root line") {
		t.Fatalf("error = %v, want 'no root line'", err)
	}
}

// TestImportFromRemoteUsesMetaFidelity is the end-to-end proof that the meta path
// is wired into ImportFromRemote: a source engine with a non-flat line tree
// (main -> a -> b) is pushed to a cairn-kind bare repo (which receives
// refs/cairn/meta), then a fresh engine ImportFromRemote's it. The reconstructed
// catalogue must reproduce the line TREE (b parents a, a parents the root) rather
// than the lossy flat projection (which would parent every line directly on root).
func TestImportFromRemoteUsesMetaFidelity(t *testing.T) {
	skipOnWindows(t)

	bareDir := t.TempDir()
	if _, err := git.PlainInit(bareDir, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}

	src := newTestEngine(t)
	rootID, aID, bID := buildMetaFixture(t, src)
	// Names for cross-engine assertions (ids are stable across the meta round-trip).
	rootLine, _ := src.lineByID(rootID)
	aLine, _ := src.lineByID(aID)
	bLine, _ := src.lineByID(bID)

	if err := src.AddRemote("origin", bareDir, "cairn"); err != nil {
		t.Fatalf("AddRemote(cairn): %v", err)
	}
	if err := src.PushToRemote("origin", false); err != nil {
		t.Fatalf("PushToRemote: %v", err)
	}

	dst := newTestEngine(t)
	def, err := dst.ImportFromRemote(bareDir)
	if err != nil {
		t.Fatalf("ImportFromRemote: %v", err)
	}
	if def != rootLine.Name {
		t.Fatalf("default branch = %q, want %q", def, rootLine.Name)
	}

	// The line TREE was reconstructed (NOT flattened): b -> a -> root.
	gotA, err := dst.lineByID(aID)
	if err != nil {
		t.Fatalf("dst lineByID(a): %v", err)
	}
	if gotA.Name != aLine.Name || gotA.ParentLine != rootID {
		t.Fatalf("line a = %+v, want name %q parent root %q", gotA, aLine.Name, rootID)
	}
	gotB, err := dst.lineByID(bID)
	if err != nil {
		t.Fatalf("dst lineByID(b): %v", err)
	}
	if gotB.Name != bLine.Name || gotB.ParentLine != aID {
		t.Fatalf("line b = %+v, want name %q parent a %q (flat projection would parent it on root)", gotB, bLine.Name, aID)
	}

	// Sealed changes and the conflict survived the clone.
	var sealedCount, conflictCount int
	if err := dst.db.QueryRow(`SELECT COUNT(*) FROM change WHERE sealed=1`).Scan(&sealedCount); err != nil {
		t.Fatalf("count sealed: %v", err)
	}
	if sealedCount == 0 {
		t.Fatalf("no sealed changes reconstructed on clone")
	}
	if err := dst.db.QueryRow(`SELECT COUNT(*) FROM conflict`).Scan(&conflictCount); err != nil {
		t.Fatalf("count conflicts: %v", err)
	}
	if conflictCount == 0 {
		t.Fatalf("no conflicts reconstructed on clone")
	}
}
