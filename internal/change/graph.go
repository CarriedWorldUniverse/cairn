package change

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/go-git/go-git/v5/plumbing"
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
//
// They run on the commit graph (commitgraph.go), not on commit objects: a
// walk resolves its starting commits to graph ids — which reads from the
// object store whatever history the graph has not seen yet — and from there
// on a commit is an integer and its flags a byte in a slice.

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
	g := e.lockGraph()
	defer e.cgMu.Unlock()
	ia, err := g.id(plumbing.NewHash(a))
	if err != nil {
		return nil, err
	}
	ib, err := g.id(plumbing.NewHash(b))
	if err != nil {
		return nil, err
	}
	if err := g.flushGenerations(); err != nil {
		return nil, err
	}
	bases, err := g.mergeBases(ia, ib)
	if err != nil {
		return nil, fmt.Errorf("change.mergeBases: %w", err)
	}
	return g.hashes(bases), nil
}

// mergeBases is the walk behind Engine.mergeBases, on graph ids; the result
// is newest first.
func (g *commitGraph) mergeBases(a, b int32) ([]int32, error) {
	if a == b {
		return []int32{a}, nil
	}
	w := newGraphWalk(g, func(f int) bool { return f&flagStale != 0 })
	w.paint(a, flagA)
	w.paint(b, flagB)
	err := w.run(func(f int) int {
		if f&flagBoth == flagBoth {
			return f | flagStale
		}
		return f
	})
	if err != nil {
		return nil, err
	}
	var out []int32
	for _, id := range w.touched {
		if f := w.flagsOf(id); f&flagBoth == flagBoth && f&flagStale == 0 {
			out = append(out, id)
		}
	}
	g.newestFirst(out)
	return out, nil
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
	g := e.lockGraph()
	defer e.cgMu.Unlock()
	start, err := g.id(plumbing.NewHash(tip))
	if err != nil {
		return nil, fmt.Errorf("change.ancestorSet: %w", err)
	}
	ids := g.ancestors(start, 0)
	set := make(map[plumbing.Hash]struct{}, len(ids))
	for _, id := range ids {
		set[g.nodes[id].hash] = struct{}{}
	}
	return set, nil
}

