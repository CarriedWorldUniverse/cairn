package change

import "testing"

// divergence is rev-list --left-right --count: exclusive commits on each
// side. The merge case is the one that broke the old first-parent count.
func TestDivergenceCountsExclusiveCommits(t *testing.T) {
	tp := newTopo(t)
	tp.commit(t, "A")
	tp.commit(t, "B")
	tp.branch(t, "feature")
	tp.commit(t, "F1")
	tp.commit(t, "F2")
	tp.checkout(t, "main")
	tp.commit(t, "C")
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	if _, err := e.ImportFromRemote(tp.dir); err != nil {
		t.Fatalf("import: %v", err)
	}
	cases := []struct {
		name          string
		a, b          string
		ahead, behind int
	}{
		{"equal", tp.sha["F2"], tp.sha["F2"], 0, 0},
		{"feature vs main: 2 ahead 1 behind", tp.sha["F2"], tp.sha["C"], 2, 1},
		{"main vs feature", tp.sha["C"], tp.sha["F2"], 1, 2},
		{"linear ahead", tp.sha["F2"], tp.sha["B"], 2, 0},
		{"linear behind", tp.sha["B"], tp.sha["F2"], 0, 2},
		{"vs root", tp.sha["C"], tp.sha["A"], 2, 0},
	}
	for _, c := range cases {
		ahead, behind, err := e.divergence(c.a, c.b)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if ahead != c.ahead || behind != c.behind {
			t.Errorf("%s: ahead=%d behind=%d, want %d/%d", c.name, ahead, behind, c.ahead, c.behind)
		}
	}
}

// The first-parent walk reported the whole history when the base sat behind
// a merge's second parent. With the base merged INTO the line (the line's
// tip is a merge whose second parent is main's tip), the divergence from
// main is exactly the line's own commits.
func TestDivergenceIsRightAcrossAMerge(t *testing.T) {
	tp := newTopo(t)
	tp.commit(t, "A")
	tp.branch(t, "feature")
	tp.commit(t, "F1")
	tp.checkout(t, "main")
	tp.commit(t, "C")
	tp.checkout(t, "feature")
	tp.mergeFrom(t, "main", "M") // feature: M(F1, C)
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	if _, err := e.ImportFromRemote(tp.dir); err != nil {
		t.Fatalf("import: %v", err)
	}
	ahead, behind, err := e.divergence(tp.sha["M"], tp.sha["C"])
	if err != nil {
		t.Fatal(err)
	}
	if ahead != 2 || behind != 0 {
		t.Fatalf("feature(merge) vs main: ahead=%d behind=%d, want 2/0 (F1 + the merge)", ahead, behind)
	}
}
