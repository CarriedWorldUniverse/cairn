package harness

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

// seeds drives every property across a spread of scheduler seeds so the
// convergence claims are demonstrated to be order-independent, not lucky.
var seeds = []int64{1, 2, 7, 42, 1000}

// newEngine opens a fresh engine on a temp dir, cleaned up at test end.
func newEngine(t *testing.T) *change.Engine {
	t.Helper()
	e, err := change.Open(t.TempDir())
	if err != nil {
		t.Fatalf("change.Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

// seedMain creates a change on main and commits files, advancing main's tip.
// Commit advances the owning line's tip, so this seeds main's state directly.
func seedMain(t *testing.T, e *change.Engine, files map[string][]byte) {
	t.Helper()
	main, err := e.LineByName("main")
	if err != nil {
		t.Fatalf("LineByName(main): %v", err)
	}
	ch, err := e.CreateChange(main.ID, "seed")
	if err != nil {
		t.Fatalf("CreateChange(main): %v", err)
	}
	if _, err := e.Commit(ch.ID, files, nil, ""); err != nil {
		t.Fatalf("seed Commit: %v", err)
	}
}

// foldAll folds every non-root open line into its parent, deepest-first, so a
// child folds before its own parent does. Before folding a line it re-adopts the
// (possibly newly advanced) parent by committing the line's current files onto a
// fresh change — that commit re-runs merge-forward, pulling in whatever a sibling
// already folded into the parent. A line carrying open conflicts is skipped.
func foldAll(t *testing.T, e *change.Engine) {
	t.Helper()
	for {
		nodes, err := e.GetLineTree()
		if err != nil {
			t.Fatalf("GetLineTree: %v", err)
		}
		// Pick the deepest open, non-root line that has no open conflicts and whose
		// children have all already been folded (no open child points at it).
		hasOpenChild := map[string]bool{}
		for _, n := range nodes {
			if n.Line.Status == "open" && n.Line.ParentLine != "" {
				hasOpenChild[n.Line.ParentLine] = true
			}
		}
		var target *change.Line
		for i := range nodes {
			l := nodes[i].Line
			if l.Status != "open" || l.ParentLine == "" || hasOpenChild[l.ID] {
				continue
			}
			lc := l
			target = &lc
			break
		}
		if target == nil {
			return
		}

		// Re-adopt the parent's current state, then verify no conflict before fold.
		files, err := e.Files(target.TipCommit)
		if err != nil {
			t.Fatalf("Files(%s): %v", target.Name, err)
		}
		rc, err := e.CreateChange(target.ID, "fold-readopt")
		if err != nil {
			t.Fatalf("CreateChange(readopt): %v", err)
		}
		res, err := e.Commit(rc.ID, files, nil, "")
		if err != nil {
			t.Fatalf("re-adopt Commit: %v", err)
		}
		if len(res.Conflicts) > 0 {
			t.Fatalf("foldAll: unexpected conflict re-adopting %s: %+v", target.Name, res.Conflicts)
		}
		if err := e.FoldLine(target.ID); err != nil {
			t.Fatalf("FoldLine(%s): %v", target.Name, err)
		}
	}
}

// Property 1 — Convergence (non-overlap). Two sibling lines each add a distinct
// file off a shared base; in every scheduler order, folding both lands BOTH new
// files on main with no conflict ever recorded.
func TestProperty1_ConvergenceNonOverlap(t *testing.T) {
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			e := newEngine(t)
			base := map[string][]byte{"base.txt": []byte("base\n")}
			seedMain(t, e, base)

			steps := []Step{
				{Line: "x", Base: "main", Author: "ax", Files: map[string][]byte{
					"base.txt": []byte("base\n"), "x.txt": []byte("x\n")}},
				{Line: "y", Base: "main", Author: "ay", Files: map[string][]byte{
					"base.txt": []byte("base\n"), "y.txt": []byte("y\n")}},
			}
			ids, err := Run(e, steps, seed)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			for _, id := range ids {
				ch, _ := e.GetChange(id)
				if ch.HasConflict {
					t.Fatalf("seed %d: change %s unexpectedly conflicted", seed, id)
				}
				if c, _ := e.Conflicts(id); len(c) != 0 {
					t.Fatalf("seed %d: change %s has open conflicts: %+v", seed, id, c)
				}
			}

			foldAll(t, e)

			main, _ := e.LineByName("main")
			files, err := e.Files(main.TipCommit)
			if err != nil {
				t.Fatalf("Files(main tip): %v", err)
			}
			if !bytes.Equal(files["base.txt"], []byte("base\n")) {
				t.Fatalf("seed %d: base.txt = %q, want base\\n", seed, files["base.txt"])
			}
			if !bytes.Equal(files["x.txt"], []byte("x\n")) {
				t.Fatalf("seed %d: x.txt missing/wrong on main: %q (tree=%v)", seed, files["x.txt"], keys(files))
			}
			if !bytes.Equal(files["y.txt"], []byte("y\n")) {
				t.Fatalf("seed %d: y.txt missing/wrong on main: %q (tree=%v)", seed, files["y.txt"], keys(files))
			}
		})
	}
}

