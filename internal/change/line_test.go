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

func TestGetLineTreeAheadBehind(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	exp, _ := e.CreateLine("exp", main.ID)
	nodes, err := e.GetLineTree()
	if err != nil {
		t.Fatalf("GetLineTree: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(nodes))
	}
	for _, n := range nodes {
		if n.Line.ID == exp.ID && n.Parent != main.ID {
			t.Fatalf("exp parent = %q, want %q", n.Parent, main.ID)
		}
	}
}
