package repo

import (
	"context"
	"testing"
)

// TestChangeGraphEmptyRepo proves the server wiring end-to-end on a real hosted
// bare: CreateRepo → OpenChangeEngine (change.OpenBare on the bare) → LoadFromMeta
// (none yet) → empty summary, no error. The populated read is covered by the
// engine-level TestOpenBareReconstructsGraphFromMeta.
func TestChangeGraphEmptyRepo(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	if _, err := svc.CreateRepo(ctx, "org-1", "widgets"); err != nil {
		t.Fatal(err)
	}
	sum, err := svc.ChangeGraph(ctx, "org-1", "widgets")
	if err != nil {
		t.Fatalf("ChangeGraph: %v", err)
	}
	if len(sum.Lines) != 0 || sum.OpenConflicts != 0 {
		t.Fatalf("empty repo summary = %+v, want empty", sum)
	}
}
