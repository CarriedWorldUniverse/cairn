package version

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// DeriveInput is the full set of facts the pure derivation needs. The caller
// gathers BaseTag/Distance/LineDistance from the change-graph; Derive performs no
// I/O.
type DeriveInput struct {
	BaseTag      string // nearest reachable tag, e.g. "v1.4.0" ("" if none)
	Distance     int    // commits from target back to BaseTag's commit (0 = on the tag)
	LineName     string // line being versioned
	IsTrunk      bool   // target line is the structural root
	LineDistance int    // commits since the line's branch point
	PendingBump  string // "major"|"minor"|"patch"|"" (explicit; else config default)
	ShortSHA     string // target commit short sha (build metadata)
	Config       Config
}

// Derive computes the canonical version. Deterministic: same input → same output.
func Derive(in DeriveInput) (Canonical, error) {
	base := Canonical{} // 0.0.0 when no tag
	if in.BaseTag != "" {
		parsed, err := Parse(strings.TrimPrefix(in.BaseTag, in.Config.TagPrefix))
		if err != nil {
			return Canonical{}, fmt.Errorf("version.Derive: base tag: %w", err)
		}
		base = Canonical{Major: parsed.Major, Minor: parsed.Minor, Patch: parsed.Patch}
	}

	// On the tagged commit itself: the release version, verbatim core.
	if in.Distance == 0 && in.BaseTag != "" {
		return base, nil
	}

	bump := in.PendingBump
	if bump == "" {
		bump = in.Config.DefaultIncrement
	}
	core, err := applyBump(base, bump)
	if err != nil {
		return Canonical{}, err
	}

	if in.IsTrunk {
		core.Build = []string{strconv.Itoa(in.Distance), "g" + in.ShortSHA}
		return core, nil
	}
	core.PreRelease = []string{sanitizeLabel(in.LineName), strconv.Itoa(in.LineDistance)}
	core.Build = []string{"g" + in.ShortSHA}
	return core, nil
}

func applyBump(b Canonical, bump string) (Canonical, error) {
	switch bump {
	case "major":
		return Canonical{Major: b.Major + 1}, nil
	case "minor":
		return Canonical{Major: b.Major, Minor: b.Minor + 1}, nil
	case "patch":
		return Canonical{Major: b.Major, Minor: b.Minor, Patch: b.Patch + 1}, nil
	default:
		return Canonical{}, fmt.Errorf("version.Derive: invalid bump %q", bump)
	}
}

var labelStrip = regexp.MustCompile(`[^0-9a-z-]+`)

// sanitizeLabel turns a line name into a valid semver pre-release identifier.
func sanitizeLabel(name string) string {
	s := strings.ToLower(name)
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, "_", "-")
	s = labelStrip.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "line"
	}
	return s
}
