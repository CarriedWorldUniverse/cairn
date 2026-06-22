package diff3

import (
	"strings"
	"testing"
)

func lines(s string) []string { return strings.SplitAfter(s, "\n") }

func TestMerge3CleanNonOverlap(t *testing.T) {
	base := lines("a\nb\nc\n")
	ours := lines("a\nB\nc\n")
	theirs := lines("a\nb\nC\n")
	got := Merge3(base, ours, theirs)
	if got.Conflict {
		t.Fatalf("expected no conflict, got conflict; merged=%q", got.Merged)
	}
	want := "a\nB\nC\n"
	if strings.Join(got.Merged, "") != want {
		t.Fatalf("merged = %q, want %q", strings.Join(got.Merged, ""), want)
	}
}

func TestMerge3ConflictBothEditSameLine(t *testing.T) {
	base := lines("a\nb\nc\n")
	ours := lines("a\nX\nc\n")
	theirs := lines("a\nY\nc\n")
	got := Merge3(base, ours, theirs)
	if !got.Conflict {
		t.Fatalf("expected conflict, got none; merged=%q", got.Merged)
	}
	out := strings.Join(got.Merged, "")
	for _, marker := range []string{"<<<<<<<", "=======", ">>>>>>>"} {
		if !strings.Contains(out, marker) {
			t.Fatalf("output missing marker %q; got:\n%s", marker, out)
		}
	}
}

func TestMerge3IdenticalEditsNoConflict(t *testing.T) {
	base := lines("a\nb\n")
	ours := lines("a\nZ\n")
	theirs := lines("a\nZ\n")
	got := Merge3(base, ours, theirs)
	if got.Conflict {
		t.Fatalf("expected no conflict, got conflict; merged=%q", got.Merged)
	}
	want := "a\nZ\n"
	if strings.Join(got.Merged, "") != want {
		t.Fatalf("merged = %q, want %q", strings.Join(got.Merged, ""), want)
	}
}

func TestMerge3DeleteVsModifyConflicts(t *testing.T) {
	base := lines("a\nb\nc\n")
	ours := lines("a\nc\n")   // deleted b
	theirs := lines("a\nB\nc\n") // modified b
	got := Merge3(base, ours, theirs)
	if !got.Conflict {
		t.Fatalf("expected conflict (delete vs modify), got none; merged=%q", got.Merged)
	}
}

func TestMerge3CleanInsertOnDifferentSidesEOF(t *testing.T) {
	// ours appends a line at the top region, theirs appends at EOF: disjoint.
	base := lines("a\nb\n")
	ours := lines("A\na\nb\n")   // insert before a
	theirs := lines("a\nb\nc\n") // insert c at EOF
	got := Merge3(base, ours, theirs)
	if got.Conflict {
		t.Fatalf("expected no conflict, got conflict; merged=%q", got.Merged)
	}
	want := "A\na\nb\nc\n"
	if strings.Join(got.Merged, "") != want {
		t.Fatalf("merged = %q, want %q", strings.Join(got.Merged, ""), want)
	}
}

func TestMerge3IdenticalDeleteNoConflict(t *testing.T) {
	base := lines("a\nb\nc\n")
	ours := lines("a\nc\n")   // deleted b
	theirs := lines("a\nc\n") // deleted b too
	got := Merge3(base, ours, theirs)
	if got.Conflict {
		t.Fatalf("expected no conflict, got conflict; merged=%q", got.Merged)
	}
	want := "a\nc\n"
	if strings.Join(got.Merged, "") != want {
		t.Fatalf("merged = %q, want %q", strings.Join(got.Merged, ""), want)
	}
}

func TestMerge3OnlyOursChanges(t *testing.T) {
	base := lines("a\nb\nc\n")
	ours := lines("a\nB\nc\n")
	theirs := lines("a\nb\nc\n")
	got := Merge3(base, ours, theirs)
	if got.Conflict {
		t.Fatalf("expected no conflict, got conflict; merged=%q", got.Merged)
	}
	want := "a\nB\nc\n"
	if strings.Join(got.Merged, "") != want {
		t.Fatalf("merged = %q, want %q", strings.Join(got.Merged, ""), want)
	}
}

func TestMerge3OnlyTheirsChanges(t *testing.T) {
	base := lines("a\nb\nc\n")
	ours := lines("a\nb\nc\n")   // unchanged
	theirs := lines("a\nB\nc\n") // only theirs modifies b
	got := Merge3(base, ours, theirs)
	if got.Conflict {
		t.Fatalf("expected no conflict, got conflict; merged=%q", got.Merged)
	}
	if strings.Join(got.Merged, "") != "a\nB\nc\n" {
		t.Fatalf("merged = %q, want a\\nB\\nc\\n", strings.Join(got.Merged, ""))
	}
}

func TestMerge3ConflictBothInsertAtSamePosition(t *testing.T) {
	base := lines("a\nb\n")
	ours := lines("X\na\nb\n")   // insert X before a
	theirs := lines("Y\na\nb\n") // insert Y before a
	if got := Merge3(base, ours, theirs); !got.Conflict {
		t.Fatal("conflicting inserts at same position must conflict")
	}
}

func TestMerge3IdenticalInsertsAtSamePositionNoConflict(t *testing.T) {
	base := lines("a\nb\n")
	ours := lines("X\na\nb\n")
	theirs := lines("X\na\nb\n")
	got := Merge3(base, ours, theirs)
	if got.Conflict {
		t.Fatalf("identical inserts must not conflict; merged=%q", got.Merged)
	}
	if strings.Join(got.Merged, "") != "X\na\nb\n" {
		t.Fatalf("merged = %q, want X\\na\\nb\\n", strings.Join(got.Merged, ""))
	}
}

func TestMerge3CascadingOverlapIsConflict(t *testing.T) {
	// ours replaces lines 0-1, theirs replaces lines 1-2 — they share line 1,
	// so they must land in a single conflict block, not split cleanly.
	base := lines("a\nb\nc\nd\n")
	ours := lines("A\nB\nc\nd\n")
	theirs := lines("a\nB2\nC\nd\n")
	got := Merge3(base, ours, theirs)
	if !got.Conflict {
		t.Fatal("overlapping cross-side regions must conflict")
	}
	if !strings.Contains(strings.Join(got.Merged, ""), "d\n") {
		t.Fatalf("clean trailing line lost: %q", strings.Join(got.Merged, ""))
	}
}

func TestMerge3EmptyBaseConflict(t *testing.T) {
	got := Merge3([]string{}, lines("X\n"), lines("Y\n"))
	if !got.Conflict {
		t.Fatal("both sides adding to empty base with different content must conflict")
	}
}

func TestMerge3EmptyBaseIdentical(t *testing.T) {
	got := Merge3([]string{}, lines("X\n"), lines("X\n"))
	if got.Conflict {
		t.Fatalf("identical adds to empty base must not conflict; merged=%q", got.Merged)
	}
}

func TestMerge3NoTrailingNewline(t *testing.T) {
	// content whose last line has no trailing newline (SplitAfter yields a final
	// element without "\n"); a clean non-overlapping merge must still work.
	base := lines("a\nb")   // ["a\n","b"]
	ours := lines("A\nb")   // change first line
	theirs := lines("a\nB") // change last (newline-less) line
	got := Merge3(base, ours, theirs)
	if got.Conflict {
		t.Fatalf("expected clean merge, got conflict; merged=%q", got.Merged)
	}
	if strings.Join(got.Merged, "") != "A\nB" {
		t.Fatalf("merged = %q, want A\\nB", strings.Join(got.Merged, ""))
	}
}
