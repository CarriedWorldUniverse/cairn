package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReflinkOrCopyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	want := []byte("hello reflink\n")
	if err := os.WriteFile(src, want, 0o644); err != nil { t.Fatal(err) }
	if err := reflinkOrCopy(src, dst); err != nil { t.Fatalf("reflinkOrCopy: %v", err) }
	got, err := os.ReadFile(dst)
	if err != nil { t.Fatalf("read dst: %v", err) }
	if string(got) != string(want) { t.Fatalf("dst = %q, want %q", got, want) }
}

func TestReflinkOrCopyIndependentAfterWrite(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("original\n"), 0o644); err != nil { t.Fatal(err) }
	if err := reflinkOrCopy(src, dst); err != nil { t.Fatalf("reflinkOrCopy: %v", err) }
	if err := os.WriteFile(dst, []byte("changed\n"), 0o644); err != nil { t.Fatal(err) }
	got, _ := os.ReadFile(src)
	if string(got) != "original\n" { t.Fatalf("src mutated to %q", got) }
}

func TestReflinkOrCopyTruncatesExistingDst(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src"); dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("new\n"), 0o644); err != nil { t.Fatal(err) }
	if err := os.WriteFile(dst, []byte("STALE-LONGER-CONTENT\n"), 0o644); err != nil { t.Fatal(err) }
	if err := reflinkOrCopy(src, dst); err != nil { t.Fatalf("reflinkOrCopy: %v", err) }
	got, _ := os.ReadFile(dst)
	if string(got) != "new\n" { t.Fatalf("dst = %q, want new (no stale residue)", got) }
}
