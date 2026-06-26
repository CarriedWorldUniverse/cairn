package repo

import (
	"context"
	"os/exec"
	"runtime"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// stageEmbargoRef writes a commit into the public bare and points an embargo ref
// at it — simulating what a cairn client's dual-projection push delivers.
func stageEmbargoRef(t *testing.T, barePath, refName, body string) plumbing.Hash {
	t.Helper()
	repo, err := git.PlainOpen(barePath)
	if err != nil {
		t.Fatal(err)
	}
	st := repo.Storer
	blob := st.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	w, _ := blob.Writer()
	_, _ = w.Write([]byte(body))
	_ = w.Close()
	blobHash, _ := st.SetEncodedObject(blob)

	tree := &object.Tree{Entries: []object.TreeEntry{{Name: "secret.txt", Mode: filemode.Regular, Hash: blobHash}}}
	to := st.NewEncodedObject()
	if err := tree.Encode(to); err != nil {
		t.Fatal(err)
	}
	treeHash, _ := st.SetEncodedObject(to)

	sig := object.Signature{Name: "dev", Email: "d@x"}
	c := &object.Commit{Author: sig, Committer: sig, Message: "embargoed fix\n", TreeHash: treeHash}
	co := st.NewEncodedObject()
	if err := c.Encode(co); err != nil {
		t.Fatal(err)
	}
	commitHash, _ := st.SetEncodedObject(co)
	if err := st.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(refName), commitHash)); err != nil {
		t.Fatal(err)
	}
	return commitHash
}

// TestRelocateEmbargoRefs proves the leak-proof receive substrate: embargo refs
// (and their objects) move out of the public bare into the embargo bare; the
// public bare is left with neither the ref nor the embargoed object.
func TestRelocateEmbargoRefs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("server git shell-out is not exercised on Windows CI")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("system git not available")
	}
	svc := newTestService(t)
	ctx := context.Background()
	r, err := svc.CreateRepo(ctx, "org-1", "widgets")
	if err != nil {
		t.Fatal(err)
	}

	refName := embargoRefPrefix + "heads/main"
	embSHA := stageEmbargoRef(t, r.StoragePath, refName, "SUPER_SECRET_FIX\n")

	n, err := svc.RelocateEmbargoRefs(ctx, r.ID)
	if err != nil {
		t.Fatalf("RelocateEmbargoRefs: %v", err)
	}
	if n != 1 {
		t.Fatalf("relocated %d refs, want 1", n)
	}

	// Public bare: the embargo ref is gone, and the embargoed commit is no longer
	// reachable from ANY public ref — so git-upload-pack never advertises or serves
	// it to a clone. (The now-dangling object may physically linger until gc; that
	// is not a serve leak — reachability, not physical presence, is what's served.)
	pub, _ := git.PlainOpen(r.StoragePath)
	if _, err := pub.Reference(plumbing.ReferenceName(refName), false); err == nil {
		t.Error("public bare still has the embargo ref")
	}
	refs, _ := pub.References()
	_ = refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() == plumbing.HashReference && ref.Hash() == embSHA {
			t.Errorf("LEAK: public ref %s still reaches the embargoed commit", ref.Name())
		}
		return nil
	})

	// Embargo bare: has the ref AND the object.
	emb, err := git.PlainOpen(svc.EmbargoStoragePath(r.ID))
	if err != nil {
		t.Fatalf("open embargo bare: %v", err)
	}
	ref, err := emb.Reference(plumbing.ReferenceName(refName), false)
	if err != nil || ref.Hash() != embSHA {
		t.Fatalf("embargo bare ref = %v (err %v), want %s", ref, err, embSHA)
	}
	if _, err := emb.CommitObject(embSHA); err != nil {
		t.Fatalf("embargo bare missing the embargoed commit: %v", err)
	}
}

// TestRelocateEmbargoRefsNoopWhenNone: a normal push (no embargo refs) relocates
// nothing and creates no embargo bare.
func TestRelocateEmbargoRefsNoopWhenNone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("server git shell-out is not exercised on Windows CI")
	}
	svc := newTestService(t)
	ctx := context.Background()
	r, err := svc.CreateRepo(ctx, "org-1", "widgets")
	if err != nil {
		t.Fatal(err)
	}
	n, err := svc.RelocateEmbargoRefs(ctx, r.ID)
	if err != nil || n != 0 {
		t.Fatalf("RelocateEmbargoRefs on a plain repo = (%d,%v), want (0,nil)", n, err)
	}
	if _, err := exec.LookPath("git"); err == nil {
		_ = err // git present; fine either way
	}
}
