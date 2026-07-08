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

// TestPush_ReconcileFlagValidation_BareRootCWD covers the third --reconcile
// rejection path: no explicit branch, --all not set, and cwd is NOT inside an
// expressed branch folder (a bare repo root), so no line resolves for
// --reconcile to scope to. This must get its OWN wording — the operator never
// typed --all here, so reusing the --reconcile+--all message would be
// confusing/wrong context (issue #91 review finding).
func TestPush_ReconcileFlagValidation_BareRootCWD(t *testing.T) {
	bare := makeSeededBareRepo(t)
	dir := t.TempDir()
	mustRun(t, "clone", bare, dir)

	err := run([]string{"push", "--repo", dir, "--reconcile"})
	if err == nil {
		t.Fatalf("--reconcile with no resolvable line: want an error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "--reconcile") || !strings.Contains(msg, "single line") {
		t.Fatalf("bare-root --reconcile: error %q missing expected wording", msg)
	}
	if strings.Contains(msg, "--all") {
		t.Fatalf("bare-root --reconcile: error %q wrongly mentions --all (operator never passed it)", msg)
	}
}
