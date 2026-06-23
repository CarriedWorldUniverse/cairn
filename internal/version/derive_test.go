package version

import "testing"

func di(base string, dist int, line string, trunk bool, lineDist int, bump, sha string) DeriveInput {
	return DeriveInput{BaseTag: base, Distance: dist, LineName: line, IsTrunk: trunk,
		LineDistance: lineDist, PendingBump: bump, ShortSHA: sha, Config: DefaultConfig()}
}

func TestReleaseVersion(t *testing.T) {
	v, err := ReleaseVersion(di("v1.4.0", 3, "main", true, 3, "", "abc"))
	if err != nil {
		t.Fatal(err)
	}
	if v.String() != "1.4.1" {
		t.Fatalf("got %q want 1.4.1 (clean, no build metadata)", v.String())
	}
	v2, _ := ReleaseVersion(di("v1.4.0", 2, "main", true, 2, "minor", "abc"))
	if v2.String() != "1.5.0" {
		t.Fatalf("got %q want 1.5.0", v2.String())
	}
}

func TestDeriveOnTagIsRelease(t *testing.T) {
	v, err := Derive(di("v1.4.0", 0, "main", true, 0, "", "abc1234"))
	if err != nil {
		t.Fatal(err)
	}
	if v.String() != "1.4.0" {
		t.Fatalf("on-tag = %q, want 1.4.0", v.String())
	}
}

func TestDeriveTrunkOffTag(t *testing.T) {
	v, err := Derive(di("v1.4.0", 3, "main", true, 3, "", "abc1234"))
	if err != nil {
		t.Fatal(err)
	}
	if v.Major != 1 || v.Minor != 4 || v.Patch != 1 || len(v.PreRelease) != 0 {
		t.Fatalf("trunk core wrong: %+v", v)
	}
	if v.String() != "1.4.1+3.gabc1234" {
		t.Fatalf("trunk off-tag = %q, want 1.4.1+3.gabc1234", v.String())
	}
}

func TestDeriveExperimentLine(t *testing.T) {
	v, err := Derive(di("v1.4.0", 6, "exp-idea", false, 5, "", "abc1234"))
	if err != nil {
		t.Fatal(err)
	}
	if v.String() != "1.4.1-exp-idea.5+gabc1234" {
		t.Fatalf("line = %q, want 1.4.1-exp-idea.5+gabc1234", v.String())
	}
}

func TestDerivePendingBumpOverridesDefault(t *testing.T) {
	v, err := Derive(di("v1.4.0", 2, "main", true, 2, "minor", "abc1234"))
	if err != nil {
		t.Fatal(err)
	}
	if v.Major != 1 || v.Minor != 5 || v.Patch != 0 {
		t.Fatalf("minor bump wrong: %+v", v)
	}
}

func TestDeriveTwoLinesNeverCollide(t *testing.T) {
	a, _ := Derive(di("v1.0.0", 2, "exp-a", false, 1, "", "aaa"))
	b, _ := Derive(di("v1.0.0", 2, "exp-b", false, 1, "", "bbb"))
	if a.String() == b.String() {
		t.Fatalf("two lines collided: %q", a.String())
	}
}

func TestDeriveNoBaseTag(t *testing.T) {
	v, err := Derive(di("", 1, "main", true, 1, "", "abc1234"))
	if err != nil {
		t.Fatal(err)
	}
	if v.Major != 0 || v.Minor != 0 || v.Patch != 1 {
		t.Fatalf("no-tag base wrong: %+v", v)
	}
}

func TestDeriveLabelSanitized(t *testing.T) {
	v, _ := Derive(di("v1.0.0", 2, "Feature/Big_Idea", false, 1, "", "abc"))
	if v.PreRelease[0] != "feature-big-idea" {
		t.Fatalf("label not sanitized: %+v", v.PreRelease)
	}
}

func TestDeriveDeterministic(t *testing.T) {
	in := di("v1.4.0", 3, "exp-x", false, 2, "", "deadbee")
	if Derive3(t, in) != Derive3(t, in) {
		t.Fatal("Derive not deterministic")
	}
}

func Derive3(t *testing.T, in DeriveInput) string {
	t.Helper()
	v, err := Derive(in)
	if err != nil {
		t.Fatal(err)
	}
	return v.String()
}
