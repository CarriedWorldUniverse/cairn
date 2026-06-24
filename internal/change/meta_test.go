package change

import (
	"encoding/json"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

// readMetaDoc loads the meta.json blob from the meta commit's tree and unmarshals
// it. The meta commit's tree has exactly one entry, "meta.json".
func readMetaDoc(t *testing.T, e *Engine, commitSha string) metaDoc {
	t.Helper()
	c, err := e.git.CommitObject(plumbing.NewHash(commitSha))
	if err != nil {
		t.Fatalf("CommitObject(%s): %v", commitSha, err)
	}
	files, err := e.readTree(c.TreeHash.String())
	if err != nil {
		t.Fatalf("readTree: %v", err)
	}
	raw, ok := files["meta.json"]
	if !ok {
		t.Fatalf("meta tree missing meta.json (have %v)", files)
	}
	var doc metaDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal meta.json: %v", err)
	}
	return doc
}

// buildMetaFixture builds a line tree (root main -> a -> b), a sealed change on a
// line, and a conflict row (via a merge-forward conflict on a child line). It
// returns the line ids for assertions.
func buildMetaFixture(t *testing.T, e *Engine) (rootID, aID, bID string) {
	t.Helper()
	main, err := e.LineByName("main")
	if err != nil {
		t.Fatalf("LineByName(main): %v", err)
	}
	// Seed root so it has a tip; child lines fork off the tip.
	seedLineTip(t, e, main.ID, map[string][]byte{"f.txt": []byte("base\n")})

	a, err := e.CreateLine("a", main.ID)
	if err != nil {
		t.Fatalf("CreateLine(a): %v", err)
	}
	// Give line a a tip so b can fork off it.
	seedLineTip(t, e, a.ID, map[string][]byte{"a.txt": []byte("a\n")})

	b, err := e.CreateLine("b", a.ID)
	if err != nil {
		t.Fatalf("CreateLine(b): %v", err)
	}

	// A sealed change on line b.
	ch := openChange(t, e, b.ID)
	if _, _, err := e.SnapshotWorking(ch, map[string]TreeEntry{"s.txt": blobEntry(t, e, "sealed\n")}); err != nil {
		t.Fatalf("SnapshotWorking(sealed): %v", err)
	}
	if _, _, err := e.Seal(ch, "sealed work"); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// A conflict: advance main, then snapshot+seal on a over the same region.
	mc, _ := e.CreateChange(main.ID, "agent-main")
	if _, err := e.Commit(mc.ID, map[string][]byte{"f.txt": []byte("X\n")}, nil, ""); err != nil {
		t.Fatalf("advance main: %v", err)
	}
	cc, _ := e.CreateChange(a.ID, "agent-a")
	if _, _, err := e.SnapshotWorking(cc.ID, map[string]TreeEntry{"f.txt": blobEntry(t, e, "Y\n")}); err != nil {
		t.Fatalf("SnapshotWorking(conflict): %v", err)
	}
	if _, conflicts, err := e.Seal(cc.ID, "a work"); err != nil {
		t.Fatalf("Seal(conflict): %v", err)
	} else if len(conflicts) < 1 {
		t.Fatalf("expected a conflict to be recorded, got %d", len(conflicts))
	}

	return main.ID, a.ID, b.ID
}

// TestExportMetaDeterministic: an unchanged catalogue yields the identical commit
// hash on repeated exports (fixed timestamp + fixed signature + no Change-Id).
func TestExportMetaDeterministic(t *testing.T) {
	e := newTestEngine(t)
	buildMetaFixture(t, e)

	sha1, err := e.ExportMeta()
	if err != nil {
		t.Fatalf("ExportMeta 1: %v", err)
	}
	sha2, err := e.ExportMeta()
	if err != nil {
		t.Fatalf("ExportMeta 2: %v", err)
	}
	if sha1 != sha2 {
		t.Fatalf("ExportMeta not deterministic: %s != %s", sha1, sha2)
	}
}

// TestExportMetaContent: the serialized meta.json round-trips the line tree, the
// sealed change, and the conflict.
func TestExportMetaContent(t *testing.T) {
	e := newTestEngine(t)
	rootID, aID, bID := buildMetaFixture(t, e)

	sha, err := e.ExportMeta()
	if err != nil {
		t.Fatalf("ExportMeta: %v", err)
	}
	doc := readMetaDoc(t, e, sha)

	if doc.Version != metaVersion {
		t.Fatalf("version = %d, want %d", doc.Version, metaVersion)
	}

	lineByID := map[string]metaLine{}
	for _, l := range doc.Lines {
		lineByID[l.ID] = l
	}
	a, ok := lineByID[aID]
	if !ok {
		t.Fatalf("line a (%s) missing from meta", aID)
	}
	if a.ParentLine != rootID {
		t.Fatalf("line a parent = %q, want root %q", a.ParentLine, rootID)
	}
	b, ok := lineByID[bID]
	if !ok {
		t.Fatalf("line b (%s) missing from meta", bID)
	}
	if b.ParentLine != aID {
		t.Fatalf("line b parent = %q, want a %q", b.ParentLine, aID)
	}
	// Lines carry their tip/base from the catalogue (round-trip vs DB).
	bLine, err := e.lineByID(bID)
	if err != nil {
		t.Fatalf("lineByID(b): %v", err)
	}
	if b.TipCommit != bLine.TipCommit || b.BaseCommit != bLine.BaseCommit {
		t.Fatalf("line b tip/base = %q/%q, want %q/%q", b.TipCommit, b.BaseCommit, bLine.TipCommit, bLine.BaseCommit)
	}

	// At least one sealed change with a head commit.
	var sealedFound bool
	for _, c := range doc.Changes {
		if c.Sealed && c.HeadCommit != "" {
			sealedFound = true
		}
	}
	if !sealedFound {
		t.Fatalf("no sealed change with head found in meta: %+v", doc.Changes)
	}

	// The conflict is present with its path and blobs.
	if len(doc.Conflicts) < 1 {
		t.Fatalf("no conflicts in meta, want >= 1")
	}
	var confFound bool
	for _, cf := range doc.Conflicts {
		if cf.Path == "f.txt" && cf.ChangeID != "" {
			confFound = true
		}
	}
	if !confFound {
		t.Fatalf("conflict on f.txt not found in meta: %+v", doc.Conflicts)
	}
}

// TestExportMetaChangesAfterEdit: sealing a new commit changes the catalogue, so a
// subsequent export yields a different commit hash.
func TestExportMetaChangesAfterEdit(t *testing.T) {
	e := newTestEngine(t)
	buildMetaFixture(t, e)

	sha1, err := e.ExportMeta()
	if err != nil {
		t.Fatalf("ExportMeta 1: %v", err)
	}

	// Mutate the catalogue: seal a new change on main.
	main, _ := e.LineByName("main")
	ch := openChange(t, e, main.ID)
	if _, _, err := e.SnapshotWorking(ch, map[string]TreeEntry{"new.txt": blobEntry(t, e, "new\n")}); err != nil {
		t.Fatalf("SnapshotWorking: %v", err)
	}
	if _, _, err := e.Seal(ch, "more work"); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	sha2, err := e.ExportMeta()
	if err != nil {
		t.Fatalf("ExportMeta 2: %v", err)
	}
	if sha1 == sha2 {
		t.Fatalf("ExportMeta unchanged after catalogue mutation: %s", sha1)
	}
}
