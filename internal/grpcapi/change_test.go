package grpcapi

import (
	"context"
	"errors"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestChangeServiceCreateLineLineage(t *testing.T) {
	e, err := change.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	svc := NewChangeService(e)
	ctx := context.Background()

	main, _ := e.LineByName("main")
	if _, err := svc.CreateLine(ctx, "exp", main.ID); err != nil {
		t.Fatalf("CreateLine: %v", err)
	}
	exp, _ := e.LineByName("exp")
	chain, err := svc.GetLineage(ctx, exp.ID)
	if err != nil {
		t.Fatalf("GetLineage: %v", err)
	}
	if len(chain) != 2 || chain[0].Name != "main" || chain[1].Name != "exp" {
		t.Fatalf("lineage = %+v", chain)
	}
}

func TestChangeServiceMapsNotFound(t *testing.T) {
	e, _ := change.Open(t.TempDir())
	t.Cleanup(func() { _ = e.Close() })
	svc := NewChangeService(e)
	_, err := svc.GetChange(context.Background(), "znope")
	if status.Code(err) != codes.NotFound {
		t.Fatalf("GetChange unknown: code = %v, want NotFound", status.Code(err))
	}
	_ = errors.Is // keep import if unused otherwise
}