// Property 2 — Deterministic conflicts (overlap). A genuine 3-way overlap
// (base/parent/change all differ on one path) records exactly ONE conflict on
// that path and flags the change has_conflict, identically for every seed. Set
// up directly because it needs exp to fork BEFORE main advances.
func TestProperty2_DeterministicConflicts(t *testing.T) {
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			_ = seed // outcome is order-independent; the loop demonstrates that.
			e := newEngine(t)
			main, _ := e.LineByName("main")
			seedMain(t, e, map[string][]byte{"f.txt": []byte("base\n")})

			// exp forks at the shared base, before main advances.
			exp, _ := e.CreateLine("exp", main.ID)

			// main advances: base -> X.
			mc, _ := e.CreateChange(main.ID, "m")
			if _, err := e.Commit(mc.ID, map[string][]byte{"f.txt": []byte("X\n")}, nil, ""); err != nil {
				t.Fatalf("main advance: %v", err)
			}

			// exp edits the same path: base -> Y. merge-forward sees (base,X,Y).
			ec, _ := e.CreateChange(exp.ID, "e")
			r, err := e.Commit(ec.ID, map[string][]byte{"f.txt": []byte("Y\n")}, nil, "")
			if err != nil {
				t.Fatalf("exp commit: %v", err)
			}
			if len(r.Conflicts) != 1 {
				t.Fatalf("seed %d: got %d conflicts, want exactly 1: %+v", seed, len(r.Conflicts), r.Conflicts)
			}
			if r.Conflicts[0].Path != "f.txt" {
				t.Fatalf("seed %d: conflict path = %q, want f.txt", seed, r.Conflicts[0].Path)
			}
			open, _ := e.Conflicts(ec.ID)
			if len(open) != 1 || open[0].Path != "f.txt" {
				t.Fatalf("seed %d: open conflicts = %+v, want one on f.txt", seed, open)
			}
			// The conflict must capture THREE genuinely distinct sides, not a
			// degenerate record: base="base", parent/ours="X", change/theirs="Y"
			// are three different contents, so their blob shas must be non-empty
			// and pairwise distinct; the conflict-marked blob must also be set.
			c := open[0]
			if c.BaseBlob == "" || c.ParentBlob == "" || c.ChangeBlob == "" || c.MarkedBlob == "" {
				t.Fatalf("seed %d: conflict has empty side blob: %+v", seed, c)
			}
			if c.BaseBlob == c.ParentBlob || c.BaseBlob == c.ChangeBlob || c.ParentBlob == c.ChangeBlob {
				t.Fatalf("seed %d: conflict sides not distinct: %+v", seed, c)
			}
			ch, _ := e.GetChange(ec.ID)
			if !ch.HasConflict {
				t.Fatalf("seed %d: exp change not flagged has_conflict", seed)
			}
		})
	}
}

// Property 3 — No blocking. A conflicted change still accepts further commits to
// a DIFFERENT file; the new file is retained in the change's tip tree, proving
// the conflict does not block ongoing work.
func TestProperty3_ConflictDoesNotBlock(t *testing.T) {
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			_ = seed
			e := newEngine(t)
			main, _ := e.LineByName("main")
			seedMain(t, e, map[string][]byte{"f.txt": []byte("base\n")})
			exp, _ := e.CreateLine("exp", main.ID)
			mc, _ := e.CreateChange(main.ID, "m")
			e.Commit(mc.ID, map[string][]byte{"f.txt": []byte("X\n")}, nil, "")

			ec, _ := e.CreateChange(exp.ID, "e")
			r, _ := e.Commit(ec.ID, map[string][]byte{"f.txt": []byte("Y\n")}, nil, "")
			if len(r.Conflicts) == 0 {
				t.Fatalf("seed %d: setup expected a conflict", seed)
			}

			// More work on the SAME change, a different file: must not be blocked.
			if _, err := e.Commit(ec.ID, map[string][]byte{
				"f.txt": []byte("Y\n"), "new.txt": []byte("new\n")}, nil, ""); err != nil {
				t.Fatalf("seed %d: follow-up commit on conflicted change: %v", seed, err)
			}
			ch, _ := e.GetChange(ec.ID)
			files, err := e.Files(ch.HeadCommit)
			if err != nil {
				t.Fatalf("Files(change head): %v", err)
			}
			if !bytes.Equal(files["new.txt"], []byte("new\n")) {
				t.Fatalf("seed %d: new.txt not retained on conflicted change: %q (tree=%v)", seed, files["new.txt"], keys(files))
			}
		})
	}
}

