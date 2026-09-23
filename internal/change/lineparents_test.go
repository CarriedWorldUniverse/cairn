package change

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// git has no notion of a branch's parent; cairn's line tree does. Before this
// every imported branch hung flat off the trunk, so a feature forked from
// develop showed lineage main → feature and counted develop's commits as its
// own. These tests build small topologies with go-git and check where each
// branch is placed and at which commit.

type topo struct {
	dir  string
	repo *git.Repository
	wt   *git.Worktree
	sha  map[string]string // label -> commit sha
	// frozen stamps every commit with the same instant.
	frozen bool
}

func newTopo(t *testing.T) *topo {
	t.Helper()
	dir := t.TempDir()
	r, err := git.PlainInitWithOptions(dir, &git.PlainInitOptions{InitOptions: git.InitOptions{DefaultBranch: plumbing.Main}})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	wt, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	return &topo{dir: dir, repo: r, wt: wt, sha: map[string]string{}}
}

var topoClock = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// commit writes one file and commits it on the current branch under label.
func (tp *topo) commit(t *testing.T, label string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(tp.dir, label+".txt"), []byte(label+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := tp.wt.Add(label + ".txt"); err != nil {
		t.Fatal(err)
	}
	if !tp.frozen {
		topoClock = topoClock.Add(time.Minute)
	}
	h, err := tp.wt.Commit(label, &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@t", When: topoClock}})
	if err != nil {
		t.Fatalf("commit %s: %v", label, err)
	}
	tp.sha[label] = h.String()
}

// branch creates name at the current HEAD and checks it out.
func (tp *topo) branch(t *testing.T, name string) {
	t.Helper()
	if err := tp.wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(name), Create: true}); err != nil {
		t.Fatalf("branch %s: %v", name, err)
	}
}

// orphan creates branch name at a brand-new root commit with no parents —
// a history unrelated to everything else — built through the object store
// because go-git's Checkout cannot make orphan branches.
func (tp *topo) orphan(t *testing.T, name, label string) {
	t.Helper()
	st := tp.repo.Storer
	blob := st.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	w, err := blob.Writer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(label + "\n")); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	blobHash, err := st.SetEncodedObject(blob)
	if err != nil {
		t.Fatal(err)
	}
	tree := &object.Tree{Entries: []object.TreeEntry{{Name: label + ".txt", Mode: 0o100644, Hash: blobHash}}}
	treeObj := st.NewEncodedObject()
	if err := tree.Encode(treeObj); err != nil {
		t.Fatal(err)
	}
	treeHash, err := st.SetEncodedObject(treeObj)
	if err != nil {
		t.Fatal(err)
	}
	if !tp.frozen {
		topoClock = topoClock.Add(time.Minute)
	}
	sig := object.Signature{Name: "t", Email: "t@t", When: topoClock}
	c := &object.Commit{Author: sig, Committer: sig, Message: label, TreeHash: treeHash}
	commitObj := st.NewEncodedObject()
	if err := c.Encode(commitObj); err != nil {
		t.Fatal(err)
	}
	commitHash, err := st.SetEncodedObject(commitObj)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName(name), commitHash)); err != nil {
		t.Fatal(err)
	}
	tp.sha[label] = commitHash.String()
}

// mergeFrom records a merge commit on the current branch with `other`'s tip
// as the second parent (tree = current tree; the content is irrelevant here).
func (tp *topo) mergeFrom(t *testing.T, other, label string) {
	t.Helper()
	head, err := tp.repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	ref, err := tp.repo.Reference(plumbing.NewBranchReferenceName(other), true)
	if err != nil {
		t.Fatal(err)
	}
	cur, err := tp.repo.CommitObject(head.Hash())
	if err != nil {
		t.Fatal(err)
	}
	if !tp.frozen {
		topoClock = topoClock.Add(time.Minute)
	}
	sig := object.Signature{Name: "t", Email: "t@t", When: topoClock}
	c := &object.Commit{Author: sig, Committer: sig, Message: label, TreeHash: cur.TreeHash, ParentHashes: []plumbing.Hash{head.Hash(), ref.Hash()}}
	obj := tp.repo.Storer.NewEncodedObject()
	if err := c.Encode(obj); err != nil {
		t.Fatal(err)
	}
	h, err := tp.repo.Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatal(err)
	}
	if err := tp.repo.Storer.SetReference(plumbing.NewHashReference(head.Name(), h)); err != nil {
		t.Fatal(err)
	}
	tp.sha[label] = h.String()
}

func (tp *topo) checkout(t *testing.T, name string) {
	t.Helper()
	if err := tp.wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(name)}); err != nil {
		t.Fatalf("checkout %s: %v", name, err)
	}
}

