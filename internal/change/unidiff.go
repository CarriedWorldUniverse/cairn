package change

import (
	"strconv"
	"strings"
)

// unifiedDiff renders the unified diff of two files given as lines (each
// keeping its "\n"), with the given header names and lines of context.
//
// It produces, byte for byte, what go-difflib's GetUnifiedDiffString
// produces — the same matching blocks from the same algorithm (Python's
// difflib: the longest contiguous match, earliest in a then earliest in b,
// then the same again to its left and right; lines occurring in more than 1%
// of a 200+ line file are not used to SEED a match), the same grouping, the
// same text. unidiff_test.go holds it to that against go-difflib itself on
// generated inputs. A cairn diff is therefore exactly the diff it was.
//
// What differs is the cost. go-difflib's inner loop allocates a fresh
// map[int]int for every line of the old file, in every recursive call; a
// commit touching one 20,000-line file spent over a second there, which was
// nearly all of `cairn show`. Here lines are interned to integers once and
// the two rows of the match-length table are slices, cleared by walking the
// few entries the last row set.
func unifiedDiff(a, b []string, fromFile, toFile string, context int) string {
	m := newLineMatcher(a, b)
	groups := m.groupedOpCodes(context)
	if len(groups) == 0 {
		return ""
	}
	var sb strings.Builder
	if fromFile != "" || toFile != "" {
		sb.WriteString("--- " + fromFile + "\n")
		sb.WriteString("+++ " + toFile + "\n")
	}
	for _, g := range groups {
		first, last := g[0], g[len(g)-1]
		sb.WriteString("@@ -" + formatRangeUnified(first.i1, last.i2) + " +" + formatRangeUnified(first.j1, last.j2) + " @@\n")
		for _, c := range g {
			if c.tag == 'e' {
				for _, line := range a[c.i1:c.i2] {
					sb.WriteByte(' ')
					sb.WriteString(line)
				}
				continue
			}
			if c.tag == 'r' || c.tag == 'd' {
				for _, line := range a[c.i1:c.i2] {
					sb.WriteByte('-')
					sb.WriteString(line)
				}
			}
			if c.tag == 'r' || c.tag == 'i' {
				for _, line := range b[c.j1:c.j2] {
					sb.WriteByte('+')
					sb.WriteString(line)
				}
			}
		}
	}
	return sb.String()
}

// formatRangeUnified renders a hunk range the way the unified format wants
// it: 1-based, the length omitted when it is 1, and an empty range placed at
// the line before it.
func formatRangeUnified(start, stop int) string {
	beginning := start + 1
	length := stop - start
	if length == 1 {
		return strconv.Itoa(beginning)
	}
	if length == 0 {
		beginning--
	}
	return strconv.Itoa(beginning) + "," + strconv.Itoa(length)
}

// lineMatch is a[a:a+size] == b[b:b+size].
type lineMatch struct{ a, b, size int }

// opCode says how to turn a[i1:i2] into b[j1:j2]: 'r'eplace, 'd'elete,
// 'i'nsert or 'e'qual.
type opCode struct {
	tag            byte
	i1, i2, j1, j2 int
}

// lineMatcher is difflib's SequenceMatcher over interned lines, with no junk
// function and autojunk on (what go-difflib's NewMatcher gives).
type lineMatcher struct {
	a, b []int32   // line ids; a line of a that is nowhere in b is -1
	b2j  [][]int32 // id → ascending positions in b; nil for a popular line
	// row and next are two rows of the match-length table, indexed by j+1:
	// row[j+1] is the length of the match ending at a[i-1], b[j]. Only the
	// positions listed in rowSet/nextSet are non-zero.
	row, next       []int32
	rowSet, nextSet []int32
}

func newLineMatcher(a, b []string) *lineMatcher {
	ia, ib := make([]int32, len(a)), make([]int32, len(b))
	ids := make(map[string]int32, len(b))
	for j, s := range b {
		id, ok := ids[s]
		if !ok {
			id = int32(len(ids))
			ids[s] = id
		}
		ib[j] = id
	}
	for i, s := range a {
		if id, ok := ids[s]; ok {
			ia[i] = id
		} else {
			ia[i] = -1
		}
	}
	return newLineMatcherIDs(ia, ib, len(ids))
}

// newLineMatcherIDs builds a matcher over lines already reduced to ids: b's
// are 0..n-1, and a line of a is either one of those or -1 (not in b).
func newLineMatcherIDs(a, b []int32, n int) *lineMatcher {
	m := &lineMatcher{a: a, b: b, b2j: make([][]int32, n)}
	for j, id := range b {
		m.b2j[id] = append(m.b2j[id], int32(j))
	}
	// Purge popular lines: they still match (the extension step below
	// compares lines directly), they just never start a match.
	if n := len(b); n >= 200 {
		ntest := n/100 + 1
		for id, indices := range m.b2j {
			if len(indices) > ntest {
				m.b2j[id] = nil
			}
		}
	}
	m.row = make([]int32, len(b)+1)
	m.next = make([]int32, len(b)+1)
	return m
}

