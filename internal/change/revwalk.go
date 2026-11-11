package change

import (
	"bytes"
	"container/heap"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// The revision walk behind log, rev-list, shortlog and format-patch.
//
// WHICH commits a range names is decided on cairn's generation-ordered graph
// (interestingSet, below): exact whatever the commit clocks say. The ORDER
// they are listed in is git's — newest committer date first, parents queued as
// their child is listed — because that is the order every reader of `git log`
// expects, and matching it is what lets the walk be checked against git
// itself (revwalk_git_test.go). An unbounded walk (no exclusions) streams, so
// `log -n 20` on a long history reads twenty-odd commits, not all of them.

// Signature is a commit's author or committer.
type Signature struct {
	Name  string
	Email string
	When  time.Time
}

// CommitMeta is one commit as the walk yields it.
type CommitMeta struct {
	SHA       string
	Tree      string
	Parents   []string
	Author    Signature
	Committer Signature
	// Message is the commit message without cairn's Change-Id trailer and
	// without trailing newlines.
	Message string
	// GraphParents are the parents to DRAW: the nearest listed ancestors. Set
	// only by a Topo walk; under path limiting they skip the commits that
	// did not touch the path.
	GraphParents []string
	// Path is the followed file's name at this commit (Follow only).
	Path string
}

// Subject is the message's first paragraph on one line, as git's %s.
func (c *CommitMeta) Subject() string {
	s, _ := splitMessage(c.Message)
	return s
}

// Body is the message after the subject paragraph, as git's %b (no trailing
// newline).
func (c *CommitMeta) Body() string {
	_, b := splitMessage(c.Message)
	return b
}

func splitMessage(msg string) (subject, body string) {
	msg = strings.Trim(msg, "\n")
	head, rest, _ := strings.Cut(msg, "\n\n")
	lines := strings.Split(head, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	return strings.Join(lines, " "), strings.Trim(rest, "\n")
}

func metaOf(c *object.Commit) *CommitMeta {
	m := &CommitMeta{
		SHA:       c.Hash.String(),
		Tree:      c.TreeHash.String(),
		Author:    Signature{c.Author.Name, c.Author.Email, c.Author.When},
		Committer: Signature{c.Committer.Name, c.Committer.Email, c.Committer.When},
		Message:   strings.TrimRight(stripChangeID(c.Message), "\n"),
	}
	m.Parents = make([]string, len(c.ParentHashes))
	for i, p := range c.ParentHashes {
		m.Parents[i] = p.String()
	}
	return m
}

// CommitMeta loads one commit by full hash.
func (e *Engine) CommitMeta(sha string) (*CommitMeta, error) {
	c, err := e.git.CommitObject(plumbing.NewHash(sha))
	if err != nil {
		return nil, fmt.Errorf("change.CommitMeta: commit %s: %w", sha, err)
	}
	return metaOf(c), nil
}

// WalkOptions selects and orders the commits of a walk.
type WalkOptions struct {
	Include []string // full hashes to walk from
	Exclude []string // full hashes whose ancestry is left out

	FirstParent bool // follow only first parents
	Topo        bool // list a commit only after all its listed children (--graph)

	Paths  []string // keep only commits that change these paths (history simplification)
	Follow bool     // with exactly one path: continue across renames

	// Filters. Patterns are Go regular expressions.
	Authors    []string
	Committers []string
	Greps      []string
	IgnoreCase bool
	AllMatch   bool // every --grep must match (default: any)
	InvertGrep bool
	Since      time.Time // zero = unset; older commits end the walk along that path
	Until      time.Time // zero = unset
	Merges     bool      // only commits with 2+ parents
	NoMerges   bool      // only commits with at most 1 parent

	PickaxeS string // keep commits that change the number of occurrences of this string
	PickaxeG string // keep commits whose diff adds/removes a line matching this regexp

	Skip    int
	Limit   int // 0 = unlimited
	Reverse bool
}

var errStopWalk = errors.New("stop walk")

// ErrStopWalk may be returned by a WalkCommits callback to end the walk early
// without an error.
var ErrStopWalk = errStopWalk

// WalkCommits calls fn for each selected commit, in order.
func (e *Engine) WalkCommits(o WalkOptions, fn func(*CommitMeta) error) error {
	err := e.walkCommits(o, fn)
	if errors.Is(err, errStopWalk) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("change.WalkCommits: %w", err)
	}
	return nil
}

func (e *Engine) walkCommits(o WalkOptions, fn func(*CommitMeta) error) error {
	match, err := e.newCommitMatcher(o)
	if err != nil {
		return err
	}
	w := &dateWalk{e: e, o: o, seen: map[plumbing.Hash]struct{}{}}
	if len(o.Paths) > 0 {
		w.ps = NewPathspec(o.Paths)
		if o.Follow && len(o.Paths) == 1 {
			w.followPath = cleanTreePath(o.Paths[0])
		}
	}
	if len(o.Exclude) > 0 {
		if w.member, err = e.interestingSet(o.Include, o.Exclude, o.FirstParent); err != nil {
			return err
		}
	}
	if o.Topo {
		w.next = map[plumbing.Hash]plumbing.Hash{}
		w.shown = map[plumbing.Hash]struct{}{}
	}
	for _, sha := range o.Include {
		if err := w.push(plumbing.NewHash(sha)); err != nil {
			return err
		}
	}

	skipped, emitted := 0, 0
	var held []*CommitMeta // Reverse buffers the (already limited) selection
	emit := func(m *CommitMeta) error {
		ok, err := match(m)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if skipped < o.Skip {
			skipped++
			return nil
		}
		emitted++
		if o.Reverse {
			held = append(held, m)
		} else if err := fn(m); err != nil {
			return err
		}
		if o.Limit > 0 && emitted >= o.Limit {
			return errStopWalk
		}
		return nil
	}
	flush := func() error {
		for i := len(held) - 1; i >= 0; i-- {
			if err := fn(held[i]); err != nil {
				return err
			}
		}
		return nil
	}

	if !o.Topo {
		for {
			m, err := w.step()
			if err != nil {
				return err
			}
			if m == nil {
				return flush()
			}
			if err := emit(m); err != nil {
				if errors.Is(err, errStopWalk) {
					if ferr := flush(); ferr != nil {
						return ferr
					}
				}
				return err
			}
		}
	}

	// Topo: the whole selection first, then git's topological sort over it.
	var list []*CommitMeta
	for {
		m, err := w.step()
		if err != nil {
			return err
		}
		if m == nil {
			break
		}
		list = append(list, m)
	}
	for _, m := range list {
		m.GraphParents = w.rewriteParents(m)
	}
	for _, m := range topoSort(list) {
		if err := emit(m); err != nil {
			if errors.Is(err, errStopWalk) {
				if ferr := flush(); ferr != nil {
					return ferr
				}
			}
			return err
		}
	}
	return flush()
}

// dateWalk lists commits newest-committer-date first, as git does by default:
// a queue ordered by date, each commit's parents queued when it is listed.
// Ties keep insertion order.
type dateWalk struct {
	e      *Engine
	o      WalkOptions
	ps     *Pathspec
	member map[plumbing.Hash]struct{} // nil = every commit is in range
	seen   map[plumbing.Hash]struct{}
	pq     dateHeap
	seq    int

	followPath string // Follow: the file's current name

	loaded map[plumbing.Hash]*object.Commit // commits read but not yet queued

	// Topo bookkeeping: for a visited commit that is NOT listed, the single
	// parent its history continues through; and the set that is listed.
	next  map[plumbing.Hash]plumbing.Hash
	shown map[plumbing.Hash]struct{}
}

type dateItem struct {
	c   *object.Commit
	at  int64
	seq int
}

type dateHeap []dateItem

func (h dateHeap) Len() int { return len(h) }
func (h dateHeap) Less(i, j int) bool {
	if h[i].at != h[j].at {
		return h[i].at > h[j].at
	}
	return h[i].seq < h[j].seq
}
func (h dateHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *dateHeap) Push(x any)   { *h = append(*h, x.(dateItem)) }
func (h *dateHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

func (w *dateWalk) push(h plumbing.Hash) error {
	if _, ok := w.seen[h]; ok {
		return nil
	}
	if w.member != nil {
		if _, ok := w.member[h]; !ok {
			return nil
		}
	}
	c, err := w.load(h)
	if err != nil {
		return err
	}
	delete(w.loaded, h)
	w.seen[h] = struct{}{}
	heap.Push(&w.pq, dateItem{c, c.Committer.When.Unix(), w.seq})
	w.seq++
	return nil
}

// load reads a commit, remembering it until it is queued: path simplification
// reads each parent to compare trees just before the walk queues it.
func (w *dateWalk) load(h plumbing.Hash) (*object.Commit, error) {
	if c, ok := w.loaded[h]; ok {
		return c, nil
	}
	c, err := w.e.git.CommitObject(h)
	if err != nil {
		return nil, fmt.Errorf("commit %s: %w", h, err)
	}
	if w.loaded == nil || len(w.loaded) > 64 {
		w.loaded = map[plumbing.Hash]*object.Commit{}
	}
	w.loaded[h] = c
	return c, nil
}

// step returns the next listed commit, or nil when the walk is done.
func (w *dateWalk) step() (*CommitMeta, error) {
	for w.pq.Len() > 0 {
		c := heap.Pop(&w.pq).(dateItem).c
		// A commit older than --since is not listed and its parents are not
		// walked — git's rule, and what keeps `--since=1.week` from reading
		// the whole history.
		if !w.o.Since.IsZero() && c.Committer.When.Before(w.o.Since) {
			continue
		}
		parents := c.ParentHashes
		if w.o.FirstParent && len(parents) > 1 {
			parents = parents[:1]
		}
		show, follow := true, parents
		path := w.followPath
		if w.ps != nil {
			var err error
			if show, follow, err = w.simplify(c, parents); err != nil {
				return nil, err
			}
		}
		for _, p := range follow {
			if err := w.push(p); err != nil {
				return nil, err
			}
		}
		if !show {
			if w.next != nil && len(follow) > 0 {
				w.next[c.Hash] = follow[0]
			}
			continue
		}
		if w.shown != nil {
			w.shown[c.Hash] = struct{}{}
		}
		m := metaOf(c)
		m.Path = path
		return m, nil
	}
	return nil, nil
}

// simplify is git's default history simplification for a path-limited walk.
// A commit is listed when the paths differ from every parent; a merge whose
// paths match one parent is not listed, and its history continues through
// that parent alone.
func (w *dateWalk) simplify(c *object.Commit, parents []plumbing.Hash) (show bool, follow []plumbing.Hash, err error) {
	if len(parents) == 0 {
		has, err := w.e.treesDiffer(plumbing.ZeroHash, c.TreeHash, w.ps)
		return has, nil, err
	}
	trees := make([]plumbing.Hash, len(parents))
	for i, p := range parents {
		pc, err := w.load(p)
		if err != nil {
			return false, nil, err
		}
		trees[i] = pc.TreeHash
		differ, err := w.e.treesDiffer(pc.TreeHash, c.TreeHash, w.ps)
		if err != nil {
			return false, nil, err
		}
		if !differ {
			return false, parents[i : i+1], nil
		}
	}
	// Follow: if the file appears here (absent from the parent), look for
	// the path it was renamed from and continue under that name.
	if w.followPath != "" && len(parents) == 1 {
		if _, ok, err := w.e.treePath(trees[0], w.followPath); err != nil {
			return false, nil, err
		} else if !ok {
			from, err := w.e.renameSource(trees[0], c.TreeHash, w.followPath)
			if err != nil {
				return false, nil, err
			}
			if from != "" {
				w.followPath = from
				w.ps = NewPathspec([]string{from})
			}
		}
	}
	return true, parents, nil
}

// rewriteParents maps a listed commit's parents to the nearest LISTED
// ancestors, dropping those with none and duplicates.
func (w *dateWalk) rewriteParents(m *CommitMeta) []string {
	parents := m.Parents
	if w.o.FirstParent && len(parents) > 1 {
		parents = parents[:1]
	}
	var out []string
	dup := map[string]bool{}
	for _, p := range parents {
		h := plumbing.NewHash(p)
		for {
			if _, ok := w.shown[h]; ok {
				break
			}
			nx, ok := w.next[h]
			if !ok {
				h = plumbing.ZeroHash
				break
			}
			h = nx
		}
		if h.IsZero() || dup[h.String()] {
			continue
		}
		dup[h.String()] = true
		out = append(out, h.String())
	}
	return out
}

// topoSort is git's sort_in_topological_order in graph order: no commit
// before all of its listed children, and a branch's commits kept together
// (a merge's LAST parent's side first), given list in date order.
func topoSort(list []*CommitMeta) []*CommitMeta {
	indeg := make(map[string]int, len(list))
	by := make(map[string]*CommitMeta, len(list))
	for _, m := range list {
		indeg[m.SHA] = 1
		by[m.SHA] = m
	}
	for _, m := range list {
		for _, p := range m.GraphParents {
			if indeg[p] > 0 {
				indeg[p]++
			}
		}
	}
	var stack []*CommitMeta
	for _, m := range list {
		if indeg[m.SHA] == 1 {
			stack = append(stack, m)
		}
	}
	// LIFO; reversed so the tips come out in their original order.
	for i, j := 0, len(stack)-1; i < j; i, j = i+1, j-1 {
		stack[i], stack[j] = stack[j], stack[i]
	}
	out := make([]*CommitMeta, 0, len(list))
	for len(stack) > 0 {
		m := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, p := range m.GraphParents {
			if indeg[p] == 0 {
				continue
			}
			if indeg[p]--; indeg[p] == 1 {
				stack = append(stack, by[p])
			}
		}
		indeg[m.SHA] = 0
		out = append(out, m)
	}
	return out
}

// interestingSet returns the commits reachable from include and NOT from
// exclude. It is a two-colour generation-ordered walk (graph.go): a commit's
// colours are final when it is popped, and the walk stops as soon as every
// commit still queued is excluded — so the cost follows the size of the
// range, not of the history.
func (e *Engine) interestingSet(include, exclude []string, firstParent bool) (map[plumbing.Hash]struct{}, error) {
	const in, out = flagA, flagB
	w := newGraphWalk(e)
	open := 0 // queued commits not (yet) known to be excluded
	paint := func(h plumbing.Hash, f int) error {
		prev, seen := w.flags[h]
		if seen && prev|f == prev {
			return nil
		}
		wasQueued := w.queued[h]
		if err := w.paint(h, f); err != nil {
			return err
		}
		now := w.flags[h]
		switch {
		case !seen:
			if now&out == 0 {
				open++
			}
		case wasQueued && prev&out == 0 && now&out != 0:
			open--
		}
		return nil
	}
	for _, x := range exclude {
		if err := paint(plumbing.NewHash(x), out); err != nil {
			return nil, err
		}
	}
	for _, x := range include {
		if err := paint(plumbing.NewHash(x), in); err != nil {
			return nil, err
		}
	}
	for w.pq.Len() > 0 && open > 0 {
		it := heap.Pop(&w.pq).(genItem)
		h := it.c.Hash
		w.queued[h] = false
		f := w.flags[h]
		if f&out == 0 {
			open--
		}
		for i, p := range it.c.ParentHashes {
			pf := f & out
			if f&in != 0 && (i == 0 || !firstParent) {
				pf |= in
			}
			if pf == 0 {
				continue
			}
			if err := paint(p, pf); err != nil {
				return nil, err
			}
		}
		// The commit cache is only needed while a commit is queued.
		delete(w.commits, h)
	}
	set := map[plumbing.Hash]struct{}{}
	for h, f := range w.flags {
		if f&in != 0 && f&out == 0 && !w.queued[h] {
			set[h] = struct{}{}
		}
	}
	return set, nil
}

// CountCommits counts the commits in a range without loading more than the
// walk needs — `rev-list --count`.
func (e *Engine) CountCommits(o WalkOptions) (int, error) {
	n := 0
	err := e.WalkCommits(o, func(*CommitMeta) error { n++; return nil })
	return n, err
}

// newCommitMatcher compiles the walk's filters into one predicate.
func (e *Engine) newCommitMatcher(o WalkOptions) (func(*CommitMeta) (bool, error), error) {
	compile := func(pats []string, multiline bool) ([]*regexp.Regexp, error) {
		var out []*regexp.Regexp
		for _, p := range pats {
			flags := ""
			if o.IgnoreCase {
				flags += "i"
			}
			if multiline {
				flags += "m"
			}
			if flags != "" {
				p = "(?" + flags + ")" + p
			}
			re, err := regexp.Compile(p)
			if err != nil {
				return nil, fmt.Errorf("bad pattern %q: %w", p, err)
			}
			out = append(out, re)
		}
		return out, nil
	}
	authors, err := compile(o.Authors, false)
	if err != nil {
		return nil, err
	}
	committers, err := compile(o.Committers, false)
	if err != nil {
		return nil, err
	}
	greps, err := compile(o.Greps, true)
	if err != nil {
		return nil, err
	}
	var pickG *regexp.Regexp
	if o.PickaxeG != "" {
		if pickG, err = regexp.Compile("(?m)" + o.PickaxeG); err != nil {
			return nil, fmt.Errorf("bad -G pattern: %w", err)
		}
	}
	var ps *Pathspec
	if len(o.Paths) > 0 {
		ps = NewPathspec(o.Paths)
	}
	anyOf := func(res []*regexp.Regexp, s string) bool {
		for _, re := range res {
			if re.MatchString(s) {
				return true
			}
		}
		return false
	}
	return func(m *CommitMeta) (bool, error) {
		if o.Merges && len(m.Parents) < 2 {
			return false, nil
		}
		if o.NoMerges && len(m.Parents) > 1 {
			return false, nil
		}
		if !o.Until.IsZero() && m.Committer.When.After(o.Until) {
			return false, nil
		}
		if len(authors) > 0 && !anyOf(authors, m.Author.Name+" <"+m.Author.Email+">") {
			return false, nil
		}
		if len(committers) > 0 && !anyOf(committers, m.Committer.Name+" <"+m.Committer.Email+">") {
			return false, nil
		}
		if len(greps) > 0 {
			hit := 0
			for _, re := range greps {
				if re.MatchString(m.Message) {
					hit++
				}
			}
			ok := hit > 0
			if o.AllMatch {
				ok = hit == len(greps)
			}
			if ok == o.InvertGrep {
				return false, nil
			}
		}
		if o.PickaxeS != "" || pickG != nil {
			if len(m.Parents) > 1 {
				return false, nil
			}
			return e.pickaxe(m, ps, o.PickaxeS, pickG)
		}
		return true, nil
	}, nil
}

// pickaxe reports whether commit m changes the number of occurrences of s
// (-S), or adds/removes a line matching re (-G), in some file.
func (e *Engine) pickaxe(m *CommitMeta, ps *Pathspec, s string, re *regexp.Regexp) (bool, error) {
	parent := ""
	if len(m.Parents) > 0 {
		parent = m.Parents[0]
	}
	changes, err := e.TreeChanges(parent, m.SHA, ps)
	if err != nil {
		return false, err
	}
	for _, ch := range changes {
		oldB, newB, err := e.changeBlobs(ch)
		if err != nil {
			return false, err
		}
		if s != "" && bytes.Count(oldB, []byte(s)) != bytes.Count(newB, []byte(s)) {
			return true, nil
		}
		if re != nil {
			if isBinary(oldB) || isBinary(newB) {
				continue
			}
			a, b := splitKeepNL(oldB), splitKeepNL(newB)
			for _, op := range diffOps(a, b, nil, nil) {
				if op.tag == 'e' {
					continue
				}
				for _, ln := range a[op.i1:op.i2] {
					if re.MatchString(ln) {
						return true, nil
					}
				}
				for _, ln := range b[op.j1:op.j2] {
					if re.MatchString(ln) {
						return true, nil
					}
				}
			}
		}
	}
	return false, nil
}
