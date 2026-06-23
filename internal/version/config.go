package version

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config drives derivation + rendering. All fields are optional; LoadConfig
// supplies DefaultConfig values when the file or a field is absent.
type Config struct {
	TagPrefix        string            `yaml:"tag-prefix"`
	DefaultIncrement string            `yaml:"default-increment"` // major|minor|patch
	Lines            []LineRule        `yaml:"lines"`
	Ecosystems       map[string]EcoCfg `yaml:"ecosystems"`
}

type LineRule struct {
	Name       string `yaml:"name"`
	PreRelease bool   `yaml:"prerelease"`
}

type EcoCfg struct {
	Manifest string `yaml:"manifest"`
}

func DefaultConfig() Config {
	return Config{TagPrefix: "v", DefaultIncrement: "patch", Ecosystems: map[string]EcoCfg{}}
}

// LoadConfig reads cairn.version from repoRoot. A missing file yields
// DefaultConfig() with no error. Present fields override defaults; the result is
// validated.
func LoadConfig(repoRoot string) (Config, error) {
	cfg := DefaultConfig()
	raw, err := os.ReadFile(filepath.Join(repoRoot, "cairn.version"))
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return Config{}, fmt.Errorf("version.LoadConfig: %w", err)
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("version.LoadConfig: parse: %w", err)
	}
	if cfg.TagPrefix == "" {
		cfg.TagPrefix = "v"
	}
	if cfg.DefaultIncrement == "" {
		cfg.DefaultIncrement = "patch"
	}
	if cfg.Ecosystems == nil {
		cfg.Ecosystems = map[string]EcoCfg{}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	switch c.DefaultIncrement {
	case "major", "minor", "patch":
	default:
		return fmt.Errorf("version.Config: default-increment %q must be major|minor|patch", c.DefaultIncrement)
	}
	return nil
}
