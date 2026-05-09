// Cairn-specific code; AGPLv3. See LICENSING.md.

package hook

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/modules/git"
)

// BuildCommitList walks the new commits introduced by a push (the
// commits reachable from newSHA but not from oldSHA) and produces a
// CommitToVerify per commit, ready for VerifyAgentCommits.
//
// The repo is the Forgejo *git.Repository as exposed by the hook
// context (ctx.Repo.GitRepo). repoPath is the on-disk path to that
// repo (ctx.Repo.Repository.RepoPath()) — required because some git
// commands need an explicit Dir. env carries through the
// GIT_OBJECT_DIRECTORY / quarantine environment Forgejo plumbs into
// the hook so we can see objects that haven't yet been moved into the
// main object store.
//
// For each commit we read the raw object bytes (`git cat-file commit
// <sha>`), which is exactly the format ExtractSignedCommitData
// expects: the commit headers and body, no type/size prefix. Author
// email and message are parsed from the same bytes; we deliberately
// do NOT instantiate Forgejo's *git.Commit here because the cat-file
// output is already what the verifier consumes and round-tripping
// would just add bug surface.
//
// Returns commits in topological order as emitted by `git rev-list`
// (newest first) — VerifyAgentCommits doesn't care about order, but
// the first failure surfaces a recent commit which reads better in
// the rejection message.
func BuildCommitList(
	ctx context.Context,
	repo *git.Repository,
	repoPath string,
	env []string,
	oldSHA, newSHA string,
) ([]CommitToVerify, error) {
	if repo == nil {
		return nil, fmt.Errorf("cairn hook: BuildCommitList: nil repo")
	}

	objectFormat, err := repo.GetObjectFormat()
	if err != nil {
		return nil, fmt.Errorf("cairn hook: get object format: %w", err)
	}

	var revListCmd *git.Command
	if oldSHA == objectFormat.EmptyObjectID().String() {
		// New branch: list commits reachable from newSHA but not from
		// any existing ref. Mirrors verifyCommits in routers/private.
		revListCmd = git.NewCommand(ctx, "rev-list").AddDynamicArguments(newSHA).AddArguments("--not", "--all")
	} else {
		// Branch update: only the commits introduced by this push
		// (oldSHA..newSHA — excludes commits already on oldSHA).
		revListCmd = git.NewCommand(ctx, "rev-list").AddDynamicArguments(oldSHA + ".." + newSHA)
	}

	stdout, _, runErr := revListCmd.RunStdBytes(&git.RunOpts{Env: env, Dir: repoPath})
	if runErr != nil {
		return nil, fmt.Errorf("cairn hook: rev-list %s..%s: %w", oldSHA, newSHA, runErr)
	}

	var out []CommitToVerify
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	for scanner.Scan() {
		sha := strings.TrimSpace(scanner.Text())
		if sha == "" {
			continue
		}
		raw, _, runErr := git.NewCommand(ctx, "cat-file", "commit").
			AddDynamicArguments(sha).
			RunStdBytes(&git.RunOpts{Env: env, Dir: repoPath})
		if runErr != nil {
			return nil, fmt.Errorf("cairn hook: cat-file commit %s: %w", sha, runErr)
		}

		authorEmail, message := parseAuthorAndMessage(raw)
		out = append(out, CommitToVerify{
			SHA:         sha,
			AuthorEmail: authorEmail,
			Message:     message,
			Raw:         raw,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("cairn hook: scan rev-list output: %w", err)
	}
	return out, nil
}

// parseAuthorAndMessage extracts the author email and commit message
// body from the raw commit-object bytes (as emitted by `git cat-file
// commit <sha>`). The format is:
//
//	tree <sha>
//	parent <sha>           (zero or more)
//	author Name <email> <ts> <tz>
//	committer Name <email> <ts> <tz>
//	[gpgsig ...]           (optional, possibly multi-line)
//	<blank line>
//	<message body>
//
// We don't validate strictly here — malformed commits will be caught
// downstream by ExtractSignedCommitData / VerifyTrailers. If we can't
// find an author line, we return empty strings; the verifier treats
// that as a non-agent commit (ParseAgentEmail rejects it), which is
// the right "fail open for vanilla commits" behaviour.
func parseAuthorAndMessage(raw []byte) (authorEmail, message string) {
	headerEnd := bytes.Index(raw, []byte("\n\n"))
	if headerEnd < 0 {
		return "", ""
	}
	headers := raw[:headerEnd]
	message = string(raw[headerEnd+2:])

	for _, line := range bytes.Split(headers, []byte("\n")) {
		// Skip continuation lines of multi-line headers (e.g. gpgsig).
		if len(line) > 0 && line[0] == ' ' {
			continue
		}
		if bytes.HasPrefix(line, []byte("author ")) {
			authorEmail = extractEmail(line[len("author "):])
			break
		}
	}
	return authorEmail, message
}

// extractEmail pulls the address out of a "Name <email> <ts> <tz>"
// signature line. Returns "" if no angle-bracketed address is present.
func extractEmail(sig []byte) string {
	lt := bytes.IndexByte(sig, '<')
	if lt < 0 {
		return ""
	}
	gt := bytes.IndexByte(sig[lt+1:], '>')
	if gt < 0 {
		return ""
	}
	return string(sig[lt+1 : lt+1+gt])
}
