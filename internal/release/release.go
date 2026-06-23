package release

import (
	"fmt"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/version"
)

// RepoPort is the repo capability surface Release needs. worktree.Repo provides a
// concrete adapter (Task 11); tests use a fake.
type RepoPort interface {
	Dirty() (bool, error)
	LatestTag() (string, error)
	ReadManifest(eco string) (content []byte, path string, err error)
	WriteManifest(path string, content []byte) error
	TagExists(name string) (bool, error)
	CreateTag(name string) error
	DeleteTag(name string) error
	ClearPendingBump() error
}

type Options struct {
	Eco     string            // npm|nuget|pypi|oci
	Version string            // rendered version for Eco
	Core    version.Canonical // canonical version core (ecosystem-agnostic; used for monotonicity)
	TagName string            // e.g. "v1.4.1"
	Name    string            // package name (registry-exists probe); optional
	Dir     string            // publish working dir
}

// runGuards runs all three guardrails (dirty / monotonic / already-exists).
func runGuards(o Options, repo RepoPort, probe RegistryProbe) error {
	dirty, err := repo.Dirty()
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("release: working tree has uncommitted changes")
	}
	latestTag, err := repo.LatestTag()
	if err != nil {
		return err
	}
	if err := guardMonotonic(o.Core, latestTag); err != nil {
		return err
	}
	tagged, err := repo.TagExists(o.TagName)
	if err != nil {
		return err
	}
	if tagged {
		return fmt.Errorf("release: version %s is already tagged (%s)", o.Version, o.TagName)
	}
	exists, err := probe.Exists(o.Eco, o.Name, o.Version)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("release: %s %s already exists on the %s registry", o.Name, o.Version, o.Eco)
	}
	return nil
}

// Plan validates and returns a human-readable dry-run plan without mutating.
func Plan(o Options, repo RepoPort, probe RegistryProbe) (string, error) {
	if err := runGuards(o, repo, probe); err != nil {
		return "", err
	}
	content, path, err := repo.ReadManifest(o.Eco)
	if err != nil {
		return "", err
	}
	stamped := "(tag-only ecosystem, no manifest)"
	if len(content) > 0 {
		stamped = path
	}
	pubStep := "(tag-only ecosystem, no publish command)"
	if argv, perr := publishArgv(o.Eco, o.Dir, o.Version); perr == nil {
		pubStep = strings.Join(argv, " ")
	}
	return fmt.Sprintf("release plan:\n  version: %s\n  tag:     %s\n  manifest: %s\n  publish: %s",
		o.Version, o.TagName, stamped, pubStep), nil
}

// Release performs the atomic release. Publish is last (the only irreversible
// step); any failure before it rolls back manifest + tag.
func Release(o Options, repo RepoPort, pub Publisher, probe RegistryProbe) error {
	if err := runGuards(o, repo, probe); err != nil {
		return err
	}

	// 1. Stamp the manifest (reversible — keep original bytes).
	content, path, err := repo.ReadManifest(o.Eco)
	if err != nil {
		return err
	}
	original := append([]byte(nil), content...)
	manifestWritten := false
	if len(content) > 0 {
		stamped, err := StampManifest(o.Eco, content, o.Version)
		if err != nil {
			return err
		}
		if err := repo.WriteManifest(path, stamped); err != nil {
			return err
		}
		manifestWritten = true
	}
	rollback := func() {
		if manifestWritten {
			_ = repo.WriteManifest(path, original)
		}
	}

	// 2. Tag (reversible).
	if err := repo.CreateTag(o.TagName); err != nil {
		rollback()
		return err
	}

	// 3. Publish — last, irreversible.
	if err := pub.Publish(o.Eco, o.Dir, o.Version); err != nil {
		delErr := repo.DeleteTag(o.TagName)
		rollback()
		if delErr != nil {
			return fmt.Errorf("release: publish failed AND tag cleanup failed (%v); manually remove tag %s: %w", delErr, o.TagName, err)
		}
		return fmt.Errorf("release: publish failed, rolled back tag+manifest: %w", err)
	}

	return repo.ClearPendingBump()
}
