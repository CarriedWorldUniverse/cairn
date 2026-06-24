package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mustWrite creates the file at rel inside dir, creating parent directories as needed.
func mustWrite(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mustWrite MkdirAll %q: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("mustWrite WriteFile %q: %v", rel, err)
	}
}

func TestScanRespectsGitignore(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, ".gitignore", "node_modules/\n*.log\n.env\n")
	mustWrite(t, dir, "keep.go", "package x\n")
	mustWrite(t, dir, ".env", "SECRET=1\n")
	mustWrite(t, dir, "debug.log", "noise\n")
	mustWrite(t, dir, "node_modules/dep/index.js", "x\n")
	mustWrite(t, dir, "src/app.go", "package app\n")
	files, _, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{".gitignore": true, "keep.go": true, "src/app.go": true}
	for p := range files {
		if !want[p] {
			t.Errorf("scanned an ignored/unexpected path: %q", p)
		}
	}
	for p := range want {
		if _, ok := files[p]; !ok {
			t.Errorf("missing expected path: %q", p)
		}
	}
	if _, ok := files["node_modules/dep/index.js"]; ok {
		t.Error("node_modules not ignored")
	}
	if _, ok := files[".env"]; ok {
		t.Error(".env not ignored")
	}
	if _, ok := files["debug.log"]; ok {
		t.Error("*.log not ignored")
	}
}

func TestScanSkipsGitAndCairnDirs(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "a.txt", "1\n")
	mustWrite(t, dir, ".git/config", "[core]\n")
	mustWrite(t, dir, ".cairn/wc.json", "{}\n")
	mustWrite(t, dir, "sub/.git/HEAD", "ref\n")
	files, _, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	for p := range files {
		if strings.Contains(p, ".git/") || strings.Contains(p, ".cairn/") {
			t.Errorf("scanned a VCS-internal path: %q", p)
		}
	}
	if _, ok := files["a.txt"]; !ok {
		t.Error("missing a.txt")
	}
}

func TestScanCairnignore(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, ".cairnignore", "secret.key\n")
	mustWrite(t, dir, "secret.key", "k\n")
	mustWrite(t, dir, "ok.txt", "1\n")
	files, _, _ := Scan(dir)
	if _, ok := files["secret.key"]; ok {
		t.Error(".cairnignore not honored")
	}
	if _, ok := files["ok.txt"]; !ok {
		t.Error("ok.txt missing")
	}
}
