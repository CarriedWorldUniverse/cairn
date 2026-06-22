package change

import "testing"

func TestOpLogRecordsAndUndoRestores(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	seedLineTip(t, e, main.ID, map[string][]byte{"a.txt": []byte("a\n")})
	before, _ := e.LineByName("main")

	// A commit on main advances its tip and should record an op.
	ch, _ := e.CreateChange(main.ID, "m")
	if _, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("a2\n")}); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	after, _ := e.LineByName("main")
	if after.TipCommit == before.TipCommit {
		t.Fatal("precondition: commit should have advanced main tip")
	}

	ops, err := e.OperationLog()
	if err != nil {
		t.Fatalf("OperationLog: %v", err)
	}
	if len(ops) == 0 {
		t.Fatal("no operations recorded")
	}
	if ops[len(ops)-1].OpType != "commit" {
		t.Fatalf("last op type = %q, want commit", ops[len(ops)-1].OpType)
	}

	if err := e.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	restored, _ := e.LineByName("main")
	if restored.TipCommit != before.TipCommit {
		t.Fatalf("undo did not restore main tip: %s != %s", restored.TipCommit, before.TipCommit)
	}
	// Undo is itself recorded as an op (append-only history).
	ops2, _ := e.OperationLog()
	if ops2[len(ops2)-1].OpType != "undo" {
		t.Fatalf("last op after undo = %q, want undo", ops2[len(ops2)-1].OpType)
	}
}