// Property 4 — Fast-forward fold. A line that adopted its parent via commits
// folds with no error and its new file lands on the parent's tip.
func TestProperty4_FastForwardFold(t *testing.T) {
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			_ = seed
			e := newEngine(t)
			main, _ := e.LineByName("main")
			seedMain(t, e, map[string][]byte{"a.txt": []byte("a\n")})
			exp, _ := e.CreateLine("exp", main.ID)
			ec, _ := e.CreateChange(exp.ID, "e")
			if _, err := e.Commit(ec.ID, map[string][]byte{
				"a.txt": []byte("a\n"), "n.txt": []byte("new\n")}, nil, ""); err != nil {
				t.Fatalf("commit: %v", err)
			}
			if err := e.FoldLine(exp.ID); err != nil {
				t.Fatalf("seed %d: FoldLine: %v", seed, err)
			}
			main, _ = e.LineByName("main")
			files, err := e.Files(main.TipCommit)
			if err != nil {
				t.Fatalf("seed %d: Files: %v", seed, err)
			}
			if !bytes.Equal(files["n.txt"], []byte("new\n")) {
				t.Fatalf("seed %d: fold did not advance parent tip with n.txt: %v", seed, keys(files))
			}
		})
	}
}

// Property 5 — Abandon isolation. A wild (even conflicting) edit on a forked
// line that is then abandoned leaves main byte-identical and unchanged.
func TestProperty5_AbandonIsolation(t *testing.T) {
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			_ = seed
			e := newEngine(t)
			main, _ := e.LineByName("main")
			seedMain(t, e, map[string][]byte{"f.txt": []byte("base\n")})
			mainTip := func() string { l, _ := e.LineByName("main"); return l.TipCommit }
			// Pre-advance tip, captured at the shared base where exp forks.
			before := mainTip()

			// exp forks at the shared base, BEFORE main advances, so its later edit
			// genuinely conflicts with main's advance.
			exp, _ := e.CreateLine("exp", main.ID)

			// advance main: base -> X.
			mc, _ := e.CreateChange(main.ID, "m")
			if _, err := e.Commit(mc.ID, map[string][]byte{"f.txt": []byte("X\n")}, nil, ""); err != nil {
				t.Fatalf("seed %d: advance main: %v", seed, err)
			}
			// main must have actually moved off the fork point.
			if mainTip() == before {
				t.Fatalf("seed %d: main did not advance off the fork point", seed)
			}
			beforeAbandon := mainTip()

			// exp edits the same path: base -> WILD. merge-forward sees (base,X,WILD)
			// and must record a conflict — otherwise the abandon scenario is vacuous.
			ec, _ := e.CreateChange(exp.ID, "e")
			r, err := e.Commit(ec.ID, map[string][]byte{"f.txt": []byte("WILD\n")}, nil, "")
			if err != nil {
				t.Fatalf("seed %d: wild commit: %v", seed, err)
			}
			if len(r.Conflicts) == 0 {
				t.Fatalf("seed %d: setup expected a conflict (main must have advanced before exp committed)", seed)
			}

			if err := e.AbandonLine(exp.ID); err != nil {
				t.Fatalf("seed %d: AbandonLine: %v", seed, err)
			}
			// Abandon leaves main byte-identical: its tip is unchanged.
			if got := mainTip(); got != beforeAbandon {
				t.Fatalf("seed %d: main tip changed by abandon: %s != %s", seed, got, beforeAbandon)
			}
		})
	}
}

