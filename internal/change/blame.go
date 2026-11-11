package change

import (
	"bytes"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// BlameLine is one line's provenance.
type BlameLine struct {
	Commit   string
	ChangeID string // Change-Id trailer of Commit ("" if none)
	Author   string
	When     time.Time
	Text     string

	// What git's blame formats also show.
	AuthorEmail    string
	Committer      string
	CommitterEmail string
	CommitterWhen  time.Time
	Summary        string // first line of the message
	Path           string // the file's name in Commit (differs from the asked path across a rename)
	OrigLine       int    // 1-based line number in Commit's version of the file
	Boundary       bool   // Commit has no parent
	PrevCommit     string // first parent holding the file ("" for none)
	PrevPath       string
}

// BlameOptions tunes Blame.
type BlameOptions struct {
	IgnoreSpace bool // -w: lines equal when they differ only in whitespace
}

// Blame returns per-line provenance for path at commit.
//
// It is git's blame, on cairn's commit graph. Every line of the file starts
// out blamed on commit; a commit holding blame (a "suspect") hands to each
// parent the lines that parent already had, and keeps the rest. What a
// suspect still holds when it has no parent left to ask is its own.
//
// Three things make that cheap on a long history, all of them git's:
//
//   - A parent whose copy of the file is the SAME BLOB takes everything, with
//     no diff. And sameness is usually known without looking the file up at
//     all: the path is resolved one directory at a time, and the first
//     directory whose tree hash matches the child's ends the descent. Most
//     commits do not touch any given file, so most commits cost one small
//     tree read.
//   - Suspects are taken in generation order (graph.go), so a commit is
//     visited once, after everything that can pass blame to it has.
//   - The walk stops when no line is left unattributed.
//
// Like git, it follows a file across a rename: when a parent has no file at
// the path, a file that parent has and the commit removed — identical, or
// failing that at least half the same — is taken to be where it came from.
func (e *Engine) Blame(commit, path string) ([]BlameLine, error) {
	return e.BlameWith(commit, path, BlameOptions{})
}

// BlameWith is Blame with options.
func (e *Engine) BlameWith(commit, path string, opt BlameOptions) ([]BlameLine, error) {
	g := e.lockGraph()
	defer e.cgMu.Unlock()
	start, err := g.id(plumbing.NewHash(commit))
	if err != nil {
		return nil, fmt.Errorf("change.Blame: commit %s: %w", commit, err)
	}
	b := &blamer{e: e, g: g, lineIDs: map[string]int32{}, suspects: map[suspectKey]*suspect{}, ws: opt.IgnoreSpace, prev: map[suspectKey]suspectKey{}}
	parts := strings.Split(path, "/")
	chain, found, err := b.resolve(g.nodes[start].tree, parts, nil)
	if err != nil {
		return nil, fmt.Errorf("change.Blame: %q: %w", path, err)
	}
	if !found {
		return nil, fmt.Errorf("change.Blame: %q: %w", path, object.ErrFileNotFound)
	}
	data, err := e.rawObject(plumbing.BlobObject, chain[len(chain)-1])
	if err != nil {
		return nil, fmt.Errorf("change.Blame: %q: %w", path, err)
	}
	texts := splitLinesKeepNL(data)
	owner := make([]int32, len(texts))
	srcLine := make([]int32, len(texts))
	ownerPath := make([]string, len(texts))
	if len(texts) > 0 {
		first := b.suspect(start, path, parts, chain)
		first.entries = []blameEntry{{final: 0, src: 0, n: len(texts)}}
		if err := b.run(owner, srcLine, ownerPath); err != nil {
			return nil, fmt.Errorf("change.Blame: %q: %w", path, err)
		}
	}

	type meta struct {
		sha, changeID, author string
		when                  time.Time
		email, committer, cem string
		cwhen                 time.Time
		summary               string
		boundary              bool
	}
	metas := map[int32]meta{}
	out := make([]BlameLine, len(texts))
	for i, text := range texts {
		m, ok := metas[owner[i]]
		if !ok {
			m.sha = g.nodes[owner[i]].hash.String()
			c, err := e.git.CommitObject(g.nodes[owner[i]].hash)
			if err != nil {
				return nil, fmt.Errorf("change.Blame: commit %s: %w", m.sha, err)
			}
			m.changeID, m.author, m.when = parseChangeID(c.Message), c.Author.Name, c.Author.When
			m.email, m.committer, m.cem, m.cwhen = c.Author.Email, c.Committer.Name, c.Committer.Email, c.Committer.When
			m.summary, _, _ = strings.Cut(strings.TrimLeft(c.Message, "\n"), "\n")
			m.boundary = len(c.ParentHashes) == 0
			metas[owner[i]] = m
		}
		out[i] = BlameLine{
			Commit:   m.sha,
			ChangeID: m.changeID,
			Author:   m.author,
			When:     m.when,
			Text:     strings.TrimSuffix(string(text), "\n"),

			AuthorEmail: m.email, Committer: m.committer, CommitterEmail: m.cem, CommitterWhen: m.cwhen,
			Summary: m.summary, Path: ownerPath[i], OrigLine: int(srcLine[i]) + 1, Boundary: m.boundary,
		}
		if pk, ok := b.prev[suspectKey{owner[i], ownerPath[i]}]; ok {
			out[i].PrevCommit, out[i].PrevPath = g.nodes[pk.commit].hash.String(), pk.path
		}
	}
	return out, nil
}

// IsWorkingHead reports whether sha is the head of an open (un-sealed) change.
func (e *Engine) IsWorkingHead(sha string) (bool, error) { return e.isWorkingHead(sha) }

// splitLinesKeepNL splits data into lines, each keeping its "\n". A final
// line with no newline is a line; an empty file has none.
func splitLinesKeepNL(data []byte) [][]byte {
	var lines [][]byte
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			lines = append(lines, data)
			break
		}
		lines = append(lines, data[:i+1])
		data = data[i+1:]
	}
	return lines
}

