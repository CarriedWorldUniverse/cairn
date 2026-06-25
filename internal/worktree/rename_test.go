package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRenameWithRetry covers the happy path on every platform (on Windows it also
// exercises the retry wrapper). The transient-lock retry itself is Windows-only
// and not deterministically reproducible in a unit test.
func TestRenameWithRetry(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(root, "nested", "dst") // parent must already exist
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := renameWithRetry(src, dst); err != nil {
		t.Fatalf("renameWithRetry: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dst, "f.txt")); err != nil || string(got) != "hi\n" {
		t.Fatalf("renamed content = %q err %v", got, err)
	}
	if err := removeAllWithRetry(dst); err != nil {
		t.Fatalf("removeAllWithRetry: %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("dst still exists after removeAllWithRetry: %v", err)
	}
}
