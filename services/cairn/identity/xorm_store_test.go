package identity

import (
	"context"
	"testing"
	"time"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
	cairnmigrations "github.com/CarriedWorldUniverse/cairn/models/cairn/migrations"
	_ "github.com/mattn/go-sqlite3"
	"xorm.io/xorm"
	"xorm.io/xorm/names"
)

func newTestEngine(t *testing.T) *xorm.Engine {
	t.Helper()
	eng, err := xorm.NewEngine("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	// Match production engine config (models/db/engine.go) so column
	// name mapping (UserID -> user_id, CreatedAt -> created_at) lines up
	// with the runtime models.
	eng.SetMapper(names.GonicMapper{})
	if err := cairnmigrations.V500CreateAgentTables(eng); err != nil {
		t.Fatal(err)
	}
	return eng
}

func TestXormAgentStore_RegisterAndGet(t *testing.T) {
	eng := newTestEngine(t)
	defer eng.Close()
	s := NewXormAgentStore(eng)

	ctx := context.Background()
	a := &cairn.Agent{
		Fingerprint: "cairn:abc123",
		UserID:      1,
		Slug:        "plumb",
		Domain:      "darksoft.co.nz",
		PublicKey:   []byte{1, 2, 3, 4},
		Status:      cairn.AgentStatusActive,
		CreatedAt:   time.Now(),
	}
	if err := s.Register(ctx, a); err != nil {
		t.Fatal(err)
	}
	if a.ID == 0 {
		t.Error("ID not populated after Register")
	}

	got, err := s.GetByFingerprint(ctx, "cairn:abc123")
	if err != nil {
		t.Fatal(err)
	}
	if got.Slug != "plumb" {
		t.Errorf("Slug = %q, want %q", got.Slug, "plumb")
	}
}

func TestXormAgentStore_GetByEmail(t *testing.T) {
	eng := newTestEngine(t)
	defer eng.Close()
	s := NewXormAgentStore(eng)

	ctx := context.Background()
	a := &cairn.Agent{
		Fingerprint: "cairn:def456",
		UserID:      1,
		Slug:        "plumb",
		Domain:      "darksoft.co.nz",
		PublicKey:   []byte{5, 6, 7, 8},
		Status:      cairn.AgentStatusActive,
		CreatedAt:   time.Now(),
	}
	if err := s.Register(ctx, a); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetByEmail(ctx, "plumb", "darksoft.co.nz")
	if err != nil {
		t.Fatal(err)
	}
	if got.Fingerprint != "cairn:def456" {
		t.Errorf("Fingerprint = %q, want %q", got.Fingerprint, "cairn:def456")
	}
}

func TestXormAgentStore_NotFound(t *testing.T) {
	eng := newTestEngine(t)
	defer eng.Close()
	s := NewXormAgentStore(eng)

	_, err := s.GetByFingerprint(context.Background(), "cairn:no-such-fp")
	if err != ErrAgentNotFound {
		t.Errorf("err = %v, want %v", err, ErrAgentNotFound)
	}

	_, err = s.GetByEmail(context.Background(), "ghost", "example.com")
	if err != ErrAgentNotFound {
		t.Errorf("err = %v, want %v", err, ErrAgentNotFound)
	}
}

func TestXormAgentStore_DuplicateRegister(t *testing.T) {
	eng := newTestEngine(t)
	defer eng.Close()
	s := NewXormAgentStore(eng)

	ctx := context.Background()
	a := &cairn.Agent{
		Fingerprint: "cairn:dup",
		UserID:      1,
		Slug:        "plumb",
		Domain:      "darksoft.co.nz",
		PublicKey:   []byte{1},
		Status:      cairn.AgentStatusActive,
		CreatedAt:   time.Now(),
	}
	if err := s.Register(ctx, a); err != nil {
		t.Fatal(err)
	}

	// Same (user_id, slug) should fail.
	a2 := *a
	a2.ID = 0
	a2.Fingerprint = "cairn:dup-2"
	err := s.Register(ctx, &a2)
	if err == nil {
		t.Error("expected error registering duplicate (user_id, slug)")
	}
}

func TestXormAgentStore_ListByUser(t *testing.T) {
	eng := newTestEngine(t)
	defer eng.Close()
	s := NewXormAgentStore(eng)

	ctx := context.Background()
	now := time.Now()
	for i, slug := range []string{"plumb", "anvil", "forge"} {
		a := &cairn.Agent{
			Fingerprint: "cairn:fp" + slug,
			UserID:      1,
			Slug:        slug,
			Domain:      "darksoft.co.nz",
			PublicKey:   []byte{byte(i)},
			Status:      cairn.AgentStatusActive,
			CreatedAt:   now,
		}
		if err := s.Register(ctx, a); err != nil {
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

func TestXormAgentStore_Approve(t *testing.T) {
	eng := newTestEngine(t)
	defer eng.Close()
	s := NewXormAgentStore(eng)

	ctx := context.Background()
	a := &cairn.Agent{
		Fingerprint: "cairn:pending",
		UserID:      1,
		Slug:        "plumb",
		Domain:      "darksoft.co.nz",
		PublicKey:   []byte{1},
		Status:      cairn.AgentStatusPending,
		CreatedAt:   time.Now(),
	}
	if err := s.Register(ctx, a); err != nil {
		t.Fatal(err)
	}

	if err := s.Approve(ctx, "cairn:pending"); err != nil {
		t.Fatal(err)
	}

	got, _ := s.GetByFingerprint(ctx, "cairn:pending")
	if got.Status != cairn.AgentStatusActive {
		t.Errorf("Status = %q, want %q", got.Status, cairn.AgentStatusActive)
	}
	if got.ActivatedAt == nil || got.ActivatedAt.IsZero() {
		t.Error("ActivatedAt not set after approve")
	}
}
