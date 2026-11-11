package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPagerGlobals(t *testing.T) {
	for _, c := range []struct {
		in   []string
		rest []string
		mode pagerMode
	}{
		{[]string{"log", "-n", "3"}, []string{"log", "-n", "3"}, pagerAuto},
		{[]string{"--no-pager", "log"}, []string{"log"}, pagerNever},
		{[]string{"-P", "log", "-p"}, []string{"log", "-p"}, pagerNever},
		{[]string{"-p", "diff"}, []string{"diff"}, pagerForce},
		{[]string{"--paginate", "--no-pager", "show"}, []string{"show"}, pagerNever},
	} {
		rest, mode := pagerGlobals(c.in)
		if !reflect.DeepEqual(rest, c.rest) || mode != c.mode {
			t.Errorf("pagerGlobals(%v) = %v,%v want %v,%v", c.in, rest, mode, c.rest, c.mode)
		}
	}
}

func TestPagerCommandPrecedence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	unsetenv(t, "CAIRN_PAGER", "GIT_PAGER", "PAGER")
	if got := pagerCommand("log", pagerAuto); got != "less" {
		t.Errorf("default = %q", got)
	}
	t.Setenv("PAGER", "more")
	if got := pagerCommand("log", pagerAuto); got != "more" {
		t.Errorf("PAGER = %q", got)
	}
	t.Setenv("GIT_PAGER", "gp")
	if got := pagerCommand("log", pagerAuto); got != "gp" {
		t.Errorf("GIT_PAGER = %q", got)
	}
	t.Setenv("CAIRN_PAGER", "cp")
	if got := pagerCommand("log", pagerAuto); got != "cp" {
		t.Errorf("CAIRN_PAGER = %q", got)
	}
	if got := pagerCommand("log", pagerNever); got != "" {
		t.Errorf("--no-pager = %q", got)
	}
	if got := pagerCommand("commit", pagerAuto); got != "" {
		t.Errorf("unpaged command = %q", got)
	}
	t.Setenv("CAIRN_PAGER", "") // set but empty: no pager
	if got := pagerCommand("log", pagerAuto); got != "" {
		t.Errorf("empty CAIRN_PAGER = %q", got)
	}
	t.Setenv("CAIRN_PAGER", "cat")
	if got := pagerCommand("log", pagerAuto); got != "" {
		t.Errorf("cat = %q", got)
	}
}

func TestStartPagerPipesOutputOnlyOnTTY(t *testing.T) {
	skipOnWindows(t)
	sink := filepath.Join(t.TempDir(), "paged")
	t.Setenv("CAIRN_PAGER", "cat > "+sink+"; echo $LESS $LV >> "+sink)
	unsetenv(t, "LESS", "LV")

	// Not a terminal: never paged, even when forced.
	stop := startPager([]string{"log"}, pagerForce, false)
	if pagerActive {
		t.Fatal("paged without a TTY")
	}
	stop()

	real := os.Stdout
	stop = startPager([]string{"log"}, pagerAuto, true)
	if !pagerActive || os.Stdout == real || !stdoutIsTTY() {
		t.Fatal("pager not engaged on a TTY")
	}
	os.Stdout.WriteString("hello\n")
	stop()
	if pagerActive || os.Stdout != real {
		t.Fatal("stdout not restored")
	}
	b, err := os.ReadFile(sink)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); !strings.HasPrefix(got, "hello\n") || !strings.Contains(got, "FRX -c") {
		t.Errorf("pager saw %q", got)
	}
}

// unsetenv removes variables for the test and restores them afterwards.
func unsetenv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}
