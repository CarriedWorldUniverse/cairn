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
