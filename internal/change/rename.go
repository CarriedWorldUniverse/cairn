package change

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// Rename detection, as git's diffcore-rename does it with its defaults: a
// deleted file and an added file are a rename when they are the same blob, or
// when at least half of the new one is the old one (the span-hash score of
// similarity.go). A file is the source of at most one rename.

// MaxRenameScore is 100% on the scale scores use.
const MaxRenameScore = maxScore

// DefaultRenameScore is git's -M default: 50%.
const DefaultRenameScore = renameScore

// renameLimit bounds the inexact search the way git's diff.renameLimit does:
// more than this many sources times destinations squared and only identical
// files are paired.
const renameLimit = 1000

// DetectRenames pairs the deleted and added files in changes. minScore is the
// least similarity (0..MaxRenameScore) that makes a rename. Each pair becomes
// one TreeChange with OldPath and Score set, in the place of its destination.
func (e *Engine) DetectRenames(changes []TreeChange, minScore int) ([]TreeChange, error) {
	var srcs, dsts []int
	for i, c := range changes {
		if c.OldPath != "" || !renameable(c) {
			continue
		}
		switch c.Status() {
		case 'D':
			srcs = append(srcs, i)
		case 'A':
			dsts = append(dsts, i)
		}
	}
	if len(srcs) == 0 || len(dsts) == 0 {
		return changes, nil
	}
	srcUsed := map[int]bool{}
	dstSrc := map[int]int{} // dst index -> src index
	score := map[int]int{}

	// Exact renames: the same blob (and kind of file).
	bySHA := map[string][]int{}
	for _, s := range srcs {
		bySHA[changes[s].Old.SHA] = append(bySHA[changes[s].Old.SHA], s)
	}
	for _, d := range dsts {
		best, bestName := -1, -1
		for _, s := range bySHA[changes[d].New.SHA] {
			if srcUsed[s] || modeKind(changes[s].Old.Mode) != modeKind(changes[d].New.Mode) {
				continue
			}
			n := 0
			if path.Base(changes[s].Path) == path.Base(changes[d].Path) {
				n = 1
			}
			if n > bestName {
				best, bestName = s, n
			}
		}
		if best >= 0 {
			srcUsed[best] = true
			dstSrc[d], score[d] = best, MaxRenameScore
		}
	}

	// Inexact: every remaining pair scored, best first.
	var remS, remD []int
	for _, s := range srcs {
		if !srcUsed[s] {
			remS = append(remS, s)
		}
	}
	for _, d := range dsts {
		if _, ok := dstSrc[d]; !ok {
			remD = append(remD, d)
		}
	}
	if minScore < MaxRenameScore && len(remS) > 0 && len(remD) > 0 && len(remS)*len(remD) <= renameLimit*renameLimit {
		type cand struct{ s, d, score, name int }
		var cands []cand
		type blob struct {
			data   []byte
			binary bool
			hashes [2]map[uint32]int // by text flag, computed on demand
		}
		blobs := map[string]*blob{}
		load := func(te TreeEntry) (*blob, error) {
			if b, ok := blobs[te.SHA]; ok {
				return b, nil
			}
			data, err := e.readBlob(te.SHA)
			if err != nil {
				return nil, err
			}
			b := &blob{data: data, binary: isBinary(data)}
			blobs[te.SHA] = b
			return b, nil
		}
		spans := func(b *blob, text bool) map[uint32]int {
			i := 0
			if text {
				i = 1
			}
			if b.hashes[i] == nil {
				b.hashes[i] = spanHashes(b.data, text)
			}
			return b.hashes[i]
		}
		for _, d := range remD {
			db, err := load(changes[d].New)
			if err != nil {
				return nil, fmt.Errorf("change.DetectRenames: %w", err)
			}
			for _, s := range remS {
				if modeKind(changes[s].Old.Mode) != modeKind(changes[d].New.Mode) {
					continue
				}
				sb, err := load(changes[s].Old)
				if err != nil {
					return nil, fmt.Errorf("change.DetectRenames: %w", err)
				}
				// git does not weigh files whose sizes alone rule a rename out.
				big, small := len(sb.data), len(db.data)
				if small > big {
					big, small = small, big
				}
				if big == 0 || big*(maxScore-renameScore) < (big-small)*maxScore {
					continue
				}
				text := !sb.binary && !db.binary
				sc := similarityHashed(spans(sb, text), spans(db, text), big)
				if sc < minScore {
					continue
				}
				n := 0
				if path.Base(changes[s].Path) == path.Base(changes[d].Path) {
					n = 1
				}
				cands = append(cands, cand{s, d, sc, n})
			}
		}
		sort.SliceStable(cands, func(i, j int) bool {
			if cands[i].score != cands[j].score {
				return cands[i].score > cands[j].score
			}
			return cands[i].name > cands[j].name
		})
		for _, c := range cands {
			if srcUsed[c.s] {
				continue
			}
			if _, done := dstSrc[c.d]; done {
				continue
			}
			srcUsed[c.s] = true
			dstSrc[c.d], score[c.d] = c.s, c.score
		}
	}
	if len(dstSrc) == 0 {
		return changes, nil
	}
	out := make([]TreeChange, 0, len(changes))
	for i, c := range changes {
		switch {
		case srcUsed[i] && c.Status() == 'D':
			// folded into its destination
		default:
			if s, ok := dstSrc[i]; ok {
				c = TreeChange{Path: c.Path, OldPath: changes[s].Path, Old: changes[s].Old, New: c.New, Score: score[i]}
			}
			out = append(out, c)
		}
	}
	return out, nil
}

// renameable: only whole files take part; a gitlink is never renamed and an
// empty file matches too much to mean anything.
func renameable(c TreeChange) bool {
	te := c.New
	if c.Status() == 'D' {
		te = c.Old
	}
	return te.Mode != ModeGitlink && te.SHA != emptyBlobSHA
}

const emptyBlobSHA = "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"

// RenameName is how git writes a rename in --stat, --numstat and --summary:
// "a => b", with the common leading directories and trailing path pulled out,
// as in "src/{old => new}/file".
func RenameName(a, b string) string {
	if QuotePath(a) != a || QuotePath(b) != b {
		return QuotePath(a) + " => " + QuotePath(b)
	}
	pfx := 0
	for i := 0; i < len(a) && i < len(b) && a[i] == b[i]; i++ {
		if a[i] == '/' {
			pfx = i + 1
		}
	}
	adjust := 0
	if pfx > 0 {
		adjust = 1
	}
	sfx := 0
	for i, j := len(a), len(b); pfx-adjust <= i && pfx-adjust <= j && i >= 0 && j >= 0; i, j = i-1, j-1 {
		var ca, cb byte
		if i < len(a) {
			ca = a[i]
		}
		if j < len(b) {
			cb = b[j]
		}
		if ca != cb {
			break
		}
		if ca == '/' {
			sfx = len(a) - i
		}
	}
	amid, bmid := max(len(a)-pfx-sfx, 0), max(len(b)-pfx-sfx, 0)
	var sb strings.Builder
	if pfx+sfx > 0 {
		sb.WriteString(a[:pfx] + "{")
	}
	sb.WriteString(a[pfx:pfx+amid] + " => " + b[pfx:pfx+bmid])
	if pfx+sfx > 0 {
		sb.WriteString("}" + a[len(a)-sfx:])
	}
	return sb.String()
}
