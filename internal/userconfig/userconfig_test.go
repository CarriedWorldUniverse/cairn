package userconfig

import (
	"os"
	"path/filepath"
	"testing"
)

// isolate points os.UserConfigDir at a temp dir so the test never touches the
// real user config. On Linux/most-unix os.UserConfigDir honours XDG_CONFIG_HOME.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return dir
}

func TestGetUnsetReturnsEmpty(t *testing.T) {
	isolate(t)
	if got := Get("user.name"); got != "" {
		t.Fatalf("unset key = %q, want empty", got)
	}
}

func TestSetThenGet(t *testing.T) {
	dir := isolate(t)
	if err := Set("user.name", "Jane Dev"); err != nil {
		t.Fatal(err)
	}
	if got := Get("user.name"); got != "Jane Dev" {
		t.Fatalf("user.name = %q, want Jane Dev", got)
	}
	// File created under <configdir>/cairn/config.
	if _, err := os.Stat(filepath.Join(dir, "cairn", "config")); err != nil {
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
	dir := isolate(t)
	cfg := filepath.Join(dir, "cairn", "config")
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
