package change

import (
	"strings"
	"testing"
)

// buildChildLineWith6FlagSeals forks a child line off main and seals SIX commits
// on it (S1..S6). Each step snapshots flag.txt plus a distinct per-step file (so
// every sealed tree is unique). flag.txt is "ok\n" for S1,S2,S3 and "bad\n" for
// S4,S5,S6 — so S4 is the first bad commit. Returns the child line id and the six
// sealed commit shas in base→top order.
func buildChildLineWith6FlagSeals(t *testing.T, e *Engine) (childLineID string, commits []string) {
	t.Helper()
	main, err := e.LineByName("main")
	if err != nil {
		t.Fatalf("LineByName(main): %v", err)
	}
	// Seed main with a base so the child has a real fork point.
	seedLineTip(t, e, main.ID, map[string][]byte{"base.txt": []byte("base\n")})

	child, err := e.CreateLine("child", main.ID)
	if err != nil {
		t.Fatalf("CreateLine(child): %v", err)
	}

	msgs := []string{"S1", "S2", "S3", "S4", "S5", "S6"}
	flags := []string{"ok\n", "ok\n", "ok\n", "bad\n", "bad\n", "bad\n"}
	cur := openChange(t, e, child.ID)
	for i, m := range msgs {
		// Snapshot flag.txt with this step's verdict content plus a distinct file,
		// so each sealed tree (and thus each commit) is unique.
		if _, _, err := e.SnapshotWorking(cur, map[string]TreeEntry{
			"flag.txt": blobEntry(t, e, flags[i]),
			m + ".txt": blobEntry(t, e, m+" content\n"),
		}); err != nil {
			t.Fatalf("SnapshotWorking %s: %v", m, err)
		}
		next, conflicts, err := e.Seal(cur, m)
		if err != nil {
			t.Fatalf("Seal %s: %v", m, err)
		}
		if len(conflicts) != 0 {
			t.Fatalf("Seal %s conflicts = %d, want 0", m, len(conflicts))
		}
		sealed, err := e.GetChange(cur)
		if err != nil {
			t.Fatalf("GetChange(sealed %s): %v", m, err)
		}
		commits = append(commits, sealed.HeadCommit)
		cur = next
	}
	return child.ID, commits
}

// flagOf reads flag.txt out of a commit's materialized files.
func flagOf(t *testing.T, e *Engine, sha string) string {
	t.Helper()
	files, err := e.Files(sha)
	if err != nil {
		t.Fatalf("Files(%s): %v", sha, err)
	}
	return string(files["flag.txt"])
}

// TestBisectFindsFirstBad: a full driven bisect over S1..S6 (S4 first bad)
// converges to S4. The driver reads each midpoint's flag.txt to mark good/bad.
func TestBisectFindsFirstBad(t *testing.T) {
	e := newTestEngine(t)
	childID, commits := buildChildLineWith6FlagSeals(t, e)
	S1, S4, S6 := commits[0], commits[3], commits[5]

	step, err := e.BisectStart(childID, "feat", S1, S6)
	if err != nil {
		t.Fatalf("BisectStart: %v", err)
	}

	// Sanity: a real midpoint to test, and a session is active.
	if step.Done {
		t.Fatalf("BisectStart returned Done immediately for a 6-commit range")
	}
	if step.Current == "" {
		t.Fatal("BisectStart: empty Current")
	}
	if active, _ := e.BisectActive(); !active {
		t.Fatal("BisectActive() = false after start, want true")
	}

	// Drive the search to convergence.
	for guard := 0; !step.Done; guard++ {
		if guard > 16 {
			t.Fatal("bisect did not converge within bound")
		}
		var verdict string
		if flagOf(t, e, step.Current) == "ok\n" {
			verdict = "good"
		} else {
			verdict = "bad"
		}
		step, err = e.BisectMark(verdict)
		if err != nil {
			t.Fatalf("BisectMark(%s): %v", verdict, err)
		}
	}

	if step.FirstBad != S4 {
		// Help diagnose an off-by-one (S3 or S5).
		idx := -1
		for i, c := range commits {
			if c == step.FirstBad {
				idx = i
			}
		}
		t.Fatalf("FirstBad = %s (S%d), want S4 (%s)", step.FirstBad, idx+1, S4)
	}

	// The session STAYS ALIVE in a done state until reset (so the auto-snapshot
	// stays suspended while the historical first-bad is in the folder). BisectInfo
	// reports Done; only BisectReset clears it.
	if active, _ := e.BisectActive(); !active {
		t.Fatal("BisectActive() = false after Done, want true (session persists until reset)")
	}
	if info, _ := e.BisectInfo(); !info.Done || info.FirstBad != S4 {
		t.Fatalf("BisectInfo after Done = {Done:%v FirstBad:%s}, want {true %s}", info.Done, info.FirstBad, S4)
	}
	// A redundant mark after Done is idempotent (re-reports, no error/change).
	if again, err := e.BisectMark("bad"); err != nil || !again.Done || again.FirstBad != S4 {
		t.Fatalf("BisectMark after Done = (%+v, %v), want idempotent Done/%s", again, err, S4)
	}
	// reset clears it.
	if _, err := e.BisectReset(); err != nil {
		t.Fatalf("BisectReset: %v", err)
	}
	if active, _ := e.BisectActive(); active {
		t.Fatal("BisectActive() = true after reset, want false")
	}
}

