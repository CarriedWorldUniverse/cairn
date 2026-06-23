package change

import (
	"strings"
	"testing"
)

func TestDiffTrees(t *testing.T) {
	old := map[string][]byte{
		"a":   []byte("1\n"),
		"b":   []byte("x\n"),
		"gone": []byte("bye\n"),
	}
	new := map[string][]byte{
		"a": []byte("1\n"),
		"b": []byte("y\n"),
		"c": []byte("new\n"),
	}

	diffs := DiffTrees(old, new, "old", "new")

	// Expect b Modified, c Added, gone Deleted; a (equal) skipped. Sorted by path.
	byPath := map[string]FileDiff{}
	var order []string
	for _, d := range diffs {
		byPath[d.Path] = d
		order = append(order, d.Path)
	}
	if _, ok := byPath["a"]; ok {
		t.Fatalf("equal file a should be skipped, got %+v", byPath["a"])
	}
	want := []string{"b", "c", "gone"}
	if len(order) != len(want) {
		t.Fatalf("paths = %v, want %v", order, want)
	}
	for i, p := range want {
		if order[i] != p {
			t.Fatalf("paths not sorted: %v, want %v", order, want)
		}
	}

	if b := byPath["b"]; b.Status != Modified {
		t.Fatalf("b status = %v, want Modified", b.Status)
	}
	if b := byPath["b"]; !strings.Contains(b.Unified, "-x") || !strings.Contains(b.Unified, "+y") {
		t.Fatalf("b unified missing -x/+y: %q", b.Unified)
	}
	if c := byPath["c"]; c.Status != Added {
		t.Fatalf("c status = %v, want Added", c.Status)
	}
	if g := byPath["gone"]; g.Status != Deleted {
		t.Fatalf("gone status = %v, want Deleted", g.Status)
	}
}

func TestDiffTreesBinary(t *testing.T) {
	old := map[string][]byte{"bin": {0x00, 0x01, 0x02}}
	new := map[string][]byte{"bin": {0x00, 0x03, 0x04}}
	diffs := DiffTrees(old, new, "old", "new")
	if len(diffs) != 1 {
		t.Fatalf("want 1 diff, got %d", len(diffs))
	}
	d := diffs[0]
	if d.Status != Modified {
		t.Fatalf("status = %v, want Modified", d.Status)
	}
	if !d.Binary {
		t.Fatalf("want Binary=true for NUL-containing file")
	}
	if d.Unified != "" {
		t.Fatalf("want empty Unified for binary, got %q", d.Unified)
	}
}

func TestFileStatusString(t *testing.T) {
	cases := map[FileStatus]string{Added: "added", Modified: "modified", Deleted: "deleted", FileStatus(99): "unknown"}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Fatalf("%d.String() = %q, want %q", int(s), got, want)
		}
	}
}
