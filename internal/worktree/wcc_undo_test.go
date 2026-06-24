package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ancestrySHAs returns the first-parent ancestry SHAs of commit (inclusive),
// walked via the engine. Used to assert an undone commit does NOT resurface in
// the lineage of work continued after the undo.
func ancestrySHAs(t *testing.T, r *Repo, commit string) []string {
	t.Helper()
	var out []string
	cur := commit
	for cur != "" {
		out = append(out, cur)
		parent, err := r.eng.FirstParent(cur)
		if err != nil {
			t.Fatalf("FirstParent(%s): %v", cur, err)
		}
		cur = parent
	}
	return out
}

// workingHead returns the open working change's head_commit for branch.
func workingHead(t *testing.T, r *Repo, branch string) string {
	t.Helper()
	entry := r.st.Expressed[branch]
	ch, err := r.eng.GetChange(entry.ChangeID)
	if err != nil {
		t.Fatalf("GetChange(%s): %v", entry.ChangeID, err)
	}
	return ch.HeadCommit
}

// TestUndoSnapshotThenContinue exercises Fix 3 (open-change head reconcile) for a
// post-seal SNAPSHOT that gets undone. The open change's head is non-empty (it
// amended in place), so after undo restores the line tip the stale head would
// still point AT the undone snapshot commit — leaving the undone state live for
// the next snapshot/no-op/status. Fix 3 must reconcile that head against the
// restored tip so the undone commit is NOT resurfaced as the working head, and
// continuing with {a:v3} parents cleanly on the restored sealed tip.
func TestUndoSnapshotThenContinue(t *testing.T) {
	skipOnWindows(t)

	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	branch, err := r.DefaultBranch()
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	aPath := filepath.Join(root, branch, "a.txt")

	// Seal a baseline {a:v1} so the post-seal snapshot has a real sealed tip to
	// restore back to (and the open change's head starts non-empty after sync).
	if err := os.WriteFile(aPath, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res1, err := r.Commit(branch, "first")
	if err != nil {
		t.Fatalf("Commit first: %v", err)
	}
	firstSealed := res1.HeadCommit

	// Post-seal snapshot {a:v2}: amends the fresh open change in place, so its head
	// becomes snapV2 (parent = firstSealed) and the line tip advances to snapV2.
	if err := os.WriteFile(aPath, []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.SyncWorking(); err != nil {
		t.Fatalf("SyncWorking v2: %v", err)
	}
	snapV2 := workingHead(t, r, branch)
	if snapV2 == "" || snapV2 == firstSealed {
		t.Fatalf("snapV2 head unexpected: %q (first=%q)", snapV2, firstSealed)
	}

	// Undo the v2 snapshot → line tip restored to firstSealed.
	if err := r.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	line, err := r.eng.LineByName(branch)
	if err != nil {
		t.Fatalf("LineByName: %v", err)
	}
	if line.TipCommit != firstSealed {
		t.Fatalf("tip after undo = %q, want firstSealed %q", line.TipCommit, firstSealed)
	}

	// LOAD-BEARING: the open change's head must NOT still be the undone snapV2.
	// Fix 3 reconciles it (here to "", since the restored tip is a different/sealed
	// commit), so the undone commit is not live.
	if h := workingHead(t, r, branch); h == snapV2 {
		t.Fatalf("undone snapV2 %s resurfaced as the open working head", snapV2)
	}

	// Disk reflects v1 (re-materialized to the restored sealed tip).
	if got, err := os.ReadFile(aPath); err != nil || string(got) != "v1\n" {
		t.Fatalf("a.txt after undo = %q (err=%v), want v1", got, err)
	}

	// Continue: write {a:v3} + snapshot. The new working commit must parent on the
	// restored firstSealed and must NOT thread the undone snapV2 into its ancestry.
	if err := os.WriteFile(aPath, []byte("v3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.SyncWorking(); err != nil {
		t.Fatalf("SyncWorking v3: %v", err)
	}
	v3Head := workingHead(t, r, branch)
	for _, sha := range ancestrySHAs(t, r, v3Head) {
		if sha == snapV2 {
			t.Fatalf("undone snapV2 %s resurfaced in v3 ancestry", snapV2)
		}
	}
	parent, err := r.eng.FirstParent(v3Head)
	if err != nil {
		t.Fatalf("FirstParent(v3Head): %v", err)
	}
	if parent != firstSealed {
		t.Fatalf("v3 parent = %q, want restored firstSealed %q", parent, firstSealed)
	}
	files, err := r.eng.Files(v3Head)
	if err != nil {
		t.Fatalf("Files(v3Head): %v", err)
	}
	if string(files["a.txt"]) != "v3\n" {
		t.Fatalf("working a.txt = %q, want v3", files["a.txt"])
	}
}

// TestUndoCommitThenContinue exercises Fix 3 for the seal case: after sealing
// "first", editing+sealing "second", then undoing the second commit, the line
// tip returns to the "first" sealed commit. Continuing with "third" must yield a
// sealed lineage of third -> first that does NOT contain the undone "second".
func TestUndoCommitThenContinue(t *testing.T) {
	skipOnWindows(t)

	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	branch, err := r.DefaultBranch()
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	aPath := filepath.Join(root, branch, "a.txt")

	// Commit "first" {a:v1}.
	if err := os.WriteFile(aPath, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res1, err := r.Commit(branch, "first")
	if err != nil {
		t.Fatalf("Commit first: %v", err)
	}
	firstSealed := res1.HeadCommit
	if firstSealed == "" {
		t.Fatal("first commit has empty head")
	}

	// Edit {a:v2} + sync, then commit "second".
	if err := os.WriteFile(aPath, []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.SyncWorking(); err != nil {
		t.Fatalf("SyncWorking v2: %v", err)
	}
	res2, err := r.Commit(branch, "second")
	if err != nil {
		t.Fatalf("Commit second: %v", err)
	}
	secondSealed := res2.HeadCommit
	if secondSealed == "" || secondSealed == firstSealed {
		t.Fatalf("second commit head unexpected: %q (first=%q)", secondSealed, firstSealed)
	}

	// Undo the "second" commit: the line tip returns to the "first" sealed commit.
	if err := r.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	line, err := r.eng.LineByName(branch)
	if err != nil {
		t.Fatalf("LineByName after undo: %v", err)
	}
	if line.TipCommit != firstSealed {
		t.Fatalf("tip after undo = %q, want first sealed %q", line.TipCommit, firstSealed)
	}
	// Disk reflects "first" (v1).
	got, err := os.ReadFile(aPath)
	if err != nil {
		t.Fatalf("ReadFile after undo: %v", err)
	}
	if string(got) != "v1\n" {
		t.Fatalf("a.txt after undo = %q, want v1", got)
	}

	// Continue: edit {a:v3} + sync + commit "third".
	if err := os.WriteFile(aPath, []byte("v3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.SyncWorking(); err != nil {
		t.Fatalf("SyncWorking v3: %v", err)
	}
	if _, err := r.Commit(branch, "third"); err != nil {
		t.Fatalf("Commit third: %v", err)
	}

	// The sealed lineage must be third -> first, and must NOT contain "second".
	infos, err := r.Log(branch, 0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	var subjects []string
	for _, ci := range infos {
		if ci.Working {
			continue // ignore the fresh (working) commit opened by the seal
		}
		subjects = append(subjects, ci.Subject)
		if ci.Subject == "second" {
			t.Fatalf("undone 'second' commit appears in sealed lineage: %v", infos)
		}
		if ci.SHA == secondSealed {
			t.Fatalf("undone 'second' sha %s appears in sealed lineage", secondSealed)
		}
	}
	if len(subjects) < 2 || subjects[0] != "third" || subjects[len(subjects)-1] != "first" {
		t.Fatalf("sealed lineage subjects = %v, want third...first", subjects)
	}
}

// TestSealAbsorbsThisChangeSnapshotMultiBranch exercises Fix 1: in a multi-branch
// repo, SyncWorking snapshots every expressed branch, so the GLOBAL-last snapshot
// op may belong to another branch. Committing branch "alpha" must absorb ALPHA's
// own snapshot op (keyed on detail=change-id), so undoing that commit restores
// alpha's line to its prior SEALED tip — not to some intermediate working state.
func TestSealAbsorbsThisChangeSnapshotMultiBranch(t *testing.T) {
	skipOnWindows(t)

	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	// Seed main, then express two sibling branches off it.
	if err := os.WriteFile(filepath.Join(root, "main", "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("main", "seed"); err != nil {
		t.Fatalf("commit main: %v", err)
	}
	if err := r.Express("alpha", "main"); err != nil {
		t.Fatalf("Express alpha: %v", err)
	}
	if err := r.Express("beta", "main"); err != nil {
		t.Fatalf("Express beta: %v", err)
	}

	// Seal a baseline on alpha so it has a prior sealed tip to return to.
	if err := os.WriteFile(filepath.Join(root, "alpha", "a.txt"), []byte("a1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resA1, err := r.Commit("alpha", "alpha first")
	if err != nil {
		t.Fatalf("commit alpha first: %v", err)
	}
	alphaSealed := resA1.HeadCommit

	// Edit BOTH branches, then SyncWorking everything. SyncWorking iterates the
	// expressed set, so the global-last snapshot op is whichever branch is synced
	// last — possibly beta, NOT alpha. Fix 1 must still find alpha's own snapshot.
	if err := os.WriteFile(filepath.Join(root, "alpha", "a.txt"), []byte("a2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "beta", "b.txt"), []byte("b1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.SyncWorking(); err != nil {
		t.Fatalf("SyncWorking both: %v", err)
	}

	// Commit alpha (seals a2). The seal must absorb alpha's snapshot op so the
	// commit is a single undo step landing on alpha's prior sealed tip.
	if _, err := r.Commit("alpha", "alpha second"); err != nil {
		t.Fatalf("commit alpha second: %v", err)
	}

	// Undo the alpha commit → alpha's line tip must restore to its prior sealed tip
	// (alphaSealed), proving the seal absorbed ALPHA's snapshot (not beta's).
	if err := r.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	alpha, err := r.eng.LineByName("alpha")
	if err != nil {
		t.Fatalf("LineByName alpha: %v", err)
	}
	if alpha.TipCommit != alphaSealed {
		t.Fatalf("alpha tip after undo = %q, want prior sealed %q", alpha.TipCommit, alphaSealed)
	}
}

// TestAbandonRefusesUnsealedWork verifies the new isDirty (un-sealed working
// delta) makes a non-force Abandon refuse a child with un-sealed edits, force
// discards them, and a just-sealed (clean) child abandons without force.
func TestAbandonRefusesUnsealedWork(t *testing.T) {
	skipOnWindows(t)

	root := t.TempDir()
	r, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	// Seed main and express a child.
	if err := os.WriteFile(filepath.Join(root, "main", "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("main", "seed"); err != nil {
		t.Fatalf("commit main: %v", err)
	}
	if err := r.Express("child", "main"); err != nil {
		t.Fatalf("Express child: %v", err)
	}

	// Un-sealed edits on the child (write + SyncWorking, no commit).
	if err := os.WriteFile(filepath.Join(root, "child", "c.txt"), []byte("c\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.SyncWorking(); err != nil {
		t.Fatalf("SyncWorking: %v", err)
	}

	// force=false must refuse un-sealed work.
	err = r.Abandon("child", false)
	if err == nil {
		t.Fatal("Abandon(child, false) must refuse un-sealed work")
	}
	if !strings.Contains(err.Error(), "un-sealed") {
		t.Fatalf("error must mention 'un-sealed', got: %v", err)
	}
	if _, ok := r.Ls()["child"]; !ok {
		t.Fatal("child must still be expressed after refused Abandon")
	}

	// force=true discards.
	if err := r.Abandon("child", true); err != nil {
		t.Fatalf("Abandon(child, true) must succeed: %v", err)
	}
	if _, ok := r.Ls()["child"]; ok {
		t.Fatal("child must no longer be expressed after force Abandon")
	}

	// A clean (just-sealed, no new edits) child abandons without force.
	if err := r.Express("child2", "main"); err != nil {
		t.Fatalf("Express child2: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "child2", "d.txt"), []byte("d\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("child2", "child2 clean"); err != nil {
		t.Fatalf("commit child2: %v", err)
	}
	// No new edits since the seal → not dirty.
	if err := r.Abandon("child2", false); err != nil {
		t.Fatalf("Abandon(child2, false) on clean branch must succeed: %v", err)
	}
	if _, ok := r.Ls()["child2"]; ok {
		t.Fatal("child2 must no longer be expressed after clean Abandon")
	}
}
