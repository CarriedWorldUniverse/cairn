// Package diff3 implements a pure, line-level three-way merge (diff3).
//
// It depends only on the standard library and github.com/pmezard/go-difflib.
// It performs no I/O and has no knowledge of git. Callers pass three slices of
// lines (each line keeps its trailing "\n", as produced by
// strings.SplitAfter(s, "\n")) and receive the merged lines plus a flag
// indicating whether any region conflicted. Conflicting regions are emitted
// inline with git-style diff3 conflict markers.
package diff3

import (
	"github.com/pmezard/go-difflib/difflib"
)

// Result is the outcome of a three-way merge.
type Result struct {
	// Merged is the merged sequence of lines. Conflicting regions are
	// included inline wrapped in git-style diff3 conflict markers.
	Merged []string
	// Conflict reports whether any region conflicted.
	Conflict bool
}

// region describes a single change one side made relative to base. It replaces
// base[Start:End] with Repl. Pure insertions have Start == End.
type region struct {
	Start, End int
	Repl       []string
}

// changedRegions returns the segments of base that side altered, expressed as
// regions over base indices. Unchanged ('e') opcodes are skipped.
func changedRegions(base, side []string) []region {
	m := difflib.NewMatcher(base, side)
	var regs []region
	for _, op := range m.GetOpCodes() {
		if op.Tag == 'e' {
			continue
		}
		regs = append(regs, region{
			Start: op.I1,
			End:   op.I2,
			Repl:  side[op.J1:op.J2],
		})
	}
	return regs
}

// regionJoins reports whether r belongs in the current chunk spanning
// base[next:chunkEnd]. When the chunk is still empty (chunkEnd == next) the
// region that starts there seeds it. Once the chunk has content, a region joins
// only if its base range overlaps (Start < chunkEnd); a region that merely
// abuts the chunk end starts a new chunk so disjoint edits merge cleanly.
func regionJoins(r region, next, chunkEnd int) bool {
	if r.Start > chunkEnd {
		return false
	}
	if chunkEnd == next {
		// Empty chunk span: admit the region that begins it.
		return r.Start == next
	}
	return r.Start < chunkEnd
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Merge3 performs a three-way merge of ours and theirs against the common
// ancestor base. Non-overlapping changes are combined cleanly; changes to the
// same base region are taken once if identical, otherwise emitted as a conflict
// with diff3 markers and Conflict set to true.
func Merge3(base, ours, theirs []string) Result {
	oursRegs := changedRegions(base, ours)
	theirsRegs := changedRegions(base, theirs)

	var merged []string
	conflict := false

	oi, ti := 0, 0 // indices into oursRegs / theirsRegs
	b := 0         // current position in base

	for b < len(base) || oi < len(oursRegs) || ti < len(theirsRegs) {
		// Find the next region (by base start) on each side that we have not
		// yet consumed and that starts at or after b.
		nextO := -1
		if oi < len(oursRegs) {
			nextO = oursRegs[oi].Start
		}
		nextT := -1
		if ti < len(theirsRegs) {
			nextT = theirsRegs[ti].Start
		}

		// Determine the start of the next change chunk.
		next := -1
		switch {
		case nextO >= 0 && nextT >= 0:
			if nextO < nextT {
				next = nextO
			} else {
				next = nextT
			}
		case nextO >= 0:
			next = nextO
		case nextT >= 0:
			next = nextT
		}

		if next < 0 {
			// No more changes: copy the rest of base verbatim.
			merged = append(merged, base[b:]...)
			b = len(base)
			break
		}

		// Emit untouched base lines up to the start of the chunk.
		if next > b {
			merged = append(merged, base[b:next]...)
			b = next
		}

		// Gather all regions from both sides that overlap this chunk. A chunk
		// grows to include any region that starts before the current chunk end,
		// so adjacent/overlapping edits from either side resolve together.
		chunkEnd := next
		var chunkOurs, chunkTheirs []region

		// A region is pulled into the chunk if its base range overlaps the
		// chunk so far (Start < chunkEnd). The very first region of the chunk
		// (when chunkEnd == next, i.e. an empty span) is admitted with <= so
		// the chunk is seeded. Adjacent-but-disjoint edits (Start == chunkEnd
		// after the chunk has content) stay in separate chunks and merge
		// cleanly.
		for {
			grew := false
			if oi < len(oursRegs) && regionJoins(oursRegs[oi], next, chunkEnd) {
				if oursRegs[oi].End > chunkEnd {
					chunkEnd = oursRegs[oi].End
				}
				chunkOurs = append(chunkOurs, oursRegs[oi])
				oi++
				grew = true
			}
			if ti < len(theirsRegs) && regionJoins(theirsRegs[ti], next, chunkEnd) {
				if theirsRegs[ti].End > chunkEnd {
					chunkEnd = theirsRegs[ti].End
				}
				chunkTheirs = append(chunkTheirs, theirsRegs[ti])
				ti++
				grew = true
			}
			if !grew {
				break
			}
		}

		// Build the effective replacement of base[next:chunkEnd] for each side.
		// A side that did not touch part of the chunk keeps the corresponding
		// base lines.
		oursText := applyRegions(base, next, chunkEnd, chunkOurs)
		theirsText := applyRegions(base, next, chunkEnd, chunkTheirs)
		baseText := base[next:chunkEnd]

		switch {
		case len(chunkOurs) == 0:
			// Only theirs changed this chunk.
			merged = append(merged, theirsText...)
		case len(chunkTheirs) == 0:
			// Only ours changed this chunk.
			merged = append(merged, oursText...)
		case equalLines(oursText, theirsText):
			// Both sides made the identical change; take it once.
			merged = append(merged, oursText...)
		default:
			// True conflict: emit diff3 markers.
			conflict = true
			merged = append(merged, "<<<<<<< ours\n")
			merged = append(merged, oursText...)
			merged = append(merged, "||||||| base\n")
			merged = append(merged, baseText...)
			merged = append(merged, "=======\n")
			merged = append(merged, theirsText...)
			merged = append(merged, ">>>>>>> theirs\n")
		}

		b = chunkEnd
	}

	return Result{Merged: merged, Conflict: conflict}
}

// applyRegions produces the side's content for base[start:end] by applying the
// given regions (which all fall within [start,end]) over the base lines,
// preserving any base lines the side left untouched.
func applyRegions(base []string, start, end int, regs []region) []string {
	var out []string
	pos := start
	for _, r := range regs {
		if r.Start > pos {
			out = append(out, base[pos:r.Start]...)
		}
		out = append(out, r.Repl...)
		pos = r.End
	}
	if pos < end {
		out = append(out, base[pos:end]...)
	}
	return out
}
