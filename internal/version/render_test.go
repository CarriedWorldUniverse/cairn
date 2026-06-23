package version

import "testing"

func TestRender(t *testing.T) {
	rel := Canonical{Major: 1, Minor: 4, Patch: 1}
	pre := Canonical{Major: 1, Minor: 4, Patch: 1, PreRelease: []string{"exp-idea", "5"}, Build: []string{"gabc1234"}}
	rc := Canonical{Major: 1, Minor: 4, Patch: 1, PreRelease: []string{"rc", "1"}}

	cases := []struct {
		v    Canonical
		eco  string
		want string
	}{
		{rel, "npm", "1.4.1"},
		{pre, "npm", "1.4.1-exp-idea.5+gabc1234"},
		{rel, "nuget", "1.4.1"},
		{rel, "pypi", "1.4.1"},
		{pre, "pypi", "1.4.1.dev5+gabc1234"},
		{rc, "pypi", "1.4.1rc1"},
		{pre, "oci", "1.4.1-exp-idea.5_gabc1234"},
		{rel, "go", "v1.4.1"},
		{pre, "go", "v1.4.1-exp-idea.5"},
	}
	for _, tc := range cases {
		got, err := Render(tc.v, tc.eco)
		if err != nil {
			t.Fatalf("Render(%v,%s): %v", tc.v, tc.eco, err)
		}
		if got != tc.want {
			t.Errorf("Render(%s) = %q, want %q", tc.eco, got, tc.want)
		}
	}
	if _, err := Render(rel, "bogus"); err == nil {
		t.Error("expected error for unknown ecosystem")
	}
}
