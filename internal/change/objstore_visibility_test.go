package change

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing"
)

func gitIn(t *testing.T, dir string, stdin []byte, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

// Two object stores on one repository, as two cairn processes are: one scans
// the pack directory and misses, the other then lands a pack (the way a fetch
// does, through PackfileWriter). The first must see it without being reopened.
func TestObjStoreSeesPackWrittenByAnotherStore(t *testing.T) {
	src := t.TempDir()
	gitIn(t, src, nil, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("hello from the other process\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, src, nil, "add", "-A")
	gitIn(t, src, nil, "commit", "-qm", "c")
	head := strings.TrimSpace(string(gitIn(t, src, nil, "rev-parse", "HEAD")))
	pack := gitIn(t, src, []byte(head+"\n"), "pack-objects", "--stdout", "--revs")

	bare := filepath.Join(t.TempDir(), "objects.git")
	if err := os.MkdirAll(filepath.Join(bare, "objects", "pack"), 0o755); err != nil {
		t.Fatal(err)
	}
	reader := newObjStore(osfs.New(bare), bare)
	writer := newObjStore(osfs.New(bare), bare)
	defer reader.Close()
	defer writer.Close()

	h := plumbing.NewHash(head)
	if _, err := reader.EncodedObject(plumbing.AnyObject, h); err == nil {
		t.Fatal("object present before any pack was written")
	}
	if reader.HasEncodedObject(h) == nil {
		t.Fatal("Has true before any pack was written")
	}

	w, err := writer.PackfileWriter()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(pack); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	if err := reader.HasEncodedObject(h); err != nil {
		t.Fatalf("reader does not see the pack the other store wrote: %v", err)
	}
	o, err := reader.EncodedObject(plumbing.CommitObject, h)
	if err != nil || o.Type() != plumbing.CommitObject {
		t.Fatalf("reader cannot read the commit from the new pack: %v", err)
	}
	// go-git's own pack index is refreshed too (a miss reindexes it).
	if reader.gitStale() {
		reader.mu.Lock()
		_, gerr := reader.Storage.EncodedObject(plumbing.CommitObject, h)
		reader.mu.Unlock()
		if gerr != nil {
			t.Fatalf("go-git index still stale after the refresh: %v", gerr)
		}
	}
}

// An object-not-found from the engine names the layers consulted and why each
// missed, and still satisfies errors.Is(plumbing.ErrObjectNotFound).
func TestReadBlobMissNamesTheLayers(t *testing.T) {
	e := newTestEngine(t)
	_, err := e.ReadBlob("1111111111111111111111111111111111111111")
	if err == nil {
		t.Fatal("read of a missing blob succeeded")
	}
	if !errors.Is(err, plumbing.ErrObjectNotFound) {
		t.Fatalf("error no longer wraps ErrObjectNotFound: %v", err)
	}
	for _, want := range []string{"fast pack reader", "loose", "go-git"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}
