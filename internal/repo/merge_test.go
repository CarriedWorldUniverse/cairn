package repo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// writeCommit writes a commit object (zero tree — only history matters for
// ancestor checks) with the given parents and returns its hash.
func writeCommit(t *testing.T, g *git.Repository, msg string, parents ...plumbing.Hash) plumbing.Hash {
	t.Helper()
	c := &object.Commit{
		Author:       object.Signature{Name: "t", Email: "t@t", When: time.Unix(0, 0).UTC()},
		Committer:    object.Signature{Name: "t", Email: "t@t", When: time.Unix(0, 0).UTC()},
		Message:      msg,
		TreeHash:     plumbing.ZeroHash,
		ParentHashes: parents,
	}
	enc := g.Storer.NewEncodedObject()
	if err := c.Encode(enc); err != nil {
		t.Fatalf("encode commit: %v", err)
	}
	h, err := g.Storer.SetEncodedObject(enc)
	if err != nil {
		t.Fatalf("set object: %v", err)
	}
	return h
}

func setBranch(t *testing.T, g *git.Repository, name string, h plumbing.Hash) {
	t.Helper()
	if err := g.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName(name), h)); err != nil {
		t.Fatalf("set ref %s: %v", name, err)
	}
}

func TestFastForward(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	r, err := svc.CreateRepo(ctx, "org-1", "widgets")
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	g, err := git.PlainOpen(r.StoragePath)
	if err != nil {
		t.Fatalf("PlainOpen: %v", err)
	}

	// main = A; feature = A→B  →  main is ancestor of feature  →  ff.
	a := writeCommit(t, g, "A")
	b := writeCommit(t, g, "B", a)
	setBranch(t, g, "main", a)
	setBranch(t, g, "feature", b)

	sha, err := svc.FastForward(ctx, r.ID, "feature", "main")
	if err != nil {
		t.Fatalf("FastForward (ff case): %v", err)
	}
	if sha != b.String() {
		t.Fatalf("merged sha = %s, want %s", sha, b.String())
	}
	ref, _ := svc.GetRef(ctx, r.ID, "refs/heads/main")
	if ref.Hash != b.String() {
		t.Fatalf("main = %s, want %s (advanced)", ref.Hash, b.String())
	}

	// Already up to date: merge old(=A, ancestor of main=B) into main(=B).
	setBranch(t, g, "main", b)
	setBranch(t, g, "old", a)
	if _, err := svc.FastForward(ctx, r.ID, "old", "main"); !errors.Is(err, ErrAlreadyUpToDate) {
		t.Fatalf("up-to-date err = %v, want ErrAlreadyUpToDate", err)
	}

	// Diverged: two unrelated roots → not a fast-forward.
	c := writeCommit(t, g, "C") // independent root
	setBranch(t, g, "diverged", c)
	setBranch(t, g, "trunk", a)
	if _, err := svc.FastForward(ctx, r.ID, "diverged", "trunk"); !errors.Is(err, ErrNotFastForward) {
		t.Fatalf("diverged err = %v, want ErrNotFastForward", err)
	}
}

func TestSetPullState(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	r, _ := svc.CreateRepo(ctx, "org-1", "widgets")
	p := Pull{RepoID: r.ID, Source: "feature", Target: "main", Title: "x", LedgerIssueKey: "WID-1", OpenedBy: "a"}
	if err := svc.CreatePull(ctx, &p); err != nil {
		t.Fatalf("CreatePull: %v", err)
	}
	if err := svc.SetPullState(ctx, r.ID, p.ID, "merged"); err != nil {
		t.Fatalf("SetPullState: %v", err)
	}
	got, _ := svc.GetPull(ctx, r.ID, p.ID)
	if got.State != "merged" {
		t.Fatalf("state = %q, want merged", got.State)
	}
}
