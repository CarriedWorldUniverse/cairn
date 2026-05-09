// Package identity holds Cairn's agent identity primitives:
// fingerprinting, email parsing, signature verification, and
// instance-HMAC-key handling.
//
// See docs/cairn/specs/2026-05-09-cairn-foundation-design.md §6.
package identity

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"regexp"
)

const fingerprintPrefix = "cairn:"

// Fingerprint computes an agent's fingerprint as
// "cairn:" + base64-url-no-padding(HMAC-SHA256(instanceHMACKey, publicKey)).
// The HMAC binds the fingerprint to the issuing instance, providing
// cross-instance unlinkability and resistance to fingerprint spoofing
// without the instance key.
//
// Encoding is base64 URL-safe (RFC 4648 §5) without padding so the
// fingerprint is safe to embed in URL paths and HTTP headers without
// further encoding. This is part of the on-the-wire contract:
// changing the encoding or HMAC algorithm invalidates every stored
// fingerprint.
func Fingerprint(instanceHMACKey []byte, publicKey ed25519.PublicKey) string {
	mac := hmac.New(sha256.New, instanceHMACKey)
	mac.Write(publicKey)
	sum := mac.Sum(nil)
	return fingerprintPrefix + base64.RawURLEncoding.EncodeToString(sum)
}

// agentEmailPattern matches "nexus-<slug>@<domain>" with slug consisting
// of lowercase letters, digits, and hyphens. Domain is anything after @.
var agentEmailPattern = regexp.MustCompile(`^nexus-([a-z0-9][a-z0-9-]*)@([^\s@]+)$`)

// ParseAgentEmail extracts (slug, domain) from an agent email of the form
// "nexus-{slug}@{domain}". Returns ok=false for non-agent emails or
// malformed input.
func ParseAgentEmail(email string) (slug, domain string, ok bool) {
	m := agentEmailPattern.FindStringSubmatch(email)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}
