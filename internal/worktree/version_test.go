package worktree

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/internal/version"
)

func TestDeriveInputTrunk(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	def, err := r.DefaultBranch()
	if err != nil {
		t.Fatal(err)
	}

	// First commit on default branch
	if err := os.WriteFile(filepath.Join(root, def, "a.txt"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(def, ""); err != nil {
		t.Fatalf("commit a.txt: %v", err)
	}

	// Tag the first commit
	if err := r.Tag("v1.0.0", def); err != nil {
		t.Fatal(err)
	}

	// Second commit (one ahead of the tag)
	if err := os.WriteFile(filepath.Join(root, def, "b.txt"), []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(def, ""); err != nil {
		t.Fatalf("commit b.txt: %v", err)
	}

	in, err := r.DeriveInput(def, version.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if in.BaseTag != "v1.0.0" || in.Distance != 1 {
		t.Fatalf("DeriveInput = %+v; want BaseTag v1.0.0 Distance 1", in)
	}
	if !in.IsTrunk || in.LineName != def {
		t.Fatalf("trunk/line wrong: %+v", in)
	}
	if in.ShortSHA == "" {
		t.Error("ShortSHA empty")
	}
}

func TestPendingBumpRoundTrip(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	if err := r.SetPendingBump("minor"); err != nil {
		t.Fatal(err)
	}
	got, err := r.PendingBump()
	if err != nil {
		t.Fatal(err)
	}
	if got != "minor" {
		t.Fatalf("PendingBump = %q, want minor", got)
	}
}
