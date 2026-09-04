package worktree

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// The folder IS the working change — no staging area, nothing to stash — so
// `cairn pull` must never rewrite it from the line tip without snapshotting it
// first. Before #182 it did exactly that: un-sealed edits were reverted and
// untracked new files removed by materialize's deletion sweep, silently.

// TestPullKeepsUnsealedEditsAndUntrackedFiles is the reproduction from #182:
// express, edit a tracked file, add a new one, pull. Both must survive.
func TestPullKeepsUnsealedEditsAndUntrackedFiles(t *testing.T) {
	r, def := seedBranch(t)
	if err := r.Express("feat", def); err != nil {
		t.Fatalf("Express: %v", err)
	}
	dir := filepath.Join(r.Root(), r.st.Expressed["feat"].Path)
	edited := filepath.Join(dir, "readme.txt")
	added := filepath.Join(dir, "new.txt")
	if err := os.WriteFile(edited, []byte("EDITED BUT NOT COMMITTED\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(added, []byte("new file\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Pull("origin"); err != nil {
		t.Fatalf("Pull: %v", err)
	}

	got, err := os.ReadFile(edited)
	if err != nil || string(got) != "EDITED BUT NOT COMMITTED\n" {
		t.Fatalf("pull reverted an un-sealed edit: readme.txt = %q (err %v)", got, err)
	}
	if _, err := os.Stat(added); err != nil {
		t.Fatalf("pull deleted an untracked new file: %v", err)
	}
}

// The harder case: the parent moved on the remote while the edits were
// un-sealed. Pull must bring the new parent content in AND keep the edits —
// reconciled, not discarded.
func TestPullKeepsUnsealedEditsWhenOriginMoved(t *testing.T) {
	skipOnWindows(t)
	originDir, def := makeOriginRepoWT(t)
	root := filepath.Join(t.TempDir(), "wc")
	r, err := Clone(originDir, root, "tester", nil)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if err := r.Express("feat", def); err != nil {
		t.Fatalf("Express: %v", err)
	}
	dir := filepath.Join(r.Root(), r.st.Expressed["feat"].Path)
	if err := os.WriteFile(filepath.Join(dir, "mine.txt"), []byte("un-sealed work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Advance the origin's default branch independently.
	commitOnOrigin(t, originDir, "upstream.txt", "from upstream\n")

	if _, err := r.Pull("origin"); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "mine.txt")); err != nil || string(got) != "un-sealed work\n" {
		t.Fatalf("pull with a moved origin lost the un-sealed file: %q (err %v)", got, err)
	}
	// And main's folder received the upstream change.
	if _, err := os.Stat(filepath.Join(r.Root(), r.st.Expressed[def].Path, "upstream.txt")); err != nil {
		t.Fatalf("upstream change did not land in %s's folder: %v", def, err)
	}
}

// commitOnOrigin adds a file to the origin repo's default branch with go-git,
// simulating someone else pushing while our edits are un-sealed.
func commitOnOrigin(t *testing.T, originDir, name, content string) {
	t.Helper()
	g, err := git.PlainOpen(originDir)
	if err != nil {
		t.Fatalf("PlainOpen origin: %v", err)
	}
	wt, err := g.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(originDir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add(name); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("upstream: "+name, &git.CommitOptions{Author: &object.Signature{Name: "o", Email: "o@x"}}); err != nil {
		t.Fatalf("origin commit: %v", err)
	}
}
