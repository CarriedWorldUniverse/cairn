package worktree

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `cairn abandon` discards a line; `tree` stops listing it and `push` refuses
// it (#159). Express under the same name used to reuse the abandoned line's
// change and materialize a folder for it anyway (#172) — a working copy for a
// line nothing else acknowledges. It must refuse, and say what to do instead.
func TestExpressRefusesAnAbandonedLine(t *testing.T) {
	r, def := seedBranch(t)

	if err := r.Express("feat", def); err != nil {
		t.Fatalf("Express feat: %v", err)
	}
	dir := filepath.Join(r.Root(), r.st.Expressed["feat"].Path)
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("feat", "work"); err != nil {
		t.Fatalf("Commit feat: %v", err)
	}
	// Abandon unexpresses the folder itself as part of discarding the line.
	if err := r.Abandon("feat", true); err != nil {
		t.Fatalf("Abandon: %v", err)
	}

	err := r.Express("feat", "")
	if err == nil {
		t.Fatal("Express of an abandoned line succeeded; it silently revived a discarded line")
	}
	if !errors.Is(err, ErrLineAbandoned) {
		t.Fatalf("error is not ErrLineAbandoned: %v", err)
	}
	if !strings.Contains(err.Error(), "--from "+def) {
		t.Fatalf("error does not tell the operator how to proceed (want an 'express NEW --from %s' hint): %v", def, err)
	}
	if _, serr := os.Stat(dir); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("a folder was created for the abandoned line: %v", serr)
	}
	if _, expressed := r.st.Expressed["feat"]; expressed {
		t.Fatal("wc.json records the abandoned line as expressed")
	}
}

// The refusal must not leak into the ordinary case: a brand-new name still
// expresses, and so does a live line that was merely unexpressed.
func TestExpressStillWorksForLiveAndNewLines(t *testing.T) {
	r, def := seedBranch(t)
	if err := r.Express("live", def); err != nil {
		t.Fatalf("Express new line: %v", err)
	}
	if err := r.Unexpress("live", true); err != nil {
		t.Fatalf("Unexpress: %v", err)
	}
	if err := r.Express("live", ""); err != nil {
		t.Fatalf("re-Express of a live (not abandoned) line: %v", err)
	}
}
