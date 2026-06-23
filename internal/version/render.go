package version

import (
	"fmt"
	"strings"
)

// Render maps the canonical version to a packaging ecosystem's version string.
func Render(v Canonical, eco string) (string, error) {
	switch eco {
	case "npm", "nuget", "":
		return v.String(), nil // semver2
	case "oci":
		return strings.ReplaceAll(v.String(), "+", "_"), nil // OCI tags forbid '+'
	case "go":
		core := Canonical{Major: v.Major, Minor: v.Minor, Patch: v.Patch, PreRelease: v.PreRelease}
		return "v" + core.String(), nil // build metadata dropped
	case "pypi":
		return renderPEP440(v), nil
	default:
		return "", fmt.Errorf("version.Render: unknown ecosystem %q", eco)
	}
}

// renderPEP440 maps to PEP 440. A pre-release whose first identifier is an
// rc/alpha/beta stage renders as that stage; any other pre-release renders as a
// dev release using its last numeric identifier; build metadata becomes a local
// segment.
func renderPEP440(v Canonical) string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.PreRelease) > 0 {
		stage := strings.ToLower(v.PreRelease[0])
		num := lastNumeric(v.PreRelease)
		switch stage {
		case "rc", "a", "alpha", "b", "beta":
			canon := map[string]string{"alpha": "a", "beta": "b"}
			if c, ok := canon[stage]; ok {
				stage = c
			}
			s += fmt.Sprintf("%s%d", stage, num)
		default:
			s += fmt.Sprintf(".dev%d", num)
		}
	}
	if len(v.Build) > 0 {
		local := strings.Join(v.Build, ".")
		local = strings.ToLower(strings.ReplaceAll(local, "_", "."))
		s += "+" + local
	}
	return s
}

func lastNumeric(parts []string) int {
	for i := len(parts) - 1; i >= 0; i-- {
		var n int
		if _, err := fmt.Sscanf(parts[i], "%d", &n); err == nil {
			return n
		}
	}
	return 0
}
