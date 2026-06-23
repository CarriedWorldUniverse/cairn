package release

import (
	"errors"
	"testing"
)

type fakeRepo struct {
	dirty       bool
	latestTag   string
	manifest    []byte
	tagged      string
	bumpCleared bool
}

func (f *fakeRepo) Dirty() (bool, error)       { return f.dirty, nil }
func (f *fakeRepo) LatestTag() (string, error) { return f.latestTag, nil }
func (f *fakeRepo) ReadManifest(eco string) ([]byte, string, error) {
	return f.manifest, "manifest." + eco, nil
}
func (f *fakeRepo) WriteManifest(path string, b []byte) error { f.manifest = b; return nil }
func (f *fakeRepo) CreateTag(name string) error               { f.tagged = name; return nil }
func (f *fakeRepo) DeleteTag(name string) error               { f.tagged = ""; return nil }
func (f *fakeRepo) ClearPendingBump() error                   { f.bumpCleared = true; return nil }

type fakePub struct {
	called bool
	fail   bool
}

func (p *fakePub) Publish(eco, dir, version string) error {
	p.called = true
	if p.fail {
		return errors.New("publish boom")
	}
	return nil
}

type okProbe struct{}

func (okProbe) Exists(eco, name, version string) (bool, error) { return false, nil }

func opts() Options {
	return Options{Eco: "npm", Version: "1.4.1", TagName: "v1.4.1", Dir: "/repo"}
}

func TestReleaseSuccess(t *testing.T) {
	fr := &fakeRepo{manifest: []byte(`{"version":"0.0.0"}`)}
	pub := &fakePub{}
	if err := Release(opts(), fr, pub, okProbe{}); err != nil {
		t.Fatal(err)
	}
	if !pub.called || fr.tagged != "v1.4.1" || !fr.bumpCleared {
		t.Fatalf("incomplete: pub=%v tag=%q cleared=%v", pub.called, fr.tagged, fr.bumpCleared)
	}
	if string(fr.manifest) != `{"version":"1.4.1"}` {
		t.Fatalf("manifest not stamped: %s", fr.manifest)
	}
}

func TestReleaseRollbackOnPublishFailure(t *testing.T) {
	fr := &fakeRepo{manifest: []byte(`{"version":"0.0.0"}`)}
	pub := &fakePub{fail: true}
	if err := Release(opts(), fr, pub, okProbe{}); err == nil {
		t.Fatal("expected publish failure")
	}
	if fr.tagged != "" {
		t.Errorf("tag not rolled back: %q", fr.tagged)
	}
	if string(fr.manifest) != `{"version":"0.0.0"}` {
		t.Errorf("manifest not rolled back: %s", fr.manifest)
	}
	if fr.bumpCleared {
		t.Error("pending bump must NOT be cleared on failure")
	}
}

func TestReleaseDirtyTreeRefused(t *testing.T) {
	fr := &fakeRepo{dirty: true, manifest: []byte(`{"version":"0.0.0"}`)}
	pub := &fakePub{}
	if err := Release(opts(), fr, pub, okProbe{}); err == nil {
		t.Fatal("dirty tree must be refused")
	}
	if pub.called {
		t.Error("must not publish a dirty tree")
	}
}

func TestReleaseNonMonotonicRefused(t *testing.T) {
	fr := &fakeRepo{latestTag: "v1.4.1", manifest: []byte(`{"version":"0.0.0"}`)}
	pub := &fakePub{}
	if err := Release(opts(), fr, pub, okProbe{}); err == nil {
		t.Fatal("non-monotonic version must be refused")
	}
	if pub.called {
		t.Error("must not publish below the latest tag")
	}
}

func TestReleaseExistingOnRegistryRefused(t *testing.T) {
	fr := &fakeRepo{manifest: []byte(`{"version":"0.0.0"}`)}
	pub := &fakePub{}
	existsProbe := probeFunc(func(eco, name, version string) (bool, error) { return true, nil })
	if err := Release(opts(), fr, pub, existsProbe); err == nil {
		t.Fatal("already-on-registry must be refused")
	}
	if pub.called {
		t.Error("must not publish an existing version")
	}
}

func TestPlanDryRunNoMutation(t *testing.T) {
	fr := &fakeRepo{manifest: []byte(`{"version":"0.0.0"}`)}
	plan, err := Plan(opts(), fr, okProbe{})
	if err != nil {
		t.Fatal(err)
	}
	if plan == "" {
		t.Error("plan should be non-empty")
	}
	if fr.tagged != "" || string(fr.manifest) != `{"version":"0.0.0"}` {
		t.Error("dry-run must not mutate")
	}
}

type probeFunc func(eco, name, version string) (bool, error)

func (f probeFunc) Exists(eco, name, version string) (bool, error) { return f(eco, name, version) }
