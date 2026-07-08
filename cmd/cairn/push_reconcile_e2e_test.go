package main

import (
	"strings"
	"testing"
)

// TestPush_ReconcileFlagValidation covers issue #91's --reconcile flag
// contract: it applies to a single-line push only, so it is rejected when
// combined with --all (which has its own all-lines auto-reconcile) or --force
// (which never pulls, so "reconcile" is meaningless). Both checks run before
// any repo is opened, so no fixture is needed.
func TestPush_ReconcileFlagValidation(t *testing.T) {
	err := run([]string{"push", "--reconcile", "--all"})
	if err == nil {
		t.Fatalf("--reconcile + --all: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "--reconcile") || !strings.Contains(err.Error(), "single-line") {
		t.Fatalf("--reconcile + --all: error %q missing expected wording", err.Error())
	}

	err = run([]string{"push", "--reconcile", "--force"})
	if err == nil {
		t.Fatalf("--reconcile + --force: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "--reconcile") || !strings.Contains(err.Error(), "--force") || !strings.Contains(err.Error(), "contradictory") {
		t.Fatalf("--reconcile + --force: error %q missing expected wording", err.Error())
	}
}
