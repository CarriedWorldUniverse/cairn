// Cairn-specific code; AGPLv3. See LICENSING.md.
//
// Package cairnforgejo bridges Forgejo's loaded request context
// (services/context.Context, *git.Commit, *issues_model.Issue,
// *issues_model.PullRequest) to Cairn's renderer data types. It provides
// MaybeRender* short-circuit helpers callable from the top of Forgejo
// handlers — when the request asks for markdown via cairnweb.WantsMarkdown,
// the helper renders and returns true; otherwise it returns false and the
// vanilla handler proceeds.
//
// This sub-package exists to avoid an import cycle: routers/web/cairn is
// imported by modules/templates (for agent-author helpers), and
// services/context (transitively) imports modules/templates — so the
// package that imports services/context must sit outside routers/web/cairn
// proper.

package cairnforgejo

import (
	stdctx "context"
	"io"
	"strings"
	"time"

	asymkey_model "github.com/CarriedWorldUniverse/cairn/models/asymkey"
	issues_model "github.com/CarriedWorldUniverse/cairn/models/issues"
	repo_model "github.com/CarriedWorldUniverse/cairn/models/repo"
	user_model "github.com/CarriedWorldUniverse/cairn/models/user"
	"github.com/CarriedWorldUniverse/cairn/modules/git"
	"github.com/CarriedWorldUniverse/cairn/modules/log"
	"github.com/CarriedWorldUniverse/cairn/modules/setting"
	"github.com/CarriedWorldUniverse/cairn/modules/util"
	cairnweb "github.com/CarriedWorldUniverse/cairn/routers/web/cairn"
	"github.com/CarriedWorldUniverse/cairn/services/cairn/identity"
	"github.com/CarriedWorldUniverse/cairn/services/context"
)

// Re-export the renderer data types so call sites only need this package.
type (
	CommitData      = cairnweb.CommitData
	RepoData        = cairnweb.RepoData
	IssueData       = cairnweb.IssueData
	PullRequestData = cairnweb.PullRequestData
	CommentData     = cairnweb.CommentData
	FileData        = cairnweb.FileData
)

// repoDataFromForgejo extracts the minimal repo identity used by templates.
func repoDataFromForgejo(repo *repo_model.Repository) RepoData {
	if repo == nil {
		return RepoData{}
	}
	return RepoData{
		ID:    repo.ID,
		Owner: repo.OwnerName,
		Name:  repo.Name,
	}
}

// commitDataFromForgejo builds a CommitData from a *git.Commit and verification flags.
func commitDataFromForgejo(c *git.Commit, signed, verified bool, diff string) CommitData {
	if c == nil {
		return CommitData{}
	}
	subject := c.Summary()
	body := strings.TrimSpace(strings.TrimPrefix(c.CommitMessage, subject))
	var (
		authorName, authorEmail string
		when                    time.Time
	)
	if c.Author != nil {
		authorName = c.Author.Name
		authorEmail = c.Author.Email
		when = c.Author.When
	}
	return CommitData{
		SHA:         c.ID.String(),
		AuthorName:  authorName,
		AuthorEmail: authorEmail,
		Time:        when,
		Subject:     subject,
		Body:        body,
		Signed:      signed,
		Verified:    verified,
		Diff:        diff,
	}
}

// resolveAgentOwnerUsername best-effort resolves the Forgejo username of
// the user that owns the agent identified by the given email. Returns the
// empty string on any failure (no agent service, non-agent email, agent
// not found, user not found) — the renderer falls back to the email
// domain in that case.
func resolveAgentOwnerUsername(ctx stdctx.Context, email string) string {
	if email == "" {
		return ""
	}
	at := strings.IndexByte(email, '@')
	if at <= 0 || !strings.HasPrefix(email, "nexus-") {
		return ""
	}
	slug := email[len("nexus-"):at]
	domain := email[at+1:]
	svc := identity.GlobalAgentService()
	if svc == nil {
		return ""
	}
	agent, err := svc.GetByEmail(ctx, slug, domain)
	if err != nil || agent == nil {
		return ""
	}
	owner, err := user_model.GetUserByID(ctx, agent.UserID)
	if err != nil || owner == nil {
		return ""
	}
	return owner.Name
}

// issueCommentsFromForgejo filters comment events down to actual textual comments.
// The caller must have populated issue.Comments (via LoadAttributes or LoadComments).
func issueCommentsFromForgejo(comments issues_model.CommentList) []CommentData {
	out := make([]CommentData, 0, len(comments))
	for _, c := range comments {
		if c == nil || c.Type != issues_model.CommentTypeComment {
			continue
		}
		var name, email string
		if c.Poster != nil {
			name = c.Poster.Name
			email = c.Poster.Email
		}
		out = append(out, CommentData{
			Author:      name,
			AuthorEmail: email,
			Body:        c.Content,
			CreatedAt:   c.CreatedUnix.AsTime(),
		})
	}
	return out
}