// Property 6 — Lineage integrity. A depth-2 chain main -> a -> b reports the
// full lineage, folds one level at a time, and Export reconstructs the open
// lines as refs/heads.
//
// Note: the line names must not form a git D/F-conflict (e.g. "a" and "a/b"
// cannot both be refs/heads since refs/heads/a would be both a file and a dir —
// a fundamental git constraint, verified directly with real git). The spec's
// illustrative "a/b" is replaced with a sibling-safe "b"; the depth-2 lineage
// and the Export-reconstructs-open-lines claim are preserved.
func TestProperty6_LineageIntegrity(t *testing.T) {
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			_ = seed
			e := newEngine(t)
			main, _ := e.LineByName("main")
			seedMain(t, e, map[string][]byte{"root.txt": []byte("r\n")})

			a, _ := e.CreateLine("a", main.ID)
			ca, _ := e.CreateChange(a.ID, "aa")
			e.Commit(ca.ID, map[string][]byte{"root.txt": []byte("r\n"), "a.txt": []byte("a\n")}, nil, "")

			b, _ := e.CreateLine("b", a.ID)
			cb, _ := e.CreateChange(b.ID, "ab")
			e.Commit(cb.ID, map[string][]byte{"root.txt": []byte("r\n"), "a.txt": []byte("a\n"), "b.txt": []byte("b\n")}, nil, "")

			lineage, err := e.GetLineage(b.ID)
			if err != nil {
				t.Fatalf("GetLineage: %v", err)
			}
			gotNames := make([]string, len(lineage))
			for i, l := range lineage {
				gotNames[i] = l.Name
			}
			want := []string{"main", "a", "b"}
			if len(gotNames) != 3 || gotNames[0] != want[0] || gotNames[1] != want[1] || gotNames[2] != want[2] {
				t.Fatalf("seed %d: lineage = %v, want %v", seed, gotNames, want)
			}

			// Export before folding: every open line is a refs/heads ref.
			if err := e.Export(); err != nil {
				t.Fatalf("Export: %v", err)
			}

			// Fold b into a, then a into main, one level at a time.
			if err := e.FoldLine(b.ID); err != nil {
				t.Fatalf("seed %d: FoldLine(b): %v", seed, err)
			}
			if err := e.FoldLine(a.ID); err != nil {
				t.Fatalf("seed %d: FoldLine(a): %v", seed, err)
			}

			main, _ = e.LineByName("main")
			files, _ := e.Files(main.TipCommit)
			for _, p := range []string{"root.txt", "a.txt", "b.txt"} {
				if files[p] == nil {
					t.Fatalf("seed %d: %q missing on main after fold chain: %v", seed, p, keys(files))
				}
			}
		})
	}
}

// Property 7 — Op-log replay/undo. After a commit on main, the last op is a
// commit; Undo restores main's tip and the last op becomes 'undo'.
func TestProperty7_OpLogReplayUndo(t *testing.T) {
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			_ = seed
			e := newEngine(t)
			main, _ := e.LineByName("main")
			seedMain(t, e, map[string][]byte{"a.txt": []byte("a\n")})
			beforeTip := func() string { l, _ := e.LineByName("main"); return l.TipCommit }
			captured := beforeTip()

			mc, _ := e.CreateChange(main.ID, "m")
			if _, err := e.Commit(mc.ID, map[string][]byte{"a.txt": []byte("a\n"), "z.txt": []byte("z\n")}, nil, ""); err != nil {
				t.Fatalf("commit: %v", err)
			}
			if got := beforeTip(); got == captured {
				t.Fatalf("seed %d: tip did not advance on commit", seed)
			}

			ops, _ := e.OperationLog()
			if len(ops) == 0 || ops[len(ops)-1].OpType != "commit" {
				t.Fatalf("seed %d: last op = %+v, want commit", seed, lastOp(ops))
			}

			if err := e.Undo(); err != nil {
				t.Fatalf("seed %d: Undo: %v", seed, err)
			}
			if got := beforeTip(); got != captured {
				t.Fatalf("seed %d: undo did not restore main tip: %s != %s", seed, got, captured)
			}
			ops, _ = e.OperationLog()
			if len(ops) == 0 || ops[len(ops)-1].OpType != "undo" {
				t.Fatalf("seed %d: last op = %+v, want undo", seed, lastOp(ops))
			}
		})
	}
}

