package change

import (
	"net/url"
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

// storeAndStrip moves any credential embedded in an HTTP(S) URL into the
// user-level credstore (keyed by host) and returns the URL WITHOUT the
// credential, so a token is NEVER persisted in a repo's remote config. It only
// touches http(s) URLs — for ssh the "user" (e.g. git@host) is not a secret and
// auth is handled by the ssh agent/keys. Stripping is the invariant (creds never
// in the repo); saving to the credstore is the convenience (a clone-with-token
// still authenticates on later push/pull) and is best-effort.
func storeAndStrip(raw string) string {
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return raw
	}
	bare, host, tok := splitCredURL(raw)
	if tok == "" || host == "" {
		return raw
	}
	_ = credstore.Set(host, tok)
	return bare
}
