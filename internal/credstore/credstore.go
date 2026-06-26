// Package credstore is cairn's user-level credential store: per-host access
// tokens kept OUT of every repo, in a 0600 file under the OS user config dir
// (alongside userconfig — ~/.config/cairn/credentials on Linux). Standalone by
// design: no server or infrastructure is required (herald/custodian are optional
// layers above this, never a dependency). Format is "host = token" lines.
//
// v1 protects the file with 0600 perms + a 0700 parent dir. v2 will seal it at
// rest (casket) with a once-per-session unlock; v1 is the always-works baseline.
package credstore

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Path returns the credentials file path. Its directory is created (0700) on Set.
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("credstore.Path: %w", err)
	}
	return filepath.Join(dir, "cairn", "credentials"), nil
}

// Get returns the token stored for host, or "" if none (or on any read error —
// a missing/unreadable store is never fatal, auth just falls through).
func Get(host string) string {
	if host == "" {
		return ""
	}
	m, err := load()
	if err != nil {
		return ""
	}
	return m[host]
}

// Hosts returns the hosts that have a stored credential, sorted. It never
// returns the tokens — for `cairn auth` listing.
func Hosts() []string {
	m, err := load()
	if err != nil {
		return nil
	}
	hs := make([]string, 0, len(m))
	for h := range m {
		hs = append(hs, h)
	}
	sort.Strings(hs)
	return hs
}

// Set stores token for host (overwriting any existing), creating the file (0600)
// and its dir (0700) on first use and preserving the other hosts. An empty host
// or token is a no-op (never write a blank credential).
func Set(host, token string) error {
	if host == "" || token == "" {
		return nil
	}
	m, err := load()
	if err != nil {
		return err
	}
	if m == nil {
		m = map[string]string{}
	}
	m[host] = token
	return save(m)
}

// Delete removes host's credential. Absent host = no error.
func Delete(host string) error {
	m, err := load()
	if err != nil {
		return err
	}
	if _, ok := m[host]; !ok {
		return nil
	}
	delete(m, host)
	return save(m)
}

func save(m map[string]string) error {
	path, err := Path()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("credstore.save: %w", err)
	}
	_ = os.Chmod(dir, 0o700) // tighten if userconfig already created it 0755 (best-effort)
	hosts := make([]string, 0, len(m))
	for h := range m {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	var b strings.Builder
	b.WriteString("# cairn credentials — host = token. Private (0600); never commit this file.\n")
	for _, h := range hosts {
		fmt.Fprintf(&b, "%s = %s\n", h, m[h])
	}
	// Atomic write at 0600 via a temp file in the same dir + rename, so there is
	// never a moment where the credentials are world-readable.
	tmp, err := os.CreateTemp(dir, ".credentials-*")
	if err != nil {
		return fmt.Errorf("credstore.save: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("credstore.save: %w", err)
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("credstore.save: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("credstore.save: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("credstore.save: %w", err)
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
		return nil, fmt.Errorf("credstore.load: %w", err)
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
