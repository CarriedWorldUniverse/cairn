package change

import (
	"fmt"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
)

// NameRevTip is a ref name-rev may name commits from.
type NameRevTip struct {
	Name       string // as it will print: "main", "tags/v1"
	Commit     string // the commit the ref points at (peeled)
	TaggerDate int64  // an annotated tag's date, else the commit's
	FromTag    bool
	Deref      bool // the ref is an annotated tag: the tip's own name carries ^0
}

type revName struct {
	tip        string
	taggerDate int64
	generation int
	distance   int
	fromTag    bool
}

// mergeTraversalWeight is git's: stepping through a merge's later parent
// counts as this many ordinary steps, so first-parent names are preferred.
const mergeTraversalWeight = 65535

// NameRev names each target after the tips that reach it, as git name-rev
// does: <tip>~<n> along first parents, ^<k> through a merge's other parents;
// tags beat other refs, older tags beat newer, then the fewest steps. A
// target no tip reaches maps to "". tips must be in refname order (ties go to
// the earlier).
func (e *Engine) NameRev(tips []NameRevTip, targets []string) (map[string]string, error) {
	g := e.lockGraph()
	defer e.cgMu.Unlock()
	cutoff := int64(1<<62 - 1)
	tids := make([]int32, len(targets))
	for i, t := range targets {
		id, err := g.id(plumbing.NewHash(t))
		if err != nil {
			return nil, fmt.Errorf("change.NameRev: %w", err)
		}
		tids[i] = id
		if tm := g.nodes[id].time; tm < cutoff {
			cutoff = tm
		}
	}
	cutoff -= 86400
	names := make([]*revName, len(g.nodes))
	better := func(n *revName, date int64, distance int, fromTag bool) bool {
		if fromTag && n.fromTag {
			return n.taggerDate > date
		}
		if n.fromTag != fromTag {
			return fromTag
		}
		if n.distance != distance {
			return n.distance > distance
		}
		return n.taggerDate > date
	}
	type item struct{ id int32 }
	for _, tip := range tips {
		start, err := g.id(plumbing.NewHash(tip.Commit))
		if err != nil {
			continue
		}
		if g.nodes[start].time < cutoff {
			continue
		}
		tipName := tip.Name
		if tip.Deref {
			tipName += "^0"
		}
		if n := names[start]; n != nil && !better(n, tip.TaggerDate, 0, tip.FromTag) {
			continue
		}
		names[start] = &revName{tip: tipName, taggerDate: tip.TaggerDate, fromTag: tip.FromTag}
		stack := []int32{start}
		for len(stack) > 0 {
			id := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			cur := names[id]
			for i, p := range g.parentsOf(id) {
				if g.nodes[p].time < cutoff {
					continue
				}
				var nn revName
				if i > 0 {
					base := strings.TrimSuffix(cur.tip, "^0")
					nn = revName{taggerDate: cur.taggerDate, fromTag: cur.fromTag, distance: cur.distance + mergeTraversalWeight}
					if cur.generation > 0 {
						nn.tip = fmt.Sprintf("%s~%d^%d", base, cur.generation, i+1)
					} else {
						nn.tip = fmt.Sprintf("%s^%d", base, i+1)
					}
				} else {
					nn = revName{tip: cur.tip, taggerDate: cur.taggerDate, fromTag: cur.fromTag, generation: cur.generation + 1, distance: cur.distance + 1}
				}
				if old := names[p]; old != nil && !better(old, nn.taggerDate, nn.distance, nn.fromTag) {
					continue
				}
				names[p] = &nn
				stack = append(stack, p)
			}
		}
	}
	out := make(map[string]string, len(targets))
	for i, t := range targets {
		n := names[tids[i]]
		switch {
		case n == nil:
			out[t] = ""
		case n.generation == 0:
			out[t] = n.tip
		default:
			out[t] = fmt.Sprintf("%s~%d", strings.TrimSuffix(n.tip, "^0"), n.generation)
		}
	}
	return out, nil
}