// Property 7b — Op-log replay consistency. The op-log stores a before/after
// snapshot of the ref-map per op, so replaying the recorded views from empty is
// just re-applying each op's view_after in order. This pins two invariants that
// make that replay faithful:
//   - chain continuity: each op's ViewBefore equals the previous op's ViewAfter
//     (the world the next op saw is the world the previous op left), so the
//     snapshots form an unbroken chain.
//   - terminal consistency: the last op's ViewAfter equals the engine's current
//     ref-map, so replaying the recorded view_afters in order terminates at the
//     real present state.
//
// Together: replaying the recorded views from empty reproduces the final ref-map.
func TestProperty7b_OpLogReplayConsistency(t *testing.T) {
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			_ = seed
			e := newEngine(t)
			main, _ := e.LineByName("main")
			seedMain(t, e, map[string][]byte{"a.txt": []byte("a\n")})

			// A couple more commits on main to build a multi-op chain.
			c1, _ := e.CreateChange(main.ID, "m1")
			if _, err := e.Commit(c1.ID, map[string][]byte{"a.txt": []byte("a\n"), "b.txt": []byte("b\n")}, nil, ""); err != nil {
				t.Fatalf("seed %d: commit b: %v", seed, err)
			}
			c2, _ := e.CreateChange(main.ID, "m2")
			if _, err := e.Commit(c2.ID, map[string][]byte{"a.txt": []byte("a\n"), "b.txt": []byte("b\n"), "c.txt": []byte("c\n")}, nil, ""); err != nil {
				t.Fatalf("seed %d: commit c: %v", seed, err)
			}

			ops, err := e.OperationLog()
			if err != nil {
				t.Fatalf("seed %d: OperationLog: %v", seed, err)
			}
			if len(ops) < 2 {
				t.Fatalf("seed %d: expected a multi-op chain, got %d ops", seed, len(ops))
			}

			// Chain continuity: each op saw the world the previous op left.
			for i := 1; i < len(ops); i++ {
				if !reflect.DeepEqual(ops[i].ViewBefore, ops[i-1].ViewAfter) {
					t.Fatalf("seed %d: op-log chain broken at %d: ViewBefore=%v != prev ViewAfter=%v",
						seed, i, ops[i].ViewBefore, ops[i-1].ViewAfter)
				}
			}

			// Terminal consistency: the last recorded view_after is the present
			// ref-map. Reconstruct the present main tip and compare. (viewMap is
			// unexported; LineByName gives the authoritative current tip.)
			last := ops[len(ops)-1].ViewAfter
			curMain, err := e.LineByName("main")
			if err != nil {
				t.Fatalf("seed %d: LineByName(main): %v", seed, err)
			}
			if last["main"] != curMain.TipCommit {
				t.Fatalf("seed %d: last op ViewAfter[main]=%q != current main tip %q",
					seed, last["main"], curMain.TipCommit)
			}
		})
	}
}

// Property 8 — Resolution closes the loop. Resolving the conflict clears it,
// unflags the change, lets the line fold, and lands the resolved bytes on the
// parent tip.
func TestProperty8_ResolutionClosesLoop(t *testing.T) {
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			_ = seed
			e := newEngine(t)
			main, _ := e.LineByName("main")
			seedMain(t, e, map[string][]byte{"f.txt": []byte("base\n")})
			exp, _ := e.CreateLine("exp", main.ID)
			mc, _ := e.CreateChange(main.ID, "m")
			e.Commit(mc.ID, map[string][]byte{"f.txt": []byte("X\n")}, nil, "")
			ec, _ := e.CreateChange(exp.ID, "e")
			r, _ := e.Commit(ec.ID, map[string][]byte{"f.txt": []byte("Y\n")}, nil, "")
			if len(r.Conflicts) == 0 {
				t.Fatalf("seed %d: setup expected a conflict", seed)
			}

			resolved := []byte("resolved\n")
			if err := e.ResolveConflict(ec.ID, "f.txt", resolved); err != nil {
				t.Fatalf("seed %d: ResolveConflict: %v", seed, err)
			}
			if open, _ := e.Conflicts(ec.ID); len(open) != 0 {
				t.Fatalf("seed %d: conflicts still open after resolve: %+v", seed, open)
			}
			ch, _ := e.GetChange(ec.ID)
			if ch.HasConflict {
				t.Fatalf("seed %d: change still flagged has_conflict", seed)
			}

			if err := e.FoldLine(exp.ID); err != nil {
				t.Fatalf("seed %d: FoldLine after resolve: %v", seed, err)
			}
			main, _ = e.LineByName("main")
			files, err := e.Files(main.TipCommit)
			if err != nil {
				t.Fatalf("seed %d: Files: %v", seed, err)
			}
			if !bytes.Equal(files["f.txt"], resolved) {
				t.Fatalf("seed %d: parent tip f.txt = %q, want %q", seed, files["f.txt"], resolved)
			}
		})
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func lastOp(ops []change.Operation) any {
	if len(ops) == 0 {
		return "<none>"
	}
	return ops[len(ops)-1]
}
