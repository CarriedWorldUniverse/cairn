package worktree

import (
	"path/filepath"
	"testing"
)

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "wc.json")
	st := &State{Expressed: map[string]Entry{
		"main": {Path: "main", ChangeID: "zxy1"},
		"exp":  {Path: "exp", ChangeID: "zab2"},
	}}
	if err := SaveState(p, st); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	got, err := LoadState(p)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if len(got.Expressed) != 2 || got.Expressed["exp"].ChangeID != "zab2" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestLoadStateMissingReturnsEmpty(t *testing.T) {
	got, err := LoadState(filepath.Join(t.TempDir(), "none.json"))
	if err != nil {
		t.Fatalf("LoadState missing: %v", err)
	}
	if got == nil || len(got.Expressed) != 0 {
		t.Fatalf("missing state should be empty, got %+v", got)
	}
}
