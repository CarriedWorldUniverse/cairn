package change

import (
	"testing"

	httpauth "github.com/go-git/go-git/v5/plumbing/transport/http"
)

func TestAuthForFileURL(t *testing.T) {
	a, err := authFor("file:///tmp/x")
	if err != nil {
		t.Fatalf("authFor(file://) err = %v, want nil", err)
	}
	if a != nil {
		t.Fatalf("authFor(file://) = %v, want nil (anonymous)", a)
	}
}

func TestAuthForHTTPSWithToken(t *testing.T) {
	t.Setenv("CAIRN_TOKEN", "tok123")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GITLAB_TOKEN", "")
	a, err := authFor("https://github.com/o/r.git")
	if err != nil {
		t.Fatalf("authFor err = %v, want nil", err)
	}
	ba, ok := a.(*httpauth.BasicAuth)
	if !ok {
		t.Fatalf("authFor = %T, want *http.BasicAuth", a)
	}
	if ba.Password != "tok123" {
		t.Fatalf("Password = %q, want %q", ba.Password, "tok123")
	}
	if ba.Username == "" {
		t.Fatalf("Username is empty, want non-empty")
	}
}

func TestAuthForHTTPSTokenPrecedence(t *testing.T) {
	// Only GITHUB_TOKEN set → it is used.
	t.Run("github-only", func(t *testing.T) {
		t.Setenv("CAIRN_TOKEN", "")
		t.Setenv("GITHUB_TOKEN", "ghtok")
		t.Setenv("GITLAB_TOKEN", "")
		a, err := authFor("https://github.com/o/r.git")
		if err != nil {
			t.Fatalf("authFor err = %v", err)
		}
		ba, ok := a.(*httpauth.BasicAuth)
		if !ok {
			t.Fatalf("authFor = %T, want *http.BasicAuth", a)
		}
		if ba.Password != "ghtok" {
			t.Fatalf("Password = %q, want %q", ba.Password, "ghtok")
		}
	})
	// Both set → CAIRN_TOKEN wins.
	t.Run("cairn-wins", func(t *testing.T) {
		t.Setenv("CAIRN_TOKEN", "cairntok")
		t.Setenv("GITHUB_TOKEN", "ghtok")
		t.Setenv("GITLAB_TOKEN", "")
		a, err := authFor("https://github.com/o/r.git")
		if err != nil {
			t.Fatalf("authFor err = %v", err)
		}
		ba, ok := a.(*httpauth.BasicAuth)
		if !ok {
			t.Fatalf("authFor = %T, want *http.BasicAuth", a)
		}
		if ba.Password != "cairntok" {
			t.Fatalf("Password = %q, want %q (CAIRN_TOKEN precedence)", ba.Password, "cairntok")
		}
	})
}

func TestAuthForHTTPSNoTokenAnonymous(t *testing.T) {
	// Force no env tokens. Do NOT assert a live credential-helper result (it is
	// environment-dependent); only assert the call does not panic and returns a
	// nil or *http.BasicAuth without error.
	t.Setenv("CAIRN_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GITLAB_TOKEN", "")
	a, err := authFor("https://github.com/o/r.git")
	if err != nil {
		t.Fatalf("authFor err = %v, want nil", err)
	}
	if a != nil {
		if _, ok := a.(*httpauth.BasicAuth); !ok {
			t.Fatalf("authFor = %T, want nil or *http.BasicAuth", a)
		}
	}
}

func TestIsSSHURL(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"git@github.com:o/r.git", true},
		{"ssh://git@h/p", true},
		{"https://github.com/o/r.git", false},
		{"http://github.com/o/r.git", false},
		{"file:///tmp/x", false},
	}
	for _, c := range cases {
		if got := isSSHURL(c.in); got != c.want {
			t.Errorf("isSSHURL(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestSSHUser(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"git@h:p", "git"},
		{"ssh://alice@h/p", "alice"},
		{"https://github.com/o/r.git", "git"},
		{"ssh://h/p", "git"},
	}
	for _, c := range cases {
		if got := sshUser(c.in); got != c.want {
			t.Errorf("sshUser(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFirstEnv(t *testing.T) {
	t.Setenv("CW_A", "")
	t.Setenv("CW_B", "bval")
	t.Setenv("CW_C", "cval")
	if got := firstEnv("CW_A", "CW_B", "CW_C"); got != "bval" {
		t.Errorf("firstEnv = %q, want %q (first non-empty)", got, "bval")
	}
	if got := firstEnv("CW_A"); got != "" {
		t.Errorf("firstEnv(empty only) = %q, want \"\"", got)
	}
}

func TestGitCredentialFillNoGit(t *testing.T) {
	t.Setenv("PATH", "") // make the "git" binary unfindable
	u, p, ok := gitCredentialFill("https://github.com/o/r.git")
	if ok || u != "" || p != "" {
		t.Fatalf("with no git on PATH expected ok=false/empty, got user=%q pass=%q ok=%v", u, p, ok)
	}
}

func TestAuthForSSHClassified(t *testing.T) {
	// An scp-like URL must take the SSH branch. In CI with no agent/key it may
	// return an error; the deterministic assertion is that it never returns a
	// *http.BasicAuth (it did not go down the http path).
	if !isSSHURL("git@github.com:o/r.git") {
		t.Fatalf("isSSHURL(scp-like) = false, want true")
	}
	a, _ := authFor("git@github.com:o/r.git")
	if _, ok := a.(*httpauth.BasicAuth); ok {
		t.Fatalf("authFor(ssh url) returned *http.BasicAuth, want ssh path")
	}
}
