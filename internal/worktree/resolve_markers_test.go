package worktree

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/internal/change/diff3"
)

// conflictedRepo produces a real modify/modify conflict on exp/f.txt exactly
// as cairn writes it (ours / base / ======= / theirs markers).
func conflictedRepo(t *testing.T) (*Repo, string) {
	t.Helper()
	root := t.TempDir()
	r, err := Open(root, "t")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	write := func(rel, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main/f.txt", "line1\nline2\nline3\n")
	if _, err := r.Commit("main", ""); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := r.Express("exp", "main"); err != nil {
		t.Fatalf("express: %v", err)
	}
	write("main/f.txt", "line1\nMAIN\nline3\n")
	if _, err := r.Commit("main", ""); err != nil {
		t.Fatalf("main adv: %v", err)
	}
	write("exp/f.txt", "line1\nFEAT\nline3\n")
	res, err := r.Commit("exp", "")
	if err != nil {
		t.Fatalf("exp commit: %v", err)
	}
	if len(res.Conflicts) == 0 {
		t.Fatal("expected a conflict on f.txt")
	}
	return r, filepath.Join(root, "exp", "f.txt")
}

// The report: delete ONLY the "<<<<<<< ours" line and run resolve. The file
// still carries the base marker, the separator and the closer, and used to
// be accepted — and sealed — as the resolution.
func TestResolveRefusesAPartiallyResolvedFile(t *testing.T) {
	r, p := conflictedRepo(t)
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "<<<<<<< ours\n") || !strings.Contains(string(data), "||||||| base\n") {
		t.Fatalf("precondition: not the diff3 layout cairn writes:\n%s", data)
	}
	partial := strings.Replace(string(data), "<<<<<<< ours\n", "", 1)
	if err := os.WriteFile(p, []byte(partial), 0o644); err != nil {
		t.Fatal(err)
	}

	err = r.Resolve("exp", "f.txt", false)
	if err == nil {
		t.Fatal("resolve accepted a file that still contains ||||||| / ======= / >>>>>>> lines")
	}
	var me *diff3.MarkerError
	if !errors.As(err, &me) {
		t.Fatalf("error does not carry the marker location: %v", err)
	}
	if me.Marker != "||||||| base" || me.Line != 3 {
		t.Fatalf("reported %q at line %d, want \"||||||| base\" at line 3", me.Marker, me.Line)
	}
	open, err := r.eng.Conflicts(r.st.Expressed["exp"].ChangeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 {
		t.Fatalf("conflict row was cleared by a refused resolve: %d open", len(open))
	}

	// --force is the escape hatch for content that really is meant to look
	// like that; it still takes the file as-is.
	if err := r.Resolve("exp", "f.txt", true); err != nil {
		t.Fatalf("--force should accept: %v", err)
	}
}

// A genuinely resolved file still goes through, and a lone stray closer is
// caught even though there is no block around it.
func TestResolveAcceptsCleanContentAndCatchesAStrayCloser(t *testing.T) {
	r, p := conflictedRepo(t)
	if err := os.WriteFile(p, []byte("line1\nMERGED\nline3\n>>>>>>> theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.Resolve("exp", "f.txt", false); err == nil {
		t.Fatal("a stray >>>>>>> line was accepted")
	}
	if err := os.WriteFile(p, []byte("line1\nMERGED\nline3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.Resolve("exp", "f.txt", false); err != nil {
		t.Fatalf("clean content refused: %v", err)
	}
	open, _ := r.eng.Conflicts(r.st.Expressed["exp"].ChangeID)
	if len(open) != 0 {
		t.Fatalf("conflict still open after a clean resolve: %d", len(open))
	}
}
