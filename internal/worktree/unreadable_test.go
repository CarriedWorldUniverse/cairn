package worktree

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
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

	out, _, skipped, err := Scan(dir, nil)
	if err != nil {
		t.Fatalf("Scan: unexpected error for unreadable untracked file: %v", err)
	}
	if _, ok := out["locked.txt"]; ok {
		t.Fatalf("expected locked.txt absent from scan result, got %v", out)
	}
	if want := []string{"locked.txt"}; !reflect.DeepEqual(skipped, want) {
		t.Fatalf("skipped = %v, want %v", skipped, want)
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
	if _, _, _, err := Scan(dir, tracked); err == nil {
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

	out, _, skipped, err := Scan(dir, nil)
	if err != nil {
		t.Fatalf("Scan: unexpected error for unreadable untracked subdir: %v", err)
	}
	if _, ok := out["locked-dir/inside.txt"]; ok {
		t.Fatalf("expected locked-dir/inside.txt absent from scan result, got %v", out)
	}
	if _, ok := out["visible.txt"]; !ok {
		t.Fatalf("expected sibling visible.txt to still be scanned, got %v", out)
	}
	if want := []string{"locked-dir/"}; !reflect.DeepEqual(skipped, want) {
		t.Fatalf("skipped = %v, want %v", skipped, want)
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
	entries, cache, _, skipped, err := CachedScan(eng, dir, nil, nil, start)
	if err != nil {
		t.Fatalf("CachedScan: unexpected error for unreadable untracked file: %v", err)
	}
	if _, ok := entries["locked.txt"]; ok {
		t.Fatalf("expected locked.txt absent from entries, got %v", entries)
	}
	if _, ok := cache["locked.txt"]; ok {
		t.Fatalf("expected locked.txt absent from rebuilt cache, got %v", cache)
	}
	if want := []string{"locked.txt"}; !reflect.DeepEqual(skipped, want) {
		t.Fatalf("skipped = %v, want %v", skipped, want)
	}
	if len(*warnings) == 0 {
		t.Fatalf("expected a warning about the skipped unreadable path, got none")
	}
}

func TestCachedScanErrorsOnUnreadableTrackedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based unreadability is not meaningful on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("chmod ineffective as root")
	}

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

	tracked := map[string]struct{}{"locked.txt": {}}
	start := time.Now().UnixNano() + int64(time.Second)
	_, _, _, _, err := CachedScan(eng, dir, tracked, nil, start)
	if err == nil {
		t.Fatalf("expected CachedScan to error on an unreadable TRACKED file, got nil")
	}
	if !strings.Contains(err.Error(), "locked.txt") {
		t.Fatalf("expected error to mention the tracked path %q, got: %v", "locked.txt", err)
	}
}

func TestScanErrorsOnTrackedFileUnderUnreadableDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based unreadability is not meaningful on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("chmod ineffective as root")
	}

	dir := t.TempDir()
	sub := filepath.Join(dir, "locked-dir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	inner := filepath.Join(sub, "inside.txt")
	if err := os.WriteFile(inner, []byte("hidden\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(sub, 0o000); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	// A tracked path lives under the now-unreadable directory: skipping it
	// (the untracked treatment) would silently drop a committed file from the
	// snapshot, so this must be a hard error instead.
	tracked := map[string]struct{}{"locked-dir/inside.txt": {}}
	if _, _, _, err := Scan(dir, tracked); err == nil {
		t.Fatalf("expected Scan to error when a tracked path lives under an unreadable directory, got nil")
	}
}

// TestScanCapsIndividualWarningsAndSummarizes covers the LOW item (#130):
// a single walk emits at most maxIndividualSkipWarnings per-file warnf lines,
// then one "…and N more" summary — while the structural skipped list (what
// CommitResult/StatusInfo actually surface) still carries every path.
func TestScanCapsIndividualWarningsAndSummarizes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based unreadability is not meaningful on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("chmod ineffective as root")
	}
	warnings := captureWarnings(t)

	const n = 12
	dir := t.TempDir()
	var want []string
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("locked-%02d.txt", i)
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("secret\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		if err := os.Chmod(path, 0o000); err != nil {
			t.Fatalf("chmod %s: %v", name, err)
		}
		t.Cleanup(func(p string) func() { return func() { _ = os.Chmod(p, 0o644) } }(path))
		want = append(want, name)
	}

	_, _, skipped, err := Scan(dir, nil)
	if err != nil {
		t.Fatalf("Scan: unexpected error: %v", err)
	}
	if len(skipped) != n {
		t.Fatalf("skipped = %v (len %d), want %d entries", skipped, len(skipped), n)
	}
	sort.Strings(skipped)
	sort.Strings(want)
	if !reflect.DeepEqual(skipped, want) {
		t.Fatalf("skipped = %v, want %v", skipped, want)
	}
	// maxIndividualSkipWarnings per-file lines + exactly one summary line.
	if got, want := len(*warnings), maxIndividualSkipWarnings+1; got != want {
		t.Fatalf("warnf call count = %d, want %d (got %v)", got, want, *warnings)
	}
	last := (*warnings)[len(*warnings)-1]
	if !strings.Contains(last, "more") {
		t.Fatalf("expected the final warnf call to be the summary line, got %q", last)
	}
}

// TestMaterializeLeavesUnreadableUntrackedFileAlone guards the deletion-side
// half of #130: Materialize's stale-file pass must not delete an unreadable
// untracked file just because it isn't in the target tree — that would be
// STRICTLY WORSE than the original bug (silent data loss instead of a hard
// abort) and would defeat the whole point of tolerating it during the scan.
func TestMaterializeLeavesUnreadableUntrackedFileAlone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based unreadability is not meaningful on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("chmod ineffective as root")
	}
	warnings := captureWarnings(t)

	eng := newCacheTestEngine(t)
	main, err := eng.LineByName("main")
	if err != nil {
		t.Fatalf("LineByName: %v", err)
	}
	ch, err := eng.CreateChange(main.ID, "t")
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	res, err := eng.Commit(ch.ID, map[string][]byte{"base.txt": []byte("base\n")}, nil, "")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	cacheDir := filepath.Join(t.TempDir(), "cache")
	dir := t.TempDir()
	if err := Materialize(eng, cacheDir, res.HeadCommit, dir); err != nil {
		t.Fatalf("initial Materialize: %v", err)
	}

	locked := filepath.Join(dir, "locked.txt")
	if err := os.WriteFile(locked, []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("write locked.txt: %v", err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o644) })

	// Re-materialize the SAME commit: locked.txt is untracked (not in the
	// target tree) and would normally be pruned as stale cruft.
	if err := Materialize(eng, cacheDir, res.HeadCommit, dir); err != nil {
		t.Fatalf("second Materialize: %v", err)
	}
	if _, err := os.Lstat(locked); err != nil {
		t.Fatalf("expected locked.txt to survive Materialize's deletion pass, but: %v", err)
	}
	if len(*warnings) == 0 {
		t.Fatalf("expected a warning about the untouched unreadable path, got none")
	}
}

func TestDisplayPath(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "src/main.go", "src/main.go"},
		{"esc-sequence", "src/\x1b[31mmain.go", `"src/\x1b[31mmain.go"`},
		{"del-byte", "src/main\x7f.go", `"src/main\x7f.go"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DisplayPath(c.in); got != c.want {
				t.Fatalf("DisplayPath(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
