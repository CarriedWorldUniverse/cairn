package change

import (
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"
)

// divergence counts the commits reachable from a but not from b (ahead) and
// from b but not from a (behind) — `git rev-list --left-right --count a...b`.
//
// It is a two-colour walk (graph.go): a's ancestry painted A, b's painted B,
// in generation order, stopping once every commit still queued carries both
// colours — everything below is common. The cost is proportional to the
// DIVERGENCE between a and b, not to the repository's history, which is what
// lets `tree` compute this for every line on a 20-year repository. (The
// earlier first-parent walk from the tip to the line's base ran to the ROOT
// whenever the base sat behind a merge's second parent; the clock-ordered
// walk that replaced it miscounted commits stamped in the same second.)
func (e *Engine) divergence(a, b string) (ahead, behind int, err error) {
	if a == b {
		return 0, 0, nil
	}
	// One side empty (a line or parent with no commits yet): everything on
	// the other side is exclusive to it.
	if a == "" || b == "" {
		n, err := e.countAncestors(a + b)
		if a == "" {
			return 0, n, err
		}
		return n, 0, err
	}
	g := e.lockGraph()
	defer e.cgMu.Unlock()
	ia, err := g.id(plumbing.NewHash(a))
	if err != nil {
		return 0, 0, fmt.Errorf("change.divergence: %w", err)
	}
	ib, err := g.id(plumbing.NewHash(b))
	if err != nil {
		return 0, 0, fmt.Errorf("change.divergence: %w", err)
	}
	if err := g.flushGenerations(); err != nil {
		return 0, 0, err
	}
	w := newGraphWalk(g, func(f int) bool { return f&flagBoth == flagBoth })
	w.paint(ia, flagA)
	w.paint(ib, flagB)
	if err := w.run(func(f int) int { return f }); err != nil {
		return 0, 0, fmt.Errorf("change.divergence: %w", err)
	}
	for _, id := range w.touched {
		switch w.flagsOf(id) & flagBoth {
		case flagA:
			ahead++
		case flagB:
			behind++
		}
	}
	return ahead, behind, nil
}

// countAncestors counts the commits reachable from sha, capped like the walk.
func (e *Engine) countAncestors(sha string) (int, error) {
	g := e.lockGraph()
	defer e.cgMu.Unlock()
	start, err := g.id(plumbing.NewHash(sha))
	if err != nil {
		return 0, fmt.Errorf("change.divergence: %w", err)
	}
	n := len(g.ancestors(start, describeWalkCap))
	if n > describeWalkCap {
		return 0, fmt.Errorf("change.divergence: ancestry of %s exceeded %d commits", sha[:8], describeWalkCap)
	}
	return n, nil
}
