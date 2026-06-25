package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// makeBareWithAnnotatedTag seeds a bare git repo with one commit AND an annotated
// tag (a tag object, not a lightweight ref) pointing at it, then returns the bare
// path. Mirrors a real GitHub repo with release tags.
func makeBareWithAnnotatedTag(t *testing.T) string {
	t.Helper()
	bare := t.TempDir()
	if _, err := git.PlainInit(bare, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	work := t.TempDir()
	repo, err := git.PlainInit(work, false)
	if err != nil {
		t.Fatalf("PlainInit work: %v", err)
	}
	wt, _ := repo.Worktree()
	if err := os.WriteFile(filepath.Join(work, "readme.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("readme.txt"); err != nil {
		t.Fatal(err)
	}
	h, err := wt.Commit("init", &git.CommitOptions{Author: &object.Signature{Name: "o", Email: "o@example.com"}})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	// Annotated tag (CreateTagOptions with a message ⇒ a tag object).
	if _, err := repo.CreateTag("v1.0", h, &git.CreateTagOptions{
		Tagger:  &object.Signature{Name: "o", Email: "o@example.com"},
		Message: "release v1.0",
	}); err != nil {
		t.Fatalf("CreateTag: %v", err)
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{bare}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{
		"refs/heads/*:refs/heads/*", "refs/tags/*:refs/tags/*",
	}}); err != nil {
		t.Fatalf("push seed: %v", err)
	}
	return bare
}

// TestE2E_ReauthorAnnotatedTagClone reproduces the rig bug: `cairn reauthor
// --dry-run` on a clone that has an annotated tag crashed with
// "topoCommits: load <oid>: object not found". It must now complete.
func TestE2E_ReauthorAnnotatedTagClone(t *testing.T) {
	skipOnWindows(t)
	bare := makeBareWithAnnotatedTag(t)
	dir := t.TempDir()
	mustRun(t, "clone", bare, dir)

	out, err := captureRunResult(t, "reauthor", "--repo", dir,
		"--old-email", "*@nowhere.invalid", "--name", "X", "--email", "x@y.z", "--dry-run")
	if err != nil {
		t.Fatalf("reauthor --dry-run crashed on an annotated-tag clone: %v", err)
	}
	if !strings.Contains(out, "would rewrite") {
		t.Fatalf("unexpected reauthor output: %q", out)
	}
}
