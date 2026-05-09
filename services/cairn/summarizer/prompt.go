//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package summarizer

import (
	"fmt"
	"strings"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

// SystemPrompt is the standardized prompt the simplifier ships with.
// Per-org override is a v1.x feature; MVP ships one tunable unit.
const SystemPrompt = `You are Cairn, the simplifier.

Your job is to rewrite a pull request's content into a short, plain-language
summary that helps a human reviewer decide whether to drill in. You do NOT
review, judge, grade, or recommend anything. You compress.

Output format:
- Start with one sentence describing what the PR changes.
- Then 2-4 bullet points: the major moving parts, in plain language.
- Keep it under 200 words. Skip generic phrases like "this commit".
- If the PR is trivial (typo fix, version bump), say so in one sentence
  and stop.

Do not output code blocks unless they're directly necessary to the
explanation. Do not add caveats, disclaimers, or commentary about your
own limitations.`

// PRContext is the input to the prompt builder. The orchestrator fills
// this in based on the per-repo data scope.
type PRContext struct {
	Title          string
	Body           string
	BaseBranch     string
	HeadBranch     string
	CommitMessages []string
	FilePaths      []string
	Diff           string
}

// BuildUserPrompt formats the PRContext into the user message sent to
// the AI. Sections are clearly labelled so the AI can ignore missing ones.
func BuildUserPrompt(c PRContext) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Pull request: %s\n", c.Title)
	if c.Body != "" {
		fmt.Fprintf(&b, "\nDescription:\n%s\n", strings.TrimSpace(c.Body))
	}
	if c.BaseBranch != "" || c.HeadBranch != "" {
		fmt.Fprintf(&b, "\nBranch: %s -> %s\n", c.HeadBranch, c.BaseBranch)
	}
	if len(c.CommitMessages) > 0 {
		b.WriteString("\nCommits:\n")
		for _, msg := range c.CommitMessages {
			fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(msg))
		}
	}
	if len(c.FilePaths) > 0 {
		b.WriteString("\nFiles changed:\n")
		for _, p := range c.FilePaths {
			fmt.Fprintf(&b, "- %s\n", p)
		}
	}
	if c.Diff != "" {
		fmt.Fprintf(&b, "\nDiff:\n```diff\n%s\n```\n", c.Diff)
	}
	return b.String()
}

// SelectFields returns a PRContext populated according to the data scope.
// Source is the full PR data; the function strips fields not allowed by scope.
func SelectFields(scope cairnmodels.DataScope, full PRContext) PRContext {
	out := PRContext{Title: full.Title, Body: full.Body, BaseBranch: full.BaseBranch, HeadBranch: full.HeadBranch}
	switch scope {
	case cairnmodels.DataScopeFull:
		return full
	case cairnmodels.DataScopeCommitMessages:
		out.CommitMessages = full.CommitMessages
		return out
	case cairnmodels.DataScopeMetadata:
		out.FilePaths = full.FilePaths
		return out
	}
	out.FilePaths = full.FilePaths
	return out
}
