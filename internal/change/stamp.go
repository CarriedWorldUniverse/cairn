package change

import "time"

// stampFormat is RFC3339 with a FIXED-WIDTH nanosecond field.
//
// time.RFC3339Nano trims trailing zeros from the fractional seconds, which
// destroys the one property every stored timestamp here is relied on for:
// that lexicographic order equals chronological order. A trimmed fraction ends
// in 'Z' (0x5A), and every digit sorts below it, so ".9Z" compares ABOVE
// ".925Z" even though .9 precedes .925 (#157). Padding to a constant nine
// digits makes the comparison correct by construction.
const stampFormat = "2006-01-02T15:04:05.000000000Z07:00"

// stamp renders t as a UTC, fixed-width, lexicographically sortable timestamp.
// Every timestamp cairn stores and later compares with ORDER BY, MAX() or a
// range predicate must go through here rather than time.RFC3339Nano.
func stamp(t time.Time) string { return t.UTC().Format(stampFormat) }
