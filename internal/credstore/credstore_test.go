package credstore

import (
	"os"
	"runtime"
	"testing"
)

// isolate redirects the OS user-config dir at a temp dir so a test never touches
// the real store. On Linux os.UserConfigDir uses $XDG_CONFIG_HOME (or $HOME/.config);
// set both so the credentials file lands in temp regardless.
func isolate(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("AppData", dir) // os.UserConfigDir uses %AppData% on Windows
}

func TestSetGetDelete(t *testing.T) {
	isolate(t)
	if got := Get("github.com"); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
	if err := Set("github.com", "tok123"); err != nil {
		t.Fatal(err)
	}
	if got := Get("github.com"); got != "tok123" {
		t.Fatalf("got %q", got)
	}
	if err := Set("gitlab.com", "tok456"); err != nil {
		t.Fatal(err)
	}
	if Get("github.com") != "tok123" || Get("gitlab.com") != "tok456" {
		t.Fatal("hosts not both preserved")
	}
	hs := Hosts()
	if len(hs) != 2 || hs[0] != "github.com" || hs[1] != "gitlab.com" {
		t.Fatalf("hosts %v", hs)
	}
	if err := Delete("github.com"); err != nil {
		t.Fatal(err)
	}
	if Get("github.com") != "" {
		t.Fatal("delete failed")
	}
	if Get("gitlab.com") != "tok456" {
		t.Fatal("delete clobbered the other host")
	}
}

func TestFilePerm0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not honor Unix file permissions")
	}
	isolate(t)
	if err := Set("github.com", "tok"); err != nil {
		t.Fatal(err)
	}
	p, _ := Path()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("credentials file perm = %o, want 600", perm)
	}
}

func TestEmptyIsNoop(t *testing.T) {
	isolate(t)
	if err := Set("", "tok"); err != nil {
		t.Fatal(err)
	}
	if err := Set("github.com", ""); err != nil {
		t.Fatal(err)
	}
	if len(Hosts()) != 0 {
		t.Fatalf("empty set should not write a credential, hosts=%v", Hosts())
	}
}
