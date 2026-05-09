// Cairn-specific code; AGPLv3. See LICENSING.md.
package reviewpolicy

import (
	"context"
	"testing"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
)

func TestLoad_DefaultPolicyWhenNoRow(t *testing.T) {
	eng := cairntest.NewEngine(t)
	svc := NewService(eng, nil)
	p, err := svc.Load(context.Background(), 42)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !p.RequireHumanOnly {
		t.Error("default policy should require human-only")
	}
	if p.OwnerID != 42 {
		t.Errorf("default policy ownerID: want 42, got %d", p.OwnerID)
	}
}

func TestLoad_ReadsExistingRow(t *testing.T) {
	eng := cairntest.NewEngine(t)
	if _, err := eng.Insert(&cairnmodels.ReviewPolicy{OwnerID: 7, RequireHumanOnly: false}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	svc := NewService(eng, nil)
	p, err := svc.Load(context.Background(), 7)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.RequireHumanOnly {
		t.Error("disabled-policy row should be returned as-is (RequireHumanOnly=false)")
	}
}

func TestRequireHumanOnly_FailsClosedOnError(t *testing.T) {
	eng := cairntest.NewEngine(t)
	// Force an engine error by closing the engine before query.
	if err := eng.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	svc := NewService(eng, nil)

	// Direct Load should error.
	if _, err := svc.Load(context.Background(), 1); err == nil {
		t.Fatal("expected Load to error against closed engine")
	}

	// RequireHumanOnly must fail closed → return true.
	if got := svc.RequireHumanOnly(context.Background(), 1); !got {
		t.Error("RequireHumanOnly must fail closed (return true) when Load errors")
	}
}
