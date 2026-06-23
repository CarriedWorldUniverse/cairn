package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mustRunOut runs run(args) with os.Stdout redirected to a pipe and returns
// the captured stdout. The test fails if run returns an error.
func mustRunOut(t *testing.T, args ...string) string {
	t.Helper()
	old := os.Stdout
	rp, wp, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = wp
	runErr := run(args)
	wp.Close()
	os.Stdout = old
	var sb strings.Builder
	if _, err := io.Copy(&sb, rp); err != nil {
		t.Fatalf("read stdout pipe: %v", err)
	}
	if runErr != nil {
		t.Fatalf("run(%v): %v", args, runErr)
	}
	return sb.String()
}

func TestE2E_Version(t *testing.T) {
	skipOnWindows(t)
	dir := filepath.Join(t.TempDir(), "repo")
	mustRun(t, "init", dir)
	def := soleExpressedDir(t, dir)

	if err := os.WriteFile(filepath.Join(dir, def, "a.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", dir, def)
	// tag the tip of the default branch as v1.0.0
	mustRun(t, "tag", "--repo", dir, "v1.0.0")

	// on the tag: version should be exactly 1.0.0
	out := mustRunOut(t, "version", "--repo", dir)
	if strings.TrimSpace(out) != "1.0.0" {
		t.Fatalf("version on tag = %q, want 1.0.0", out)
	}

	// one commit ahead of the tag: version should be 1.0.1+<dist>.g<sha>
	if err := os.WriteFile(filepath.Join(dir, def, "b.txt"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", dir, def)
	out = mustRunOut(t, "version", "--repo", dir)
	if !strings.HasPrefix(strings.TrimSpace(out), "1.0.1+") {
		t.Fatalf("trunk off-tag version = %q, want 1.0.1+...", out)
	}

	// pypi render should produce a PEP 440 compatible version starting with 1.0.1
	out = mustRunOut(t, "version", "--repo", dir, "--target", "pypi")
	if !strings.HasPrefix(strings.TrimSpace(out), "1.0.1") {
		t.Fatalf("pypi version = %q, want 1.0.1...", out)
	}

	// bump minor: next derived version should start with 1.1.0
	mustRun(t, "version", "bump", "minor", "--repo", dir)
	out = mustRunOut(t, "version", "--repo", dir)
	if !strings.HasPrefix(strings.TrimSpace(out), "1.1.0") {
		t.Fatalf("after minor bump = %q, want 1.1.0...", out)
	}
}
