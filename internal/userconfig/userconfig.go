// Package userconfig is cairn's user-level (global) configuration: identity and
// preferences that span every repo, stored once per user rather than per repo.
// It is the global layer beneath a repo's own config — repo settings override
// global ones (like git's local vs global config).
//
// The file lives under the OS user config dir (os.UserConfigDir): on Linux
// ~/.config/cairn/config, on Windows %AppData%\cairn\config, on macOS
// ~/Library/Application Support/cairn/config. Format is simple "key = value"
// lines; blank lines and '#' comments are ignored.
package userconfig

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Path returns the user-level config file path (its directory is created lazily
// on Set, not here).
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("userconfig.Path: %w", err)
	}
	return filepath.Join(dir, "cairn", "config"), nil
}

// Get returns the value for key, or "" if the key (or the file) is absent.
func Get(key string) string {
	m, err := load()
	if err != nil {
		return ""
	}
	return m[key]
}

// Set writes key=value into the global config, creating the file (and its parent
// directory) on first use and preserving any other keys.
func Set(key, value string) error {
	path, err := Path()
	if err != nil {
		return err
	}
	m, err := load()
	if err != nil {
		return err
	}
	if m == nil {
		m = map[string]string{}
	}
	m[key] = value
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("userconfig.Set: %w", err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s = %s\n", k, m[k])
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("userconfig.Set: %w", err)
	}
	return nil
}

func load() (map[string]string, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("userconfig.load: %w", err)
	}
	defer f.Close()
	m := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		m[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return m, sc.Err()
}
