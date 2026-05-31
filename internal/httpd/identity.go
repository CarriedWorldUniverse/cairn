package httpd

import (
	"net/http"
	"strings"
)

// Identity is the gateway-verified caller, read from the trusted X-CWB-*
// headers interchange injects after herald verification. cairn's HTTP path
// TRUSTS these over the mTLS gateway->cairn hop and does NOT re-verify.
type Identity struct {
	Subject string // herald agent/human id (the actor)
	Org     string
	Kind    string // "agent" | "human"
	Scopes  []string
}

// HasScope reports whether the identity holds the named scope.
func (i Identity) HasScope(s string) bool {
	for _, have := range i.Scopes {
		if have == s {
			return true
		}
	}
	return false
}

// identityFromHeaders reads the trusted X-CWB-* headers. ok is false when no
// Subject is present (the gateway always sets Subject for an authed request;
// its absence means the request did not come through the gateway authed path).
func identityFromHeaders(r *http.Request) (Identity, bool) {
	sub := r.Header.Get("X-CWB-Subject")
	if sub == "" {
		return Identity{}, false
	}
	return Identity{
		Subject: sub,
		Org:     r.Header.Get("X-CWB-Org"),
		Kind:    r.Header.Get("X-CWB-Kind"),
		Scopes:  strings.Fields(r.Header.Get("X-CWB-Scopes")), // space-joined by the gateway
	}, true
}
