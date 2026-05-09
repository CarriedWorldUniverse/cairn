// Package cairn — Cairn web UI augmentations.
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package cairn

import (
	"context"
	"embed"
	"fmt"
	"net/http"
	"strings"
	"text/template"
	"time"

	"github.com/CarriedWorldUniverse/cairn/services/cairn/summarizer"
)

//go:embed templates/md/*.tmpl
var markdownTemplates embed.FS

// CommitData carries the minimal commit attributes the markdown renderer
// needs. Forgejo's hook handler adapts from *git.Commit; tests construct
// it directly.
type CommitData struct {
	SHA         string
	AuthorName  string
	AuthorEmail string
	Time        time.Time
	Subject     string
	Body        string
	Signed      bool
	Verified    bool
	Diff        string
	// OwnerUsername is the username of the agent's owning Forgejo user,
	// resolved via the agent service. Empty for non-agent commits or when
	// lookup failed; the template falls back to AuthorOwner (email domain).
	OwnerUsername string
}

// RepoData carries the minimal repo context for path/url rendering.
type RepoData struct {
	ID    int64
	Owner string
	Name  string
}

// WantsMarkdown reports whether the request asks for a markdown
// representation via ?format=md or Accept: text/markdown.
func WantsMarkdown(r *http.Request) bool {
	if r.URL.Query().Get("format") == "md" {
		return true
	}
	for _, accept := range r.Header.Values("Accept") {
		if strings.Contains(accept, "text/markdown") {
			return true
		}
	}
	return false
}

// RenderCommit writes a markdown representation of the commit to w.
func RenderCommit(w http.ResponseWriter, c CommitData, repo RepoData) error {
	tmpl, err := template.ParseFS(markdownTemplates, "templates/md/commit.tmpl")
	if err != nil {
		return fmt.Errorf("cairn markdown: parse commit.tmpl: %w", err)
	}

	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")

	data := struct {
		Commit        CommitData
		Repo          RepoData
		IsAgent       bool
		AgentSlug     string
		AgentBadge    string
		AuthorOwner   string
		OwnerUsername string
	}{
		Commit:        c,
		Repo:          repo,
		IsAgent:       IsAgentAuthor(c.AuthorEmail),
		AgentSlug:     AgentAuthorSlug(c.AuthorEmail),
		AgentBadge:    AgentAuthorBadge(c.AuthorEmail),
		OwnerUsername: c.OwnerUsername,
	}
	if data.IsAgent {
		_, domain, _ := splitAgentEmail(c.AuthorEmail)
		data.AuthorOwner = domain
	}
	return tmpl.Execute(w, data)
}

// CommentData carries the minimal attributes for rendering a single
// PR or issue comment in markdown.
type CommentData struct {
	Author      string
	AuthorEmail string
	Body        string
	CreatedAt   time.Time
}

// PullRequestData carries PR metadata + comments for markdown rendering.
type PullRequestData struct {
	Number      int
	Title       string
	State       string // "open" | "merged" | "closed"
	Author      string
	AuthorEmail string
	BaseBranch  string
	HeadBranch  string
	Body        string
	Comments    []CommentData
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// IssueData carries issue metadata + comments + labels.
type IssueData struct {
	Number      int
	Title       string
	State       string // "open" | "closed"
	Author      string
	AuthorEmail string
	Body        string
	Labels      []string
	Comments    []CommentData
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// agentBadgeOf returns the badge string ("agent:slug") if the email
// is agent-format, or empty otherwise. Tiny helper for templates.
func agentBadgeOf(email string) string {
	return AgentAuthorBadge(email)
}

// RenderPullRequest writes the PR as markdown to w.
func RenderPullRequest(w http.ResponseWriter, pr PullRequestData, repo RepoData) error {
	tmpl, err := template.New("pull_request.tmpl").Funcs(template.FuncMap{
		"agentBadge": agentBadgeOf,
		"isAgent":    IsAgentAuthor,
	}).ParseFS(markdownTemplates, "templates/md/pull_request.tmpl")
	if err != nil {
		return fmt.Errorf("cairn markdown: parse pull_request.tmpl: %w", err)
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	if svc := summarizer.Global(); svc != nil {
		if row, err := svc.GetCachedSummary(context.Background(), repo.ID, int64(pr.Number)); err == nil {
			fmt.Fprintf(w, "## Summary by cairn\n\n%s\n\n---\n\n", row.SummaryMD)
		}
	}
	data := struct {
		PR   PullRequestData
		Repo RepoData
	}{PR: pr, Repo: repo}
	return tmpl.ExecuteTemplate(w, "pull_request.tmpl", data)
}

// RenderIssue writes the issue as markdown to w.
func RenderIssue(w http.ResponseWriter, issue IssueData, repo RepoData) error {
	tmpl, err := template.New("issue.tmpl").Funcs(template.FuncMap{
		"agentBadge": agentBadgeOf,
		"isAgent":    IsAgentAuthor,
	}).ParseFS(markdownTemplates, "templates/md/issue.tmpl")
	if err != nil {
		return fmt.Errorf("cairn markdown: parse issue.tmpl: %w", err)
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	data := struct {
		Issue IssueData
		Repo  RepoData
	}{Issue: issue, Repo: repo}
	return tmpl.ExecuteTemplate(w, "issue.tmpl", data)
}

// FileData carries file-content + metadata for markdown rendering.
type FileData struct {
	Path       string
	Branch     string
	Size       int64
	IsBinary   bool
	Content    []byte // empty if IsBinary
	LastCommit CommitData // last commit that touched the file
}

// RenderFile writes a file-content markdown view to w.
func RenderFile(w http.ResponseWriter, f FileData, repo RepoData) error {
	tmpl, err := template.New("file.tmpl").Funcs(template.FuncMap{
		"agentBadge":     agentBadgeOf,
		"isAgent":        IsAgentAuthor,
		"languageOfFile": languageOfFile,
	}).ParseFS(markdownTemplates, "templates/md/file.tmpl")
	if err != nil {
		return fmt.Errorf("cairn markdown: parse file.tmpl: %w", err)
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")

	data := struct {
		File FileData
		Repo RepoData
	}{File: f, Repo: repo}
	return tmpl.ExecuteTemplate(w, "file.tmpl", data)
}

// languageOfFile maps file extensions to fenced-code-block language
// hints. Defaults to "text" for unknown extensions.
func languageOfFile(path string) string {
	idx := strings.LastIndexByte(path, '.')
	if idx < 0 || idx == len(path)-1 {
		return "text"
	}
	ext := path[idx+1:]
	switch ext {
	case "go":
		return "go"
	case "js", "mjs":
		return "javascript"
	case "ts":
		return "typescript"
	case "py":
		return "python"
	case "rs":
		return "rust"
	case "rb":
		return "ruby"
	case "sh", "bash":
		return "bash"
	case "json":
		return "json"
	case "yaml", "yml":
		return "yaml"
	case "toml":
		return "toml"
	case "md":
		return "markdown"
	case "html":
		return "html"
	case "css":
		return "css"
	case "sql":
		return "sql"
	case "tmpl":
		return "go-template"
	}
	return "text"
}

// splitAgentEmail parses an agent email "nexus-{slug}@{domain}" into
// its parts. Returns ok=false if the email isn't agent-shaped.
func splitAgentEmail(email string) (slug, domain string, ok bool) {
	at := strings.IndexByte(email, '@')
	if at <= 0 || !strings.HasPrefix(email, "nexus-") {
		return "", "", false
	}
	return email[len("nexus-"):at], email[at+1:], true
}
