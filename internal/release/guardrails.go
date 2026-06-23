package release

import (
	"fmt"

	"github.com/CarriedWorldUniverse/cairn/internal/version"
)

// RegistryProbe reports whether name@version already exists on the eco registry.
// ExecProbe is the real implementation; tests inject a fake.
type RegistryProbe interface {
	Exists(eco, name, version string) (bool, error)
}

// ExecProbe is the production probe. In slice 1 it is a conservative no-op that
// reports not-exists; the orchestrator's tag-existence + monotonicity guards are
// the always-on protection. Per-registry network probes are wired in a follow-up.
type ExecProbe struct{}

func (ExecProbe) Exists(eco, name, ver string) (bool, error) { return false, nil }

// guardMonotonic fails unless newV is strictly greater (semver precedence) than
// latestTag. An empty latestTag (no prior release) always passes.
func guardMonotonic(newV version.Canonical, latestTag string) error {
	if latestTag == "" {
		return nil
	}
	b, err := version.Parse(latestTag)
	if err != nil {
		return fmt.Errorf("release.guardMonotonic: latest %q: %w", latestTag, err)
	}
	if version.Compare(newV, b) <= 0 {
		return fmt.Errorf("release: version %s is not greater than latest %s", newV.String(), latestTag)
	}
	return nil
}
