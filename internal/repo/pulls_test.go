package repo

import (
	"context"
	"errors"
	"testing"
)

func TestCreateGetFindOpenPull(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)

	r, err := svc.CreateRepo(ctx, "org-1", "widgets")
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	p := Pull{
		RepoID: r.ID, Source: "feature", Target: "main",
		Title: "Add X", LedgerIssueKey: "WID-1", OpenedBy: "agent-1",
	}
	if err := svc.CreatePull(ctx, &p); err != nil {
		t.Fatalf("CreatePull: %v", err)
	}
	if p.ID == "" || p.State != PullStateOpen {
		t.Fatalf("CreatePull did not populate id/state: %+v", p)
	}

	got, err := svc.GetPull(ctx, r.ID, p.ID)
	if err != nil {
		t.Fatalf("GetPull: %v", err)
	}
	if got.LedgerIssueKey != "WID-1" || got.Source != "feature" {
		t.Fatalf("GetPull mismatch: %+v", got)
	}

	open, err := svc.FindOpenPull(ctx, r.ID, "feature", "main")
	if err != nil {
		t.Fatalf("FindOpenPull: %v", err)
	}
	if open.ID != p.ID {
		t.Fatalf("FindOpenPull id = %s, want %s", open.ID, p.ID)
	}

	// No open PR for a different pair → ErrPullNotFound.
	if _, err := svc.FindOpenPull(ctx, r.ID, "other", "main"); !errors.Is(err, ErrPullNotFound) {
		t.Fatalf("FindOpenPull(other) err = %v, want ErrPullNotFound", err)
	}

	// A second open PR for the same (repo, source, target) is rejected by the index.
	dup := Pull{RepoID: r.ID, Source: "feature", Target: "main", Title: "dup", LedgerIssueKey: "WID-2", OpenedBy: "agent-1"}
	if err := svc.CreatePull(ctx, &dup); err == nil {
		t.Fatal("CreatePull duplicate open PR: want error, got nil")
	}
}
