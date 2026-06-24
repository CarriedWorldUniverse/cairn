package change

import (
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// TestPushWritesMetaForCairnRemote: a cairn-kind remote receives refs/cairn/meta
// after PushToRemote, and the ref points at a commit whose tree contains meta.json.
func TestPushWritesMetaForCairnRemote(t *testing.T) {
	skipOnWindows(t)

	bareDir := t.TempDir()
	if _, err := git.PlainInit(bareDir, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}

	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	// Build some content so Export() has a real ref to push.
	main, _ := e.LineByName("main")
	ch, _ := e.CreateChange(main.ID, "tester")
	if _, err := e.Commit(ch.ID, map[string][]byte{"hello.txt": []byte("hello\n")}, nil, "init"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if err := e.AddRemote("origin", bareDir, "cairn"); err != nil {
		t.Fatalf("AddRemote(cairn): %v", err)
	}
	if err := e.PushToRemote("origin", false); err != nil {
		t.Fatalf("PushToRemote: %v", err)
	}

	bare, err := git.PlainOpen(bareDir)
	if err != nil {
		t.Fatalf("PlainOpen bare: %v", err)
	}

	metaRef, err := bare.Reference(plumbing.ReferenceName("refs/cairn/meta"), true)
	if err != nil {
		t.Fatalf("refs/cairn/meta missing from bare repo: %v", err)
	}
	if metaRef.Hash().IsZero() {
		t.Fatal("refs/cairn/meta points at zero hash")
	}

	// Verify the commit tree contains meta.json.
	commit, err := bare.CommitObject(metaRef.Hash())
	if err != nil {
		t.Fatalf("CommitObject(meta): %v", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("Tree(meta): %v", err)
	}
	if _, err := tree.File("meta.json"); err != nil {
		t.Fatalf("meta.json not found in meta commit tree: %v", err)
	}
}

// TestPushNoMetaForGitRemote: a git-kind remote must NOT receive refs/cairn/meta.
func TestPushNoMetaForGitRemote(t *testing.T) {
	skipOnWindows(t)

	bareDir := t.TempDir()
	if _, err := git.PlainInit(bareDir, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}

	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	main, _ := e.LineByName("main")
	ch, _ := e.CreateChange(main.ID, "tester")
	if _, err := e.Commit(ch.ID, map[string][]byte{"hello.txt": []byte("hello\n")}, nil, "init"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if err := e.AddRemote("origin", bareDir, "git"); err != nil {
		t.Fatalf("AddRemote(git): %v", err)
	}
	if err := e.PushToRemote("origin", false); err != nil {
		t.Fatalf("PushToRemote: %v", err)
	}

	bare, err := git.PlainOpen(bareDir)
	if err != nil {
		t.Fatalf("PlainOpen bare: %v", err)
	}

	// refs/cairn/meta must NOT be present on a git remote.
	if _, err := bare.Reference(plumbing.ReferenceName("refs/cairn/meta"), true); err == nil {
		t.Fatal("refs/cairn/meta must not be pushed to a git remote, but it was found")
	}
}

// TestFetchPullsCairnRefs: after a cairn push to a bare repo, a fresh engine that
// clones (ImportFromRemote) gets refs/cairn/meta in its local store.
func TestFetchPullsCairnRefs(t *testing.T) {
	skipOnWindows(t)

	bareDir := t.TempDir()
	if _, err := git.PlainInit(bareDir, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}

	// Source engine: push with cairn kind so the bare has refs/cairn/meta.
	src, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open src: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })

	main, _ := src.LineByName("main")
	ch, _ := src.CreateChange(main.ID, "tester")
	if _, err := src.Commit(ch.ID, map[string][]byte{"a.txt": []byte("a\n")}, nil, "base"); err != nil {
		t.Fatalf("Commit src: %v", err)
	}

	if err := src.AddRemote("origin", bareDir, "cairn"); err != nil {
		t.Fatalf("AddRemote src: %v", err)
	}
	if err := src.PushToRemote("origin", false); err != nil {
		t.Fatalf("PushToRemote src: %v", err)
	}

	// Sanity: bare must have refs/cairn/meta at this point.
	bare, err := git.PlainOpen(bareDir)
	if err != nil {
		t.Fatalf("PlainOpen bare: %v", err)
	}
	if _, err := bare.Reference(plumbing.ReferenceName("refs/cairn/meta"), true); err != nil {
		t.Fatalf("bare missing refs/cairn/meta before fetch test: %v", err)
	}

	// Fresh engine: fetch from the bare repo and verify refs/cairn/meta lands locally.
	dst, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open dst: %v", err)
	}
	t.Cleanup(func() { _ = dst.Close() })

	if err := dst.fetchRemote(bareDir); err != nil {
		t.Fatalf("fetchRemote dst: %v", err)
	}

	// refs/cairn/meta should now be in the destination's git store.
	dstRef, err := dst.git.Reference(plumbing.ReferenceName("refs/cairn/meta"), true)
	if err != nil {
		t.Fatalf("dst refs/cairn/meta missing after fetchRemote: %v", err)
	}
	if dstRef.Hash().IsZero() {
		t.Fatal("dst refs/cairn/meta points at zero hash")
	}

	// Verify the commit object is accessible in the destination.
	if _, err := dst.git.CommitObject(dstRef.Hash()); err != nil {
		t.Fatalf("meta commit not accessible in dst store: %v", err)
	}

	// Verify the meta commit tree has meta.json (object reachability check).
	commit, err := dst.git.CommitObject(dstRef.Hash())
	if err != nil {
		t.Fatalf("CommitObject dst: %v", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("Tree dst: %v", err)
	}
	if _, err := tree.File("meta.json"); err != nil {
		t.Fatalf("meta.json not in dst meta commit tree: %v", err)
	}
}

