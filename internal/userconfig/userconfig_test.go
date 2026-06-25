package userconfig

import (
	"os"
	"path/filepath"
	"testing"
)

// isolate redirects os.UserConfigDir at a temp dir so a test never touches the
// real user config. os.UserConfigDir reads a different env var per platform
// (XDG_CONFIG_HOME on Linux, HOME/Library/Application Support on macOS, AppData
// on Windows), so set all of them at the temp root to cover every runner.
func isolate(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("AppData", dir)
}

// configPath returns the isolated config file path via the package's own
// resolver, so assertions never hard-code a platform's directory layout.
func configPath(t *testing.T) string {
	t.Helper()
	p, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	return p
}

func TestGetUnsetReturnsEmpty(t *testing.T) {
	isolate(t)
	if got := Get("user.name"); got != "" {
		t.Fatalf("unset key = %q, want empty", got)
	}
}

func TestSetThenGet(t *testing.T) {
	isolate(t)
	if err := Set("user.name", "Jane Dev"); err != nil {
		t.Fatal(err)
	}
	if got := Get("user.name"); got != "Jane Dev" {
		t.Fatalf("user.name = %q, want Jane Dev", got)
	}
	if _, err := os.Stat(configPath(t)); err != nil {
		t.Fatalf("config file not created: %v", err)
	}
}

func TestSetPreservesOtherKeys(t *testing.T) {
	isolate(t)
	if err := Set("user.name", "Jane Dev"); err != nil {
		t.Fatal(err)
	}
	if err := Set("user.email", "jane@example.com"); err != nil {
		t.Fatal(err)
	}
	if got := Get("user.name"); got != "Jane Dev" {
		t.Fatalf("user.name clobbered by second Set: %q", got)
	}
	if got := Get("user.email"); got != "jane@example.com" {
		t.Fatalf("user.email = %q", got)
	}
}

func TestSetOverwritesExistingKey(t *testing.T) {
	isolate(t)
	_ = Set("user.name", "Old Name")
	_ = Set("user.name", "New Name")
	if got := Get("user.name"); got != "New Name" {
		t.Fatalf("user.name = %q, want New Name", got)
	}
}

func TestLoadIgnoresCommentsAndBlanks(t *testing.T) {
	isolate(t)
	cfg := configPath(t)
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "# a comment\n\nuser.name = Spaced Out  \n  user.email = e@x.io\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Get("user.name"); got != "Spaced Out" {
		t.Fatalf("user.name = %q, want trimmed 'Spaced Out'", got)
	}
	if got := Get("user.email"); got != "e@x.io" {
		t.Fatalf("user.email = %q", got)
	}
}
