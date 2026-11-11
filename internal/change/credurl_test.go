package change

import (
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/internal/credstore"
)

func TestStoreAndStrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)

	// http(s) with an embedded token: stripped from the URL + saved to the store
	got := storeAndStrip("https://x-access-token:TOK123@github.com/o/r.git")
	if got != "https://github.com/o/r.git" {
		t.Fatalf("bare = %q", got)
	}
	if strings.Contains(got, "TOK123") {
		t.Fatal("token left in URL")
	}
	if credstore.Get("github.com") != "TOK123" {
		t.Fatalf("token not stored, got %q", credstore.Get("github.com"))
	}

	// user:pass form → the password is the token
	_ = storeAndStrip("https://user:PASSWORD@gitlab.com/o/r")
	if credstore.Get("gitlab.com") != "PASSWORD" {
		t.Fatal("user:pass token not stored")
	}

	// ssh: untouched, and NOT stored (the "git" user is not a secret)
	ssh := "ssh://git@bitbucket.org/o/r"
	if storeAndStrip(ssh) != ssh {
		t.Fatal("ssh url mutated")
	}
	if credstore.Get("bitbucket.org") != "" {
		t.Fatal("ssh user wrongly stored as a credential")
	}

	// credential-less http: untouched
	clean := "https://github.com/public/repo"
	if storeAndStrip(clean) != clean {
		t.Fatal("clean url mutated")
	}
}

func TestSplitCredURL(t *testing.T) {
	bare, host, tok := splitCredURL("https://x-access-token:TOK@github.com/o/r")
	if bare != "https://github.com/o/r" || host != "github.com" || tok != "TOK" {
		t.Fatalf("got bare=%q host=%q tok=%q", bare, host, tok)
	}
}

// TestStoreAndStripNeverOverwrites (security review #3): a crafted URL must not
// replace the credential the user stored for that host, nor save a plain
// user name as a credential.
func TestStoreAndStripNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	if err := credstore.Set("github.com", "REALTOKEN"); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{
		"https://not-a-token@github.com/victim/private",
		"https://evil:attackerpass@github.com/victim/private",
		"https://ghp_0123456789abcdefghijklmnopqrstuvwxyz@github.com/victim/private",
	} {
		got := storeAndStrip(u)
		if got != "https://github.com/victim/private" {
			t.Fatalf("storeAndStrip(%q) = %q", u, got)
		}
		if credstore.Get("github.com") != "REALTOKEN" {
			t.Fatalf("%q overwrote the stored credential: %q", u, credstore.Get("github.com"))
		}
	}
	// A short user name on a host with no credential is not persisted either.
	_ = storeAndStrip("https://alice@example.org/o/r")
	if credstore.Get("example.org") != "" {
		t.Fatal("a plain user name was saved as a credential")
	}
	// A token-looking username fills an empty host.
	_ = storeAndStrip("https://ghp_0123456789abcdefghijklmnopqrstuvwxyz@example.net/o/r")
	if credstore.Get("example.net") == "" {
		t.Fatal("token-shaped userinfo not saved for an empty host")
	}
}