// blameEntry says n lines of the blamed file starting at final are, in the
// suspect's version of the file, the n lines starting at src.
type blameEntry struct{ final, src, n int }

type suspectKey struct {
	commit int32
	path   string
}

// suspect is a commit's version of the file, and the lines currently blamed
// on it.
type suspect struct {
	commit  int32
	path    string
	parts   []string
	chain   []plumbing.Hash // the tree at each directory of the path, then the blob
	entries []blameEntry    // sorted by src, never overlapping
}

func (s *suspect) blob() plumbing.Hash { return s.chain[len(s.chain)-1] }

type blamer struct {
	e *Engine
	g *commitGraph

	ws   bool                      // -w
	prev map[suspectKey]suspectKey // (commit, path) → the first parent origin holding the file

	suspects map[suspectKey]*suspect
	queue    []*suspect // heap, highest generation first

	// Lines are interned across every version of the file, so comparing two
	// lines is comparing two integers and a version's lines are a []int32.
	lineIDs map[string]int32
	indents []int16                   // by line id: the line's indentation, -1 if blank
	files   map[plumbing.Hash][]int32 // blob → interned lines, recent versions only
	recent  []plumbing.Hash
}

// suspect returns the suspect for (commit, path), creating and queueing it
// the first time.
func (b *blamer) suspect(commit int32, path string, parts []string, chain []plumbing.Hash) *suspect {
	key := suspectKey{commit, path}
	if s, ok := b.suspects[key]; ok {
		return s
	}
	s := &suspect{commit: commit, path: path, parts: parts, chain: chain}
	b.suspects[key] = s
	b.queue = append(b.queue, s)
	for i := len(b.queue) - 1; i > 0; {
		parent := (i - 1) / 2
		if !b.before(b.queue[i], b.queue[parent]) {
			break
		}
		b.queue[i], b.queue[parent] = b.queue[parent], b.queue[i]
		i = parent
	}
	return s
}

// before orders the queue: higher generation first, so a suspect is popped
// only after every descendant that could hand it blame; then newest, then
// lowest hash, then path, so a run is deterministic.
func (b *blamer) before(x, y *suspect) bool {
	nx, ny := &b.g.nodes[x.commit], &b.g.nodes[y.commit]
	if nx.gen != ny.gen {
		return nx.gen > ny.gen
	}
	if nx.time != ny.time {
		return nx.time > ny.time
	}
	if c := bytes.Compare(nx.hash[:], ny.hash[:]); c != 0 {
		return c < 0
	}
	return x.path < y.path
}

