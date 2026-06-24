package change

import (
	"strings"
	"testing"
)

// sealOne forks a child line off main (seeding main with a base first) and seals
// ONE commit on it that snapshots the given files, returning the line id, the
// sealed commit sha, its change-id, and its message. The line keeps a fresh open
// working change after the seal (as Seal leaves it).
func sealOne(t *testing.T, e *Engine, lineName string, files map[string]string, msg string) (lineID, commit, changeID string) {
	t.Helper()
	main, err := e.LineByName("main")
	if err != nil {
		t.Fatalf("LineByName(main): %v", err)
	}
	if main.TipCommit == "" {
		seedLineTip(t, e, main.ID, map[string][]byte{"base.txt": []byte("base\n")})
	}
	line, err := e.CreateLine(lineName, main.ID)
	if err != nil {
		t.Fatalf("CreateLine(%s): %v", lineName, err)
	}
	cur := openChange(t, e, line.ID)
	entries := map[string]TreeEntry{}
	for p, c := range files {
		entries[p] = blobEntry(t, e, c)
	}
	if _, _, err := e.SnapshotWorking(cur, entries); err != nil {
		t.Fatalf("SnapshotWorking(%s): %v", lineName, err)
	}
	if _, conflicts, err := e.Seal(cur, msg); err != nil {
		t.Fatalf("Seal(%s): %v", lineName, err)
	} else if len(conflicts) != 0 {
		t.Fatalf("Seal(%s) conflicts = %d, want 0", lineName, len(conflicts))
	}
	sealed, err := e.GetChange(cur)
	if err != nil {
		t.Fatalf("GetChange(sealed %s): %v", lineName, err)
	}
	return line.ID, sealed.HeadCommit, sealed.ID
}

