package diff3

import "bytes"

// HasMarkers reports whether data still contains an (unresolved) diff3
// conflict block as emitted by Merge: a line-anchored "<<<<<<< " opener,
// followed in order by a "=======" separator line and a ">>>>>>> " closer.
// The three parts must appear in sequence for a hit, so ordinary text that
// merely mentions one marker (docs, test fixtures) does not trip it. It walks
// the buffer line-by-line in place (no per-line allocation), so it is safe on
// arbitrarily large / line-dense input.
func HasMarkers(data []byte) bool {
	const (
		wantOpen = iota
		wantSep
		wantClose
	)
	state := wantOpen
	for len(data) > 0 {
		line := data
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			line, data = data[:i], data[i+1:]
		} else {
			data = nil
		}
		line = bytes.TrimSuffix(line, []byte("\r"))
		switch state {
		case wantOpen:
			if bytes.HasPrefix(line, []byte("<<<<<<< ")) {
				state = wantSep
			}
		case wantSep:
			if bytes.Equal(line, []byte("=======")) {
				state = wantClose
			}
		case wantClose:
			if bytes.HasPrefix(line, []byte(">>>>>>> ")) {
				return true
			}
		}
	}
	return false
}