func (b *blamer) pop() *suspect {
	top := b.queue[0]
	n := len(b.queue) - 1
	b.queue[0] = b.queue[n]
	b.queue[n] = nil
	b.queue = b.queue[:n]
	for i := 0; ; {
		l, r, min := 2*i+1, 2*i+2, i
		if l < n && b.before(b.queue[l], b.queue[min]) {
			min = l
		}
		if r < n && b.before(b.queue[r], b.queue[min]) {
			min = r
		}
		if min == i {
			break
		}
		b.queue[i], b.queue[min] = b.queue[min], b.queue[i]
		i = min
	}
	return top
}

// run drains the queue, recording in owner the commit each line ends up
// blamed on.
func (b *blamer) run(owner, srcLine []int32, ownerPath []string) error {
	for len(b.queue) > 0 {
		s := b.pop()
		delete(b.suspects, suspectKey{s.commit, s.path})
		if len(s.entries) == 0 {
			continue
		}
		if err := b.passToParents(s); err != nil {
			return err
		}
		for _, ent := range s.entries {
			for i := 0; i < ent.n; i++ {
				owner[ent.final+i] = s.commit
				srcLine[ent.final+i] = int32(ent.src + i)
				ownerPath[ent.final+i] = s.path
			}
		}
	}
	return nil
}

// origin is where a suspect's file is in one of its parents.
type origin struct {
	found bool
	path  string
	parts []string
	chain []plumbing.Hash
}

// passToParents hands s's lines to its parents, leaving in s.entries only
// the lines no parent had.
func (b *blamer) passToParents(s *suspect) error {
	parents := b.g.parentsOf(s.commit)
	if len(parents) == 0 {
		return nil
	}
	origins := make([]origin, len(parents))
	// First the same path in every parent, then — for the parents that have
	// nothing there — a rename. Either way, a parent holding the identical
	// blob takes all the blame at once: nothing changed between it and s.
	for pass := 0; pass < 2; pass++ {
		for i, p := range parents {
			if origins[i].found {
				continue
			}
			var o origin
			var err error
			if pass == 0 {
				o.path, o.parts = s.path, s.parts
				o.chain, o.found, err = b.resolve(b.g.nodes[p].tree, s.parts, s.chain)
			} else {
				o, err = b.findRename(p, s)
			}
			if err != nil {
				return err
			}
			if !o.found {
				continue
			}
			origins[i] = o
			if _, ok := b.prev[suspectKey{s.commit, s.path}]; !ok {
				b.prev[suspectKey{s.commit, s.path}] = suspectKey{p, o.path}
			}
			if o.chain[len(o.chain)-1] == s.blob() {
				ps := b.suspect(p, o.path, o.parts, o.chain)
				ps.entries = mergeEntries(ps.entries, s.entries)
				s.entries = nil
				return nil
			}
		}
	}
	var mine []int32
	for i, p := range parents {
		if !origins[i].found {
			continue
		}
		if len(s.entries) == 0 {
			break
		}
		o := origins[i]
		if mine == nil {
			var err error
			if mine, err = b.lines(s.blob()); err != nil {
				return err
			}
		}
		theirs, err := b.lines(o.chain[len(o.chain)-1])
		if err != nil {
			return err
		}
		passed, kept := splitEntries(s.entries, b.matchingLines(theirs, mine))
		if len(passed) > 0 {
			ps := b.suspect(p, o.path, o.parts, o.chain)
			ps.entries = mergeEntries(ps.entries, passed)
		}
		s.entries = kept
	}
	return nil
}

// resolve finds the file at parts under the tree root, returning the hash at
// every step: chain[i] is the tree of the directory parts[:i] (chain[0] is
// root) and the last element is the blob.
//
// like, if given, is the chain of the same path in a neighbouring commit. As
// soon as a directory on the way down has the same tree as in like, the rest
// of the answer IS like's — nothing below that point can differ — and the
// descent stops there without reading another tree.
func (b *blamer) resolve(root plumbing.Hash, parts []string, like []plumbing.Hash) ([]plumbing.Hash, bool, error) {
	chain := make([]plumbing.Hash, 0, len(parts)+1)
	chain = append(chain, root)
	cur := root
	for i, name := range parts {
		if like != nil && cur == like[i] {
			return append(chain, like[i+1:]...), true, nil
		}
		if name == "" {
			return nil, false, nil
		}
		data, err := b.e.rawObject(plumbing.TreeObject, cur)
		if err != nil {
			return nil, false, fmt.Errorf("tree %s: %w", cur, err)
		}
		mode, hash, ok := findTreeEntry(data, name)
		if !ok {
			return nil, false, nil
		}
		last := i == len(parts)-1
		switch {
		case last && !isBlameableFile(mode), !last && mode != filemode.Dir:
			return nil, false, nil
		}
		chain = append(chain, hash)
		cur = hash
	}
	return chain, true, nil
}

