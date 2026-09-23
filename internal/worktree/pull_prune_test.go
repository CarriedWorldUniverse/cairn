package worktree

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// A branch deleted on the remote — usually after its PR merged — used to
// live on locally forever, listed by tree and walked by every sync. pull now
// abandons such lines unless they are expressed on disk; abandon is an op,
// so undo restores one.
func deleteOnOrigin(t *testing.T, originDir, branch string) {
	t.Helper()
	g, err := git.PlainOpen(originDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Storer.RemoveReference(plumbing.NewBranchReferenceName(branch)); err != nil {
		t.Fatalf("delete %s on origin: %v", branch, err)
	}
}

func lineStatus(t *testing.T, r *Repo, name string) string {
	t.Helper()
	l, err := r.eng.LineByName(name)
	if err != nil {
		t.Fatalf("LineByName %s: %v", name, err)
	}
	return l.Status
}

func TestPullPrunesUnexpressedLinesWhoseBranchIsGone(t *testing.T) {
	skipOnWindows(t)
	originDir, def := makeOriginRepoWT(t)
	r, err := Clone(originDir, filepath.Join(t.TempDir(), "wc"), "tester", nil)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if lineStatus(t, r, "feature") != "open" {
		t.Fatal("precondition: feature imported open")
	}

	deleteOnOrigin(t, originDir, "feature")
	sum, err := r.Pull("origin")
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(sum.Pruned) != 1 || sum.Pruned[0] != "feature" {
		t.Fatalf("Pruned = %v, want [feature]", sum.Pruned)
	}
	if lineStatus(t, r, "feature") != "abandoned" {
		t.Fatalf("feature status = %s, want abandoned", lineStatus(t, r, "feature"))
	}
	if lineStatus(t, r, def) != "open" {
		t.Fatalf("%s must stay open", def)
	}
	// It was an op: undo brings the line back.
	if err := r.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if lineStatus(t, r, "feature") != "open" {
		t.Fatalf("after undo, feature status = %s, want open", lineStatus(t, r, "feature"))
	}
}

// An expressed line is the operator's — pull reports it and leaves it alone.
func TestPullKeepsExpressedLinesWhoseBranchIsGone(t *testing.T) {
	skipOnWindows(t)
	originDir, _ := makeOriginRepoWT(t)
	r, err := Clone(originDir, filepath.Join(t.TempDir(), "wc"), "tester", nil)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if err := r.Express("feature", ""); err != nil {
		t.Fatalf("Express: %v", err)
	}
	deleteOnOrigin(t, originDir, "feature")
	sum, err := r.Pull("origin")
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(sum.Pruned) != 0 || len(sum.KeptGone) != 1 || sum.KeptGone[0] != "feature" {
		t.Fatalf("Pruned=%v KeptGone=%v, want none pruned and feature kept", sum.Pruned, sum.KeptGone)
	}
	if lineStatus(t, r, "feature") != "open" {
		t.Fatal("expressed feature must stay open")
	}
}

// --keep-gone opts out entirely.
func TestPullKeepGoneLeavesLinesAlone(t *testing.T) {
	skipOnWindows(t)
	originDir, _ := makeOriginRepoWT(t)
	r, err := Clone(originDir, filepath.Join(t.TempDir(), "wc"), "tester", nil)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	r.SetKeepGone(true)
	deleteOnOrigin(t, originDir, "feature")
	sum, err := r.Pull("origin")
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(sum.Pruned) != 0 || lineStatus(t, r, "feature") != "open" {
		t.Fatalf("keep-gone: Pruned=%v status=%s", sum.Pruned, lineStatus(t, r, "feature"))
	}
}

// A line pushed by a cairn from before remote_seen existed has the flag
// unset but still holds a tracking ref (older releases never pruned). The
// pruning fetch must record the ref's presence BEFORE pruning it, or the line
// would read as never-pushed and survive the prune.
func TestPullPrunesLinesPushedBeforeRemoteSeenExisted(t *testing.T) {
	skipOnWindows(t)
	originDir, def := makeOriginRepoWT(t)
	r, err := Clone(originDir, filepath.Join(t.TempDir(), "wc"), "tester", nil)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if err := r.Express("mine", def); err != nil {
		t.Fatalf("Express: %v", err)
	}
	if err := os.WriteFile(filepath.Join(r.Root(), r.st.Expressed["mine"].Path, "m.txt"), []byte("m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("mine", "m"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := r.PushBranch("origin", "mine", false); err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	if err := r.Unexpress("mine", true); err != nil {
		t.Fatalf("Unexpress: %v", err)
	}
	// Simulate the pre-remote_seen state: flag off, tracking ref present.
	if _, err := r.eng.DB().Exec(`UPDATE line SET remote_seen=0 WHERE name='mine'`); err != nil {
		t.Fatal(err)
	}
	deleteOnOrigin(t, originDir, "mine")
	sum, err := r.Pull("origin")
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(sum.Pruned) != 1 || sum.Pruned[0] != "mine" {
		t.Fatalf("Pruned = %v, want [mine] — the tracking ref's presence was not recorded before pruning", sum.Pruned)
	}
}
