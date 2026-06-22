package change

import (
	"bytes"
	"testing"
)

func TestFilesReturnsCommittedTree(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	want := map[string][]byte{
		"a.txt":     []byte("a\n"),
		"dir/b.txt": []byte("b\n"),
	}
	seedLineTip(t, e, main.ID, want)

	main2, _ := e.LineByName("main")
	got, err := e.Files(main2.TipCommit)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("Files returned %d entries, want %d: %v", len(got), len(want), got)
	}
	for path, content := range want {
		if !bytes.Equal(got[path], content) {
			t.Fatalf("Files[%q] = %q, want %q", path, got[path], content)
		}
	}
}