// isBlameableFile reports whether a tree entry is a file with content: not a
// directory, and not a gitlink (whose hash names a commit elsewhere).
func isBlameableFile(m filemode.FileMode) bool {
	return m != filemode.Dir && m != filemode.Submodule
}

// findTreeEntry scans a raw tree object for the entry called name.
func findTreeEntry(data []byte, name string) (filemode.FileMode, plumbing.Hash, bool) {
	var (
		mode  filemode.FileMode
		hash  plumbing.Hash
		found bool
	)
	_ = parseTree(data, func(ent rawTreeEntry) error {
		if string(ent.name) == name {
			mode, hash, found = ent.mode, ent.hash, true
			return errStopTreeScan
		}
		return nil
	})
	return mode, hash, found
}

var errStopTreeScan = fmt.Errorf("stop")

// maxRenameSources bounds the inexact rename search, as git's rename limit
// does: a commit that deletes more files than this is searched for an
// identical file only.
const maxRenameSources = 1000

// findRename looks, in parent p, for the file s's file was renamed from: a
// file p has that s's commit does not, with the same content — or, failing
// that, the most similar one that is at least half the same.
func (b *blamer) findRename(p int32, s *suspect) (origin, error) {
	old, new, err := b.e.diffTreeRefs(b.g.nodes[p].tree.String(), b.g.nodes[s.commit].tree.String())
	if err != nil {
		// A tree this cannot read is a tree it cannot search; the file is
		// then simply not followed past this commit.
		return origin{}, nil
	}
	var sources []string
	for path, ent := range old {
		if _, still := new[path]; still || ent.Mode == ModeGitlink {
			continue
		}
		sources = append(sources, path)
	}
	if len(sources) == 0 {
		return origin{}, nil
	}
	sort.Strings(sources)
	want := s.blob().String()
	base := path.Base(s.path)
	best, bestScore := "", -1
	for _, src := range sources {
		if old[src].SHA != want {
			continue
		}
		// Identical: prefer the one that kept its name.
		score := 0
		if path.Base(src) == base {
			score = 1
		}
		if score > bestScore {
			best, bestScore = src, score
		}
	}
	if best == "" && len(sources) <= maxRenameSources {
		mine, err := b.e.rawObject(plumbing.BlobObject, s.blob())
		if err != nil {
			return origin{}, err
		}
		for _, src := range sources {
			theirs, err := b.e.rawObject(plumbing.BlobObject, plumbing.NewHash(old[src].SHA))
			if err != nil {
				return origin{}, err
			}
			score := blameSimilarity(theirs, mine)
			if score < 5000 { // git's default: 50%
				continue
			}
			if path.Base(src) == base {
				score++
			}
			if score > bestScore {
				best, bestScore = src, score
			}
		}
	}
	if best == "" {
		return origin{}, nil
	}
	parts := strings.Split(best, "/")
	chain, found, err := b.resolve(b.g.nodes[p].tree, parts, nil)
	if err != nil || !found {
		return origin{}, err
	}
	return origin{found: true, path: best, parts: parts, chain: chain}, nil
}

// blameSimilarity scores how much of the larger of two files the other shares, in
// units of 1/10000: the bytes of the lines they have in common (as multisets)
// over the size of the larger. Files whose sizes alone rule out 50% score 0
// without being compared.
func blameSimilarity(a, b []byte) int {
	big, small := len(a), len(b)
	if big < small {
		big, small = small, big
	}
	if big == 0 {
		return 10000
	}
	if small*2 < big {
		return 0
	}
	count := map[string]int{}
	for _, l := range splitLinesKeepNL(a) {
		count[string(l)]++
	}
	common := 0
	for _, l := range splitLinesKeepNL(b) {
		if count[string(l)] > 0 {
			count[string(l)]--
			common += len(l)
		}
	}
	return int(int64(common) * 10000 / int64(big))
}

