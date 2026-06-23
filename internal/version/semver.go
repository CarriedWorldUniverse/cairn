// Package version derives deterministic semantic versions from the cairn
// change-graph and renders them per packaging ecosystem. Pure: the only I/O is
// reading the cairn.version config file (config.go).
package version

import (
	"fmt"
	"strconv"
	"strings"
)

// Canonical is one logical semantic version. PreRelease and Build are dot-joined
// identifier lists (semver2). Build metadata never affects precedence.
type Canonical struct {
	Major, Minor, Patch int
	PreRelease          []string
	Build               []string
}

func (v Canonical) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.PreRelease) > 0 {
		s += "-" + strings.Join(v.PreRelease, ".")
	}
	if len(v.Build) > 0 {
		s += "+" + strings.Join(v.Build, ".")
	}
	return s
}

// Parse reads a semver core (with optional "v" prefix, pre-release, build). It is
// lenient on the prefix and strict on the X.Y.Z core.
func Parse(s string) (Canonical, error) {
	orig := s
	s = strings.TrimPrefix(s, "v")
	var v Canonical
	if i := strings.IndexByte(s, '+'); i >= 0 {
		if s[i+1:] == "" {
			return Canonical{}, fmt.Errorf("version.Parse: %q has empty build metadata", orig)
		}
		v.Build = strings.Split(s[i+1:], ".")
		s = s[:i]
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		if s[i+1:] == "" {
			return Canonical{}, fmt.Errorf("version.Parse: %q has empty pre-release", orig)
		}
		v.PreRelease = strings.Split(s[i+1:], ".")
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Canonical{}, fmt.Errorf("version.Parse: %q is not MAJOR.MINOR.PATCH", orig)
	}
	var err error
	if v.Major, err = atoi(parts[0]); err != nil {
		return Canonical{}, fmt.Errorf("version.Parse %q: %w", orig, err)
	}
	if v.Minor, err = atoi(parts[1]); err != nil {
		return Canonical{}, fmt.Errorf("version.Parse %q: %w", orig, err)
	}
	if v.Patch, err = atoi(parts[2]); err != nil {
		return Canonical{}, fmt.Errorf("version.Parse %q: %w", orig, err)
	}
	return v, nil
}

func atoi(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("empty numeric segment")
	}
	if len(s) > 1 && s[0] == '0' {
		return 0, fmt.Errorf("numeric segment %q has a leading zero", s)
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid numeric segment %q", s)
	}
	return n, nil
}

// Compare returns -1/0/1 by semver2 precedence. Build metadata is ignored; a
// pre-release sorts below the same core release; pre-release identifiers compare
// numeric<numeric numerically, otherwise lexically, numeric < alphanumeric.
func Compare(a, b Canonical) int {
	if c := cmpInt(a.Major, b.Major); c != 0 {
		return c
	}
	if c := cmpInt(a.Minor, b.Minor); c != 0 {
		return c
	}
	if c := cmpInt(a.Patch, b.Patch); c != 0 {
		return c
	}
	if len(a.PreRelease) == 0 && len(b.PreRelease) == 0 {
		return 0
	}
	if len(a.PreRelease) == 0 {
		return 1
	}
	if len(b.PreRelease) == 0 {
		return -1
	}
	return cmpIdents(a.PreRelease, b.PreRelease)
}

func cmpIdents(a, b []string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		an, aerr := strconv.Atoi(a[i])
		bn, berr := strconv.Atoi(b[i])
		switch {
		case aerr == nil && berr == nil:
			if c := cmpInt(an, bn); c != 0 {
				return c
			}
		case aerr == nil:
			return -1
		case berr == nil:
			return 1
		default:
			if c := strings.Compare(a[i], b[i]); c != 0 {
				return c
			}
		}
	}
	return cmpInt(len(a), len(b))
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
