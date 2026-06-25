package change

import "testing"

func TestCreateLineLineage(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	exp, err := e.CreateLine("exp/idea", main.ID)
	if err != nil {
		t.Fatalf("CreateLine: %v", err)
	}
	idea2, err := e.CreateLine("exp/idea/idea2", exp.ID)
	if err != nil {
		t.Fatalf("CreateLine child: %v", err)
	}
	chain, err := e.GetLineage(idea2.ID)
	if err != nil {
		t.Fatalf("GetLineage: %v", err)
	}
	if len(chain) != 3 || chain[0].Name != "main" || chain[2].Name != "exp/idea/idea2" {
		t.Fatalf("lineage = %v", names(chain))
	}
}

func names(ls []Line) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.Name
	}
	return out
}

func TestGetLineTreeAhead(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	exp, _ := e.CreateLine("exp", main.ID)
	ch, _ := e.CreateChange(exp.ID, "agent-test")
	if _, err := e.Commit(ch.ID, map[string][]byte{"f.txt": []byte("hi\n")}, nil, ""); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	nodes, err := e.GetLineTree()
	if err != nil {
		t.Fatalf("GetLineTree: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(nodes))
	}
	for _, n := range nodes {
		switch n.Line.ID {
		case exp.ID:
			if n.Parent != main.ID {
				t.Fatalf("exp parent = %q, want %q", n.Parent, main.ID)
			}
			if n.Ahead != 1 {
				t.Fatalf("exp Ahead = %d, want 1", n.Ahead)
			}
		case main.ID:
			if n.Ahead != 0 {
				t.Fatalf("main Ahead = %d, want 0", n.Ahead)
			}
		}
	}
}

// TestReparent: a line flat-projected onto the root (as a git import does) can be
// reparented onto its real parent, which updates parent_line and the base; cycles
// and reparenting the root are refused.
func TestReparent(t *testing.T) {
	e := newTestEngine(t)
	root, _ := e.RootLine()
	seedLineTip(t, e, root.ID, map[string][]byte{"m.txt": []byte("m\n")})

	rc, _ := e.CreateLine("rc/4-1", root.ID)
	seedLineTip(t, e, rc.ID, map[string][]byte{"rc.txt": []byte("rc\n")})
	// base is created rooted at the ROOT (the flat-import mistake), not rc.
	base, _ := e.CreateLine("base/5-0", root.ID)
	seedLineTip(t, e, base.ID, map[string][]byte{"base.txt": []byte("b\n")})

	if got, _ := e.lineByID(base.ID); got.ParentLine != root.ID {
		t.Fatalf("precondition: base parent = %q, want root", got.ParentLine)
	}
	if err := e.Reparent(base.ID, rc.ID); err != nil {
		t.Fatalf("Reparent: %v", err)
	}
	got, _ := e.lineByID(base.ID)
	if got.ParentLine != rc.ID {
		t.Fatalf("after reparent, base parent = %q, want rc %q", got.ParentLine, rc.ID)
	}

	// Cycle: rc cannot be reparented onto its now-descendant base.
	if err := e.Reparent(rc.ID, base.ID); err == nil {
		t.Fatal("reparent onto a descendant must be refused (cycle)")
	}
	// The root cannot be reparented.
	if err := e.Reparent(root.ID, rc.ID); err == nil {
		t.Fatal("reparenting the root must be refused")
	}
}
