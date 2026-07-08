package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// advanceRemoteBranchWT clones bare at branch and pushes a new commit directly
// — advancing the remote independently of any cairn Repo, so the next
// cairn-side push against that line is a genuine non-fast-forward.
func advanceRemoteBranchWT(t *testing.T, bare, branch, path, content string) {
	t.Helper()
	work := t.TempDir()
	repo, err := git.PlainClone(work, false, &git.CloneOptions{
		URL:           bare,
		ReferenceName: plumbing.NewBranchReferenceName(branch),
	})
	if err != nil {
		t.Fatalf("clone bare at %s: %v", branch, err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	full := filepath.Join(work, path)
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if _, err := wt.Add(path); err != nil {
		t.Fatalf("add %s: %v", path, err)
	}
	if _, err := wt.Commit("advance "+branch, &git.CommitOptions{
		Author: &object.Signature{Name: "o", Email: "o@x"},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := repo.Push(&git.PushOptions{RemoteName: "origin"}); err != nil {
		t.Fatalf("push advance %s: %v", branch, err)
	}
}

// remoteBranchSHA returns the current sha of refs/heads/<branch> in the bare repo.
func remoteBranchSHA(t *testing.T, bare, branch string) string {
	t.Helper()
	bareRepo, err := git.PlainOpen(bare)
	if err != nil {
		t.Fatalf("PlainOpen bare: %v", err)
	}
	ref, err := bareRepo.Reference(plumbing.ReferenceName("refs/heads/"+branch), false)
	if err != nil {
		t.Fatalf("Reference %s: %v", branch, err)
	}
	return ref.Hash().String()
}

// seedTwoLinesPushedWT builds a bare remote and a cairn clone, expresses,
// commits, and pushes TWO independent lines (b1, b2) so both exist on the
// remote — the starting point for the divergence tests below.
func seedTwoLinesPushedWT(t *testing.T) (*Repo, string) {
	t.Helper()
	bare, def := seedBareRemoteWT(t)
	root := t.TempDir()
	r, err := Clone(bare, root, "t", nil)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	for _, b := range []string{"b1", "b2"} {
		if err := r.Express(b, def); err != nil {
			t.Fatalf("express %s: %v", b, err)
		}
		if err := os.WriteFile(filepath.Join(root, b, b+".txt"), []byte(b+"\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", b, err)
		}
		if _, err := r.Commit(b, "add "+b); err != nil {
			t.Fatalf("commit %s: %v", b, err)
		}
		if err := r.PushBranch("origin", b, false); err != nil {
			t.Fatalf("seed push %s: %v", b, err)
		}
	}
	return r, bare
}

// TestPushBranchDivergedGuidedError is the regression test for issue #91: a
// single-line push against a diverged remote, WITHOUT --reconcile, must fail
// with a guided error naming the branch and the remedies (--reconcile /
// `cairn pull`) — not just the bare engine "diverged" message.
func TestPushBranchDivergedGuidedError(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("uses POSIX flock semantics; covered on unix")
	}
	r, bare := seedTwoLinesPushedWT(t)
	defer r.Close()
	root := r.Root()

	// remote b1 advances independently.
	advanceRemoteBranchWT(t, bare, "b1", "remote.txt", "R\n")
	// local b1 also advances, on a different file → clean divergence.
	if err := os.WriteFile(filepath.Join(root, "b1", "local.txt"), []byte("L\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("b1", "local edit"); err != nil {
		t.Fatalf("commit: %v", err)
	}

	err := r.PushBranch("origin", "b1", false)
	if err == nil {
		t.Fatalf("PushBranch against a diverged remote succeeded; want a guided error")
	}
	msg := err.Error()
	for _, want := range []string{`"b1"`, "--reconcile", "cairn pull"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q missing %q", msg, want)
		}
	}
}

// TestPushBranchReconcileCleanMergeScopedToOneLine is the regression test for
// issue #91's opt-in reconcile: PushBranchReconcile against a cleanly-diverged
// remote pulls + retries and lands the merged tip, while a second, also-
// diverged line's remote ref is left byte-identical — proving the reconcile is
// scoped to the single named branch, not all lines.
func TestPushBranchReconcileCleanMergeScopedToOneLine(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("uses POSIX flock semantics; covered on unix")
	}
	r, bare := seedTwoLinesPushedWT(t)
	defer r.Close()
	root := r.Root()

	b2Before := remoteBranchSHA(t, bare, "b2")

	// remote b1 advances independently.
	advanceRemoteBranchWT(t, bare, "b1", "remote.txt", "R\n")
	// local b1 advances on a DIFFERENT file → clean divergence.
	if err := os.WriteFile(filepath.Join(root, "b1", "local.txt"), []byte("L\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("b1", "local edit"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// b2 ALSO diverges (remote-side only) so its remote ref moving would be a
	// real leak, not a no-op by construction.
	advanceRemoteBranchWT(t, bare, "b2", "remote2.txt", "R2\n")
	b2AfterDiverge := remoteBranchSHA(t, bare, "b2")

	// b2's LOCAL state (catalogue tip + expressed folder) before the b1-scoped
	// reconcile: this is the property that actually distinguishes a scoped
	// pullBranch from a mutation to the all-lines r.Pull(remote) — asserting
	// only on b2's REMOTE ref (below) passes under BOTH implementations,
	// because neither one pushes b2 back out. Local mutation is where an
	// all-lines-pull regression actually shows up.
	b2LineBefore, err := r.eng.LineByName("b2")
	if err != nil {
		t.Fatalf("LineByName b2 (before): %v", err)
	}
	b2ExpressedPath := filepath.Join(root, r.st.Expressed["b2"].Path)
	if _, err := os.Stat(filepath.Join(b2ExpressedPath, "remote2.txt")); err == nil {
		t.Fatalf("test bug: b2's expressed folder already has remote2.txt before reconcile")
	}

	if err := r.PushBranchReconcile("origin", "b1"); err != nil {
		t.Fatalf("PushBranchReconcile: %v", err)
	}

	// b2's LOCAL catalogue tip must be byte-identical: a b1-scoped reconcile
	// must not pull/merge b2 at all.
	b2LineAfter, err := r.eng.LineByName("b2")
	if err != nil {
		t.Fatalf("LineByName b2 (after): %v", err)
	}
	if b2LineAfter.TipCommit != b2LineBefore.TipCommit {
		t.Fatalf("b2 local tip changed: before=%s after=%s (PushBranchReconcile leaked scope to b2's local state)",
			b2LineBefore.TipCommit, b2LineAfter.TipCommit)
	}
	// b2's expressed folder must NOT have been re-materialized with the
	// remote's new file: a b1-scoped reconcile only re-materializes b1.
	if _, err := os.Stat(filepath.Join(b2ExpressedPath, "remote2.txt")); err == nil {
		t.Fatalf("b2's expressed folder was re-materialized with remote2.txt by a b1-scoped reconcile")
	}

	bareRepo, err := git.PlainOpen(bare)
	if err != nil {
		t.Fatalf("PlainOpen bare: %v", err)
	}
	ref, err := bareRepo.Reference(plumbing.ReferenceName("refs/heads/b1"), false)
	if err != nil {
		t.Fatalf("Reference b1: %v", err)
	}
	commit, err := bareRepo.CommitObject(ref.Hash())
	if err != nil {
		t.Fatalf("CommitObject: %v", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	for _, f := range []string{"remote.txt", "local.txt", "b1.txt"} {
		if _, err := tree.File(f); err != nil {
			t.Fatalf("merged remote b1 tip missing %s: %v", f, err)
		}
	}

	// b2's remote ref is byte-identical before/after the b1 reconcile: single-
	// line scope proven (b2 is unaffected even though it, too, diverged).
	if got := remoteBranchSHA(t, bare, "b2"); got != b2AfterDiverge {
		t.Fatalf("b2 remote ref changed: before=%s after=%s (PushBranchReconcile leaked scope)", b2AfterDiverge, got)
	}
	if b2AfterDiverge == b2Before {
		t.Fatalf("test bug: b2 never actually diverged")
	}
}

// TestPushBranchReconcileConflictReturnsResolveError is the regression test for
// issue #91's conflict path: PushBranchReconcile against a conflicting
// divergence returns an error naming the branch and telling the operator to
// resolve — never retrying the push — and the remote branch ref is untouched.
func TestPushBranchReconcileConflictReturnsResolveError(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("uses POSIX flock semantics; covered on unix")
	}
	r, bare := seedTwoLinesPushedWT(t)
	defer r.Close()
	root := r.Root()

	// remote and local both edit b1.txt's same region, differently.
	advanceRemoteBranchWT(t, bare, "b1", "b1.txt", "remote edit\n")
	if err := os.WriteFile(filepath.Join(root, "b1", "b1.txt"), []byte("local edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("b1", "local edit"); err != nil {
		t.Fatalf("commit: %v", err)
	}

	remoteBefore := remoteBranchSHA(t, bare, "b1")

	err := r.PushBranchReconcile("origin", "b1")
	if err == nil {
		t.Fatalf("PushBranchReconcile on a conflicting divergence succeeded; want a resolve error")
	}
	if !strings.Contains(err.Error(), "resolve") {
		t.Fatalf("error %q does not mention 'resolve'", err.Error())
	}
	if !strings.Contains(err.Error(), "b1") {
		t.Fatalf("error %q does not name the branch", err.Error())
	}

	if got := remoteBranchSHA(t, bare, "b1"); got != remoteBefore {
		t.Fatalf("remote b1 ref changed despite an unresolved conflict: before=%s after=%s", remoteBefore, got)
	}
}
