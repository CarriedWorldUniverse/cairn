package main

import (
	"os"
	"testing"
)

// isolateUserConfig points every platform's user-config location at a fresh
// temp dir for one test. os.UserConfigDir reads XDG_CONFIG_HOME on Linux,
// $HOME/Library/Application Support on macOS and %AppData% on Windows, so
// setting XDG_CONFIG_HOME alone leaks a --global write into the real config on
// macOS — and from there into every later test in the package.
func isolateUserConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("AppData", dir)
}

// TestMain gives the whole package a throwaway user config, so no test can
// read or write the machine's real one on any OS, whatever it forgets to set.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cairn-test-home-")
	if err != nil {
		panic(err)
	}
	for _, k := range []string{"XDG_CONFIG_HOME", "HOME", "AppData"} {
		_ = os.Setenv(k, dir)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
