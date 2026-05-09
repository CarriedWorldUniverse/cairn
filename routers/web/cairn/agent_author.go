// Package cairn — Cairn web UI augmentations.
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package cairn

import (
	cairnidentity "github.com/CarriedWorldUniverse/cairn/services/cairn/identity"
)

// IsAgentAuthor reports whether the given email is in Cairn's agent
// format (nexus-{slug}@{domain}).
func IsAgentAuthor(email string) bool {
	_, _, ok := cairnidentity.ParseAgentEmail(email)
	return ok
}

// AgentAuthorSlug returns the agent slug from an agent-format email,
// or empty string if the email isn't agent-format.
func AgentAuthorSlug(email string) string {
	slug, _, ok := cairnidentity.ParseAgentEmail(email)
	if !ok {
		return ""
	}
	return slug
}

// AgentAuthorBadge returns a short label suitable for inline display
// alongside the author email — e.g. "agent:plumb" — or empty string
// if the email isn't agent-format. Templates SHOULD escape this on
// insertion (the slug grammar is already restricted to safe chars
// but defensive practice). The template wraps the badge in a
// `cairn-agent-badge` span so future CSS can style it visually
// without committing to a specific glyph here.
func AgentAuthorBadge(email string) string {
	slug, _, ok := cairnidentity.ParseAgentEmail(email)
	if !ok {
		return ""
	}
	return "agent:" + slug
}
