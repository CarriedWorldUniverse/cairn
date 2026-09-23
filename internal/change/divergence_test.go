package change

import (
	"testing"
	"time"
)

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

// A PR that merged main in, with every commit stamped in the same second —
// what a scripted rebase, a bot, or a fast operator produces. Equal
// timestamps let the heap pop a shared commit before the other side has
// reached it; the walk counted it as exclusive and never took it back, so
// the line showed main's merged-in commits as its own (#193).
func TestDivergenceWithEqualTimestamps(t *testing.T) {
	saved := topoClock
	defer func() { topoClock = saved }()
	tp := newTopo(t)
	tp.frozen = true
	tp.commit(t, "A")
	tp.branch(t, "feature")
	tp.commit(t, "F1")
	tp.checkout(t, "main")
	tp.commit(t, "M1")
	tp.commit(t, "M2")
	tp.commit(t, "M3")
	tp.checkout(t, "feature")
	tp.mergeFrom(t, "main", "X") // feature: X(F1, M3)
	tp.commit(t, "F2")
	e := importTopo(t, tp)
	ahead, behind, err := e.divergence(tp.sha["F2"], tp.sha["M3"])
	if err != nil {
		t.Fatal(err)
	}
	if ahead != 3 || behind != 0 {
		t.Fatalf("feature vs main: ahead=%d behind=%d, want 3/0 (F1, the merge, F2)", ahead, behind)
	}
}

// A skewed clock: main's commits are dated BEFORE the root they descend
// from, so the time order is the reverse of the topology. The count must
// still come from the graph, not the clock.
func TestDivergenceWithASkewedClock(t *testing.T) {
	saved := topoClock
	defer func() { topoClock = saved }()
	tp := newTopo(t)
	tp.commit(t, "A")
	tp.branch(t, "feature")
	tp.commit(t, "F1")
	tp.checkout(t, "main")
	topoClock = topoClock.Add(-24 * time.Hour)
	tp.commit(t, "M1")
	tp.commit(t, "M2")
	tp.checkout(t, "feature")
	topoClock = topoClock.Add(48 * time.Hour)
	tp.mergeFrom(t, "main", "X")
	e := importTopo(t, tp)
	ahead, behind, err := e.divergence(tp.sha["X"], tp.sha["M2"])
	if err != nil {
		t.Fatal(err)
	}
	if ahead != 2 || behind != 0 {
		t.Fatalf("feature vs main: ahead=%d behind=%d, want 2/0 (F1 and the merge)", ahead, behind)
	}
}

func importTopo(t *testing.T, tp *topo) *Engine {
	t.Helper()
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if _, err := e.ImportFromRemote(tp.dir); err != nil {
		t.Fatalf("import: %v", err)
	}
	return e
}
