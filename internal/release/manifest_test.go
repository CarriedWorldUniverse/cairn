package release

import (
	"strings"
	"testing"
)

func TestStampManifest(t *testing.T) {
	npm := `{
  "name": "x",
  "version": "0.0.0"
}`
	out, err := StampManifest("npm", []byte(npm), "1.4.1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"version": "1.4.1"`) {
		t.Fatalf("npm not stamped: %s", out)
	}

	csproj := "<Project>\n  <PropertyGroup>\n    <Version>0.0.0</Version>\n  </PropertyGroup>\n</Project>"
	out, err = StampManifest("nuget", []byte(csproj), "1.4.1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "<Version>1.4.1</Version>") {
		t.Fatalf("csproj not stamped: %s", out)
	}

	pyproject := "[project]\nname = \"x\"\nversion = \"0.0.0\"\n"
	out, err = StampManifest("pypi", []byte(pyproject), "1.4.1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `version = "1.4.1"`) {
		t.Fatalf("pyproject not stamped: %s", out)
	}
}

func TestStampManifestTagOnly(t *testing.T) {
	// oci/go have no manifest to stamp → src returned unchanged.
	for _, eco := range []string{"oci", "go"} {
		out, err := StampManifest(eco, []byte("whatever"), "1.4.1")
		if err != nil {
			t.Fatalf("%s: %v", eco, err)
		}
		if string(out) != "whatever" {
			t.Errorf("%s should pass through unchanged, got %s", eco, out)
		}
	}
}

func TestStampManifestMissingField(t *testing.T) {
	if _, err := StampManifest("npm", []byte(`{"name":"x"}`), "1.4.1"); err == nil {
		t.Error("expected error when version field is absent")
	}
	if _, err := StampManifest("bogus", []byte("x"), "1.4.1"); err == nil {
		t.Error("expected error for unknown ecosystem")
	}
}

func TestStampManifestReplacesOnlyFirst(t *testing.T) {
	npm := `{"version":"0.0.0","deps":{"version":"9.9.9"}}`
	out, err := StampManifest("npm", []byte(npm), "1.4.1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"version":"1.4.1"`) {
		t.Fatalf("top-level not stamped: %s", out)
	}
	if !strings.Contains(string(out), `"version":"9.9.9"`) {
		t.Fatalf("nested version must be untouched: %s", out)
	}
}
