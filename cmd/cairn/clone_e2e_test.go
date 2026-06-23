package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// skipOnWindows skips tests that clone from a local go-git fixture repo: the
// go-git local-transport + modernc sqlite handle release flakes under Windows'
// mandatory file locking. Production clone targets real remotes on Linux/dMon,
// so this is an environment artifact only.
func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("go-git local-transport fixtures + sqlite handle release flake under Windows file locking")
	}
}

// makeOriginRepoCLI builds a real (non-bare) git repo with one commit on its
// default branch and returns a file:// URL plus the default branch short name.
func makeOriginRepoCLI(t *testing.T) (string, string) {
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
	head, err := r.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	return dir, head.Name().Short()
}

func TestE2E_CloneViaCLI(t *testing.T) {
	skipOnWindows(t)
	url, def := makeOriginRepoCLI(t)
	dir := filepath.Join(t.TempDir(), "myrepo")
	mustRun(t, "clone", url, dir)
	got, err := os.ReadFile(filepath.Join(dir, def, "readme.txt"))
	if err != nil {
		t.Fatalf("expressed default %q not found: %v", def, err)
	}
	if string(got) != "hello\n" {
		t.Fatalf("readme = %q", got)
	}
}