// lines returns blob's lines, interned.
func (b *blamer) lines(blob plumbing.Hash) ([]int32, error) {
	if ids, ok := b.files[blob]; ok {
		return ids, nil
	}
	data, err := b.e.rawObject(plumbing.BlobObject, blob)
	if err != nil {
		return nil, fmt.Errorf("blob %s: %w", blob, err)
	}
	raw := splitLinesKeepNL(data)
	ids := make([]int32, len(raw))
	for i, l := range raw {
		key := l
		if b.ws {
			key = stripSpace(l)
		}
		id, ok := b.lineIDs[string(key)]
		if !ok {
			id = int32(len(b.lineIDs))
			b.lineIDs[string(key)] = id
			b.indents = append(b.indents, measureIndent(l))
		}
		ids[i] = id
	}
	// A version is needed twice in quick succession — as a parent, then as
	// the suspect — and rarely again, so a few recent ones are all that is
	// worth keeping.
	const keep = 8
	if b.files == nil {
		b.files = map[plumbing.Hash][]int32{}
	}
	if len(b.recent) >= keep {
		delete(b.files, b.recent[0])
		b.recent = b.recent[1:]
	}
	b.files[blob] = ids
	b.recent = append(b.recent, blob)
	return ids, nil
}

// lineBlock says theirs[a:a+n] and mine[b:b+n] are the same lines.
type lineBlock struct{ a, b, n int }

// splitEntries divides blame entries (over "mine") by the blocks a parent
// shares: the parts inside a block pass to the parent, renumbered to the
// parent's lines; the rest are kept. Both results stay sorted by src.
func splitEntries(entries []blameEntry, blocks []lineBlock) (passed, kept []blameEntry) {
	for _, ent := range entries {
		pos, end := ent.src, ent.src+ent.n
		// The first block ending after the entry starts. Found afresh per
		// entry rather than carried over: two children can hand a parent
		// entries that overlap in its lines (a line both of them kept).
		bi := sort.Search(len(blocks), func(i int) bool { return blocks[i].b+blocks[i].n > pos })
		for pos < end {
			for bi < len(blocks) && blocks[bi].b+blocks[bi].n <= pos {
				bi++
			}
			if bi == len(blocks) || blocks[bi].b >= end {
				kept = append(kept, blameEntry{ent.final + pos - ent.src, pos, end - pos})
				break
			}
			blk := blocks[bi]
			if blk.b > pos {
				kept = append(kept, blameEntry{ent.final + pos - ent.src, pos, blk.b - pos})
				pos = blk.b
			}
			stop := min(end, blk.b+blk.n)
			passed = append(passed, blameEntry{ent.final + pos - ent.src, blk.a + pos - blk.b, stop - pos})
			pos = stop
		}
	}
	// Blocks ascend in both files, so the passed entries are in the parent's
	// order whenever the entries did not overlap; when they did, put them so.
	if !sort.SliceIsSorted(passed, func(i, j int) bool { return passed[i].src < passed[j].src }) {
		sort.SliceStable(passed, func(i, j int) bool { return passed[i].src < passed[j].src })
	}
	return passed, kept
}

// mergeEntries merges two lists sorted by src into one.
func mergeEntries(x, y []blameEntry) []blameEntry {
	if len(x) == 0 {
		return y
	}
	if len(y) == 0 {
		return x
	}
	out := make([]blameEntry, 0, len(x)+len(y))
	i, j := 0, 0
	for i < len(x) && j < len(y) {
		if x[i].src <= y[j].src {
			out = append(out, x[i])
			i++
		} else {
			out = append(out, y[j])
			j++
		}
	}
	out = append(out, x[i:]...)
	return append(out, y[j:]...)
}

// maxMyersD bounds the edit distance the exact diff will chase. Its trace
// takes memory quadratic in the distance; past this the two versions have
// little enough in common that the contiguous-block matcher (unidiff.go)
// lines them up as well as anything would.
const maxMyersD = 1500

