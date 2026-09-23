package diff3

import (
	"errors"
	"testing"
)

// A resolved file contains no marker line at all. The old check looked for a
// complete ordered block and therefore accepted every partially resolved
// file — the cases below marked "stray" are exactly those it let through.
func TestCheckMarkers(t *testing.T) {
	cases := []struct {
		name   string
		data   string
		line   int    // 0 = clean
		marker string // expected marker text when line > 0
	}{
		{"empty", "", 0, ""},
		{"plain", "hello\nworld\n", 0, ""},
		{"full block", "<<<<<<< ours\nX\n||||||| base\nb\n=======\nY\n>>>>>>> theirs\n", 1, "<<<<<<< ours"},
		{"block amid content", "top\n<<<<<<< ours\nX\n=======\nY\n>>>>>>> theirs\nbottom\n", 2, "<<<<<<< ours"},
		{"crlf block", "<<<<<<< ours\r\nX\r\n=======\r\nY\r\n>>>>>>> theirs\r\n", 1, "<<<<<<< ours"},
		// The report: opener deleted, everything else left behind.
		{"stray: opener removed", "line1\nMAIN\n||||||| base\nline2\n=======\nFEAT\n>>>>>>> theirs\nline3\n", 3, "||||||| base"},
		{"stray: separator only", "a\n=======\nb\n", 2, "======="},
		{"stray: closer only", "a\n>>>>>>> theirs\n", 2, ">>>>>>> theirs"},
		{"stray: opener only", "<<<<<<< ours\njust talking about markers\n", 1, "<<<<<<< ours"},
		{"stray: base only", "x\n||||||| base\n", 2, "||||||| base"},
		{"stray: out of order", ">>>>>>> theirs\n=======\n<<<<<<< ours\n", 1, ">>>>>>> theirs"},
		{"no trailing newline", "X\n=======\nY", 2, "======="},
		// Not markers: not line-anchored, or not the exact form Merge writes.
		{"opener without space", "<<<<<<<ours\n", 0, ""},
		{"separator not exact", "========\n=== x\n", 0, ""},
		{"markers mid-line", "see <<<<<<< ours and ======= and >>>>>>> theirs\n", 0, ""},
		{"indented marker is text", "  <<<<<<< ours\n", 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckMarkers([]byte(c.data))
			if c.line == 0 {
				if err != nil {
					t.Fatalf("CheckMarkers(%q) = %v, want clean", c.data, err)
				}
				if HasMarkers([]byte(c.data)) {
					t.Fatalf("HasMarkers(%q) = true, want false", c.data)
				}
				return
			}
			var me *MarkerError
			if !errors.As(err, &me) {
				t.Fatalf("CheckMarkers(%q) = %v, want a MarkerError at line %d", c.data, err, c.line)
			}
			if me.Line != c.line || me.Marker != c.marker {
				t.Fatalf("CheckMarkers(%q) = line %d %q, want line %d %q", c.data, me.Line, me.Marker, c.line, c.marker)
			}
			if !HasMarkers([]byte(c.data)) {
				t.Fatalf("HasMarkers(%q) = false, want true", c.data)
			}
		})
	}
}
