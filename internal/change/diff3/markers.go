package diff3

import (
	"bytes"
	"fmt"
)

// MarkerError names the first conflict-marker line left in a file.
type MarkerError struct {
	Line   int    // 1-based
	Marker string // the marker as written, e.g. ">>>>>>> theirs"
}

func (e *MarkerError) Error() string {
	return fmt.Sprintf("conflict marker %q at line %d", e.Marker, e.Line)
}

// CheckMarkers reports the first conflict-marker line in data, or nil when
// there is none. A resolved file must contain NO marker line at all: the
// line-anchored forms are "<<<<<<< " (ours), "||||||| " (base), "=======" (the
// separator, exactly) and ">>>>>>> " (theirs), as Merge writes them.
//
// This deliberately does not look for a well-formed block. The earlier check
// did — opener, then separator, then closer, in order — so that prose merely
// mentioning a marker would not trip it; but that accepted every PARTIALLY
// resolved file: delete just the "<<<<<<< ours" line and the remaining
// "||||||| base", "=======" and ">>>>>>> theirs" sailed through and were
// sealed as the resolution. A stray marker is an unresolved fragment by
// definition, so every one is reported. Legitimate content that happens to
// start a line with a marker (a Markdown setext underline of exactly seven
// '=', a fixture) is what Resolve's --force is for. Markers not at the start
// of a line are ordinary text.
//
// Walks the buffer line by line in place, so it is safe on large input.
func CheckMarkers(data []byte) error {
	lineNo := 0
	for len(data) > 0 {
		lineNo++
		line := data
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			line, data = data[:i], data[i+1:]
		} else {
			data = nil
		}
		line = bytes.TrimSuffix(line, []byte("\r"))
		switch {
		case bytes.HasPrefix(line, []byte("<<<<<<< ")),
			bytes.HasPrefix(line, []byte("||||||| ")),
			bytes.Equal(line, []byte("=======")),
			bytes.HasPrefix(line, []byte(">>>>>>> ")):
			return &MarkerError{Line: lineNo, Marker: string(line)}
		}
	}
	return nil
}

// HasMarkers reports whether data contains any conflict-marker line.
func HasMarkers(data []byte) bool { return CheckMarkers(data) != nil }