// matchingLines returns the lines two versions of a file share, as blocks
// ascending in both: a shortest edit script (Myers), then git's compaction
// pass (compactChanges) to settle WHICH of several identical lines the
// script counts as changed — the question that decides, when a function is
// added between two others, whether the new commit gets the blank line above
// it or the one below.
func (b *blamer) matchingLines(a, c []int32) []lineBlock {
	pre := 0
	for pre < len(a) && pre < len(c) && a[pre] == c[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(c)-pre && a[len(a)-1-suf] == c[len(c)-1-suf] {
		suf++
	}
	ma, mc := a[pre:len(a)-suf], c[pre:len(c)-suf]
	// Changed flags carry one unchanged sentinel at each end (index i+1 is
	// line i), so the compaction pass can look one past either edge.
	fa := &diffSide{ids: a, changed: make([]bool, len(a)+2)}
	fc := &diffSide{ids: c, changed: make([]bool, len(c)+2)}
	chA, chC := fa.changed[1+pre:1+len(a)-suf], fc.changed[1+pre:1+len(c)-suf]
	if !myersDiff(ma, mc, chA, chC) {
		// Too far apart for the exact diff: mark everything changed, then
		// clear what the block matcher pairs up.
		for i := range chA {
			chA[i] = true
		}
		for j := range chC {
			chC[j] = true
		}
		local := make(map[int32]int32, len(mc))
		lc := make([]int32, len(mc))
		for j, id := range mc {
			l, ok := local[id]
			if !ok {
				l = int32(len(local))
				local[id] = l
			}
			lc[j] = l
		}
		la := make([]int32, len(ma))
		for i, id := range ma {
			if l, ok := local[id]; ok {
				la[i] = l
			} else {
				la[i] = -1
			}
		}
		for _, blk := range newLineMatcherIDs(la, lc, len(local)).matchingBlocks() {
			for k := 0; k < blk.size; k++ {
				chA[blk.a+k] = false
				chC[blk.b+k] = false
			}
		}
	}
	b.compactChanges(fa, fc)
	b.compactChanges(fc, fa)

	// The unchanged lines of the two files pair up in order.
	var blocks []lineBlock
	i, j := 0, 0
	for i < len(a) && j < len(c) {
		switch {
		case fa.changed[i+1]:
			i++
		case fc.changed[j+1]:
			j++
		default:
			if n := len(blocks); n > 0 && blocks[n-1].a+blocks[n-1].n == i && blocks[n-1].b+blocks[n-1].n == j {
				blocks[n-1].n++
			} else {
				blocks = append(blocks, lineBlock{i, j, 1})
			}
			i++
			j++
		}
	}
	return blocks
}

// diffSide is one file of a diff: its lines and which of them the edit
// script changes. changed[i+1] is line i; changed[0] and changed[len-1] are
// always false.
type diffSide struct {
	ids     []int32
	changed []bool
}

func (f *diffSide) n() int            { return len(f.ids) }
func (f *diffSide) chg(i int) bool    { return f.changed[i+1] }
func (f *diffSide) set(i int, v bool) { f.changed[i+1] = v }

// lineGroup is a (possibly empty) run of changed lines [start, end) lying
// between two unchanged lines. The k-th group of one file corresponds to the
// k-th group of the other, because their unchanged lines pair up in order.
type lineGroup struct{ start, end int }

func (f *diffSide) firstGroup() lineGroup {
	g := lineGroup{}
	for f.chg(g.end) {
		g.end++
	}
	return g
}

// next moves g to the following group; false at the end of the file.
func (f *diffSide) next(g *lineGroup) bool {
	if g.end == f.n() {
		return false
	}
	g.start = g.end + 1
	for g.end = g.start; f.chg(g.end); g.end++ {
	}
	return true
}

// previous moves g to the preceding group; false at the start of the file.
func (f *diffSide) previous(g *lineGroup) bool {
	if g.start == 0 {
		return false
	}
	g.end = g.start - 1
	for g.start = g.end; f.chg(g.start - 1); g.start-- {
	}
	return true
}

// slideDown moves g one line toward the end of the file if the line after it
// equals its first line, absorbing a group it runs into.
func (f *diffSide) slideDown(g *lineGroup) bool {
	if g.end < f.n() && f.ids[g.start] == f.ids[g.end] {
		f.set(g.start, false)
		g.start++
		f.set(g.end, true)
		g.end++
		for f.chg(g.end) {
			g.end++
		}
		return true
	}
	return false
}

// slideUp moves g one line toward the start of the file if the line before
// it equals its last line, absorbing a group it runs into.
func (f *diffSide) slideUp(g *lineGroup) bool {
	if g.start > 0 && f.ids[g.start-1] == f.ids[g.end-1] {
		g.start--
		f.set(g.start, true)
		g.end--
		f.set(g.end, false)
		for f.chg(g.start - 1) {
			g.start--
		}
		return true
	}
	return false
}

// compactChanges is git's xdl_change_compact, for file f against the other
// file o. An edit script is often ambiguous — a run of changed lines can
// slide up or down over lines identical to its ends and still be a correct
// script — and this picks the position, group by group, as git does:
//
//   - slide each group as far up, then as far down, as it goes, merging the
//     groups it meets;
//   - if somewhere in that range it lines up with a change in the other file
//     (the two are one replacement), leave it at the last such place;
//   - otherwise score each position by how it reads (the indent heuristic:
//     prefer to split at blank lines and at lower indentation) and take the
//     best.
//
// It is ported line for line because agreeing with `git blame` on exactly
// these cases is the point; the scoring constants are git's.
func (b *blamer) compactChanges(f, o *diffSide) {
	g, og := f.firstGroup(), o.firstGroup()
	for {
		if g.end != g.start {
			var groupSize, earliestEnd, endMatchingOther int
			for {
				groupSize = g.end - g.start
				endMatchingOther = -1
				for f.slideUp(&g) {
					if !o.previous(&og) {
						return // the two files' groups fell out of step
					}
				}
				earliestEnd = g.end
				if og.end > og.start {
					endMatchingOther = g.end
				}
				for f.slideDown(&g) {
					if !o.next(&og) {
						return
					}
					if og.end > og.start {
						endMatchingOther = g.end
					}
				}
				if groupSize == g.end-g.start {
					break
				}
			}
			switch {
			case g.end == earliestEnd:
				// No room to move.
			case endMatchingOther != -1:
				for og.end == og.start {
					if !f.slideUp(&g) || !o.previous(&og) {
						return
					}
				}
			default:
				shift := earliestEnd
				if s := g.end - groupSize - 1; s > shift {
					shift = s
				}
				if s := g.end - indentHeuristicMaxSliding; s > shift {
					shift = s
				}
				bestShift := -1
				var best splitScore
				for ; shift <= g.end; shift++ {
					var score splitScore
					b.scoreSplit(f, shift, &score)
					b.scoreSplit(f, shift-groupSize, &score)
					if bestShift == -1 || score.cmp(best) <= 0 {
						best, bestShift = score, shift
					}
				}
				for g.end > bestShift {
					if !f.slideUp(&g) || !o.previous(&og) {
						return
					}
				}
			}
		}
		if !f.next(&g) {
			return
		}
		if !o.next(&og) {
			return
		}
	}
}

// The indent heuristic's weights, from git's xdiff/xdiffi.c.
const (
	maxIndent                 = 200
	maxBlanks                 = 20
	startOfFilePenalty        = 1
	endOfFilePenalty          = 21
	totalBlankWeight          = -30
	postBlankWeight           = 6
	relativeIndentPenalty     = -4
	relativeIndentBlankPen    = 10
	relativeOutdentPenalty    = 24
	relativeOutdentBlankPen   = 17
	relativeDedentPenalty     = 23
	relativeDedentBlankPen    = 17
	indentWeight              = 60
	indentHeuristicMaxSliding = 100
)

type splitScore struct{ effectiveIndent, penalty int }

func (s splitScore) cmp(o splitScore) int {
	c := 0
	switch {
	case s.effectiveIndent > o.effectiveIndent:
		c = 1
	case s.effectiveIndent < o.effectiveIndent:
		c = -1
	}
	return indentWeight*c + (s.penalty - o.penalty)
}

// indentOf is a line's indentation in columns (tabs to multiples of 8), or
// -1 for a line that is all whitespace.
func (b *blamer) indentOf(id int32) int {
	if int(id) < len(b.indents) {
		return int(b.indents[id])
	}
	return -1
}

func measureIndent(line []byte) int16 {
	ret := 0
	for _, c := range line {
		switch c {
		case ' ':
			ret++
		case '\t':
			ret += 8 - ret%8
		case '\n', '\r', '\v', '\f':
			// other whitespace: ignored
		default:
			return int16(ret)
		}
		if ret >= maxIndent {
			return maxIndent
		}
	}
	return -1
}

// scoreSplit adds to s the badness of splitting file f just before line
// split.
func (b *blamer) scoreSplit(f *diffSide, split int, s *splitScore) {
	endOfFile := split >= f.n()
	indent := -1
	if !endOfFile {
		indent = b.indentOf(f.ids[split])
	}
	preBlank, preIndent := 0, -1
	for i := split - 1; i >= 0; i-- {
		preIndent = b.indentOf(f.ids[i])
		if preIndent != -1 {
			break
		}
		preBlank++
		if preBlank == maxBlanks {
			preIndent = 0
			break
		}
	}
	postBlank, postIndent := 0, -1
	for i := split + 1; i < f.n(); i++ {
		postIndent = b.indentOf(f.ids[i])
		if postIndent != -1 {
			break
		}
		postBlank++
		if postBlank == maxBlanks {
			postIndent = 0
			break
		}
	}

	if preIndent == -1 && preBlank == 0 {
		s.penalty += startOfFilePenalty
	}
	if endOfFile {
		s.penalty += endOfFilePenalty
	}
	post := 0
	if indent == -1 {
		post = 1 + postBlank
	}
	total := preBlank + post
	s.penalty += totalBlankWeight * total
	s.penalty += postBlankWeight * post
	if indent == -1 {
		indent = postIndent
	}
	anyBlanks := total != 0
	s.effectiveIndent += indent
	pick := func(withBlank, without int) int {
		if anyBlanks {
			return withBlank
		}
		return without
	}
	switch {
	case indent == -1, preIndent == -1, indent == preIndent:
	case indent > preIndent:
		s.penalty += pick(relativeIndentBlankPen, relativeIndentPenalty)
	case postIndent != -1 && postIndent > indent:
		// Less indented than the line before, more to follow: the start of
		// a block.
		s.penalty += pick(relativeOutdentBlankPen, relativeOutdentPenalty)
	default:
		// Less indented, nothing deeper after: the end of a block.
		s.penalty += pick(relativeDedentBlankPen, relativeDedentPenalty)
	}
}

// myersDiff marks, in changedA and changedB, the lines a shortest edit
// script deletes from a and inserts from b (Myers' O(ND) algorithm, forward
// greedy). It reports false, with nothing marked, if the script is longer
// than maxMyersD.
func myersDiff(a, b []int32, changedA, changedB []bool) bool {
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		for i := range changedA {
			changedA[i] = true
		}
		for j := range changedB {
			changedB[j] = true
		}
		return true
	}
	limit := min(n+m, maxMyersD)
	off := limit + 1
	v := make([]int32, 2*limit+3)
	var trace [][]int32 // trace[d] is v[-d..d] after round d
	found := -1
	for d := 0; d <= limit && found < 0; d++ {
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = int(v[off+k+1])
			} else {
				x = int(v[off+k-1]) + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = int32(x)
			if x >= n && y >= m {
				found = d
				break
			}
		}
		if found < 0 {
			trace = append(trace, append([]int32(nil), v[off-d:off+d+1]...))
		}
	}
	if found < 0 {
		return false
	}
	x, y := n, m
	for d := found; d > 0; d-- {
		prev := trace[d-1] // index k+(d-1)
		at := func(k int) int32 {
			if k < -(d-1) || k > d-1 {
				return -1
			}
			return prev[k+d-1]
		}
		k := x - y
		var pk int
		if k == -d || (k != d && at(k-1) < at(k+1)) {
			pk = k + 1
		} else {
			pk = k - 1
		}
		px := int(at(pk))
		py := px - pk
		if pk == k+1 {
			changedB[py] = true // a step down: b[py] inserted
		} else {
			changedA[px] = true // a step right: a[px] deleted
		}
		x, y = px, py
	}
	return true
}

// stripSpace drops every whitespace byte (git blame -w compares lines so).
func stripSpace(l []byte) []byte {
	out := make([]byte, 0, len(l))
	for _, c := range l {
		switch c {
		case ' ', '\t', '\r', '\n', '\f', '\v':
		default:
			out = append(out, c)
		}
	}
	return out
}
