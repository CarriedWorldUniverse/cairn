package cairn

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

// Paths holds the resolved filesystem locations Cairn's CLI uses.
//
// Layout:
//
//	$XDG_CONFIG_HOME/cairn/             ← config root (or $HOME/.config/cairn/)
//	   seed                             ← owner's HKDF seed (mode 0600)
//	   <host>/                          ← per-instance, mode 0700
//	     token                          ← API auth token (mode 0600)
//	     <slug>.key                     ← cached agent keypair (mode 0600)
//	     <slug>.key.pub                 ← agent public key (mode 0644)
type Paths struct {
	ConfigRoot string // $XDG_CONFIG_HOME/cairn or $HOME/.config/cairn
	HostDir    string // ConfigRoot/<host>
	SeedFile   string // ConfigRoot/seed (shared across hosts)
	TokenFile  string // HostDir/token
}

// ResolvePaths returns CLI paths for the given Cairn instance URL.
// Examples: "https://cairn.darksoft.co.nz", "http://localhost:3000".
func ResolvePaths(instanceURL string) (*Paths, error) {
	if instanceURL == "" {
		return nil, errors.New("cairn cli: instance URL must not be empty")
	}
	u, err := url.Parse(instanceURL)
	if err != nil {
		return nil, fmt.Errorf("cairn cli: parse URL: %w", err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("cairn cli: URL has no host: %q", instanceURL)
	}

	cfgRoot := os.Getenv("XDG_CONFIG_HOME")
	if cfgRoot == "" {
		home := os.Getenv("HOME")
		if home == "" {
			return nil, errors.New("cairn cli: neither XDG_CONFIG_HOME nor HOME set")
		}
		cfgRoot = filepath.Join(home, ".config")
	}
	cfgRoot = filepath.Join(cfgRoot, "cairn")

	hostDir := filepath.Join(cfgRoot, u.Host)
	return &Paths{
		ConfigRoot: cfgRoot,
		HostDir:    hostDir,
		SeedFile:   filepath.Join(cfgRoot, "seed"),
		TokenFile:  filepath.Join(hostDir, "token"),
	}, nil
}

// KeyFile returns the per-agent private-key file path under HostDir.
// (Currently used as a cache; commit-sign-helper derives on demand.)
func (p *Paths) KeyFile(slug string) string {
	return filepath.Join(p.HostDir, slug+".key")
}

// EnsureHostDir creates the per-host config directory with mode 0700.
func (p *Paths) EnsureHostDir() error {
	return os.MkdirAll(p.HostDir, 0700)
}

// ReadSeed reads the owner's seed file. Returns an error if the file
// is missing or has insecure permissions.
func (p *Paths) ReadSeed() ([]byte, error) {
	info, err := os.Stat(p.SeedFile)
	if err != nil {
		return nil, fmt.Errorf("cairn cli: stat seed %q: %w", p.SeedFile, err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		return nil, fmt.Errorf("cairn cli: seed %q has insecure mode %#o (want 0600)", p.SeedFile, perm)
	}
	return os.ReadFile(p.SeedFile)
}

// WriteToken stores the API token at TokenFile with mode 0600.
func (p *Paths) WriteToken(token string) error {
	if err := p.EnsureHostDir(); err != nil {
		return err
	}
	return os.WriteFile(p.TokenFile, []byte(token), 0600)
}

// ReadToken reads the API token. Returns an error if missing or
// insecure permissions.
func (p *Paths) ReadToken() (string, error) {
	info, err := os.Stat(p.TokenFile)
	if err != nil {
		return "", fmt.Errorf("cairn cli: stat token %q: %w", p.TokenFile, err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		return "", fmt.Errorf("cairn cli: token %q has insecure mode %#o (want 0600)", p.TokenFile, perm)
	}
	b, err := os.ReadFile(p.TokenFile)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