// ancestors returns the ids of every commit reachable from start, start
// included. A positive limit stops the walk once MORE than limit commits have
// been collected, so the caller can tell a capped walk from a complete one.
func (g *commitGraph) ancestors(start int32, limit int) []int32 {
	seen := make([]bool, len(g.nodes))
	seen[start] = true
	out := []int32{start}
	for i := 0; i < len(out); i++ {
		if limit > 0 && len(out) > limit {
			return out
		}
		for _, p := range g.parentsOf(out[i]) {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
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
	g := e.lockGraph()
	defer e.cgMu.Unlock()
	startID, err := g.id(start)
	if err != nil {
		return "", fmt.Errorf("change.mergeBaseIn: %w", err)
	}
	seen := map[int32]struct{}{startID: {}}
	cands := map[int32]struct{}{}
	stack := []int32{startID}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, p := range g.parentsOf(id) {
			if _, ok := set[g.nodes[p].hash]; ok {
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
	var best []int32
	for c := range cands {
		below := false
		for o := range cands {
			if o == c {
				continue
			}
			anc, err := g.isAncestor(c, o)
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
	g.newestFirst(best)
	return g.nodes[best[0]].hash.String(), nil
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
	g := e.lockGraph()
	defer e.cgMu.Unlock()
	ia, err := g.id(plumbing.NewHash(a))
	if err != nil {
		return false, err
	}
	ib, err := g.id(plumbing.NewHash(b))
	if err != nil {
		return false, err
	}
	if err := g.flushGenerations(); err != nil {
		return false, err
	}
	return g.isAncestor(ia, ib)
}

// isAncestor is Engine.isAncestor on graph ids.
func (g *commitGraph) isAncestor(a, b int32) (bool, error) {
	if a == b {
		return true, nil
	}
	// Every ancestor of b has a lower generation than b, so a commit at or
	// above b's generation cannot be one — no walk needed.
	if g.nodes[a].gen >= g.nodes[b].gen {
		return false, nil
	}
	bases, err := g.mergeBases(a, b)
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

// hashes renders graph ids as full hex hashes.
func (g *commitGraph) hashes(ids []int32) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = g.nodes[id].hash.String()
	}
	return out
}

// Flag bits a walk keeps for its own bookkeeping, above the colours.
const (
	walkSeen   = 0x40 // reached at least once
	walkQueued = 0x80 // reached and not yet popped
	walkColour = flagA | flagB | flagStale
)

// graphWalk is the shared machinery: flags per commit and a queue ordered by
// generation, highest first. Popping in that order means a commit's flags are
// final when it is popped — every path to it from a starting commit runs
// through higher generations, all popped already.
//
// settled says when a commit's flags mean nothing below it can change the
// answer; the walk stops once every queued commit is settled. unsettled counts
// the queued commits that are not, so that test is a comparison rather than a
// scan of the queue on every step.
type graphWalk struct {
	g         *commitGraph
	flags     []uint8 // indexed by graph id
	touched   []int32 // every id reached, in the order reached
	pq        []int32 // heap of queued ids, see less
	settled   func(int) bool
	unsettled int
	visited   int
}

func newGraphWalk(g *commitGraph, settled func(int) bool) *graphWalk {
	return &graphWalk{g: g, flags: make([]uint8, len(g.nodes)), settled: settled}
}

// flagsOf returns the colours painted on id.
func (w *graphWalk) flagsOf(id int32) int { return int(w.flags[id] & walkColour) }

// paint ORs f into id's flags, queueing id the first time it is reached.
func (w *graphWalk) paint(id int32, f int) {
	old := w.flags[id]
	if old&walkSeen == 0 {
		w.flags[id] = uint8(f) | walkSeen | walkQueued
		w.touched = append(w.touched, id)
		w.push(id)
		if !w.settled(f) {
			w.unsettled++
		}
		return
	}
	now := old | uint8(f)
	if now == old {
		return
	}
	w.flags[id] = now
	if old&walkQueued != 0 {
		was, is := w.settled(int(old&walkColour)), w.settled(int(now&walkColour))
		switch {
		case !was && is:
			w.unsettled--
		case was && !is:
			w.unsettled++
		}
	}
}

// run drains the queue, painting each commit's parents with pass(flags), and
// stops once every queued commit is settled: anything not yet reached is
// reachable only through those, so it is settled too.
func (w *graphWalk) run(pass func(int) int) error {
	for len(w.pq) > 0 {
		if w.unsettled == 0 {
			return nil
		}
		if w.visited++; w.visited > describeWalkCap {
			return fmt.Errorf("walk exceeded %d commits", describeWalkCap)
		}
		id := w.pop()
		w.flags[id] &^= walkQueued
		f := w.flagsOf(id)
		if !w.settled(f) {
			w.unsettled--
		}
		pf := pass(f)
		for _, p := range w.g.parentsOf(id) {
			w.paint(p, pf)
		}
	}
	return nil
}

// less orders the queue: the highest generation first; among equals, the
// newest commit, then the lowest hash, so a walk is deterministic.
func (w *graphWalk) less(a, b int32) bool {
	na, nb := &w.g.nodes[a], &w.g.nodes[b]
	if na.gen != nb.gen {
		return na.gen > nb.gen
	}
	if na.time != nb.time {
		return na.time > nb.time
	}
	return bytes.Compare(na.hash[:], nb.hash[:]) < 0
}

func (w *graphWalk) push(id int32) {
	w.pq = append(w.pq, id)
	i := len(w.pq) - 1
	for i > 0 {
		parent := (i - 1) / 2
		if !w.less(w.pq[i], w.pq[parent]) {
			break
		}
		w.pq[i], w.pq[parent] = w.pq[parent], w.pq[i]
		i = parent
	}
}

func (w *graphWalk) pop() int32 {
	top := w.pq[0]
	n := len(w.pq) - 1
	w.pq[0] = w.pq[n]
	w.pq = w.pq[:n]
	for i := 0; ; {
		l, r, min := 2*i+1, 2*i+2, i
		if l < n && w.less(w.pq[l], w.pq[min]) {
			min = l
		}
		if r < n && w.less(w.pq[r], w.pq[min]) {
			min = r
		}
		if min == i {
			break
		}
		w.pq[i], w.pq[min] = w.pq[min], w.pq[i]
		i = min
	}
	return top
}

// newestFirst orders ids by committer time, newest first, ties by hash.
func (g *commitGraph) newestFirst(ids []int32) {
	sort.Slice(ids, func(i, j int) bool {
		a, b := &g.nodes[ids[i]], &g.nodes[ids[j]]
		if a.time != b.time {
			return a.time > b.time
		}
		return bytes.Compare(a.hash[:], b.hash[:]) < 0
	})
}
