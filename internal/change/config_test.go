package change

import "testing"

func TestConfigRoundTrip(t *testing.T) {
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if _, ok, _ := e.GetConfig("autosync"); ok {
		t.Fatal("autosync should be unset initially")
	}
	if err := e.SetConfig("autosync", "true"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	v, ok, err := e.GetConfig("autosync")
	if err != nil || !ok || v != "true" {
		t.Fatalf("GetConfig = %q,%v,%v", v, ok, err)
	}
	if err := e.SetConfig("autosync", "false"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if v, _, _ := e.GetConfig("autosync"); v != "false" {
		t.Fatalf("after update = %q", v)
	}
}

func TestConfigTruthy(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"true", true}, {"TRUE", true}, {"1", true}, {"on", true}, {"ON", true}, {" on ", true},
		{"false", false}, {"", false}, {"yes", false}, {"0", false}, {"off", false},
	} {
		if got := ConfigTruthy(tc.in); got != tc.want {
			t.Errorf("ConfigTruthy(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
