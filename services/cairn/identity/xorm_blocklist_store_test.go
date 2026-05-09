package identity

import (
	"context"
	"testing"
)

func TestXormBlocklistStore_BlockAndIsBlocked(t *testing.T) {
	eng := newTestEngine(t) // helper from xorm_store_test.go
	defer eng.Close()
	s := NewXormBlocklistStore(eng)

	ctx := context.Background()
	const agentID int64 = 42

	blocked, err := s.IsBlocked(ctx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if blocked {
		t.Error("agent reported blocked before any Block() call")
	}

	if err := s.Block(ctx, agentID, "key compromised"); err != nil {
		t.Fatal(err)
	}

	blocked, err = s.IsBlocked(ctx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Error("agent not reported blocked after Block() call")
	}
}

func TestXormBlocklistStore_List(t *testing.T) {
	eng := newTestEngine(t)
	defer eng.Close()
	s := NewXormBlocklistStore(eng)

	ctx := context.Background()
	for _, id := range []int64{1, 2, 3} {
		if err := s.Block(ctx, id, "test"); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("len = %d, want 3", len(got))
	}
}

func TestXormBlocklistStore_BlockIsIdempotent(t *testing.T) {
	eng := newTestEngine(t)
	defer eng.Close()
	s := NewXormBlocklistStore(eng)

	ctx := context.Background()
	const agentID int64 = 7

	if err := s.Block(ctx, agentID, "first reason"); err != nil {
		t.Fatal(err)
	}
	if err := s.Block(ctx, agentID, "second reason"); err != nil {
		t.Fatal(err)
	}

	got, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("expected 1 row after two Block calls (idempotent), got %d", len(got))
	}
	if got[0].Reason != "first reason" {
		t.Errorf("expected first-reason preserved (idempotent doesn't overwrite), got %q", got[0].Reason)
	}
}
