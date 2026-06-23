package change

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
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

// makeOriginRepoFull extends makeOriginRepo: after the initial commit on the
// default branch it adds a "feature" branch with another commit and a tag "v1"
// on the default branch tip. It returns the file:// URL and the default branch
// short name.
func makeOriginRepoFull(t *testing.T) (string, string) {
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
	defaultHeadHash, err := wt.Commit("init", &git.CommitOptions{Author: &object.Signature{Name: "o", Email: "o@x"}})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Determine the default branch short name from HEAD after the first commit.
	head, err := r.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	def := head.Name().Short()

	// feature branch + a second commit.
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

	// Back to the default branch and tag its tip.
	if err := wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(def),
	}); err != nil {
		t.Fatalf("checkout default: %v", err)
	}
	if _, err := r.CreateTag("v1", defaultHeadHash, nil); err != nil {
		t.Fatalf("CreateTag: %v", err)
	}

	return "file://" + dir, def
}

func TestImportFromRemoteMapsLinesAndTags(t *testing.T) {
	url, def := makeOriginRepoFull(t) // returns url + the default branch name
	e, _ := Open(t.TempDir())
	t.Cleanup(func() { _ = e.Close() })
	got, err := e.ImportFromRemote(url)
	if err != nil {
		t.Fatalf("ImportFromRemote: %v", err)
	}
	if got != def {
		t.Fatalf("default = %q, want %q", got, def)
	}

	root, err := e.LineByName(def)
	if err != nil {
		t.Fatalf("root line %q: %v", def, err)
	}
	if root.ParentLine != "" {
		t.Fatalf("default must be root, parent=%q", root.ParentLine)
	}
	if root.TipCommit == "" {
		t.Fatal("root tip not set")
	}

	feat, err := e.LineByName("feature")
	if err != nil {
		t.Fatalf("feature line: %v", err)
	}
	if feat.ParentLine != root.ID {
		t.Fatalf("feature parent=%q want root %q", feat.ParentLine, root.ID)
	}
	if feat.TipCommit == "" {
		t.Fatal("feature tip not set")
	}

	tags, _ := e.ListTags()
	found := false
	for _, tg := range tags {
		if tg.Name == "v1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("tag v1 missing: %v", tags)
	}

	// idempotent re-import: no duplicate lines
	if _, err := e.ImportFromRemote(url); err != nil {
		t.Fatalf("re-import: %v", err)
	}
	tree, _ := e.GetLineTree()
	counts := map[string]int{}
	for _, n := range tree {
		counts[n.Line.Name]++
	}
	if counts[def] != 1 || counts["feature"] != 1 {
		t.Fatalf("dup lines after re-import: %v", counts)
	}
}

func TestReopenAfterImportNoSecondRoot(t *testing.T) {
	url, def := makeOriginRepoFull(t)
	dir := t.TempDir()
	e, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := e.ImportFromRemote(url); err != nil {
		t.Fatalf("import: %v", err)
	}
	_ = e.Close()
	e2, err := Open(dir) // re-open the SAME dir
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = e2.Close() })
	var roots int
	if err := e2.db.QueryRow(`SELECT COUNT(*) FROM line WHERE parent_line IS NULL`).Scan(&roots); err != nil {
		t.Fatalf("count roots: %v", err)
	}
	if roots != 1 {
		t.Fatalf("after reopen: %d root lines, want 1", roots)
	}
	if _, err := e2.LineByName(def); err != nil {
		t.Fatalf("default line %q gone after reopen: %v", def, err)
	}
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
	// A second fetch (already up to date) must be a no-op success.
	if err := e.fetchRemote(url); err != nil {
		t.Fatalf("second fetchRemote: %v", err)
	}
}