// TestBisectImmediateDone: good = S3, bad = S4 (adjacent) → Done immediately with
// FirstBad = S4 and NO session created.
func TestBisectImmediateDone(t *testing.T) {
	e := newTestEngine(t)
	childID, commits := buildChildLineWith6FlagSeals(t, e)
	S3, S4 := commits[2], commits[3]

	step, err := e.BisectStart(childID, "feat", S3, S4)
	if err != nil {
		t.Fatalf("BisectStart(S3,S4): %v", err)
	}
	if !step.Done {
		t.Fatalf("BisectStart(S3,S4): Done = false, want true (adjacent)")
	}
	if step.FirstBad != S4 {
		t.Fatalf("FirstBad = %s, want S4 (%s)", step.FirstBad, S4)
	}
	if active, _ := e.BisectActive(); active {
		t.Fatal("BisectActive() = true after immediate Done, want false (no session created)")
	}
}

// TestBisectStartValidations: good after bad is rejected; a second start while a
// session is active is rejected.
func TestBisectStartValidations(t *testing.T) {
	e := newTestEngine(t)
	childID, commits := buildChildLineWith6FlagSeals(t, e)
	S2, S5 := commits[1], commits[4]

	// good after bad on the line → error.
	if _, err := e.BisectStart(childID, "feat", S5, S2); err == nil {
		t.Fatal("BisectStart(S5,S2): want error (good after bad), got nil")
	} else if !strings.Contains(err.Error(), "ancestor") {
		t.Fatalf("BisectStart(S5,S2) error = %v, want ancestor refusal", err)
	}

	// Start a real session, then a second start must be refused.
	if _, err := e.BisectStart(childID, "feat", commits[0], commits[5]); err != nil {
		t.Fatalf("BisectStart: %v", err)
	}
	if _, err := e.BisectStart(childID, "feat", commits[0], commits[5]); err == nil {
		t.Fatal("second BisectStart while active: want error, got nil")
	} else if !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("second BisectStart error = %v, want already-in-progress refusal", err)
	}
}

// TestBisectReset: after starting, BisectReset returns the recorded restore tip
// (the line's tip at start) and clears the session.
func TestBisectReset(t *testing.T) {
	e := newTestEngine(t)
	childID, commits := buildChildLineWith6FlagSeals(t, e)

	line, err := e.lineByID(childID)
	if err != nil {
		t.Fatalf("lineByID: %v", err)
	}
	wantTip := line.TipCommit

	if _, err := e.BisectStart(childID, "feat", commits[0], commits[5]); err != nil {
		t.Fatalf("BisectStart: %v", err)
	}
	if active, _ := e.BisectActive(); !active {
		t.Fatal("BisectActive() = false after start, want true")
	}

	restore, err := e.BisectReset()
	if err != nil {
		t.Fatalf("BisectReset: %v", err)
	}
	if restore != wantTip {
		t.Fatalf("BisectReset restore tip = %s, want line tip %s", restore, wantTip)
	}
	if active, _ := e.BisectActive(); active {
		t.Fatal("BisectActive() = true after reset, want false")
	}

	// Reset with no session in progress → error.
	if _, err := e.BisectReset(); err == nil {
		t.Fatal("BisectReset with no session: want error, got nil")
	}
}

// TestBisectSkip: skip moves Current one step toward good without resolving.
func TestBisectSkip(t *testing.T) {
	e := newTestEngine(t)
	childID, commits := buildChildLineWith6FlagSeals(t, e)

	step, err := e.BisectStart(childID, "feat", commits[0], commits[5])
	if err != nil {
		t.Fatalf("BisectStart: %v", err)
	}
	before := step.Current

	skipped, err := e.BisectSkip()
	if err != nil {
		t.Fatalf("BisectSkip: %v", err)
	}
	if skipped.Current == "" {
		t.Fatal("BisectSkip: empty Current")
	}
	if skipped.Current == before {
		t.Fatalf("BisectSkip: Current unchanged (%s); want a different candidate toward good", before)
	}
	if skipped.Done {
		t.Fatal("BisectSkip: Done = true, want false")
	}

	// Info reflects the active session.
	info, err := e.BisectInfo()
	if err != nil {
		t.Fatalf("BisectInfo: %v", err)
	}
	if !info.Active {
		t.Fatal("BisectInfo.Active = false, want true")
	}
	if info.Current != skipped.Current {
		t.Fatalf("BisectInfo.Current = %s, want %s", info.Current, skipped.Current)
	}
}
