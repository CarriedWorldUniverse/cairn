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
