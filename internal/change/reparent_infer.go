package change

import (
	"fmt"
	"sort"
)

// ParentChange is one line whose inferred parent differs from its recorded
// one. Names, not ids, so it can be printed as-is; Base is the fork commit the
// inference found ("" when the line only relates to the trunk by merge-base,
// in which case the reparent computes it).
type ParentChange struct {
	Line      string
	OldParent string
	NewParent string
	Base      string
}

// InferredParentChanges re-derives every open line's parent from topology —
// the same inference a clone runs (inferParents) — and returns the lines
// whose recorded parent disagrees. A clone made before parents were
// inferred holds every branch flat under the root; this is how such a clone
// is brought to the same shape a fresh one would have. Nothing is changed.
func (e *Engine) InferredParentChanges() ([]ParentChange, error) {
	root, err := e.RootLine()
	if err != nil {
		return nil, fmt.Errorf("change.InferredParentChanges: %w", err)
	}
	nodes, err := e.GetLineTree()
	if err != nil {
		return nil, fmt.Errorf("change.InferredParentChanges: %w", err)
	}
	byID := map[string]Line{}
	heads := map[string]string{}
	for _, n := range nodes {
		l := n.Line
		if l.Status != "open" {
			continue
		}
		byID[l.ID] = l
		tip, err := e.sealedTip(l)
		if err != nil {
			return nil, fmt.Errorf("change.InferredParentChanges: %s: %w", l.Name, err)
		}
		if tip != "" {
			heads[l.Name] = tip
		}
	}
	trunk, err := e.ancestorSet(heads[root.Name])
	if err != nil {
		return nil, fmt.Errorf("change.InferredParentChanges: %w", err)
	}
	picks, err := e.inferParents(root.Name, heads, trunk)
	if err != nil {
		return nil, err
	}
	var out []ParentChange
	for _, l := range byID {
		if l.ID == root.ID {
			continue
		}
		pick, ok := picks[l.Name]
		if !ok {
			continue // no sealed tip: nothing to infer from
		}
		want := root.Name
		if pick.Parent != "" {
			want = pick.Parent
		}
		cur := root.Name
		if p, ok := byID[l.ParentLine]; ok {
			cur = p.Name
		}
		if want != cur {
			out = append(out, ParentChange{Line: l.Name, OldParent: cur, NewParent: want, Base: pick.Base})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out, nil
}

// ApplyParentChanges records each change through reparentAt, in breadth-first
// order over the NEW tree. Order matters: Reparent refuses to hang a line
// under its own descendant, and a batch applied in arbitrary order can hit
// that on an intermediate state (moving A under B while B is still recorded
// as A's child) even though the final tree is acyclic. Placing every parent
// before its children means a line's new parent is already at its final
// position when the line moves, so no intermediate cycle can appear.
func (e *Engine) ApplyParentChanges(changes []ParentChange) error {
	if len(changes) == 0 {
		return nil
	}
	nodes, err := e.GetLineTree()
	if err != nil {
		return fmt.Errorf("change.ApplyParentChanges: %w", err)
	}
	byName := map[string]Line{}
	nameOf := map[string]string{} // id -> name (LineNode.Parent is an id)
	for _, n := range nodes {
		byName[n.Line.Name] = n.Line
		nameOf[n.Line.ID] = n.Line.Name
	}
	newParent := map[string]string{} // line name -> parent name in the new tree
	var rootName string
	for _, n := range nodes {
		if n.Line.ParentLine == "" {
			rootName = n.Line.Name
			continue
		}
		newParent[n.Line.Name] = nameOf[n.Line.ParentLine]
	}
	pending := map[string]ParentChange{}
	for _, c := range changes {
		newParent[c.Line] = c.NewParent
		pending[c.Line] = c
	}
	children := map[string][]string{}
	for name, p := range newParent {
		children[p] = append(children[p], name)
	}
	for _, kids := range children {
		sort.Strings(kids)
	}
	queue := []string{rootName}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if c, ok := pending[cur]; ok {
			line, np := byName[c.Line], byName[c.NewParent]
			if line.ID == "" || np.ID == "" {
				return fmt.Errorf("change.ApplyParentChanges: unknown line in %s -> %s", c.Line, c.NewParent)
			}
			base := c.Base
			if base == "" {
				if mb, mberr := e.mergeBase(line.TipCommit, np.TipCommit); mberr == nil && mb != "" {
					base = mb
				} else {
					base = np.TipCommit
				}
			}
			if err := e.reparentAt(line, np, base); err != nil {
				return fmt.Errorf("change.ApplyParentChanges: %s: %w", c.Line, err)
			}
		}
		queue = append(queue, children[cur]...)
	}
	return nil
}
