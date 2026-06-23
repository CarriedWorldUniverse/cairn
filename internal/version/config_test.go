package version

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig(t.TempDir()) // no cairn.version file
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TagPrefix != "v" || cfg.DefaultIncrement != "patch" {
		t.Fatalf("defaults wrong: %+v", cfg)
	}
}

func TestLoadConfigParsesAndValidates(t *testing.T) {
	dir := t.TempDir()
	yml := "tag-prefix: rel-\ndefault-increment: minor\necosystems:\n  npm: { manifest: package.json }\n"
	if err := os.WriteFile(filepath.Join(dir, "cairn.version"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TagPrefix != "rel-" || cfg.DefaultIncrement != "minor" {
		t.Fatalf("parse wrong: %+v", cfg)
	}
	if cfg.Ecosystems["npm"].Manifest != "package.json" {
		t.Fatalf("ecosystem manifest not parsed: %+v", cfg.Ecosystems)
	}
}

func TestLoadConfigRejectsBadIncrement(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cairn.version"), []byte("default-increment: huge\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(dir); err == nil {
		t.Error("expected validation error for bad increment")
	}
}