// importInto clones tp into a fresh engine and returns it plus name -> Line.
func importInto(t *testing.T, tp *topo) (*Engine, map[string]Line) {
	t.Helper()
	tp.checkout(t, "main")
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if _, err := e.ImportFromRemote(tp.dir); err != nil {
		t.Fatalf("ImportFromRemote: %v", err)
	}
	lines := map[string]Line{}
	byID := map[string]Line{}
	for _, name := range []string{"main", "develop", "feature", "hotfix", "twin", "orphan", "late"} {
		if l, err := e.LineByName(name); err == nil {
			lines[name] = l
			byID[l.ID] = l
		}
	}
	// Resolve parent ids to names for readable assertions.
	for name, l := range lines {
		if p, ok := byID[l.ParentLine]; ok {
			l.ParentLine = p.Name
		}
		lines[name] = l
	}
	return e, lines
}

// The canonical case from the report: feature forks from develop, hotfix from
// main. Before: every branch had parent main and feature was "ahead 3".
func TestImportPlacesBranchesUnderTheirNearestAncestorBranch(t *testing.T) {
	tp := newTopo(t)
	tp.commit(t, "A")
	tp.commit(t, "B")
	tp.branch(t, "develop")
	tp.commit(t, "C")
	tp.commit(t, "D")
	tp.branch(t, "feature")
	tp.commit(t, "E")
	tp.checkout(t, "main")
	tp.branch(t, "hotfix")
	tp.commit(t, "F")

	_, lines := importInto(t, tp)
	want := map[string][2]string{ // name -> parent, base
		"develop": {"main", tp.sha["B"]},
		"feature": {"develop", tp.sha["D"]},
		"hotfix":  {"main", tp.sha["B"]},
	}
	for name, w := range want {
		l, ok := lines[name]
		if !ok {
			t.Fatalf("%s: line missing after import", name)
		}
		if l.ParentLine != w[0] {
			t.Errorf("%s: parent = %s, want %s", name, l.ParentLine, w[0])
		}
		if l.BaseCommit != w[1] {
			t.Errorf("%s: base = %.8s, want %.8s", name, l.BaseCommit, w[1])
		}
	}
}

// A branch that DESCENDS from another must never be chosen as its parent —
// feature contains develop entirely, yet develop's parent is main.
func TestImportNeverPicksADescendantAsParent(t *testing.T) {
	tp := newTopo(t)
	tp.commit(t, "A")
	tp.branch(t, "develop")
	tp.commit(t, "C")
	tp.branch(t, "feature")
	tp.commit(t, "E")
	_, lines := importInto(t, tp)
	if lines["develop"].ParentLine != "main" {
		t.Fatalf("develop's parent = %s; its own child feature was picked", lines["develop"].ParentLine)
	}
	if lines["feature"].ParentLine != "develop" {
		t.Fatalf("feature's parent = %s, want develop", lines["feature"].ParentLine)
	}
}

// Two branches at the SAME commit cannot point at each other. The tie is
// broken by name so the pair forms a chain, not a cycle.
func TestImportTwinBranchesDoNotFormACycle(t *testing.T) {
	tp := newTopo(t)
	tp.commit(t, "A")
	tp.branch(t, "develop")
	tp.commit(t, "C")
	tp.branch(t, "twin") // identical tip to develop
	_, lines := importInto(t, tp)
	d, w := lines["develop"].ParentLine, lines["twin"].ParentLine
	if d == "twin" && w == "develop" {
		t.Fatal("develop and twin point at each other")
	}
	// "develop" sorts before "twin", so develop is the parent.
	if w != "develop" || d != "main" {
		t.Fatalf("twin -> %s, develop -> %s; want twin -> develop, develop -> main", w, d)
	}
}

// An unrelated history (no commit in common with the trunk) still imports,
// under the trunk with its tip as its base — the pre-existing behaviour.
func TestImportKeepsUnrelatedHistoryUnderTrunk(t *testing.T) {
	tp := newTopo(t)
	tp.commit(t, "A")
	tp.orphan(t, "orphan", "O")
	_, lines := importInto(t, tp)
	o := lines["orphan"]
	if o.ParentLine != "main" || o.BaseCommit != tp.sha["O"] {
		t.Fatalf("orphan -> %s base %.8s; want main, base = its own tip %.8s", o.ParentLine, o.BaseCommit, tp.sha["O"])
	}
}

// The documented tie: late forks from develop at C, then both move on. The
// topology cannot say which came first; the name-order rule places late
// under develop, and the result is deterministic across imports.
func TestImportSharedForkPointIsDeterministic(t *testing.T) {
	tp := newTopo(t)
	tp.commit(t, "A")
	tp.branch(t, "develop")
	tp.commit(t, "C")
	tp.branch(t, "late")
	tp.commit(t, "L")
	tp.checkout(t, "develop")
	tp.commit(t, "D")
	_, lines := importInto(t, tp)
	if lines["late"].ParentLine != "develop" || lines["late"].BaseCommit != tp.sha["C"] {
		t.Fatalf("late -> %s at %.8s; want develop at C %.8s", lines["late"].ParentLine, lines["late"].BaseCommit, tp.sha["C"])
	}
	if lines["develop"].ParentLine != "main" {
		t.Fatalf("develop -> %s, want main", lines["develop"].ParentLine)
	}
}
