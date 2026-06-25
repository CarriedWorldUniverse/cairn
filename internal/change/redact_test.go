package change

import (
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

func mkBlob(t *testing.T, e *Engine, s string) string {
	t.Helper()
	h, err := e.writeBlob([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return h.String()
}

// reachableBlobSHAs returns every blob SHA reachable from a tree (flat).
func reachableBlobSHAs(t *testing.T, e *Engine, treeHash string) map[string]bool {
	t.Helper()
	entries, err := e.readTreeRefs(treeHash)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, ent := range entries {
		out[ent.SHA] = true
	}
	return out
}

func TestReadTreeRefsRoundTrip(t *testing.T) {
	e := newTestEngine(t)
	in := map[string]TreeEntry{
		"README.md": {SHA: mkBlob(t, e, "readme"), Mode: ModeRegular},
		"bin/run":   {SHA: mkBlob(t, e, "#!/bin/sh\n"), Mode: ModeExecutable},
		"link":      {SHA: mkBlob(t, e, "target/path"), Mode: ModeSymlink},
		"a/b/c.txt": {SHA: mkBlob(t, e, "deep"), Mode: ModeRegular},
		"a/b/d.txt": {SHA: mkBlob(t, e, "sibling"), Mode: ModeRegular},
	}
	root, err := e.writeTreeRefs(in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.readTreeRefs(root.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(in) {
		t.Fatalf("readTreeRefs len = %d, want %d", len(got), len(in))
	}
	for path, want := range in {
		g, ok := got[path]
		if !ok {
			t.Fatalf("missing path %q", path)
		}
		if g.SHA != want.SHA || g.Mode != want.Mode {
			t.Fatalf("path %q = %+v, want %+v (mode must be preserved)", path, g, want)
		}
	}
	// Round-trip reproduces the identical tree hash.
	root2, err := e.writeTreeRefs(got)
	if err != nil {
		t.Fatal(err)
	}
	if root2 != root {
		t.Fatalf("round-trip tree hash %s != %s", root2, root)
	}
}

func TestRedactTreeOmitAndShapeOnly(t *testing.T) {
	e := newTestEngine(t)
	secretSHA := mkBlob(t, e, "SECRET=xyz")
	keySHA := mkBlob(t, e, "PRIVATE-KEY-DATA")
	appSHA := mkBlob(t, e, "package main")
	in := map[string]TreeEntry{
		"app/main.go":           {SHA: appSHA, Mode: ModeRegular},
		"secrets/deep/prod.env": {SHA: secretSHA, Mode: ModeRegular},
		"config/keys/id_rsa":    {SHA: keySHA, Mode: ModeRegular},
	}
	root, err := e.writeTreeRefs(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.MarkPrivate("secrets", PrivacyOmit); err != nil {
		t.Fatal(err)
	}
	if err := e.MarkPrivate("config/keys", PrivacyShapeOnly); err != nil {
		t.Fatal(err)
	}
	red, on, err := e.newRedactor()
	if err != nil || !on {
		t.Fatalf("newRedactor on=%v err=%v", on, err)
	}
	rt, err := red.redactTree(root.String())
	if err != nil {
		t.Fatal(err)
	}
	entries, err := e.readTreeRefs(rt)
	if err != nil {
		t.Fatal(err)
	}
	// omit: the secret path is entirely gone (and its now-empty folder vanished).
	if _, ok := entries["secrets/deep/prod.env"]; ok {
		t.Error("omit: secrets/deep/prod.env should be absent")
	}
	for p := range entries {
		if p == "secrets" || len(p) >= 8 && p[:8] == "secrets/" {
			t.Errorf("omit: residual secrets path %q", p)
		}
	}
	// shape-only: the key path survives but its bytes are the placeholder.
	ks, ok := entries["config/keys/id_rsa"]
	if !ok {
		t.Fatal("shape-only: config/keys/id_rsa should still exist")
	}
	wantPlaceholder, _ := e.writeBlob(privatePlaceholder)
	if ks.SHA != wantPlaceholder.String() {
		t.Errorf("shape-only: id_rsa content = %s, want placeholder %s", ks.SHA, wantPlaceholder.String())
	}
	// non-private file is byte-identical.
	if entries["app/main.go"].SHA != appSHA {
		t.Errorf("app/main.go changed: %s != %s", entries["app/main.go"].SHA, appSHA)
	}
	// The crux: no original private blob is reachable from the redacted tree.
	reach := reachableBlobSHAs(t, e, rt)
	if reach[secretSHA] {
		t.Error("LEAK: original secret blob reachable from redacted tree")
	}
	if reach[keySHA] {
		t.Error("LEAK: original key blob reachable from redacted tree")
	}
}

// TestProjectRedactsWholeLine builds a 3-commit line where commit 2 introduces a
// private file, then asserts the whole-graph projection redacts the right commits
// and leaks no private blob.
func TestProjectRedactsWholeLine(t *testing.T) {
	e := newTestEngine(t)
	e.SetIdentity("Dev", "dev@x.io")
	root, _ := e.LineByName("main")

	ch1, _ := e.CreateChange(root.ID, "dev")
	r1, err := e.Commit(ch1.ID, map[string][]byte{"app.go": []byte("v1\n")}, nil, "c1 clean")
	if err != nil {
		t.Fatal(err)
	}
	ch2, _ := e.CreateChange(root.ID, "dev")
	r2, err := e.Commit(ch2.ID, map[string][]byte{"app.go": []byte("v1\n"), "secrets/prod.env": []byte("SECRET=xyz\n")}, nil, "c2 adds secret")
	if err != nil {
		t.Fatal(err)
	}
	ch3, _ := e.CreateChange(root.ID, "dev")
	r3, err := e.Commit(ch3.ID, map[string][]byte{"app.go": []byte("v2\n"), "secrets/prod.env": []byte("SECRET=xyz\n")}, nil, "c3 edits app")
	if err != nil {
		t.Fatal(err)
	}

	if err := e.MarkPrivate("secrets", PrivacyOmit); err != nil {
		t.Fatal(err)
	}
	red, _, _ := e.newRedactor()
	mapping, err := red.project([]string{r3.HeadCommit})
	if err != nil {
		t.Fatal(err)
	}

	// c1 had no secret → unchanged (maps to itself).
	if mapping[r1.HeadCommit] != r1.HeadCommit {
		t.Errorf("c1 (clean) should be unchanged, got %s", mapping[r1.HeadCommit])
	}
	// c2 and c3 carried the secret → redacted to new SHAs.
	if mapping[r2.HeadCommit] == r2.HeadCommit || mapping[r2.HeadCommit] == "" {
		t.Error("c2 should be redacted")
	}
	if mapping[r3.HeadCommit] == r3.HeadCommit || mapping[r3.HeadCommit] == "" {
		t.Error("c3 should be redacted")
	}

	// The redacted tip: secret gone, app.go intact and byte-identical, parent rechained.
	redTip, _ := e.git.CommitObject(plumbing.NewHash(mapping[r3.HeadCommit]))
	entries, _ := e.readTreeRefs(redTip.TreeHash.String())
	if _, ok := entries["secrets/prod.env"]; ok {
		t.Error("LEAK: redacted tip still has secrets/prod.env")
	}
	if _, ok := entries["app.go"]; !ok {
		t.Error("redacted tip lost app.go")
	}
	// Walk the redacted history: NO commit's tree may reach the original secret blob.
	origSecret := mkBlob(t, e, "SECRET=xyz\n")
	for _, red := range []string{mapping[r2.HeadCommit], mapping[r3.HeadCommit]} {
		c, _ := e.git.CommitObject(plumbing.NewHash(red))
		if reachableBlobSHAs(t, e, c.TreeHash.String())[origSecret] {
			t.Errorf("LEAK: redacted commit %s reaches the original secret blob", red)
		}
	}
	// Redacted parent chain is intact: redacted c3's first parent is redacted c2.
	if redTip.ParentHashes[0].String() != mapping[r2.HeadCommit] {
		t.Errorf("redacted c3 parent = %s, want redacted c2 %s", redTip.ParentHashes[0], mapping[r2.HeadCommit])
	}
}
