package change

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/credstore"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport"
	httpauth "github.com/go-git/go-git/v5/plumbing/transport/http"
	sshauth "github.com/go-git/go-git/v5/plumbing/transport/ssh"
)

// authFor resolves a transport credential for a remote URL. It returns nil
// (anonymous) when nothing applies, so public HTTPS and file:// keep working.
func authFor(rawurl string) (transport.AuthMethod, error) {
	if isSSHURL(rawurl) {
		return sshAuth(sshUser(rawurl))
	}
	if strings.HasPrefix(rawurl, "http://") || strings.HasPrefix(rawurl, "https://") {
		if tok := firstEnv("CAIRN_TOKEN", "GITHUB_TOKEN", "GITLAB_TOKEN"); tok != "" {
			return &httpauth.BasicAuth{Username: "x-access-token", Password: tok}, nil
		}
		if h := hostOf(rawurl); h != "" { // cairn's own user-level credential store (set via `cairn login`)
			if tok := credstore.Get(h); tok != "" {
				return &httpauth.BasicAuth{Username: "x-access-token", Password: tok}, nil
			}
		}
		if user, pass, ok := gitCredentialFill(rawurl); ok {
			return &httpauth.BasicAuth{Username: user, Password: pass}, nil
		}
		return nil, nil // anonymous (public repos still clone)
	}
	return nil, nil // file:// or unknown — let go-git handle anonymously
}

// authForRemote resolves auth for a configured remote's first URL.
func (e *Engine) authForRemote(rem *git.Remote) (transport.AuthMethod, error) {
	urls := rem.Config().URLs
	if len(urls) == 0 {
		return nil, nil
	}
	return authFor(urls[0])
}

func isSSHURL(s string) bool {
	if strings.HasPrefix(s, "ssh://") {
		return true
	}
	// scp-like: user@host:path, no scheme
	return !strings.Contains(s, "://") && strings.Contains(s, "@") && strings.Contains(s, ":")
}

func sshUser(s string) string {
	if i := strings.Index(s, "@"); i >= 0 {
		// ssh://user@host or user@host:path
		u := s[:i]
		u = strings.TrimPrefix(u, "ssh://")
		if u != "" {
			return u
		}
	}
	return "git"
}

func sshAuth(user string) (transport.AuthMethod, error) {
	if os.Getenv("SSH_AUTH_SOCK") != "" {
		if a, err := sshauth.NewSSHAgentAuth(user); err == nil {
			return a, nil
		}
	}
	home, _ := os.UserHomeDir()
	for _, name := range []string{"id_ed25519", "id_rsa"} {
		p := filepath.Join(home, ".ssh", name)
		if _, err := os.Stat(p); err == nil {
			return sshauth.NewPublicKeysFromFile(user, p, "")
		}
	}
	// Last resort: try the agent even without the env hint.
	return sshauth.NewSSHAgentAuth(user)
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// gitCredentialFill asks the user's configured git credential helper for a
// username/password for the URL's protocol+host. Any failure (git absent, helper
// declines) returns ok=false and is never fatal.
func gitCredentialFill(rawurl string) (user, pass string, ok bool) {
	proto, host := "https", ""
	if i := strings.Index(rawurl, "://"); i >= 0 {
		proto = rawurl[:i]
		rest := rawurl[i+3:]
		if j := strings.IndexAny(rest, "/:"); j >= 0 {
			host = rest[:j]
		} else {
			host = rest
		}
	}
	if host == "" {
		return "", "", false
	}
	cmd := exec.Command("git", "credential", "fill")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Stdin = strings.NewReader("protocol=" + proto + "\nhost=" + host + "\n\n")
	out, err := cmd.Output()
	if err != nil {
		return "", "", false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if v, found := strings.CutPrefix(line, "username="); found {
			user = v
		} else if v, found := strings.CutPrefix(line, "password="); found {
			pass = v
		}
	}
	if user == "" || pass == "" {
		return "", "", false
	}
	return user, pass, true
}
