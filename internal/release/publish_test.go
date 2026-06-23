package release

import "testing"

func TestPublishArgv(t *testing.T) {
	for _, tc := range []struct {
		eco   string
		want0 string
		want1 string
	}{
		{"npm", "npm", "publish"},
		{"pypi", "python", "-m"},
		{"nuget", "dotnet", "nuget"},
		{"oci", "docker", "push"},
	} {
		argv, err := publishArgv(tc.eco, "/repo", "1.4.1")
		if err != nil {
			t.Fatal(err)
		}
		if argv[0] != tc.want0 || argv[1] != tc.want1 {
			t.Errorf("%s argv = %v, want prefix [%s %s]", tc.eco, argv, tc.want0, tc.want1)
		}
	}
	if _, err := publishArgv("bogus", "/repo", "1.4.1"); err == nil {
		t.Error("expected error for unknown ecosystem")
	}
}

func TestGuardMonotonic(t *testing.T) {
	if err := guardMonotonic("1.4.1", "1.4.0"); err != nil {
		t.Errorf("1.4.1 > 1.4.0 should pass: %v", err)
	}
	if err := guardMonotonic("1.4.0", "1.4.0"); err == nil {
		t.Error("equal version should fail monotonicity")
	}
	if err := guardMonotonic("1.3.0", "1.4.0"); err == nil {
		t.Error("lower version should fail monotonicity")
	}
	if err := guardMonotonic("1.0.0", ""); err != nil {
		t.Errorf("no prior tag should pass: %v", err)
	}
}

func TestExecProbeDefaultsFalse(t *testing.T) {
	// Slice 1 ExecProbe is a no-op probe (the tag-existence guard in the
	// orchestrator is the always-on protection). It must not error or hit network.
	ok, err := ExecProbe{}.Exists("npm", "x", "1.4.1")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("ExecProbe should report not-exists in slice 1")
	}
}
