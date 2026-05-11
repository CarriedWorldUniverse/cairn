package identity

import (
	"context"
	"errors"
	"testing"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
)

func TestXormAgentStore_FindOrCreateAndGet(t *testing.T) {
	eng := cairntest.NewEngine(t)
	s := NewXormAgentStore(eng)

	ctx := context.Background()
	a, created, err := s.FindOrCreateByUserSlug(ctx, 1, "plumb", "darksoft.co.nz")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("expected created=true on first insert")
	}
	if a.ID == 0 {
		t.Error("ID not populated after create")
	}

	// Re-call returns the same row, created=false.
	again, created, err := s.FindOrCreateByUserSlug(ctx, 1, "plumb", "darksoft.co.nz")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("expected created=false on second call")
	}
	if again.ID != a.ID {
		t.Errorf("ID = %d, want %d", again.ID, a.ID)
	}

	got, err := s.GetByID(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Slug != "plumb" {
		t.Errorf("Slug = %q, want %q", got.Slug, "plumb")
	}
}

func TestXormAgentStore_GetByEmail(t *testing.T) {
	eng := cairntest.NewEngine(t)
	s := NewXormAgentStore(eng)

	ctx := context.Background()
	if _, _, err := s.FindOrCreateByUserSlug(ctx, 1, "plumb", "darksoft.co.nz"); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetByEmail(ctx, "plumb", "darksoft.co.nz")
	if err != nil {
		t.Fatal(err)
	}
	if got.Domain != "darksoft.co.nz" {
		t.Errorf("Domain = %q, want %q", got.Domain, "darksoft.co.nz")
	}
}

func TestXormAgentStore_NotFound(t *testing.T) {
	eng := cairntest.NewEngine(t)
	s := NewXormAgentStore(eng)

	_, err := s.GetByID(context.Background(), 9999)
	if !errors.Is(err, ErrAgentNotFound) {
		t.Errorf("err = %v, want %v", err, ErrAgentNotFound)
	}

	_, err = s.GetByEmail(context.Background(), "ghost", "example.com")
	if !errors.Is(err, ErrAgentNotFound) {
		t.Errorf("err = %v, want %v", err, ErrAgentNotFound)
	}
}

func TestXormAgentStore_ListByUser(t *testing.T) {
	eng := cairntest.NewEngine(t)
	s := NewXormAgentStore(eng)

	ctx := context.Background()
	for _, slug := range []string{"plumb", "anvil", "forge"} {
		a, _, err := s.FindOrCreateByUserSlug(ctx, 1, slug, "darksoft.co.nz")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetStatus(ctx, a.ID, cairn.AgentStatusActive); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.ListByUser(ctx, 1, cairn.AgentStatusActive)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("len = %d, want 3", len(got))
	}
}

func TestXormAgentStore_SetStatus_StampsActivatedAt(t *testing.T) {
	eng := cairntest.NewEngine(t)
	s := NewXormAgentStore(eng)

	ctx := context.Background()
	a, _, err := s.FindOrCreateByUserSlug(ctx, 1, "plumb", "darksoft.co.nz")
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != cairn.AgentStatusPending {
		t.Fatalf("setup: status = %q, want pending", a.Status)
	}

	if err := s.SetStatus(ctx, a.ID, cairn.AgentStatusActive); err != nil {
		t.Fatal(err)
	}

	got, _ := s.GetByID(ctx, a.ID)
	if got.Status != cairn.AgentStatusActive {
		t.Errorf("Status = %q, want %q", got.Status, cairn.AgentStatusActive)
	}
	if got.ActivatedAt == nil || got.ActivatedAt.IsZero() {
		t.Error("ActivatedAt not set after SetStatus(active)")
	}
}
