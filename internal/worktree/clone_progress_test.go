package worktree

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// TestClonePhasesAfterThePackReportProgress pins #146: once the git sideband
// stopped (its last line is "Total N (delta M) …") the clone used to go silent
// for as long as the remaining phases took — resolving the default branch over
// a SECOND network round-trip, one mergeBase per branch, and materializing
// every file. On a large repo that was minutes of apparent hang.
func TestClonePhasesAfterThePackReportProgress(t *testing.T) {
	skipOnWindows(t)
	url, _ := makeOriginRepoWT(t) // default branch + "feature", so a branch IS mapped

	var progress bytes.Buffer
	r, err := Clone(url, filepath.Join(t.TempDir(), "wc"), "tester", &progress)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	got := progress.String()
	for _, want := range []string{
		"resolving the remote's default branch", // the second network round-trip
		"mapping branches onto the line tree",   // the per-branch mergeBase loop
		"materializing",                         // the per-file write
	} {
		if !strings.Contains(got, want) {
			t.Errorf("clone progress never mentioned %q — that phase is silent again (#146).\ngot:\n%s", want, got)
		}
	}
}

// TestMaterializeIsSilentWithoutAProgressWriter is the containment half of the
// fix, and the reason the counter can live in materialize at all: ten verbs
// besides clone (pull, express, resolve, bisect …) reach the same function.
// Only Clone ever installs a progress writer, so every one of them must keep
// the output it had. A regression here spams unrelated commands.
func TestMaterializeIsSilentWithoutAProgressWriter(t *testing.T) {
	skipOnWindows(t)
	url, _ := makeOriginRepoWT(t)

	var progress bytes.Buffer
	root := filepath.Join(t.TempDir(), "wc")
	r, err := Clone(url, root, "tester", &progress)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if progress.Len() == 0 {
		t.Fatal("clone wrote no progress at all — the fixture cannot detect a regression")
	}

	// Same engine, same materialize, but reached the way every non-clone verb
	// reaches it: no progress writer installed.
	r.eng.SetProgress(nil)
	before := progress.Len()
	if err := r.Express("feature", ""); err != nil {
		t.Fatalf("Express: %v", err)
	}
	if progress.Len() != before {
		t.Errorf("a non-clone verb wrote %d byte(s) of progress; it must stay silent:\n%s",
			progress.Len()-before, progress.String()[before:])
	}
}
