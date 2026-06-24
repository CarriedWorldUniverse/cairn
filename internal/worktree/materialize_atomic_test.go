package worktree

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

// TestMaterializeAtomicSwap verifies that materializing a second commit into an
// already-materialized directory replaces all content correctly and leaves no
// .cairn-tmp sibling directory behind.
func TestMaterializeAtomicSwap(t *testing.T) {
	eng, err := change.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	main, _ := eng.LineByName("main")
	ch, _ := eng.CreateChange(main.ID, "t")

	// First commit: a.txt = v1.
	r1, err := eng.Commit(ch.ID, map[string][]byte{"a.txt": []byte("v1\n")}, nil, "")
	if err != nil {
		t.Fatalf("Commit r1: %v", err)
	}

	cacheDir := filepath.Join(t.TempDir(), "cache")
	dir := filepath.Join(t.TempDir(), "wc")

	if err := Materialize(eng, cacheDir, r1.HeadCommit, dir); err != nil {
		t.Fatalf("Materialize r1: %v", err)
	}

	// Second commit: a.txt = v2, add b.txt.
	r2, err := eng.Commit(ch.ID, map[string][]byte{"a.txt": []byte("v2\n"), "b.txt": []byte("hello\n")}, nil, "")
	if err != nil {
		t.Fatalf("Commit r2: %v", err)
	}

	if err := Materialize(eng, cacheDir, r2.HeadCommit, dir); err != nil {
		t.Fatalf("Materialize r2: %v", err)
	}

	// a.txt must be v2.
	got, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatalf("read a.txt: %v", err)
	}
	if string(got) != "v2\n" {
		t.Fatalf("a.txt = %q, want %q", got, "v2\n")
	}

	// b.txt must be present.
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); err != nil {
		t.Fatalf("b.txt not found: %v", err)
	}

	// No .cairn-tmp sibling left in the parent directory.
	tmp := dir + ".cairn-tmp"
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf(".cairn-tmp sibling %q still exists after successful Materialize", tmp)
	}
}

// TestMaterializeFailureLeavesOriginalIntact verifies that a failure during
// eng.Files (e.g. a bogus commit SHA) leaves the already-materialized dir
// completely untouched and removes any temp directory that was created.
func TestMaterializeFailureLeavesOriginalIntact(t *testing.T) {
	eng, err := change.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	main, _ := eng.LineByName("main")
	ch, _ := eng.CreateChange(main.ID, "t")

	// Good first commit.
	r1, err := eng.Commit(ch.ID, map[string][]byte{"a.txt": []byte("v1\n")}, nil, "")
	if err != nil {
		t.Fatalf("Commit r1: %v", err)
	}

	cacheDir := filepath.Join(t.TempDir(), "cache")
	dir := filepath.Join(t.TempDir(), "wc")

	if err := Materialize(eng, cacheDir, r1.HeadCommit, dir); err != nil {
		t.Fatalf("Materialize r1: %v", err)
	}

	// Attempt to materialize a bogus SHA — eng.Files must error before any
	// destructive step, so dir is left intact.
	bogus := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	if err := Materialize(eng, cacheDir, bogus, dir); err == nil {
		t.Fatal("Materialize with bogus SHA should have returned an error")
	}

	// dir must still contain a.txt = v1.
	got, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatalf("read a.txt after failed Materialize: %v", err)
	}
	if string(got) != "v1\n" {
		t.Fatalf("a.txt = %q after failed Materialize, want %q (dir was modified)", got, "v1\n")
	}

	// No .cairn-tmp sibling litter.
	tmp := dir + ".cairn-tmp"
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf(".cairn-tmp sibling %q still exists after failed Materialize", tmp)
	}
}
