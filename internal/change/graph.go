package change

import (
	"container/heap"
	"fmt"
	"sort"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Commit-graph queries are cairn's own, not go-git's. go-git stays the object
// store and transport; everything that answers a question about HISTORY —
// merge bases, ancestry, divergence — lives here and in divergence.go, and is
// checked against real git on generated histories (graph_git_test.go).
//
// The reason is correctness, not taste: go-git's walks order commits by
// committer time and trust that order, so commits stamped in the same second
// (a script, a bot, a fast operator) or with a skewed clock gave wrong merge
// bases — and a wrong merge base is a merge that re-marks conflicts the
// operator already resolved. A clock-ordered walk cannot be made exact by
// walking a little further; the walks here are ordered by GENERATION number
// (generation.go), as git's are with a commit-graph, so a commit is only
// processed after every descendant the walk can reach — and the walk can stop
// the moment everything still queued is settled.

const (
	flagA     = 1
	flagB     = 2
	flagStale = 4
	flagBoth  = flagA | flagB
)

// mergeBases returns every best common ancestor of a and b — `git merge-base
// --all a b` — newest first (ties by hash, so the order is deterministic).
// Empty when the histories are unrelated.
//
// It is git's paint-down-to-common: paint a's ancestry A and b's B; a commit
// painted both is common, and its ancestors are painted STALE, because a
// common commit below another common commit is not a BEST one. What is left
// common and not stale is the answer.
func (e *Engine) mergeBases(a, b string) ([]string, error) {
	if a == "" || b == "" {
		return nil, nil
	}
	if a == b {
		return []string{a}, nil
	}
	w := newGraphWalk(e)
	if err := w.paint(plumbing.NewHash(a), flagA); err != nil {
		return nil, err
	}
	if err := w.paint(plumbing.NewHash(b), flagB); err != nil {
		return nil, err
	}
	settled := func(f int) bool { return f&flagStale != 0 }
	err := w.run(settled, func(f int) int {
		if f&flagBoth == flagBoth {
			return f | flagStale
		}
		return f
	})
	if err != nil {
		return nil, fmt.Errorf("change.mergeBases: %w", err)
	}
	var out []plumbing.Hash
	for h, f := range w.flags {
		if f&flagBoth == flagBoth && f&flagStale == 0 {
			out = append(out, h)
		}
	}
	return w.newestFirst(out), nil
}

// mergeBase returns the best common ancestor of commits a and b, or "" if
// either input is empty or no common ancestor exists. With several best
// bases (a criss-cross merge) it takes the newest, as `git merge-base` does.
func (e *Engine) mergeBase(a, b string) (string, error) {
	bases, err := e.mergeBases(a, b)
	if err != nil || len(bases) == 0 {
		return "", err
	}
	return bases[0], nil
}

// ancestorSet returns every commit reachable from tip, tip included.
//
// It exists so a clone can compute the default branch's ancestry ONCE instead
// of per branch (#148): mapping N branches onto trunk with a pairwise merge
// base re-walked trunk N times.
func (e *Engine) ancestorSet(tip string) (map[plumbing.Hash]struct{}, error) {
	if tip == "" {
		return nil, nil
	}
	set := map[plumbing.Hash]struct{}{}
	stack := []plumbing.Hash{plumbing.NewHash(tip)}
	for len(stack) > 0 {
		h := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, ok := set[h]; ok {
			continue
		}
		c, err := e.git.CommitObject(h)
		if err != nil {
			return nil, fmt.Errorf("change.ancestorSet: commit %s: %w", h, err)
		}
		set[h] = struct{}{}
		stack = append(stack, c.ParentHashes...)
	}
	return set, nil
}

// mergeBaseIn returns the merge base of head against the branch whose ancestry
// is set (from ancestorSet). "" means the histories are unrelated.
//
// set is closed under ancestry, so this needs no clock at all: walk head's
// history OUTSIDE set, and every edge that lands inside it names a candidate.
// Candidates below another candidate are not best, and are dropped. The cost
// is proportional to how far head has diverged, not to the length of trunk.
func (e *Engine) mergeBaseIn(head string, set map[plumbing.Hash]struct{}) (string, error) {
	if head == "" || len(set) == 0 {
		return "", nil
	}
	start := plumbing.NewHash(head)
	if _, ok := set[start]; ok {
		return head, nil
	}
	seen := map[plumbing.Hash]struct{}{start: {}}
	cands := map[plumbing.Hash]struct{}{}
	stack := []plumbing.Hash{start}
	for len(stack) > 0 {
		h := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		c, err := e.git.CommitObject(h)
		if err != nil {
			return "", fmt.Errorf("change.mergeBaseIn: commit %s: %w", h, err)
		}
		for _, p := range c.ParentHashes {
			if _, ok := set[p]; ok {
				cands[p] = struct{}{}
				continue
			}
			if _, ok := seen[p]; ok {
				continue
			}
			if len(seen) >= describeWalkCap {
				return "", fmt.Errorf("change.mergeBaseIn: walk exceeded %d commits from %s", describeWalkCap, head[:8])
			}
			seen[p] = struct{}{}
			stack = append(stack, p)
		}
	}
	var best []plumbing.Hash
	for c := range cands {
		below := false
		for o := range cands {
			if o == c {
				continue
			}
			anc, err := e.isAncestor(c.String(), o.String())
			if err != nil {
				return "", err
			}
			if anc {
				below = true
				break
			}
		}
		if !below {
			best = append(best, c)
		}
	}
	if len(best) == 0 {
		return "", nil
	}
	return newGraphWalk(e).newestFirst(best)[0], nil
}

// isAncestor reports whether commit a is an ancestor of (or identical to) b.
// Used to decide whether a merge-forward must record the adopted parent-line tip
// as a second parent: if that tip is already in the commit's ancestry, the merge
// base is current and a redundant merge commit would only add noise.
func (e *Engine) isAncestor(a, b string) (bool, error) {
	if a == "" || b == "" {
		return false, nil
	}
	if a == b {
		return true, nil
	}
	bases, err := e.mergeBases(a, b)
	if err != nil {
		return false, err
	}
	for _, x := range bases {
		if x == a {
			return true, nil
		}
	}
	return false, nil
}

// graphWalk is the shared machinery: flags per commit and a queue ordered by
// generation, highest first. Popping in that order means a commit's flags are
// final when it is popped — every path to it from a starting commit runs
// through higher generations, all popped already.
type graphWalk struct {
	e       *Engine
	flags   map[plumbing.Hash]int
	queued  map[plumbing.Hash]bool
	commits map[plumbing.Hash]*object.Commit
	pq      genHeap
	visited int
}

func newGraphWalk(e *Engine) *graphWalk {
	return &graphWalk{
		e:       e,
		flags:   map[plumbing.Hash]int{},
		queued:  map[plumbing.Hash]bool{},
		commits: map[plumbing.Hash]*object.Commit{},
	}
}

func (w *graphWalk) commit(h plumbing.Hash) (*object.Commit, error) {
	if c, ok := w.commits[h]; ok {
		return c, nil
	}
	c, err := w.e.git.CommitObject(h)
	if err != nil {
		return nil, fmt.Errorf("commit %s: %w", h, err)
	}
	w.commits[h] = c
	return c, nil
}

// paint ORs f into h's flags, queueing h the first time it is reached.
func (w *graphWalk) paint(h plumbing.Hash, f int) error {
	_, seen := w.flags[h]
	w.flags[h] |= f
	if seen {
		return nil
	}
	c, err := w.commit(h)
	if err != nil {
		return err
	}
	g, err := w.e.generation(h)
	if err != nil {
		return err
	}
	w.queued[h] = true
	heap.Push(&w.pq, genItem{c, g})
	return nil
}

// run drains the queue, painting each commit's parents with pass(flags), and
// stops once every queued commit is settled: anything not yet reached is
// reachable only through those, so it is settled too.
func (w *graphWalk) run(settled func(int) bool, pass func(int) int) error {
	for w.pq.Len() > 0 {
		all := true
		for _, q := range w.pq {
			if !settled(w.flags[q.c.Hash]) {
				all = false
				break
			}
		}
		if all {
			return nil
		}
		if w.visited++; w.visited > describeWalkCap {
			return fmt.Errorf("walk exceeded %d commits", describeWalkCap)
		}
		it := heap.Pop(&w.pq).(genItem)
		w.queued[it.c.Hash] = false
		f := w.flags[it.c.Hash]
		for _, p := range it.c.ParentHashes {
			if err := w.paint(p, pass(f)); err != nil {
				return err
			}
		}
	}
	return nil
}

// genItem is a queued commit with its generation.
type genItem struct {
	c   *object.Commit
	gen uint32
}

// genHeap pops the highest generation first; among equals, the newest commit,
// then the lowest hash, so a walk is deterministic.
type genHeap []genItem

func (h genHeap) Len() int { return len(h) }
func (h genHeap) Less(i, j int) bool {
	if h[i].gen != h[j].gen {
		return h[i].gen > h[j].gen
	}
	ti, tj := h[i].c.Committer.When, h[j].c.Committer.When
	if !ti.Equal(tj) {
		return ti.After(tj)
	}
	return h[i].c.Hash.String() < h[j].c.Hash.String()
}
func (h genHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *genHeap) Push(x any)   { *h = append(*h, x.(genItem)) }
func (h *genHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// newestFirst orders hashes by committer time, newest first, ties by hash.
func (w *graphWalk) newestFirst(hs []plumbing.Hash) []string {
	type item struct {
		h plumbing.Hash
		t int64
	}
	items := make([]item, 0, len(hs))
	for _, h := range hs {
		var t int64
		if c, err := w.commit(h); err == nil {
			t = c.Committer.When.UnixNano()
		}
		items = append(items, item{h, t})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].t != items[j].t {
			return items[i].t > items[j].t
		}
		return items[i].h.String() < items[j].h.String()
	})
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.h.String()
	}
	return out
}