// issueDataFromForgejo builds IssueData from a fully-loaded *issues_model.Issue.
func issueDataFromForgejo(issue *issues_model.Issue) IssueData {
	if issue == nil {
		return IssueData{}
	}
	state := "open"
	if issue.IsClosed {
		state = "closed"
	}
	labels := make([]string, 0, len(issue.Labels))
	for _, l := range issue.Labels {
		if l == nil {
			continue
		}
		labels = append(labels, l.Name)
	}
	var posterName, posterEmail string
	if issue.Poster != nil {
		posterName = issue.Poster.Name
		posterEmail = issue.Poster.Email
	}
	return IssueData{
		Number:      int(issue.Index),
		Title:       issue.Title,
		State:       state,
		Author:      posterName,
		AuthorEmail: posterEmail,
		Body:        issue.Content,
		Labels:      labels,
		Comments:    issueCommentsFromForgejo(issue.Comments),
		CreatedAt:   issue.CreatedUnix.AsTime(),
		UpdatedAt:   issue.UpdatedUnix.AsTime(),
	}
}

// pullRequestDataFromForgejo builds PullRequestData from a PR and its underlying issue.
func pullRequestDataFromForgejo(pr *issues_model.PullRequest, issue *issues_model.Issue) PullRequestData {
	if pr == nil || issue == nil {
		return PullRequestData{}
	}
	state := "open"
	if pr.HasMerged {
		state = "merged"
	} else if issue.IsClosed {
		state = "closed"
	}
	var posterName, posterEmail string
	if issue.Poster != nil {
		posterName = issue.Poster.Name
		posterEmail = issue.Poster.Email
	}
	return PullRequestData{
		Number:      int(issue.Index),
		Title:       issue.Title,
		State:       state,
		Author:      posterName,
		AuthorEmail: posterEmail,
		BaseBranch:  pr.BaseBranch,
		HeadBranch:  pr.HeadBranch,
		Body:        issue.Content,
		Comments:    issueCommentsFromForgejo(issue.Comments),
		CreatedAt:   issue.CreatedUnix.AsTime(),
		UpdatedAt:   issue.UpdatedUnix.AsTime(),
	}
}

// shouldShortCircuit reports whether the request wants markdown AND
// the markdown endpoints feature is enabled.
func shouldShortCircuit(ctx *context.Context) bool {
	if !setting.Cairn.Enabled || !setting.Cairn.MarkdownEndpointsEnabled {
		return false
	}
	return cairnweb.WantsMarkdown(ctx.Req)
}

// maxDiffBytes caps the buffered diff output for MaybeRenderCommit. Matches
// the file blob cap used in MaybeRenderFile. Anything beyond this is dropped
// and a truncation marker is appended.
const maxDiffBytes = 512 * 1024

// limitWriter wraps an io.Writer and stops forwarding once `remaining` bytes
// have been written, marking itself truncated. Subsequent writes are silently
// discarded (reported as successful) so upstream producers don't error out.
type limitWriter struct {
	w         io.Writer
	remaining int
	truncated bool
}

func (lw *limitWriter) Write(p []byte) (int, error) {
	if lw.remaining <= 0 {
		lw.truncated = true
		return len(p), nil // discard, but report success so GetRawDiff doesn't error
	}
	if len(p) > lw.remaining {
		n, err := lw.w.Write(p[:lw.remaining])
		lw.remaining = 0
		lw.truncated = true
		if err != nil {
			return n, err
		}
		return len(p), nil
	}
	n, err := lw.w.Write(p)
	lw.remaining -= n
	return n, err
}

// MaybeRenderCommit renders a commit as markdown if the caller asked for it.
// Returns true if it handled the response (handler should return immediately).
//
// Loads the commit (and parent) from ctx.Repo.GitRepo and computes a textual
// diff. Verification fields are best-effort.
func MaybeRenderCommit(ctx *context.Context) bool {
	if !shouldShortCircuit(ctx) {
		return false
	}
	if ctx.Repo == nil || ctx.Repo.GitRepo == nil {
		return false
	}
	sha := ctx.Params(":sha")
	commit, err := ctx.Repo.GitRepo.GetCommit(sha)
	if err != nil {
		log.Error("cairn: GetCommit(%s): %v", sha, err)
		return false
	}

	// Verification — best-effort; if it fails we render with Verified=false.
	signed, verified := false, false
	if v := asymkey_model.ParseCommitWithSignature(ctx, commit); v != nil {
		signed = v.Verified || v.Reason != ""
		verified = v.Verified
	}

	// Diff against first parent. Use plain `git diff` text output rather than
	// Forgejo's structured DiffOptions to keep this path independent of the
	// heavier Diff handler. Empty diff is acceptable for root commits.
	var diff string
	if commit.ParentCount() > 0 {
		var buf strings.Builder
		lw := &limitWriter{w: &buf, remaining: maxDiffBytes}
		if err := git.GetRawDiff(ctx.Repo.GitRepo, commit.ID.String(), git.RawDiffNormal, lw); err == nil {
			diff = buf.String()
			if lw.truncated {
				diff += "\n\n[diff truncated at 512 KB]\n"
			}
		}
	}

	cd := commitDataFromForgejo(commit, signed, verified, diff)
	cd.OwnerUsername = resolveAgentOwnerUsername(ctx, cd.AuthorEmail)
	if err := cairnweb.RenderCommit(ctx.Resp, cd, repoDataFromForgejo(ctx.Repo.Repository)); err != nil {
		log.Error("cairn: RenderCommit: %v", err)
	}
	return true
}

