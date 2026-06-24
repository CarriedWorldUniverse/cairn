package change

import "testing"

// TestWriteTreeRefsMatchesWriteTree proves writeTreeRefs (which references
// pre-stored blob SHAs) produces the byte-identical tree hash as the
// writeBlob+buildTree path for the same logical contents, across all modes.
func TestWriteTreeRefsMatchesWriteTree(t *testing.T) {
	e := newTestEngine(t)

	files := map[string][]byte{
		"a.txt":       []byte("alpha\n"),
		"bin/run":     []byte("#!/bin/sh\n"),
		"dir/b.txt":   []byte("beta\n"),
		"dir/c/d.txt": []byte("delta\n"),
		"link":        []byte("a.txt"),
	}
	modes := map[string]EntryMode{
		"bin/run": ModeExecutable,
		"link":    ModeSymlink,
	}

	// Path A: the existing writeTree (writes blobs, builds tree).
	wantHash, err := e.writeTree(files, modes)
	if err != nil {
		t.Fatalf("writeTree: %v", err)
	}

	// Path B: pre-store each blob via WriteBlob, then build via writeTreeRefs.
	entries := map[string]TreeEntry{}
	for path, data := range files {
		sha, err := e.WriteBlob(data)
		if err != nil {
			t.Fatalf("WriteBlob %q: %v", path, err)
		}
		mode := ModeRegular
		if m, ok := modes[path]; ok {
			mode = m
		}
		entries[path] = TreeEntry{SHA: sha, Mode: mode}
	}
	gotHash, err := e.writeTreeRefs(entries)
	if err != nil {
		t.Fatalf("writeTreeRefs: %v", err)
	}

	if gotHash != wantHash {
		t.Fatalf("tree hash mismatch: writeTreeRefs=%s writeTree=%s", gotHash, wantHash)
	}
}

// TestWriteTreeRefsRejectsFileDirCollision mirrors buildTree's collision guard.
func TestWriteTreeRefsRejectsFileDirCollision(t *testing.T) {
	e := newTestEngine(t)
	sha, err := e.WriteBlob([]byte("x"))
	if err != nil {
		t.Fatalf("WriteBlob: %v", err)
	}
	_, err = e.writeTreeRefs(map[string]TreeEntry{
		"x":     {SHA: sha, Mode: ModeRegular},
		"x/sub": {SHA: sha, Mode: ModeRegular},
	})
	if err == nil {
		t.Fatal("expected file/dir collision error")
	}
}
