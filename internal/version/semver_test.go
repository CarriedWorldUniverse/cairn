package version

import "testing"

func TestCanonicalString(t *testing.T) {
	for _, tc := range []struct {
		v    Canonical
		want string
	}{
		{Canonical{Major: 1, Minor: 4, Patch: 1}, "1.4.1"},
		{Canonical{Major: 1, Minor: 4, Patch: 1, PreRelease: []string{"exp-idea", "5"}}, "1.4.1-exp-idea.5"},
		{Canonical{Major: 1, Minor: 4, Patch: 1, Build: []string{"5", "g1a2b3c4"}}, "1.4.1+5.g1a2b3c4"},
		{Canonical{Major: 0, Minor: 0, Patch: 1, PreRelease: []string{"x", "2"}, Build: []string{"gabc"}}, "0.0.1-x.2+gabc"},
	} {
		if got := tc.v.String(); got != tc.want {
			t.Errorf("String() = %q, want %q", got, tc.want)
		}
	}
}

func TestParseCanonical(t *testing.T) {
	v, err := Parse("v1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if v.Major != 1 || v.Minor != 4 || v.Patch != 0 {
		t.Fatalf("got %+v", v)
	}
	if _, err := Parse("not.a.version"); err == nil {
		t.Error("expected error for bad version")
	}
}

func TestComparePrecedence(t *testing.T) {
	mk := func(s string) Canonical { v, _ := Parse(s); return v }
	cases := []struct {
		a, b string
		sign int
	}{
		{"1.4.1", "1.4.0", 1},
		{"1.4.1", "1.4.1", 0},
		{"1.4.1-exp.5", "1.4.1", -1},
		{"1.4.1-exp.5", "1.4.1-exp.4", 1},
		{"1.4.1+aaa", "1.4.1+bbb", 0},
		{"2.0.0", "1.9.9", 1},
		{"1.0.0-1", "1.0.0-alpha", -1},
	}
	for _, tc := range cases {
		got := Compare(mk(tc.a), mk(tc.b))
		if sign(got) != tc.sign {
			t.Errorf("Compare(%q,%q)=%d, want sign %d", tc.a, tc.b, got, tc.sign)
		}
	}
}

func TestParseStrict(t *testing.T) {
	for _, bad := range []string{"1.04.0", "01.2.3", "1.2.3-", "1.2.3+", "1.2", "1.2.3.4", "x.y.z"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) should error", bad)
		}
	}
	for _, good := range []string{"1.4.1", "1.4.1-exp-idea.5", "1.4.1+5.gabc", "0.0.1-x.2+gabc"} {
		v, err := Parse(good)
		if err != nil {
			t.Fatalf("Parse(%q): %v", good, err)
		}
		if v.String() != good {
			t.Errorf("round-trip %q -> %q", good, v.String())
		}
	}
}

func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	default:
		return 0
	}
}
