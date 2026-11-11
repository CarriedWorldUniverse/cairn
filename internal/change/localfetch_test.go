package change

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func localFetchSource(t *testing.T, gc bool) string {
	t.Helper()
	src := t.TempDir()
	gitIn(t, src, nil, "init", "-q", "-b", "main")
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte(strings.Repeat("line\n", i+1)), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, src, nil, "add", "-A")
		gitIn(t, src, nil, "commit", "-qm", "c")
	}
	gitIn(t, src, nil, "tag", "-a", "-m", "annotated", "v1")
	gitIn(t, src, nil, "branch", "side")
	if gc {
		gitIn(t, src, nil, "gc", "-q")
	}
	return src
}

func importRefs(t *testing.T, src string, noLocal bool) (refs, progress string) {
	t.Helper()
	if noLocal {
		t.Setenv("CAIRN_NO_LOCAL_CLONE", "1")
	}
	dir := t.TempDir()
	e, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	e.SetProgress(&buf)
	if _, err := e.ImportFromRemote(src); err != nil {
		t.Fatalf("ImportFromRemote: %v", err)
	}
	_ = e.Close()
	out := gitIn(t, dir, nil, "--git-dir", filepath.Join(dir, "objects.git"), "for-each-ref")
	// Drop cairn's own bookkeeping refs: only what came from the remote matters.
	var keep []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.Contains(l, "refs/heads/") || strings.Contains(l, "refs/remotes/origin/") || strings.Contains(l, "refs/tags/") {
			keep = append(keep, l)
		}
	}
	gitIn(t, dir, nil, "--git-dir", filepath.Join(dir, "objects.git"), "fsck", "--connectivity-only")
	return strings.Join(keep, "\n"), buf.String()
}

// A clone of a packed local repository links its packs instead of fetching,
// and ends with exactly the refs (and a connected object graph) the protocol
// fetch produces.
func TestLocalCloneLinksPacksAndMatchesProtocolFetch(t *testing.T) {
	src := localFetchSource(t, true)
	fast, progress := importRefs(t, src, false)
	if !strings.Contains(progress, "local remote: linked") {
		t.Fatalf("local shortcut not taken:\n%s", progress)
	}
	slow, progress := importRefs(t, src, true)
	if strings.Contains(progress, "local remote: linked") {
		t.Fatal("CAIRN_NO_LOCAL_CLONE=1 did not disable the shortcut")
	}
	if fast != slow || !strings.Contains(fast, "refs/tags/v1") || !strings.Contains(fast, "refs/remotes/origin/side") {
		t.Fatalf("refs differ.\nlocal shortcut:\n%s\nprotocol fetch:\n%s", fast, slow)
	}
}

// A repository with loose objects is not wholly in its packs: the shortcut
// declines and the protocol fetch does the work.
func TestLocalCloneDeclinesLooseObjects(t *testing.T) {
	src := localFetchSource(t, false)
	_, progress := importRefs(t, src, false)
	if strings.Contains(progress, "local remote: linked") {
		t.Fatalf("shortcut taken for a repository with loose objects:\n%s", progress)
	}
}

// A symlinked pack or idx in the source's objects/pack is never linked or
// copied: the shortcut declines (security review #5).
func TestLocalCloneDeclinesSymlinkedPack(t *testing.T) {
	for _, ext := range []string{".pack", ".idx"} {
		t.Run(ext, func(t *testing.T) {
			src := localFetchSource(t, true)
			dir := filepath.Join(src, ".git", "objects", "pack")
			matches, _ := filepath.Glob(filepath.Join(dir, "pack-*"+ext))
			if len(matches) == 0 {
				t.Fatal("no pack in fixture")
			}
			moved := matches[0] + ".real"
			if err := os.Rename(matches[0], moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, matches[0]); err != nil {
				t.Skip("symlinks unavailable")
			}
			if _, ok := linkablePacks(filepath.Join(src, ".git")); ok {
				t.Fatalf("linkablePacks accepted a symlinked %s", ext)
			}
		})
	}
}
