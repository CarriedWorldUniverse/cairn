package main

import (
	"os"
	"path/filepath"
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
