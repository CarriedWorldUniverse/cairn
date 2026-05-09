package cairn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigPaths_FromXDGConfigHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", "/should/not/be/used")

	c, err := ResolvePaths("https://cairn.example.com")
	if err != nil {
		t.Fatal(err)
	}

	wantHostDir := filepath.Join(dir, "cairn", "cairn.example.com")
	if c.HostDir != wantHostDir {
		t.Errorf("HostDir = %q, want %q", c.HostDir, wantHostDir)
	}
	if c.SeedFile != filepath.Join(dir, "cairn", "seed") {
		t.Errorf("SeedFile = %q, want under cairn root", c.SeedFile)
	}
	if c.TokenFile != filepath.Join(c.HostDir, "token") {
		t.Errorf("TokenFile = %q, want HostDir/token", c.TokenFile)
	}
}

func TestConfigPaths_FromHOMEFallback(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", dir)

	c, err := ResolvePaths("https://cairn.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.HostDir, filepath.Join(dir, ".config", "cairn")) {
		t.Errorf("HostDir = %q, want under HOME/.config/cairn", c.HostDir)
	}
}

func TestConfigPaths_RejectsEmptyURL(t *testing.T) {
	_, err := ResolvePaths("")
	if err == nil {
		t.Error("expected error for empty URL")
	}
}

func TestConfigPaths_RejectsMalformedURL(t *testing.T) {
	_, err := ResolvePaths("://not-a-url")
	if err == nil {
		t.Error("expected error for malformed URL")
	}
}

func TestConfigPaths_KeyFilePathBySlug(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	c, _ := ResolvePaths("https://cairn.example.com")
	got := c.KeyFile("plumb")
	want := filepath.Join(c.HostDir, "plumb.key")
	if got != want {
		t.Errorf("KeyFile = %q, want %q", got, want)
	}
}

func TestEnsureHostDir_Creates0700(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	c, _ := ResolvePaths("https://cairn.example.com")
	if err := c.EnsureHostDir(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(c.HostDir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0700 {
		t.Errorf("perm = %#o, want 0700", perm)
	}
}
