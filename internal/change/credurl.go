package change

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/credstore"
)

// splitCredURL parses a URL and separates any embedded credential.
// For "https://x-access-token:TOK@github.com/o/r" it returns
// bare="https://github.com/o/r", host="github.com", token="TOK". The token is
// the password when present, else the username — covering the two PAT-in-URL
// forms (user:pass@host and TOKEN@host). A non-parseable or credential-less URL
// returns token="" (and bare=raw).
func splitCredURL(raw string) (bareURL, host, token string) {
	u, err := url.Parse(raw)
	if err != nil {
		return raw, "", ""
	}
	host = u.Hostname()
	if u.User == nil {
		return raw, host, ""
	}
	if p, ok := u.User.Password(); ok && p != "" {
		token = p
	} else {
		token = u.User.Username()
	}
	u.User = nil
	return u.String(), host, token
}

// hostOf returns the host of an http(s) URL, or "" if not parseable.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// looksLikeToken reports whether a bare URL username is plausibly an access
// token rather than a login name: long, a single word of token characters.
func looksLikeToken(s string) bool {
	if len(s) < 20 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-', c == '.':
		default:
			return false
		}
	}
	return true
}

// storeAndStrip removes any credential embedded in an HTTP(S) URL and returns
// the URL WITHOUT it, so a token is NEVER persisted in a repo's remote config.
// It only touches http(s) URLs — for ssh the "user" (e.g. git@host) is not a
// secret and auth is handled by the ssh agent/keys.
//
// Stripping is the invariant. Saving to the user-level credstore (so a
// clone-with-token still authenticates on later push/pull) is a best-effort
// convenience and is deliberately conservative, because a URL comes from a
// README or a remote and must not be able to tamper with what the user stored:
// it never overwrites an existing credential for the host, and a bare
// username (no password) is only kept when it looks like a token. Anything
// ignored is reported on stderr; `cairn login` is the explicit way to set one.
func storeAndStrip(raw string) string {
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return raw
	}
	bare, host, tok := splitCredURL(raw)
	if tok == "" || host == "" {
		return raw
	}
	if u, err := url.Parse(raw); err == nil && u.User != nil {
		if p, ok := u.User.Password(); !ok || p == "" {
			if !looksLikeToken(tok) {
				fmt.Fprintf(os.Stderr, "cairn: ignoring the user name in the remote URL for %s (not saved as a credential; use 'cairn login %s')\n", host, host)
				return bare
			}
		}
	}
	if existing := credstore.Get(host); existing != "" {
		if existing != tok {
			fmt.Fprintf(os.Stderr, "cairn: keeping the stored credential for %s; the credential in the URL was not saved (use 'cairn login %s' to replace it)\n", host, host)
		}
		return bare
	}
	_ = credstore.Set(host, tok)
	return bare
}
