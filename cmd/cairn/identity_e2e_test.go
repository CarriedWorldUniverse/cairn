package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateIdentityEnv points the global config at a temp dir and clears every
// identity env override, so these tests exercise the repo→global→git layering
// in isolation regardless of the host's real config.
func isolateIdentityEnv(t *testing.T) {
	t.Helper()
	// os.UserConfigDir reads a different env var per platform; set them all so
	// the global config is isolated on every runner (Linux/macOS/Windows).
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("AppData", dir)
	t.Setenv("CAIRN_AUTHOR", "")
	t.Setenv("CAIRN_EMAIL", "")
	t.Setenv("GIT_AUTHOR_EMAIL", "")
}

// TestE2E_GlobalIdentityUsedForCommit verifies that `cairn config --global`
// writes the user-level identity and that a commit with no repo config authors
// with it (the global layer, not git).
func TestE2E_GlobalIdentityUsedForCommit(t *testing.T) {
	isolateIdentityEnv(t)
	mustRun(t, "config", "--global", "user.name", "Global Dev")
	mustRun(t, "config", "--global", "user.email", "global@dev.io")

	root := t.TempDir()
	mustRun(t, "init", root)
	if err := os.WriteFile(filepath.Join(root, "main", "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha := strings.TrimSpace(captureRun(t, "commit", "--repo", root, "main"))

	out := captureRun(t, "show", "--repo", root, sha)
	if !strings.Contains(out, "Global Dev <global@dev.io>") {
		t.Fatalf("commit not authored with global identity; show:\n%s", out)
	}
}

// TestE2E_RepoIdentityOverridesGlobal verifies repo config takes precedence over
// the global config (git-style local-over-global layering).
func TestE2E_RepoIdentityOverridesGlobal(t *testing.T) {
	isolateIdentityEnv(t)
	mustRun(t, "config", "--global", "user.name", "Global Dev")
	mustRun(t, "config", "--global", "user.email", "global@dev.io")

	root := t.TempDir()
	mustRun(t, "init", root)
	mustRun(t, "config", "--repo", root, "user.name", "Repo Dev")
	mustRun(t, "config", "--repo", root, "user.email", "repo@dev.io")
	if err := os.WriteFile(filepath.Join(root, "main", "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha := strings.TrimSpace(captureRun(t, "commit", "--repo", root, "main"))

	out := captureRun(t, "show", "--repo", root, sha)
	if !strings.Contains(out, "Repo Dev <repo@dev.io>") {
		t.Fatalf("repo identity did not override global; show:\n%s", out)
	}
}

// TestE2E_ConfigGetFallsThroughToGlobal verifies a bare `config <key>` get on a
// repo with the key unset shows the global value — so what you read matches what
// a commit will author with.
func TestE2E_ConfigGetFallsThroughToGlobal(t *testing.T) {
	isolateIdentityEnv(t)
	mustRun(t, "config", "--global", "user.email", "global@dev.io")
	root := t.TempDir()
	mustRun(t, "init", root)
	out := captureRun(t, "config", "--repo", root, "user.email")
	if strings.TrimSpace(out) != "global@dev.io" {
		t.Fatalf("repo config get did not fall through to global: %q", strings.TrimSpace(out))
	}
	// A repo override wins over global.
	mustRun(t, "config", "--repo", root, "user.email", "repo@dev.io")
	out = captureRun(t, "config", "--repo", root, "user.email")
	if strings.TrimSpace(out) != "repo@dev.io" {
		t.Fatalf("repo override not shown: %q", strings.TrimSpace(out))
	}
}

// TestE2E_ConfigGlobalRoundTrip verifies `config --global <key>` reads back what
// was written, and that --global needs no repo (it is user-level).
func TestE2E_ConfigGlobalRoundTrip(t *testing.T) {
	isolateIdentityEnv(t)
	mustRun(t, "config", "--global", "user.name", "Round Trip")
	out := captureRun(t, "config", "--global", "user.name")
	if strings.TrimSpace(out) != "Round Trip" {
		t.Fatalf("config --global read = %q, want Round Trip", strings.TrimSpace(out))
	}
}
