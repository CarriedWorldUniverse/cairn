package change

import (
	"errors"
	"fmt"
	"sort"

	"github.com/go-git/go-git/v5/plumbing"
)

// parentPick is where a branch belongs in the line tree: the branch it forked
// from and the commit it forked at. Parent "" means the trunk (the default
// branch); Base "" means no common history was found at all.
type parentPick struct {
	Parent string
	Base   string
}

// inferParents places every non-default branch under its NEAREST ancestor
// branch rather than flat under the trunk.
//
// git has no notion of a branch's parent, so this is inference from
// topology. For each branch B, walk its first-parent history from the tip
// down to the first commit that is on the trunk. The parent is the other
// branch P whose own first-parent history passes through the commit on that
// walk NEAREST B's tip — the branch B most recently diverged from — and the
// base is that commit. A branch that descends from B (B's tip lies on P's
// walk) is never B's parent: it is B's child. If no other branch shares any
// of B's non-trunk history, B hangs off the trunk at its merge-base, exactly
// as before.
//
//	main:    A ── B
//	develop:      └── C ── D
//	feature:               └── E        feature → develop at D, ahead 1
//	hotfix:  └── F                     hotfix  → main at A
//
// Where two branches genuinely share the fork commit and neither descends
// from the other — B forked from P at c and P has moved on, or P forked from
// B at c and B has moved on — topology alone cannot say which came first.
// The tie is broken deterministically: the default branch wins, then the
// name that sorts first. `cairn reparent` corrects the rare wrong guess.
//
// Cost is one first-parent walk per branch, stopping at the trunk, so it is
// proportional to the branches' own history, not the repository's (#148 made
// the same distinction for the trunk merge-base).
func (e *Engine) inferParents(def string, heads map[string]string, trunk map[plumbing.Hash]struct{}) (map[string]parentPick, error) {
	type walk struct {
		chain     []plumbing.Hash // non-trunk first-parent history, tip first
		trunkBase string          // first trunk commit reached, "" if none
	}
	walks := map[string]walk{}
	onChain := map[plumbing.Hash]map[string]struct{}{}
	names := make([]string, 0, len(heads))
	for name, sha := range heads {
		if name == def || sha == "" {
			continue
		}
		names = append(names, name)
		w, err := e.firstParentToTrunk(sha, trunk)
		if err != nil {
			return nil, err
		}
		walks[name] = w
		for _, h := range w.chain {
			if onChain[h] == nil {
				onChain[h] = map[string]struct{}{}
			}
			onChain[h][name] = struct{}{}
		}
	}
	sort.Strings(names)

	// rank: lower is preferred as a parent. The default branch is never a
	// candidate here (it is the trunk); among the rest, name order.
	rank := map[string]int{}
	for i, n := range names {
		rank[n] = i
	}
	contains := func(p string, h plumbing.Hash) bool {
		_, ok := onChain[h][p]
		return ok
	}

	out := make(map[string]parentPick, len(names))
	for _, b := range names {
		w := walks[b]
		pick := parentPick{Parent: "", Base: w.trunkBase}
		if len(w.chain) > 0 {
			tip := w.chain[0]
			for _, c := range w.chain {
				var best string
				for p := range onChain[c] {
					if p == b {
						continue
					}
					// P descends from B (B's tip is on P's walk): P is a child,
					// not a parent — unless the tips coincide, where the rank
					// decides which of the twins is the parent so the pair
					// cannot point at each other.
					if contains(p, tip) {
						if plumbing.NewHash(heads[p]) != tip || rank[p] > rank[b] {
							continue
						}
					}
					// P's tip IS this commit: P has not moved since B forked, so
					// P is unambiguously the ancestor. Otherwise both have moved
					// past c and topology cannot order them; only a higher-ranked
					// branch may be the parent, so the pair cannot pick each other.
					if plumbing.NewHash(heads[p]) != c && rank[p] > rank[b] {
						continue
					}
					if best == "" || rank[p] < rank[best] {
						best = p
					}
				}
				if best != "" {
					pick = parentPick{Parent: best, Base: c.String()}
					break
				}
			}
		}
		out[b] = pick
	}
	// A cycle in parent links would make every lineage walk loop forever.
	// The rules above are meant to make one impossible; this is the belt to
	// those braces: follow each branch's parents and, on a cycle, drop its
	// lowest-ranked member to the trunk.
	for _, b := range names {
		seen := map[string]bool{}
		for cur := b; cur != ""; cur = out[cur].Parent {
			if seen[cur] {
				worst := cur
				for x := out[cur].Parent; x != cur; x = out[x].Parent {
					if rank[x] > rank[worst] {
						worst = x
					}
				}
				out[worst] = parentPick{Parent: "", Base: walks[worst].trunkBase}
				break
			}
			seen[cur] = true
		}
	}
	return out, nil
}

// firstParentToTrunk walks first parents from sha until a commit in trunk,
// returning the non-trunk commits (tip first) and the trunk commit reached.
func (e *Engine) firstParentToTrunk(sha string, trunk map[plumbing.Hash]struct{}) (struct {
	chain     []plumbing.Hash
	trunkBase string
}, error) {
	var w struct {
		chain     []plumbing.Hash
		trunkBase string
	}
	h := plumbing.NewHash(sha)
	for {
		if _, ok := trunk[h]; ok {
			w.trunkBase = h.String()
			return w, nil
		}
		c, err := e.git.CommitObject(h)
		if err != nil {
			if errors.Is(err, plumbing.ErrObjectNotFound) {
				return w, nil
			}
			return w, fmt.Errorf("change.inferParents: commit %s: %w", h, err)
		}
		w.chain = append(w.chain, h)
		if c.NumParents() == 0 {
			return w, nil // unrelated history: no trunk commit at all
		}
		p, err := c.Parent(0)
		if err != nil {
			return w, fmt.Errorf("change.inferParents: parent of %s: %w", h, err)
		}
		h = p.Hash
	}
}
