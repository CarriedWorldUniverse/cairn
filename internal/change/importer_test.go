package change

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// makeOriginRepo builds a real (non-bare) git repo with one commit on its default
// branch and returns a file:// URL.
func makeOriginRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	r, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	wt, err := r.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("readme.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("init", &git.CommitOptions{Author: &object.Signature{Name: "o", Email: "o@x"}}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return "file://" + dir
}

func TestFetchRemoteLandsHeadsAndDefault(t *testing.T) {
	url := makeOriginRepo(t)
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if err := e.fetchRemote(url); err != nil {
		t.Fatalf("fetchRemote: %v", err)
	}
	def, err := e.detectDefault()
	if err != nil {
		t.Fatalf("detectDefault: %v", err)
	}
	if def == "" {
		t.Fatal("empty default branch")
	}
	heads, err := e.listHeads()
	if err != nil {
		t.Fatalf("listHeads: %v", err)
	}
	sha, ok := heads[def]
	if !ok {
		t.Fatalf("default head %q not in heads %v", def, heads)
	}
	if _, err := e.Files(sha); err != nil {
		t.Fatalf("Files(defaultTip): %v", err)
	}
}