// TestCherryPickCleanApply: seal commit C on line A adding f.txt="F\n", then
// cherry-pick C onto a separate line B. A fresh sealed change appears on B (new
// change-id, C's message); B's tip carries f.txt="F\n"; the origin change C on A
// is untouched.
func TestCherryPickCleanApply(t *testing.T) {
	e := newTestEngine(t)

	aID, cCommit, cCID := sealOne(t, e, "A", map[string]string{"f.txt": "F\n"}, "add f")
	_ = aID
	cChangeBefore, err := e.GetChange(cCID)
	if err != nil {
		t.Fatalf("GetChange(C) before: %v", err)
	}

	// Target line B with its own base content (this leaves one sealed change on B,
	// its "base of B" commit — bBaseCID).
	bLineID, _, bBaseCID := sealOne(t, e, "B", map[string]string{"b.txt": "B\n"}, "base of B")

	conflicts, err := e.CherryPick(cCommit, bLineID)
	if err != nil {
		t.Fatalf("CherryPick: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("CherryPick conflicts = %d, want 0", len(conflicts))
	}

	// A NEW sealed change row exists on B with a fresh change-id (!= C's, != B's
	// base). Exclude B's pre-existing base change to isolate the pick.
	rows, err := e.db.Query(`SELECT id, head_commit FROM change WHERE line_id=? AND sealed=1`, bLineID)
	if err != nil {
		t.Fatalf("query sealed changes on B: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var newID, newHead string
	found := 0
	for rows.Next() {
		var id, head string
		if err := rows.Scan(&id, &head); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if id == cCID {
			t.Fatalf("C's change-id %q appears on B — pick must mint a FRESH change-id", cCID)
		}
		if id == bBaseCID {
			continue // B's own base, not the pick
		}
		newID, newHead = id, head
		found++
	}
	if found != 1 {
		t.Fatalf("new sealed changes on B (excluding base) = %d, want exactly 1 (the pick)", found)
	}
	if newID == cCID {
		t.Fatalf("new sealed change-id == C's change-id %q", cCID)
	}

	// The picked sealed head carries C's message.
	pMsg, err := e.commitMessage(newHead)
	if err != nil {
		t.Fatalf("commitMessage(newHead): %v", err)
	}
	if got := stripChangeID(pMsg); got != "add f" {
		t.Errorf("picked commit message = %q, want %q", got, "add f")
	}

	// B's tip carries the picked file.
	bLine, err := e.lineByID(bLineID)
	if err != nil {
		t.Fatalf("lineByID(B): %v", err)
	}
	tipFiles, err := e.Files(bLine.TipCommit)
	if err != nil {
		t.Fatalf("Files(B tip): %v", err)
	}
	if got := string(tipFiles["f.txt"]); got != "F\n" {
		t.Errorf("f.txt at B tip = %q, want %q", got, "F\n")
	}
	if got := string(tipFiles["b.txt"]); got != "B\n" {
		t.Errorf("b.txt at B tip = %q, want %q (B's own base preserved)", got, "B\n")
	}

	// Origin C on A is untouched.
	cChangeAfter, err := e.GetChange(cCID)
	if err != nil {
		t.Fatalf("GetChange(C) after: %v", err)
	}
	if cChangeAfter.HeadCommit != cChangeBefore.HeadCommit {
		t.Errorf("C's head changed: before %s, after %s", cChangeBefore.HeadCommit, cChangeAfter.HeadCommit)
	}
}

// TestCherryPickKeepsWorkingEdits: B's open working change carries an un-sealed
// edit (g.txt="G\n") BEFORE the pick. After cherry-picking C (adds f.txt), B's
// tip carries BOTH the picked f.txt AND the working g.txt.
func TestCherryPickKeepsWorkingEdits(t *testing.T) {
	e := newTestEngine(t)

	_, cCommit, _ := sealOne(t, e, "A", map[string]string{"f.txt": "F\n"}, "add f")
	bLineID, _, _ := sealOne(t, e, "B", map[string]string{"b.txt": "B\n"}, "base of B")

	// B's working change gets an un-sealed edit.
	bOpen := openWorkingChangeID(t, e, bLineID)
	if _, _, err := e.SnapshotWorking(bOpen, map[string]TreeEntry{
		"g.txt": blobEntry(t, e, "G\n"),
	}); err != nil {
		t.Fatalf("SnapshotWorking(B open): %v", err)
	}

	conflicts, err := e.CherryPick(cCommit, bLineID)
	if err != nil {
		t.Fatalf("CherryPick: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("CherryPick conflicts = %d, want 0", len(conflicts))
	}

	bLine, err := e.lineByID(bLineID)
	if err != nil {
		t.Fatalf("lineByID(B): %v", err)
	}
	tipFiles, err := e.Files(bLine.TipCommit)
	if err != nil {
		t.Fatalf("Files(B tip): %v", err)
	}
	if got := string(tipFiles["f.txt"]); got != "F\n" {
		t.Errorf("f.txt at B tip = %q, want %q (picked)", got, "F\n")
	}
	if got := string(tipFiles["g.txt"]); got != "G\n" {
		t.Errorf("g.txt at B tip = %q, want %q (working edit preserved)", got, "G\n")
	}
}

// TestCherryPickConflictAsData: C edits x.txt="A\n" (its parent had x.txt="base\n"),
// while B's top has x.txt="B\n". CherryPick returns conflicts (len>0), still
// completes (a new sealed change exists on B and B's tip advanced), and the
// conflict is recorded.
func TestCherryPickConflictAsData(t *testing.T) {
	e := newTestEngine(t)

	// Build C on line A: A's base has x.txt="base\n", then C changes it to "A\n".
	main, err := e.LineByName("main")
	if err != nil {
		t.Fatalf("LineByName(main): %v", err)
	}
	seedLineTip(t, e, main.ID, map[string][]byte{"base.txt": []byte("base\n")})
	aLine, err := e.CreateLine("A", main.ID)
	if err != nil {
		t.Fatalf("CreateLine(A): %v", err)
	}
	aCur := openChange(t, e, aLine.ID)
	// First sealed step on A establishes x.txt="base\n" (the pick's parent state).
	if _, _, err := e.SnapshotWorking(aCur, map[string]TreeEntry{
		"x.txt": blobEntry(t, e, "base\n"),
	}); err != nil {
		t.Fatalf("SnapshotWorking(A base x): %v", err)
	}
	aNext, _, err := e.Seal(aCur, "A x base")
	if err != nil {
		t.Fatalf("Seal(A x base): %v", err)
	}
	// Second sealed step = C: x.txt "base\n"→"A\n".
	if _, _, err := e.SnapshotWorking(aNext, map[string]TreeEntry{
		"x.txt": blobEntry(t, e, "A\n"),
	}); err != nil {
		t.Fatalf("SnapshotWorking(C): %v", err)
	}
	if _, _, err := e.Seal(aNext, "C edit x"); err != nil {
		t.Fatalf("Seal(C): %v", err)
	}
	cSealed, err := e.GetChange(aNext)
	if err != nil {
		t.Fatalf("GetChange(C): %v", err)
	}
	cCommit := cSealed.HeadCommit

	// Target line B with x.txt="B\n" at its top (diverges from both base and C).
	// This leaves B with one sealed "B x" change (bBaseCID).
	bLineID, _, bBaseCID := sealOne(t, e, "B", map[string]string{"x.txt": "B\n"}, "B x")

	conflicts, err := e.CherryPick(cCommit, bLineID)
	if err != nil {
		t.Fatalf("CherryPick: %v", err)
	}
	if len(conflicts) == 0 {
		t.Fatalf("CherryPick conflicts = 0, want >0 (x.txt diverges)")
	}

	// A new sealed change (the pick) exists on B, distinct from B's own base.
	var newID string
	if err := e.db.QueryRow(
		`SELECT id FROM change WHERE line_id=? AND sealed=1 AND id!=?`,
		bLineID, bBaseCID).Scan(&newID); err != nil {
		t.Fatalf("get new sealed id on B: %v", err)
	}

	// The conflict is recorded against the new sealed change (has_conflict set
	// and a conflict row present).
	newCh, err := e.GetChange(newID)
	if err != nil {
		t.Fatalf("GetChange(new sealed): %v", err)
	}
	if !newCh.HasConflict {
		t.Errorf("new sealed change has_conflict = false, want true")
	}
	recorded, err := e.Conflicts(newID)
	if err != nil {
		t.Fatalf("Conflicts(new sealed): %v", err)
	}
	if len(recorded) == 0 {
		t.Fatalf("Conflicts(new sealed) = 0, want >0 — conflict should be recorded")
	}
	if recorded[0].Path != "x.txt" {
		t.Errorf("conflict path = %q, want %q", recorded[0].Path, "x.txt")
	}

	// The tip advanced and carries conflict markers in x.txt.
	bLine, err := e.lineByID(bLineID)
	if err != nil {
		t.Fatalf("lineByID(B): %v", err)
	}
	tipFiles, err := e.Files(bLine.TipCommit)
	if err != nil {
		t.Fatalf("Files(B tip): %v", err)
	}
	if !strings.Contains(string(tipFiles["x.txt"]), "<<<<<<<") {
		t.Errorf("x.txt at B tip = %q, want conflict markers", string(tipFiles["x.txt"]))
	}
}

// TestCherryPickWorkingRebaseConflictFlags: the pick itself is clean (pick delta
// applies without conflict), but W's rebase onto the pick conflicts. Verifies:
//   - W's has_conflict is set TRUE (W absorbed the W-rebase conflict),
//   - conflict rows are recorded under w.ID,
//   - the new sealed change's has_conflict is FALSE (pick was clean).
//
// Setup: B's top has y.txt="base\n" (established as a sealed change bBaseCID).
// B's open working change snapshots y.txt="WORK\n". Commit C (from line A) edits
// y.txt="base\n"→"PICK\n" (its parent had y.txt="base\n"). Cherry-pick C onto B:
//
//	pick merge: base="base\n" ours(B top)="base\n" theirs="PICK\n" → clean, result="PICK\n"
//	W rebase:   base="base\n" ours(pick)="PICK\n"  theirs(W)="WORK\n" → CONFLICT
func TestCherryPickWorkingRebaseConflictFlags(t *testing.T) {
	e := newTestEngine(t)

	// Establish main with a seed so CreateLine has something to branch from.
	main, err := e.LineByName("main")
	if err != nil {
		t.Fatalf("LineByName(main): %v", err)
	}
	seedLineTip(t, e, main.ID, map[string][]byte{"base.txt": []byte("base\n")})

	// Line A: first seal sets y.txt="base\n" (the pick's parent state), then seal C
	// changes y.txt="base\n"→"PICK\n".
	aLine, err := e.CreateLine("A", main.ID)
	if err != nil {
		t.Fatalf("CreateLine(A): %v", err)
	}
	aCur := openChange(t, e, aLine.ID)
	if _, _, err := e.SnapshotWorking(aCur, map[string]TreeEntry{
		"y.txt": blobEntry(t, e, "base\n"),
	}); err != nil {
		t.Fatalf("SnapshotWorking(A y base): %v", err)
	}
	aNext, _, err := e.Seal(aCur, "A y base")
	if err != nil {
		t.Fatalf("Seal(A y base): %v", err)
	}
	// C: y.txt "base\n" → "PICK\n"
	if _, _, err := e.SnapshotWorking(aNext, map[string]TreeEntry{
		"y.txt": blobEntry(t, e, "PICK\n"),
	}); err != nil {
		t.Fatalf("SnapshotWorking(C): %v", err)
	}
	if _, _, err := e.Seal(aNext, "C pick y"); err != nil {
		t.Fatalf("Seal(C): %v", err)
	}
	cSealed, err := e.GetChange(aNext)
	if err != nil {
		t.Fatalf("GetChange(C): %v", err)
	}
	cCommit := cSealed.HeadCommit

	// Line B: seal a change that sets y.txt="base\n" at B's top (bBaseCID).
	bLine, err := e.CreateLine("B", main.ID)
	if err != nil {
		t.Fatalf("CreateLine(B): %v", err)
	}
	bCur := openChange(t, e, bLine.ID)
	if _, _, err := e.SnapshotWorking(bCur, map[string]TreeEntry{
		"y.txt": blobEntry(t, e, "base\n"),
	}); err != nil {
		t.Fatalf("SnapshotWorking(B y base): %v", err)
	}
	_, _, err = e.Seal(bCur, "B y base")
	if err != nil {
		t.Fatalf("Seal(B y base): %v", err)
	}
	bBaseCID := bCur // reuse: bCur is now the sealed change-id for "B y base"

	// B's open working change: snapshot y.txt="WORK\n" (un-sealed edit).
	bOpen := openWorkingChangeID(t, e, bLine.ID)
	if _, _, err := e.SnapshotWorking(bOpen, map[string]TreeEntry{
		"y.txt": blobEntry(t, e, "WORK\n"),
	}); err != nil {
		t.Fatalf("SnapshotWorking(B open WORK): %v", err)
	}

	// Cherry-pick C onto B.
	conflicts, err := e.CherryPick(cCommit, bLine.ID)
	if err != nil {
		t.Fatalf("CherryPick: %v", err)
	}
	if len(conflicts) == 0 {
		t.Fatal("CherryPick conflicts = 0, want >0 (W-rebase should conflict on y.txt)")
	}

	// W's has_conflict must be TRUE — it absorbed the rebase conflict.
	wChange, err := e.GetChange(bOpen)
	if err != nil {
		t.Fatalf("GetChange(W): %v", err)
	}
	if !wChange.HasConflict {
		t.Errorf("W.HasConflict = false, want true (W-rebase conflicted on y.txt)")
	}

	// Conflict rows must be recorded against W's change-id.
	wConflicts, err := e.Conflicts(bOpen)
	if err != nil {
		t.Fatalf("Conflicts(W): %v", err)
	}
	if len(wConflicts) == 0 {
		t.Error("Conflicts(W) = 0, want >0 — conflict rows should be on W's change-id")
	}

	// The new sealed change (the pick) must have has_conflict=FALSE — pick was clean.
	var newSealedID string
	if err := e.db.QueryRow(
		`SELECT id FROM change WHERE line_id=? AND sealed=1 AND id!=?`,
		bLine.ID, bBaseCID).Scan(&newSealedID); err != nil {
		t.Fatalf("get new sealed change on B: %v", err)
	}
	newSealedChange, err := e.GetChange(newSealedID)
	if err != nil {
		t.Fatalf("GetChange(new sealed): %v", err)
	}
	if newSealedChange.HasConflict {
		t.Errorf("new sealed change HasConflict = true, want false (pick was clean; only W-rebase conflicted)")
	}
}

// TestCherryPickNonCairnCommit: cherry-picking a bogus / non-cairn sha returns an
// error mentioning "not a cairn commit".
func TestCherryPickNonCairnCommit(t *testing.T) {
	e := newTestEngine(t)
	bLineID, _, _ := sealOne(t, e, "B", map[string]string{"b.txt": "B\n"}, "base of B")

	_, err := e.CherryPick("0000000000000000000000000000000000000000", bLineID)
	if err == nil {
		t.Fatal("CherryPick(bogus sha): want error, got nil")
	}
	if !strings.Contains(err.Error(), "not a cairn commit") {
		t.Fatalf("CherryPick(bogus) error = %q, want it to mention %q", err.Error(), "not a cairn commit")
	}
}
