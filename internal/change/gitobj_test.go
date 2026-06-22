package change

import (
	"reflect"
	"testing"
)

func TestWriteReadTreeRoundTrip(t *testing.T) {
	e := newTestEngine(t)
	files := map[string][]byte{
		"a.txt":       []byte("alpha\n"),
		"dir/b.txt":   []byte("beta\n"),
		"dir/c/d.txt": []byte("delta\n"),
	}
	h, err := e.writeTree(files)
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
