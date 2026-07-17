package worktree

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// captureWarnings swaps the package's warnf hook to append every formatted
// message into the returned slice's backing pointer, restoring the original
// hook via t.Cleanup. Tests use it to assert a skip was actually warned about,
// not silently dropped.
func captureWarnings(t *testing.T) *[]string {
	t.Helper()
	var got []string
	orig := warnf
	warnf = func(format string, args ...any) {
		got = append(got, fmt.Sprintf(format, args...))
	}
	t.Cleanup(func() { warnf = orig })
	return &got
}

func TestScanSkipsUnreadableUntrackedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based unreadability is not meaningful on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("chmod ineffective as root")
	}
	warnings := captureWarnings(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "locked.txt")
	if err := os.WriteFile(path, []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	out, _, err := Scan(dir, nil)
	if err != nil {
		t.Fatalf("Scan: unexpected error for unreadable untracked file: %v", err)
	}
	if _, ok := out["locked.txt"]; ok {
		t.Fatalf("expected locked.txt absent from scan result, got %v", out)
	}
	if len(*warnings) == 0 {
		t.Fatalf("expected a warning about the skipped unreadable path, got none")
	}
}

func TestScanErrorsOnUnreadableTrackedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based unreadability is not meaningful on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("chmod ineffective as root")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "locked.txt")
	if err := os.WriteFile(path, []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	tracked := map[string]struct{}{"locked.txt": {}}
	if _, _, err := Scan(dir, tracked); err == nil {
		t.Fatalf("expected Scan to error on an unreadable TRACKED file, got nil")
	}
}

func TestScanSkipsUnreadableUntrackedSubdir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based unreadability is not meaningful on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("chmod ineffective as root")
	}
	warnings := captureWarnings(t)

	dir := t.TempDir()
	sub := filepath.Join(dir, "locked-dir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	inner := filepath.Join(sub, "inside.txt")
	if err := os.WriteFile(inner, []byte("hidden\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// A sibling that must still be seen, to prove the walk continues past the
	// skipped subtree.
	sibling := filepath.Join(dir, "visible.txt")
	if err := os.WriteFile(sibling, []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("write sibling: %v", err)
	}
	if err := os.Chmod(sub, 0o000); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	out, _, err := Scan(dir, nil)
	if err != nil {
		t.Fatalf("Scan: unexpected error for unreadable untracked subdir: %v", err)
	}
	if _, ok := out["locked-dir/inside.txt"]; ok {
		t.Fatalf("expected locked-dir/inside.txt absent from scan result, got %v", out)
	}
	if _, ok := out["visible.txt"]; !ok {
		t.Fatalf("expected sibling visible.txt to still be scanned, got %v", out)
	}
	if len(*warnings) == 0 {
		t.Fatalf("expected a warning about the skipped unreadable directory, got none")
	}
}

func TestCachedScanSkipsUnreadableUntrackedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based unreadability is not meaningful on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("chmod ineffective as root")
	}
	warnings := captureWarnings(t)

	eng := newCacheTestEngine(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "locked.txt")
	if err := os.WriteFile(path, []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	start := time.Now().UnixNano() + int64(time.Second)
	entries, cache, _, err := CachedScan(eng, dir, nil, nil, start)
	if err != nil {
		t.Fatalf("CachedScan: unexpected error for unreadable untracked file: %v", err)
	}
	if _, ok := entries["locked.txt"]; ok {
		t.Fatalf("expected locked.txt absent from entries, got %v", entries)
	}
	if _, ok := cache["locked.txt"]; ok {
		t.Fatalf("expected locked.txt absent from rebuilt cache, got %v", cache)
	}
	if len(*warnings) == 0 {
		t.Fatalf("expected a warning about the skipped unreadable path, got none")
	}
}
