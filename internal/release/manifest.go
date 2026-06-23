// Package release performs atomic cairn releases: stamp manifests, tag, and
// publish (publish last, the only irreversible step). External effects sit behind
// the Publisher and RegistryProbe interfaces so the package is testable offline.
package release

import (
	"fmt"
	"regexp"
)

var (
	reNpmVersion    = regexp.MustCompile(`("version"\s*:\s*)"[^"]*"`)
	reCsprojVersion = regexp.MustCompile(`(<Version>)[^<]*(</Version>)`)
	rePyVersion     = regexp.MustCompile(`(?m)^(version\s*=\s*)"[^"]*"`)
)

// StampManifest replaces the version field in a manifest's bytes for the given
// ecosystem, returning the new bytes. It does not write to disk. oci/go have no
// manifest and return src unchanged.
func StampManifest(eco string, src []byte, version string) ([]byte, error) {
	switch eco {
	case "npm":
		return replaceOne(reNpmVersion, src, `${1}"`+version+`"`, "npm package.json version")
	case "nuget":
		return replaceOne(reCsprojVersion, src, `${1}`+version+`${2}`, "csproj <Version>")
	case "pypi":
		return replaceOne(rePyVersion, src, `${1}"`+version+`"`, "pyproject version")
	case "oci", "go":
		return src, nil
	default:
		return nil, fmt.Errorf("release.StampManifest: unknown ecosystem %q", eco)
	}
}

func replaceOne(re *regexp.Regexp, src []byte, repl, what string) ([]byte, error) {
	loc := re.FindSubmatchIndex(src)
	if loc == nil {
		return nil, fmt.Errorf("release.StampManifest: %s not found", what)
	}
	out := append([]byte(nil), src[:loc[0]]...)
	out = re.Expand(out, []byte(repl), src, loc)
	out = append(out, src[loc[1]:]...)
	return out, nil
}
