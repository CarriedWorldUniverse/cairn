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

func TestListPulls(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	r, _ := svc.CreateRepo(ctx, "org-1", "widgets")

	mk := func(src string) Pull {
		p := Pull{RepoID: r.ID, Source: src, Target: "main", Title: src, LedgerIssueKey: "K-" + src}
		if err := svc.CreatePull(ctx, &p); err != nil {
			t.Fatalf("CreatePull %s: %v", src, err)
		}
		return p
	}
	a := mk("feat-a")
	b := mk("feat-b")
	if err := svc.SetPullState(ctx, r.ID, b.ID, "merged"); err != nil {
		t.Fatalf("SetPullState: %v", err)
	}

	all, err := svc.ListPulls(ctx, r.ID, "all")
	if err != nil || len(all) != 2 {
		t.Fatalf("ListPulls all: %v len=%d", err, len(all))
	}
	open, err := svc.ListPulls(ctx, r.ID, "open")
	if err != nil || len(open) != 1 || open[0].ID != a.ID {
		t.Fatalf("ListPulls open: %v %+v", err, open)
	}
	merged, _ := svc.ListPulls(ctx, r.ID, "merged")
	if len(merged) != 1 || merged[0].ID != b.ID {
		t.Fatalf("ListPulls merged: %+v", merged)
	}
	// "" behaves like "all".
	if blank, _ := svc.ListPulls(ctx, r.ID, ""); len(blank) != 2 {
		t.Fatalf("ListPulls \"\": want 2, got %d", len(blank))
	}
}

func TestRecordListPullChecks(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	r, _ := svc.CreateRepo(ctx, "org-1", "widgets")
	p := Pull{RepoID: r.ID, Source: "feature", Target: "main", Title: "x", LedgerIssueKey: "WID-1", OpenedBy: "a"}
	if err := svc.CreatePull(ctx, &p); err != nil {
		t.Fatalf("CreatePull: %v", err)
	}

	c := &PullCheck{PullID: p.ID, Name: "ci", State: CheckStateFail, Summary: "build broke", RecordedBy: "agent-1"}
	if err := svc.RecordPullCheck(ctx, c); err != nil {
		t.Fatalf("RecordPullCheck: %v", err)
	}
	if c.ID == "" || c.RecordedAt.IsZero() {
		t.Fatalf("RecordPullCheck did not populate id/recorded_at: %+v", c)
	}

	checks, err := svc.ListPullChecks(ctx, p.ID)
	if err != nil || len(checks) != 1 {
		t.Fatalf("ListPullChecks: %v len=%d", err, len(checks))
	}
	if checks[0].State != CheckStateFail || checks[0].RecordedBy != "agent-1" {
		t.Fatalf("unexpected check: %+v", checks[0])
	}
	firstID := checks[0].ID

	// Re-recording the same name upserts: same row (id unchanged), new state.
	c2 := &PullCheck{PullID: p.ID, Name: "ci", State: CheckStatePass, Summary: "green", RecordedBy: "agent-2"}
	if err := svc.RecordPullCheck(ctx, c2); err != nil {
		t.Fatalf("RecordPullCheck (upsert): %v", err)
	}
	if c2.ID != firstID {
		t.Fatalf("upsert id = %s, want unchanged %s", c2.ID, firstID)
	}
	checks, err = svc.ListPullChecks(ctx, p.ID)
	if err != nil || len(checks) != 1 {
		t.Fatalf("ListPullChecks after upsert: %v len=%d", err, len(checks))
	}
	if checks[0].State != CheckStatePass || checks[0].Summary != "green" || checks[0].RecordedBy != "agent-2" {
		t.Fatalf("upsert did not replace: %+v", checks[0])
	}

	// A different name is a second, independent check.
	c3 := &PullCheck{PullID: p.ID, Name: "security", State: CheckStatePending, RecordedBy: "agent-1"}
	if err := svc.RecordPullCheck(ctx, c3); err != nil {
		t.Fatalf("RecordPullCheck (second name): %v", err)
	}
	if checks, err := svc.ListPullChecks(ctx, p.ID); err != nil || len(checks) != 2 {
		t.Fatalf("ListPullChecks after second name: %v len=%d", err, len(checks))
	}

	// Invalid state is rejected.
	if err := svc.RecordPullCheck(ctx, &PullCheck{PullID: p.ID, Name: "ci", State: "bogus"}); err == nil {
		t.Fatal("RecordPullCheck invalid state: want error, got nil")
	}
}
