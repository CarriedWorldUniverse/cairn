package change

import (
	"container/heap"
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// divergence counts the commits reachable from a but not from b (ahead) and
// from b but not from a (behind) — `git rev-list --left-right --count a...b`.
//
// It is a two-colour walk driven by a commit-time heap: each commit popped
// carries the colours that reached it, and a commit reached by BOTH is common
// history whose parents are painted both too. The walk stops once every
// commit still queued is common, so the cost is proportional to the
// DIVERGENCE between a and b, not to the repository's history — which is
// what lets `tree` compute this for every line on a 20-year repository. (The
// earlier first-parent walk from the tip to the line's base ran to the ROOT
// whenever the base sat behind a merge's second parent, reporting the whole
// history as "ahead".) Committer-time ordering has go-git's usual caveat —
// wildly skewed clocks can make the walk visit more than it needs — so it is
// also capped, and a hit on the cap is reported rather than guessed at.
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
	const (
		colA = 1
		colB = 2
		both = colA | colB
	)
	colour := map[plumbing.Hash]int{}
	pq := &commitHeap{}
	push := func(h plumbing.Hash, c int) error {
		prev, seen := colour[h]
		colour[h] = prev | c
		if seen {
			return nil
		}
		commit, err := e.git.CommitObject(h)
		if err != nil {
			return fmt.Errorf("change.divergence: commit %s: %w", h, err)
		}
		heap.Push(pq, commit)
		return nil
	}
	if err := push(plumbing.NewHash(a), colA); err != nil {
		return 0, 0, err
	}
	if err := push(plumbing.NewHash(b), colB); err != nil {
		return 0, 0, err
	}
	visited := 0
	for pq.Len() > 0 {
		if visited++; visited > describeWalkCap {
			return 0, 0, fmt.Errorf("change.divergence: walk exceeded %d commits between %s and %s", describeWalkCap, a[:8], b[:8])
		}
		c := heap.Pop(pq).(*object.Commit)
		col := colour[c.Hash]
		switch col {
		case colA:
			ahead++
		case colB:
			behind++
		}
		for _, p := range c.ParentHashes {
			if err := push(p, col); err != nil {
				return 0, 0, err
			}
		}
		// Once every queued commit is common, nothing exclusive remains.
		allCommon := true
		for _, q := range *pq {
			if colour[q.Hash] != both {
				allCommon = false
				break
			}
		}
		if allCommon {
			break
		}
	}
	return ahead, behind, nil
}

// commitHeap orders commits newest-first by committer time, the usual
// approximation of topological order that keeps the two-colour walk local.
type commitHeap []*object.Commit

func (h commitHeap) Len() int { return len(h) }
func (h commitHeap) Less(i, j int) bool {
	return h[i].Committer.When.After(h[j].Committer.When)
}
func (h commitHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *commitHeap) Push(x any)   { *h = append(*h, x.(*object.Commit)) }
func (h *commitHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// countAncestors counts the commits reachable from sha, capped like the walk.
func (e *Engine) countAncestors(sha string) (int, error) {
	seen := map[plumbing.Hash]struct{}{}
	stack := []plumbing.Hash{plumbing.NewHash(sha)}
	for len(stack) > 0 {
		h := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, ok := seen[h]; ok {
			continue
		}
		seen[h] = struct{}{}
		if len(seen) > describeWalkCap {
			return 0, fmt.Errorf("change.divergence: ancestry of %s exceeded %d commits", sha[:8], describeWalkCap)
		}
		c, err := e.git.CommitObject(h)
		if err != nil {
			return 0, fmt.Errorf("change.divergence: commit %s: %w", h, err)
		}
		stack = append(stack, c.ParentHashes...)
	}
	return len(seen), nil
}