// MaybeRenderIssueOrPull renders the loaded issue/PR as markdown if requested.
// Must be called AFTER ctx.Repo is populated; loads the issue itself.
// Returns true if it handled the response.
func MaybeRenderIssueOrPull(ctx *context.Context) bool {
	if !shouldShortCircuit(ctx) {
		return false
	}
	if ctx.Repo == nil || ctx.Repo.Repository == nil {
		return false
	}
	idx := ctx.ParamsInt64(":index")
	issue, err := issues_model.GetIssueByIndex(ctx, ctx.Repo.Repository.ID, idx)
	if err != nil {
		log.Error("cairn: GetIssueByIndex: %v", err)
		return false
	}
	if err := issue.LoadAttributes(ctx); err != nil {
		log.Error("cairn: issue.LoadAttributes: %v", err)
		return false
	}

	repoData := repoDataFromForgejo(ctx.Repo.Repository)
	if issue.IsPull {
		if err := issue.LoadPullRequest(ctx); err != nil {
			log.Error("cairn: issue.LoadPullRequest: %v", err)
			return false
		}
		if err := cairnweb.RenderPullRequest(ctx.Resp, pullRequestDataFromForgejo(issue.PullRequest, issue), repoData); err != nil {
			log.Error("cairn: RenderPullRequest: %v", err)
		}
		return true
	}
	if err := cairnweb.RenderIssue(ctx.Resp, issueDataFromForgejo(issue), repoData); err != nil {
		log.Error("cairn: RenderIssue: %v", err)
	}
	return true
}

// MaybeRenderFile renders the file at ctx.Repo.TreePath@ctx.Repo.Commit as
// markdown if the caller asked for it. Returns true if handled.
func MaybeRenderFile(ctx *context.Context) bool {
	if !shouldShortCircuit(ctx) {
		return false
	}
	if ctx.Repo == nil || ctx.Repo.GitRepo == nil || ctx.Repo.Commit == nil || ctx.Repo.TreePath == "" {
		return false
	}
	blob, err := ctx.Repo.Commit.GetBlobByPath(ctx.Repo.TreePath)
	if err != nil {
		// Not a file (might be a tree) — fall through to vanilla rendering.
		return false
	}

	const maxFileBytes = 512 * 1024
	dataRc, err := blob.DataAsync()
	if err != nil {
		log.Error("cairn: blob.DataAsync: %v", err)
		return false
	}
	defer dataRc.Close()

	buf := make([]byte, maxFileBytes)
	n, _ := util.ReadAtMost(dataRc, buf)
	content := buf[:n]

	// Crude binary detection — null byte in first 8KB.
	isBinary := false
	limit := n
	if limit > 8192 {
		limit = 8192
	}
	for i := 0; i < limit; i++ {
		if content[i] == 0 {
			isBinary = true
			break
		}
	}

	fd := FileData{
		Path:     ctx.Repo.TreePath,
		Branch:   ctx.Repo.BranchName,
		Size:    blob.Size(),
		IsBinary: isBinary,
	}
	if !isBinary {
		fd.Content = content
	}
	// Last-commit-that-touched-the-file is best-effort; skip if the lookup
	// fails so we still produce *some* markdown view.
	if lastCommit, err := ctx.Repo.Commit.GetCommitByPath(ctx.Repo.TreePath); err == nil && lastCommit != nil {
		fd.LastCommit = commitDataFromForgejo(lastCommit, false, false, "")
		fd.LastCommit.OwnerUsername = resolveAgentOwnerUsername(ctx, fd.LastCommit.AuthorEmail)
	}

	if err := cairnweb.RenderFile(ctx.Resp, fd, repoDataFromForgejo(ctx.Repo.Repository)); err != nil {
		log.Error("cairn: RenderFile: %v", err)
	}
	return true
}
