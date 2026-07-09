package main

import (
	"strings"
	"testing"
)

// TestPRNoVerbPrintsUsage asserts `cairn pr` with no verb errors and its usage
// (printed to stdout, same convention as the top-level `cairn`/`cairn bisect`
// no-subcommand cases) lists all four verbs.
func TestPRNoVerbPrintsUsage(t *testing.T) {
	err := run([]string{"pr"})
	if err == nil {
		t.Fatal("expected error for `cairn pr` with no verb, got nil")
	}
	for _, verb := range []string{"open", "list", "view", "diff", "merge"} {
		if !strings.Contains(prUsage, verb) {
			t.Errorf("pr usage missing verb %q", verb)
		}
	}
}

// TestPRUnknownVerb asserts an unrecognised pr verb errors clearly rather than
// silently doing nothing.
func TestPRUnknownVerb(t *testing.T) {
	err := run([]string{"pr", "frobnicate"})
	if err == nil {
		t.Fatal("expected error for unknown pr verb, got nil")
	}
	if !strings.Contains(err.Error(), "frobnicate") {
		t.Fatalf("error %q should name the unknown verb", err.Error())
	}
}

// TestPRDialRequiresOrgAndSlug asserts every `pr` verb fails fast (before
// dialing any network address) when --org/--repo-slug aren't supplied and
// there's no env/config default — a clear, local error, not a dial timeout.
// isolateIdentityEnv is required here: prConnFlags falls back to the HOST's
// real global config (pr.org/pr.repo-slug) when the env vars are unset, so
// without it this test is non-hermetic — it fails on any machine that has
// `cairn config --global pr.org ...` set (exactly what our own docs tell
// users to do).
func TestPRDialRequiresOrgAndSlug(t *testing.T) {
	isolateIdentityEnv(t)
	t.Setenv("CAIRN_ORG", "")
	t.Setenv("CAIRN_REPO_SLUG", "")
	err := run([]string{"pr", "list", "--server", "127.0.0.1:0"})
	if err == nil {
		t.Fatal("expected error for `pr list` with no --org, got nil")
	}
	if !strings.Contains(err.Error(), "--org") {
		t.Fatalf("error %q should mention --org", err.Error())
	}
}
