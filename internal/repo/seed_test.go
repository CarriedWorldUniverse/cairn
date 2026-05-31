package repo

import (
	"context"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// seedCommit writes a single commit containing one file ("README", body=content)
// to refs/heads/<branch> of the bare repo behind repoID, and returns its sha.
// It builds the tree/commit objects directly so no worktree is needed.
func seedCommit(t *testing.T, svc *Service, repoID, branch, content string) string {
	t.Helper()
	ctx := context.Background()
	g, err := svc.openGit(ctx, repoID)
	if err != nil {
		t.Fatalf("openGit: %v", err)
	}
	store := g.Storer

	// Blob.
	blob := store.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	w, err := blob.Writer()
	if err != nil {
		t.Fatalf("blob writer: %v", err)
	}
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatalf("blob write: %v", err)
	}
	_ = w.Close()
	blobHash, err := store.SetEncodedObject(blob)
	if err != nil {
		t.Fatalf("set blob: %v", err)
	}

	// Tree with one entry.
	tree := &object.Tree{Entries: []object.TreeEntry{
		{Name: "README", Mode: 0o100644, Hash: blobHash},
	}}
	teo := store.NewEncodedObject()
	if err := tree.Encode(teo); err != nil {
		t.Fatalf("encode tree: %v", err)
	}
	treeHash, err := store.SetEncodedObject(teo)
	if err != nil {
		t.Fatalf("set tree: %v", err)
	}

	// Commit.
	when := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	commit := &object.Commit{
		Author:    object.Signature{Name: "cairn-test", Email: "test@cairn", When: when},
		Committer: object.Signature{Name: "cairn-test", Email: "test@cairn", When: when},
		Message:   "seed: " + content,
		TreeHash:  treeHash,
	}
	ceo := store.NewEncodedObject()
	if err := commit.Encode(ceo); err != nil {
		t.Fatalf("encode commit: %v", err)
	}
	commitHash, err := store.SetEncodedObject(ceo)
	if err != nil {
		t.Fatalf("set commit: %v", err)
	}

	// Point the branch at the commit.
	refName := plumbing.NewBranchReferenceName(branch)
	if err := store.SetReference(plumbing.NewHashReference(refName, commitHash)); err != nil {
		t.Fatalf("set ref: %v", err)
	}
	return commitHash.String()
}
