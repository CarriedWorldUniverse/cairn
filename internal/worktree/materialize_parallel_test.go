package worktree

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestMaterializeParallelMatchesSerial is the correctness gate on #151: the
// worker pool must produce byte-identical output to a single worker, content
// and mode alike. Speed is worthless if a concurrent write lands wrong.
func TestMaterializeParallelMatchesSerial(t *testing.T) {
	skipOnWindows(t)
	url, _ := makeOriginRepoWT(t)

	fingerprint := func(root string) map[string]string {
		out := map[string]string{}
		_ = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
			if err != nil || fi.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			if filepath.HasPrefix(rel, ".cairn") {
				return nil
			}
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				return nil
			}
			out[filepath.ToSlash(rel)] = fi.Mode().String() + ":" + string(b)
			return nil
		})
		return out
	}

	clone := func(workers int) map[string]string {
		t.Helper()
		t.Setenv("CAIRN_MATERIALIZE_WORKERS", strconv.Itoa(workers))
		root := filepath.Join(t.TempDir(), "wc")
		r, err := Clone(url, root, "tester", nil)
		if err != nil {
			t.Fatalf("Clone(workers=%d): %v", workers, err)
		}
		defer func() { _ = r.Close() }()
		return fingerprint(root)
	}

	serial, parallel := clone(1), clone(8)
	if len(serial) == 0 {
		t.Fatal("fingerprint empty — the fixture cannot detect a regression")
	}
	if len(serial) != len(parallel) {
		t.Fatalf("file count differs: serial=%d parallel=%d", len(serial), len(parallel))
	}
	for path, want := range serial {
		got, ok := parallel[path]
		if !ok {
			t.Errorf("%s: missing from the parallel checkout", path)
			continue
		}
		if got != want {
			t.Errorf("%s: parallel checkout differs (mode:content)", path)
		}
	}
}

// TestMaterializeWorkersOverride pins the escape hatch, which exists so a
// Windows box whose AV scans every file creation can be tuned down.
func TestMaterializeWorkersOverride(t *testing.T) {
	t.Setenv("CAIRN_MATERIALIZE_WORKERS", "3")
	if got := materializeWorkers(); got != 3 {
		t.Errorf("materializeWorkers() = %d, want 3 from the env override", got)
	}
	t.Setenv("CAIRN_MATERIALIZE_WORKERS", "garbage")
	if got := materializeWorkers(); got < 1 {
		t.Errorf("a bad override yielded %d; it must fall back to a sane default", got)
	}
	t.Setenv("CAIRN_MATERIALIZE_WORKERS", "0")
	if got := materializeWorkers(); got < 1 {
		t.Errorf("zero override yielded %d; must never be < 1", got)
	}
}
