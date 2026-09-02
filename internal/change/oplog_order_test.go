package change

import (
	"testing"
	"time"
)

// The operation log's ordering is what Undo stands on: Undo takes
// OperationLog()[len-1] and restores that op's view_before. If the log comes
// back in the wrong order, Undo reverts the wrong operation — so these tests
// are about correctness of history, not presentation.
//
// The original ids were built from time.RFC3339Nano, which TRIMS TRAILING
// ZEROS from the fractional seconds. That breaks the lexicographic ordering
// the log relied on, because a shortened fraction ends in 'Z' (0x5A) and every
// digit sorts below it:
//
//	.9Z    >  .925Z   >  .9251Z     (lexicographic)
//	.9     <  .925    <  .9251      (chronological)
//
// which is an exact reversal.

// atClock drives an engine from a fixed list of instants, one per call, so a
// test can mint operations at precisely chosen times.
func atClock(e *Engine, times []time.Time) {
	i := 0
	e.now = func() time.Time {
		t := times[i]
		if i < len(times)-1 {
			i++
		}
		return t
	}
}

// trickyTimes returns three strictly increasing instants whose RFC3339Nano
// renderings sort in exactly the reverse order. They are placed in the future
// so they are unambiguously after anything Open recorded.
func trickyTimes() []time.Time {
	base := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	return []time.Time{
		base.Add(900 * time.Millisecond),    // "...9Z"
		base.Add(925 * time.Millisecond),    // "...925Z"
		base.Add(925100 * time.Microsecond), // "...9251Z"
	}
}

// The log must come back in the order the operations actually happened.
func TestOpLogOrderIsChronologicalDespiteTrailingZeros(t *testing.T) {
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = e.Close() }()

	times := trickyTimes()
	atClock(e, times)
	actors := []string{"first", "second", "third"}
	for _, a := range actors {
		if err := e.recordOp("commit", a, map[string]string{}, map[string]string{}); err != nil {
			t.Fatalf("recordOp %s: %v", a, err)
		}
	}

	ops, err := e.OperationLog()
	if err != nil {
		t.Fatalf("OperationLog: %v", err)
	}
	// Ignore anything Open recorded; compare only the three minted here.
	var got []string
	for _, op := range ops {
		for _, a := range actors {
			if op.Actor == a {
				got = append(got, op.Actor)
			}
		}
	}
	if len(got) != len(actors) {
		t.Fatalf("recorded %d ops, log returned %d: %v", len(actors), len(got), got)
	}
	for i := range actors {
		if got[i] != actors[i] {
			t.Fatalf("operation log out of order: got %v, want %v — a later op sorted before an earlier one", got, actors)
		}
	}
}

// Undo reverts OperationLog()'s LAST entry, so the last entry must be the
// operation that genuinely happened most recently.
func TestLastOpIsTheMostRecentDespiteTrailingZeros(t *testing.T) {
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = e.Close() }()

	atClock(e, trickyTimes())
	for _, a := range []string{"first", "second", "third"} {
		if err := e.recordOp("commit", a, map[string]string{}, map[string]string{}); err != nil {
			t.Fatalf("recordOp %s: %v", a, err)
		}
	}

	ops, err := e.OperationLog()
	if err != nil {
		t.Fatalf("OperationLog: %v", err)
	}
	if last := ops[len(ops)-1]; last.Actor != "third" {
		t.Fatalf("last op is %q, want \"third\" — Undo would revert the wrong operation", last.Actor)
	}
}

// parent_op is picked inline during the insert and must name the operation that
// immediately preceded it, or the recorded chain is broken.
func TestParentOpChainsToTheImmediatelyPrecedingOp(t *testing.T) {
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = e.Close() }()

	atClock(e, trickyTimes())
	actors := []string{"first", "second", "third"}
	for _, a := range actors {
		if err := e.recordOp("commit", a, map[string]string{}, map[string]string{}); err != nil {
			t.Fatalf("recordOp %s: %v", a, err)
		}
	}

	ops, err := e.OperationLog()
	if err != nil {
		t.Fatalf("OperationLog: %v", err)
	}
	byActor := map[string]Operation{}
	for _, op := range ops {
		byActor[op.Actor] = op
	}
	for i := 1; i < len(actors); i++ {
		child, parent := byActor[actors[i]], byActor[actors[i-1]]
		if child.ParentOp != parent.ID {
			t.Fatalf("%s.parent_op = %q, want %s's id %q — the op chain skipped or reversed",
				actors[i], child.ParentOp, actors[i-1], parent.ID)
		}
	}
}

// THE test for the rowid change specifically. Fixed-width stamps make new ids
// sort correctly on their own, so every test above passes under ORDER BY id too
// — they cannot tell the two halves of the fix apart. What ORDER BY id can
// never repair is a repo written by an EARLIER cairn, whose ids are already
// trimmed and already mis-sorted. Ordering by rowid fixes those in place, with
// no migration, and this is the only test that proves it.
func TestOpLogOrderIsCorrectForLegacyTrimmedIDs(t *testing.T) {
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = e.Close() }()

	// Exactly what a pre-#157 cairn wrote: RFC3339Nano ids, inserted in
	// chronological order, whose string ordering is the reverse of it.
	actors := []string{"first", "second", "third"}
	for i, tm := range trickyTimes() {
		id := tm.Format(time.RFC3339Nano) + "-legacy00"
		if _, err := e.db.Exec(
			`INSERT INTO operation(id, op_type, actor, parent_op, view_before, view_after, detail, at)
			 VALUES(?,?,?,?,?,?,'{}',?)`,
			id, "commit", actors[i], "", "{}", "{}", id); err != nil {
			t.Fatalf("insert legacy op %d: %v", i, err)
		}
	}

	ops, err := e.OperationLog()
	if err != nil {
		t.Fatalf("OperationLog: %v", err)
	}
	var got []string
	for _, op := range ops {
		for _, a := range actors {
			if op.Actor == a {
				got = append(got, op.Actor)
			}
		}
	}
	if len(got) != len(actors) {
		t.Fatalf("inserted %d legacy ops, log returned %d: %v", len(actors), len(got), got)
	}
	for i := range actors {
		if got[i] != actors[i] {
			t.Fatalf("legacy op log out of order: got %v, want %v — an existing repo's history is still mis-ordered, so Undo still reverts the wrong op", got, actors)
		}
	}
}

// The timestamp format itself: fixed width, so lexicographic order and
// chronological order agree for every instant, which is what the ids, and every
// other stored timestamp compared with ORDER BY, depend on.
func TestStampIsLexicographicallyChronological(t *testing.T) {
	base := time.Date(2026, 9, 2, 3, 46, 38, 0, time.UTC)
	increasing := []time.Time{
		base,
		base.Add(1 * time.Nanosecond),
		base.Add(900 * time.Millisecond),
		base.Add(925 * time.Millisecond),
		base.Add(925100 * time.Microsecond),
		base.Add(1 * time.Second),
		base.Add(time.Second + 5*time.Millisecond),
	}
	width := len(stamp(increasing[0]))
	for i, tm := range increasing {
		if got := len(stamp(tm)); got != width {
			t.Fatalf("stamp(%v) is %d chars, want a fixed %d — a variable-width stamp cannot sort correctly", tm, got, width)
		}
		if i == 0 {
			continue
		}
		prev, cur := stamp(increasing[i-1]), stamp(tm)
		if !(prev < cur) {
			t.Fatalf("stamp inversion: %s (earlier) does not sort before %s (later)", prev, cur)
		}
	}
}