// findLongestMatch returns the longest block common to a[alo:ahi] and
// b[blo:bhi]; of several, the one starting earliest in a, then earliest in
// b. With none, (alo, blo, 0).
func (m *lineMatcher) findLongestMatch(alo, ahi, blo, bhi int) lineMatch {
	besti, bestj, bestsize := alo, blo, 0
	for i := alo; i != ahi; i++ {
		if id := m.a[i]; id >= 0 {
			indices := m.b2j[id]
			// First occurrence at or after blo.
			lo, hi := 0, len(indices)
			for lo < hi {
				mid := int(uint(lo+hi) >> 1)
				if int(indices[mid]) < blo {
					lo = mid + 1
				} else {
					hi = mid
				}
			}
			for _, j32 := range indices[lo:] {
				j := int(j32)
				if j >= bhi {
					break
				}
				k := int(m.row[j]) + 1 // row[(j-1)+1]
				m.next[j+1] = int32(k)
				m.nextSet = append(m.nextSet, j32)
				if k > bestsize {
					besti, bestj, bestsize = i-k+1, j-k+1, k
				}
			}
		}
		for _, j := range m.rowSet {
			m.row[j+1] = 0
		}
		m.row, m.next = m.next, m.row
		m.rowSet, m.nextSet = m.nextSet, m.rowSet[:0]
	}
	for _, j := range m.rowSet {
		m.row[j+1] = 0
	}
	m.rowSet = m.rowSet[:0]

	// Extend the match over equal lines on each end. Popular lines are not
	// in b2j, so the block found above contains none; this is where they
	// join it.
	for besti > alo && bestj > blo && m.a[besti-1] == m.b[bestj-1] {
		besti, bestj, bestsize = besti-1, bestj-1, bestsize+1
	}
	for besti+bestsize < ahi && bestj+bestsize < bhi && m.a[besti+bestsize] == m.b[bestj+bestsize] {
		bestsize++
	}
	return lineMatch{besti, bestj, bestsize}
}

// matchingBlocks returns the matching blocks in order, adjacent ones merged,
// ending with the sentinel (len(a), len(b), 0).
func (m *lineMatcher) matchingBlocks() []lineMatch {
	var matched []lineMatch
	var rec func(alo, ahi, blo, bhi int)
	rec = func(alo, ahi, blo, bhi int) {
		match := m.findLongestMatch(alo, ahi, blo, bhi)
		i, j, k := match.a, match.b, match.size
		if k > 0 {
			if alo < i && blo < j {
				rec(alo, i, blo, j)
			}
			matched = append(matched, match)
			if i+k < ahi && j+k < bhi {
				rec(i+k, ahi, j+k, bhi)
			}
		}
	}
	rec(0, len(m.a), 0, len(m.b))

	out := make([]lineMatch, 0, len(matched)+1)
	i1, j1, k1 := 0, 0, 0
	for _, blk := range matched {
		if i1+k1 == blk.a && j1+k1 == blk.b {
			k1 += blk.size
			continue
		}
		if k1 > 0 {
			out = append(out, lineMatch{i1, j1, k1})
		}
		i1, j1, k1 = blk.a, blk.b, blk.size
	}
	if k1 > 0 {
		out = append(out, lineMatch{i1, j1, k1})
	}
	return append(out, lineMatch{len(m.a), len(m.b), 0})
}

// opCodes returns the edit script turning a into b.
func (m *lineMatcher) opCodes() []opCode {
	i, j := 0, 0
	blocks := m.matchingBlocks()
	codes := make([]opCode, 0, len(blocks))
	for _, blk := range blocks {
		var tag byte
		switch {
		case i < blk.a && j < blk.b:
			tag = 'r'
		case i < blk.a:
			tag = 'd'
		case j < blk.b:
			tag = 'i'
		}
		if tag > 0 {
			codes = append(codes, opCode{tag, i, blk.a, j, blk.b})
		}
		i, j = blk.a+blk.size, blk.b+blk.size
		if blk.size > 0 {
			codes = append(codes, opCode{'e', blk.a, i, blk.b, j})
		}
	}
	return codes
}

// groupedOpCodes splits the edit script into hunks with up to n lines of
// context, dropping the unchanged stretches between them.
func (m *lineMatcher) groupedOpCodes(n int) [][]opCode {
	if n < 0 {
		n = 3
	}
	codes := m.opCodes()
	if len(codes) == 0 {
		codes = []opCode{{'e', 0, 1, 0, 1}}
	}
	if c := codes[0]; c.tag == 'e' {
		codes[0] = opCode{c.tag, max(c.i1, c.i2-n), c.i2, max(c.j1, c.j2-n), c.j2}
	}
	if c := codes[len(codes)-1]; c.tag == 'e' {
		codes[len(codes)-1] = opCode{c.tag, c.i1, min(c.i2, c.i1+n), c.j1, min(c.j2, c.j1+n)}
	}
	nn := n + n
	var groups [][]opCode
	var group []opCode
	for _, c := range codes {
		i1, i2, j1, j2 := c.i1, c.i2, c.j1, c.j2
		// A long unchanged stretch ends one hunk and starts the next.
		if c.tag == 'e' && i2-i1 > nn {
			group = append(group, opCode{c.tag, i1, min(i2, i1+n), j1, min(j2, j1+n)})
			groups = append(groups, group)
			group = nil
			i1, j1 = max(i1, i2-n), max(j1, j2-n)
		}
		group = append(group, opCode{c.tag, i1, i2, j1, j2})
	}
	if len(group) > 0 && !(len(group) == 1 && group[0].tag == 'e') {
		groups = append(groups, group)
	}
	return groups
}
