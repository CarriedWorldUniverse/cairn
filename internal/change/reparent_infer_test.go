package change

import "testing"

// flatten puts every non-root line straight under the root — the shape every
// clone had before parents were inferred (#188).
func flatten(t *testing.T, e *Engine) {
	t.Helper()
	root, err := e.RootLine()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`UPDATE line SET parent_line=? WHERE parent_line IS NOT NULL`, root.ID); err != nil {
		t.Fatal(err)
	}
}

func parentNames(t *testing.T, e *Engine) map[string]string {
	t.Helper()
	nodes, err := e.GetLineTree()
	if err != nil {
		t.Fatal(err)
	}
	nameOf := map[string]string{}
	for _, n := range nodes {
		nameOf[n.Line.ID] = n.Line.Name
	}
	out := map[string]string{}
	for _, n := range nodes {
		if n.Line.ParentLine != "" {
			out[n.Line.Name] = nameOf[n.Line.ParentLine]
		}
	}
	return out
}

// An old flat clone is brought to the shape a fresh clone would have, and a
// second pass finds nothing left to do.
func TestReparentInferRepairsAFlatClone(t *testing.T) {
	tp := newTopo(t)
	tp.commit(t, "A")
	tp.branch(t, "develop")
	tp.commit(t, "C")
	tp.branch(t, "feature")
	tp.commit(t, "E")
	tp.checkout(t, "main")
	tp.branch(t, "hotfix")
	tp.commit(t, "F")
	e, _ := importInto(t, tp)
	flatten(t, e)
	if p := parentNames(t, e); p["feature"] != "main" {
		t.Fatalf("precondition: flatten did not take: %v", p)
	}

	changes, err := e.InferredParentChanges()
	if err != nil {
		t.Fatalf("InferredParentChanges: %v", err)
	}
	if len(changes) != 1 || changes[0].Line != "feature" || changes[0].OldParent != "main" || changes[0].NewParent != "develop" || changes[0].Base != tp.sha["C"] {
		t.Fatalf("changes = %+v, want exactly feature: main -> develop at C", changes)
	}
	if err := e.ApplyParentChanges(changes); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	p := parentNames(t, e)
	if p["feature"] != "develop" || p["develop"] != "main" || p["hotfix"] != "main" {
		t.Fatalf("after apply: %v", p)
	}
	l, _ := e.LineByName("feature")
	if l.BaseCommit != tp.sha["C"] {
		t.Fatalf("feature base = %.8s, want C %.8s", l.BaseCommit, tp.sha["C"])
	}
	again, err := e.InferredParentChanges()
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("second pass still wants changes: %+v", again)
	}
}

// The order test. Recorded: develop under feature (wrong way round). Inferred:
// develop -> main, feature -> develop. Applying feature -> develop FIRST would
// be refused — develop is still recorded as feature's child — so the batch
// must place develop before feature.
func TestReparentInferAppliesParentsBeforeChildren(t *testing.T) {
	tp := newTopo(t)
	tp.commit(t, "A")
	tp.branch(t, "develop")
	tp.commit(t, "C")
	tp.branch(t, "feature")
	tp.commit(t, "E")
	e, _ := importInto(t, tp)
	root, _ := e.RootLine()
	dev, _ := e.LineByName("develop")
	feat, _ := e.LineByName("feature")
	// Invert the recorded stacking: feature under root, develop under feature.
	for _, q := range [][]any{
		{`UPDATE line SET parent_line=? WHERE id=?`, root.ID, feat.ID},
		{`UPDATE line SET parent_line=? WHERE id=?`, feat.ID, dev.ID},
	} {
		if _, err := e.db.Exec(q[0].(string), q[1:]...); err != nil {
			t.Fatal(err)
		}
	}
	changes, err := e.InferredParentChanges()
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("want 2 changes, got %+v", changes)
	}
	if err := e.ApplyParentChanges(changes); err != nil {
		t.Fatalf("Apply hit an intermediate cycle: %v", err)
	}
	p := parentNames(t, e)
	if p["develop"] != "main" || p["feature"] != "develop" {
		t.Fatalf("after apply: %v", p)
	}
}
