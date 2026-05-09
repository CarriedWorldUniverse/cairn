// Package cairn — Cairn web UI augmentations.
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package cairn

import (
	"embed"
	"fmt"
	"net/http"
	"strings"
	"text/template"
	"time"
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
}

// RepoData carries the minimal repo context for path/url rendering.
type RepoData struct {
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
		Commit      CommitData
		Repo        RepoData
		IsAgent     bool
		AgentSlug   string
		AgentBadge  string
		AuthorOwner string
	}{
		Commit:     c,
		Repo:       repo,
		IsAgent:    IsAgentAuthor(c.AuthorEmail),
		AgentSlug:  AgentAuthorSlug(c.AuthorEmail),
		AgentBadge: AgentAuthorBadge(c.AuthorEmail),
	}
	if data.IsAgent {
		_, domain, _ := splitAgentEmail(c.AuthorEmail)
		data.AuthorOwner = domain
	}
	return tmpl.Execute(w, data)
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
