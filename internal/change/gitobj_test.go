package change

import (
	"reflect"
	"strings"
	"testing"
)

func TestWriteReadTreeRoundTrip(t *testing.T) {
	e := newTestEngine(t)
	files := map[string][]byte{
		"a.txt":       []byte("alpha\n"),
		"dir/b.txt":   []byte("beta\n"),
		"dir/c/d.txt": []byte("delta\n"),
	}
	h, err := e.writeTree(files, nil)
	if err != nil {
		t.Fatalf("writeTree: %v", err)
	}
	got, err := e.readTree(h.String())
	if err != nil {
		t.Fatalf("readTree: %v", err)
	}
	if !reflect.DeepEqual(got, files) {
		t.Fatalf("round-trip mismatch:\n got %v\nwant %v", got, files)
	}
}

func TestWriteTreeRejectsFileDirCollision(t *testing.T) {
	e := newTestEngine(t)
	_, err := e.writeTree(map[string][]byte{"x": []byte("file\n"), "x/sub": []byte("sub\n")}, nil)
	if err == nil {
		t.Fatal("expected error for file/dir name collision")
	}
	// E4: the error must use the friendlier "exists as both a file and a directory" wording.
	if !strings.Contains(err.Error(), "exists as both a file and a directory") {
		t.Fatalf("collision error %q should contain %q", err.Error(), "exists as both a file and a directory")
	}
}

func TestWriteTreeRejectsSlashPrefix(t *testing.T) {
	e := newTestEngine(t)
	if _, err := e.writeTree(map[string][]byte{"/bad.txt": []byte("x")}, nil); err == nil {
		t.Fatal("expected error for path beginning with /")
	}
}

func TestWriteReadEmptyTree(t *testing.T) {
	e := newTestEngine(t)
	h, err := e.writeTree(map[string][]byte{}, nil)
	if err != nil {
		t.Fatalf("writeTree empty: %v", err)
	}
	const emptyTreeSHA = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	if h.String() != emptyTreeSHA {
		t.Fatalf("empty tree hash = %s, want %s", h.String(), emptyTreeSHA)
	}
	got, err := e.readTree(h.String())
	if err != nil {
		t.Fatalf("readTree: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty map, got %v", got)
	}
}
