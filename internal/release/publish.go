package release

import (
	"fmt"
	"os/exec"
)

// Publisher publishes the artifact in dir to the eco registry. ExecPublisher is
// the real implementation; tests inject a fake.
type Publisher interface {
	Publish(eco, dir, version string) error
}

// ExecPublisher runs the ecosystem's native publish command.
type ExecPublisher struct{}

func (ExecPublisher) Publish(eco, dir, version string) error {
	argv, err := publishArgv(eco, dir, version)
	if err != nil {
		return err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("release.Publish %s: %w: %s", eco, err, out)
	}
	return nil
}

// publishArgv builds the native publish command for an ecosystem.
func publishArgv(eco, dir, version string) ([]string, error) {
	switch eco {
	case "npm":
		return []string{"npm", "publish"}, nil
	case "pypi":
		return []string{"python", "-m", "twine", "upload", "dist/*"}, nil
	case "nuget":
		return []string{"dotnet", "nuget", "push", fmt.Sprintf("bin/Release/*.%s.nupkg", version)}, nil
	case "oci":
		return []string{"docker", "push", version}, nil // version = full image ref for oci
	default:
		return nil, fmt.Errorf("release.publishArgv: unknown ecosystem %q", eco)
	}
}
