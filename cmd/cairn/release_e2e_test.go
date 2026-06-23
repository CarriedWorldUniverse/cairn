package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/internal/release"
)

type capturePub struct{ called bool }

func (p *capturePub) Publish(eco, dir, version string) error { p.called = true; return nil }

type relOKProbe struct{}

func (relOKProbe) Exists(eco, name, version string) (bool, error) { return false, nil }

func TestE2E_ReleaseDryRun(t *testing.T) {
	skipOnWindows(t)
	dir := filepath.Join(t.TempDir(), "repo")
	mustRun(t, "init", dir)
	def := soleExpressedDir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, def, "package.json"), []byte(`{"version":"0.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", dir, def)
	out := mustRunOut(t, "release", "--repo", dir, "--target", "npm", "--dry-run")
	if !strings.Contains(out, "release plan") || !strings.Contains(out, "publish: npm publish") {
		t.Fatalf("dry-run plan missing: %s", out)
	}
	if !strings.Contains(out, "0.0.1") {
		t.Fatalf("expected derived 0.0.1 in plan: %s", out)
	}
}

func TestE2E_ReleaseSuccessAndDoubleRefused(t *testing.T) {
	skipOnWindows(t)
	fp := &capturePub{}
	oldPub, oldProbe := newPublisher, newProbe
	newPublisher = func() release.Publisher { return fp }
	newProbe = func() release.RegistryProbe { return relOKProbe{} }
	t.Cleanup(func() { newPublisher, newProbe = oldPub, oldProbe })

	dir := filepath.Join(t.TempDir(), "repo")
	mustRun(t, "init", dir)
	def := soleExpressedDir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, def, "package.json"), []byte(`{"version":"0.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", dir, def)
	mustRun(t, "release", "--repo", dir, "--target", "npm")
	if !fp.called {
		t.Fatal("publisher was not called")
	}
	// A second release now fails a guardrail (tag exists and/or tree dirty from the stamp).
	if err := run([]string{"release", "--repo", dir, "--target", "npm"}); err == nil {
		t.Fatal("a second immediate release should be refused")
	}
}
