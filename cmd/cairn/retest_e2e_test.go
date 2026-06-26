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

// TestE2E_PrivateWarnsWhenAlreadyPushed verifies the footgun guard: marking a
// path private that is already on a remote warns that the pushed copy is NOT
// removed (rotate the secret); a not-yet-pushed path warns nothing.
func TestE2E_PrivateWarnsWhenAlreadyPushed(t *testing.T) {
	skipOnWindows(t)
	bare := makeSeededBareRepo(t) // readme.txt is committed + pushed to the bare
	dir := t.TempDir()
	mustRun(t, "clone", bare, dir)

	// readme.txt is already on origin → warn.
	_, stderr, err := runOutErr(t, "private", "--repo", dir, "readme.txt")
	if err != nil {
		t.Fatalf("private (pushed path): %v", err)
	}
	if !strings.Contains(stderr, "WARNING") || !strings.Contains(stderr, "already present") {
		t.Fatalf("expected already-pushed warning, got stderr: %q", stderr)
	}
	if !strings.Contains(stderr, "Rotate the secret") {
		t.Fatalf("warning should tell the user to rotate; stderr: %q", stderr)
	}

	// A fresh local-only file is NOT on origin → no warning.
	def := soleExpressedDir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, def, "local-only.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr2, err := runOutErr(t, "private", "--repo", dir, "local-only.txt")
	if err != nil {
		t.Fatalf("private (local path): %v", err)
	}
	if strings.Contains(stderr2, "WARNING") {
		t.Fatalf("unexpected warning for a not-pushed file: %q", stderr2)
	}
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
