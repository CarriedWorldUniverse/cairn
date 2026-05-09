package identity

import (
	"errors"
	"strings"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

// ErrTrailerMismatch is returned when a commit trailer (Agent-Id,
// Agent-Owner, or Agent-Domain) is present but disagrees with the
// agent record or owner username.
var ErrTrailerMismatch = errors.New("cairn identity: commit trailer mismatch")

// Cairn trailer keys, case-sensitive per git's trailer convention.
const (
	trailerAgentID     = "Agent-Id"
	trailerAgentOwner  = "Agent-Owner"
	trailerAgentDomain = "Agent-Domain"
)

// VerifyTrailers parses commit-message trailers (the optional last
// paragraph of "Key: Value" lines) and cross-validates Cairn-specific
// trailers against the agent record + caller-derived owner username.
//
// Returns nil if no Cairn trailers are present (trailers are optional
// in the wire format) or if all present Cairn trailers match.
// Returns ErrTrailerMismatch if any present Cairn trailer disagrees.
//
// Other trailers (Co-Authored-By, Signed-off-by, etc.) are ignored.
func VerifyTrailers(commitMessage string, agent *cairn.Agent, ownerUsername string) error {
	trailers := parseLastParagraphTrailers(commitMessage)
	for key, val := range trailers {
		switch key {
		case trailerAgentID:
			if val != agent.Slug {
				return ErrTrailerMismatch
			}
		case trailerAgentOwner:
			if val != ownerUsername {
				return ErrTrailerMismatch
			}
		case trailerAgentDomain:
			if val != agent.Domain {
				return ErrTrailerMismatch
			}
		}
	}
	return nil
}

// parseLastParagraphTrailers extracts "Key: Value" trailer lines from
// the last paragraph of a commit message. Values are trimmed of
// surrounding whitespace; non-trailer lines are ignored.
//
// Returns the collected trailers as a map (later occurrences overwrite
// earlier — uncommon but harmless in our use).
func parseLastParagraphTrailers(message string) map[string]string {
	paragraphs := strings.Split(strings.TrimRight(message, "\n"), "\n\n")
	if len(paragraphs) == 0 {
		return nil
	}
	last := paragraphs[len(paragraphs)-1]

	out := map[string]string{}
	for _, line := range strings.Split(last, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if key == "" {
			continue
		}
		out[key] = val
	}
	return out
}
