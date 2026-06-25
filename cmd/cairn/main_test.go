package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIInitExpressCommit(t *testing.T) {
	root := t.TempDir()
	if err := run([]string{"init", root}); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".cairn")); err != nil {
		t.Fatalf("no .cairn: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "main")); err != nil {
		t.Fatalf("no main folder: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "main", "x.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"commit", "--repo", root, "main"}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := run([]string{"express", "--repo", root, "exp"}); err != nil {
		t.Fatalf("express: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "exp")); err != nil {
		t.Fatalf("no exp folder: %v", err)
	}
}

func TestRunUnknownSubcommand(t *testing.T) {
	if err := run([]string{"bogus"}); err == nil {
		t.Fatal("expected error for unknown subcommand")
	}
}

// TestRepoDiscoveryFromSubfolder covers running cairn from a subfolder of a repo
// (e.g. inside an expressed branch folder): openRepo walks up to find .cairn,
// like git locating .git, instead of failing with "not a cairn repo".
func TestRepoDiscoveryFromSubfolder(t *testing.T) {
	root := t.TempDir()
	if err := run([]string{"init", root}); err != nil {
		t.Fatalf("init: %v", err)
	}
	sub := filepath.Join(root, "main", "deep", "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// A command pointed at the deep subfolder must resolve to the repo root.
	if err := run([]string{"status", "--repo", sub}); err != nil {
		t.Fatalf("status from subfolder %q: %v", sub, err)
	}
	// Outside any repo it must still fail clearly.
	err := run([]string{"status", "--repo", t.TempDir()})
	if err == nil {
		t.Fatal("expected 'not a cairn repo' outside a repo")
	}
	if !strings.Contains(err.Error(), "not a cairn repo") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestBranchInferenceFromCWD: running a command from inside an expressed branch
// folder defaults to that branch (like git's current branch); from the root it
// defaults to the root line. Slash branches use their flat folder name.
func TestBranchInferenceFromCWD(t *testing.T) {
	root := t.TempDir()
	mustRun(t, "init", root)
	if err := os.WriteFile(filepath.Join(root, "main", "f.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "main", "-m", "base")
	mustRun(t, "express", "--repo", root, "--from", "main", "base/5-0")

	// --repo pointing at the flat folder (or a subdir of it) infers base/5-0.
	folder := filepath.Join(root, "base-5-0")
	out := captureRun(t, "status", "--repo", folder)
	if !strings.Contains(out, "branch:    base/5-0") {
		t.Fatalf("status from inside base-5-0 did not infer the branch:\n%s", out)
	}
	// From the repo root it stays on the root line.
	out = captureRun(t, "status", "--repo", root)
	if !strings.Contains(out, "branch:    main") {
		t.Fatalf("status from the root should default to main:\n%s", out)
	}
	// commit with no branch arg, from inside the folder, commits that branch.
	if err := os.WriteFile(filepath.Join(folder, "g.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"commit", "--repo", folder, "-m", "from folder"}); err != nil {
		t.Fatalf("commit (inferred branch) from folder: %v", err)
	}
	// commit with no branch arg, from the root, still requires a branch.
	if err := run([]string{"commit", "--repo", root, "-m", "x"}); err == nil {
		t.Fatal("commit with no branch from the root must require a branch")
	}
}

// TestVersionFlag covers the top-level --version/-v build-version flag (distinct
// from the `version` subcommand, which derives the repo's semver). It defaults
// to "dev" and is overridden at link time by GoReleaser.
func TestVersionFlag(t *testing.T) {
	if buildVersion != "dev" {
		t.Fatalf("buildVersion default = %q, want dev", buildVersion)
	}
	for _, flag := range []string{"--version", "-v"} {
		out := captureRun(t, flag)
		if want := "cairn dev\n"; out != want {
			t.Fatalf("run(%q) = %q, want %q", flag, out, want)
		}
	}
}
