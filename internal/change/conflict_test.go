package change

import "testing"

func TestConflictRecordedAndResolved(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	seedLineTip(t, e, main.ID, map[string][]byte{"f.txt": []byte("base\n")})

	// exp forks at the shared base BEFORE main advances.
	exp, _ := e.CreateLine("exp", main.ID)

	// main advances: a change on main edits f.txt -> X (advances main tip).
	mc, _ := e.CreateChange(main.ID, "m")
	if _, err := e.Commit(mc.ID, map[string][]byte{"f.txt": []byte("X\n")}); err != nil {
		t.Fatalf("main commit: %v", err)
	}
	// exp edits the same region -> Y; merge-forward against main's X over base -> conflict.
	ec, _ := e.CreateChange(exp.ID, "e")
	r, err := e.Commit(ec.ID, map[string][]byte{"f.txt": []byte("Y\n")})
	if err != nil {
		t.Fatalf("exp commit: %v", err)
	}
	if len(r.Conflicts) == 0 {
		t.Fatal("expected a conflict on exp")
	}
	open, err := e.Conflicts(ec.ID)
	if err != nil {
		t.Fatalf("Conflicts: %v", err)
	}
	if len(open) != 1 || open[0].Status != "open" || open[0].Path != "f.txt" {
		t.Fatalf("open conflicts = %+v", open)
	}
	if err := e.ResolveConflict(ec.ID, "f.txt", []byte("resolved\n")); err != nil {
		t.Fatalf("ResolveConflict: %v", err)
	}
	if still, _ := e.Conflicts(ec.ID); len(still) != 0 {
		t.Fatalf("still open after resolve: %+v", still)
	}
	got, _ := e.GetChange(ec.ID)
	if got.HasConflict {
		t.Fatal("change still flagged has_conflict after resolving all")
	}
	// the resolved content is in the change's tip tree
	tree, _ := e.commitTree(got.HeadCommit)
	files, _ := e.readTree(tree)
	if string(files["f.txt"]) != "resolved\n" {
		t.Fatalf("resolved content not applied: %q", files["f.txt"])
	}
}
