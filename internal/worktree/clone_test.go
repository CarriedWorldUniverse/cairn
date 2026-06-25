package worktree

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// skipOnWindows skips tests that clone/import from a local go-git fixture repo:
// the go-git local-transport + modernc sqlite handle release flakes under
// Windows' mandatory file locking. Production clone targets real remotes on
// Linux/dMon, so this is an environment artifact only.
func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("go-git local-transport fixtures + sqlite handle release flake under Windows file locking")
	}
}

// makeOriginRepoWT builds a real (non-bare) git repo with one commit on its
// default branch and a "feature" branch with another commit. It returns the
// file:// URL and the default branch short name (captured before branching).
func makeOriginRepoWT(t *testing.T) (string, string) {
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
	def := head.Name().Short()

	if err := wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	}); err != nil {
		t.Fatalf("checkout feature: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feat\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("feature.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("feat", &git.CommitOptions{Author: &object.Signature{Name: "o", Email: "o@x"}}); err != nil {
		t.Fatalf("commit feature: %v", err)
	}

	if err := wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(def),
	}); err != nil {
		t.Fatalf("checkout default: %v", err)
	}

	return dir, def
}

func TestCloneImportsAndExpresses(t *testing.T) {
	skipOnWindows(t)
	url, def := makeOriginRepoWT(t) // returns local path url + default branch name
	dir := filepath.Join(t.TempDir(), "myrepo")
	r, err := Clone(url, dir, "tester", nil)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	got, err := os.ReadFile(filepath.Join(dir, def, "readme.txt"))
	if err != nil {
		t.Fatalf("expressed default %q not found: %v", def, err)
	}
	if string(got) != "hello\n" {
		t.Fatalf("readme = %q", got)
	}
	// the imported feature line is usable: express + commit + fold
	if err := r.Express("feature", ""); err != nil {
		t.Fatalf("Express feature: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "feature", "f.txt"), []byte("F\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("feature", ""); err != nil {
		t.Fatalf("commit feature: %v", err)
	}
	if err := r.Fold("feature", false); err != nil {
		t.Fatalf("fold feature: %v", err)
	}
}

// TestReopenAfterCloneNonMainDefault guards the cross-session reopen path: after
// cloning a remote whose default branch is not "main", the root line is named
// after that default (e.g. "master") and a fresh Open of the cloned dir must
// express the structural root by name, not the literal "main".
func TestReopenAfterCloneNonMainDefault(t *testing.T) {
	skipOnWindows(t)
	url, def := makeOriginRepoWT(t) // go-git PlainInit default is typically "master"
	dir := filepath.Join(t.TempDir(), "myrepo")
	r, err := Clone(url, dir, "t", nil)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	_ = r.Close()
	// Re-open the cloned dir in a "new session"
	r2, err := Open(dir, "t")
	if err != nil {
		t.Fatalf("re-Open after clone failed (root=%q): %v", def, err)
	}
	t.Cleanup(func() { _ = r2.Close() })
	if _, ok := r2.Ls()[def]; !ok {
		t.Fatalf("default branch %q not expressed after reopen: %v", def, r2.Ls())
	}
}
