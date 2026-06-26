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
