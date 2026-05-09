# Cairn Read Paths Implementation Plan (Plan 4)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Cairn pages render as clean markdown for AI consumption (`?format=md`) and the agent-discovery `.well-known/` endpoints exist. After this plan: agents read Cairn pages natively without MCP, and any visiting agent can self-discover Cairn's shape via `.well-known/llms.txt` + `.well-known/cairn.json`.

**Architecture:** New `routers/web/cairn/markdown.go` provides handlers that wrap Forgejo's existing data fetching (Commit, PullRequest, Issue, File models) and render Cairn-side Go templates. New `routers/web/cairn/wellknown.go` serves the three discovery endpoints from in-memory templates. Both mount under the Cairn route group established in Plan 2a — narrow Forgejo touch.

**Tech Stack:** Go 1.25+, Forgejo's existing models (`models/repo`, `models/issue`), `text/template` (or `html/template` where rendering escaped HTML matters), Cairn's setting flags (`MarkdownEndpointsEnabled`).

**Spec ref:** [`docs/cairn/specs/2026-05-09-cairn-foundation-design.md`](../specs/2026-05-09-cairn-foundation-design.md), §4.5 (markdown), §4.6 (.well-known), §10 (config).

**Plans 1-3 dependencies (already on `cairn`):**
- `cairnidentity.GlobalAgentService()` for fingerprint-aware rendering of commit attribution in markdown
- `routers/web/cairn/agent_author.go` helpers (`IsAgentAuthor`, `AgentAuthorSlug`, `AgentAuthorBadge`) for inline rendering
- `setting.Cairn.MarkdownEndpointsEnabled` (gates the markdown routes)
- Cairn's existing route group at `routers/init.go::cairnRoutes` and `routers/api/cairn/v1/routes.go`

**Pattern conventions** (established in 2a/2b/3): TDD, single commit per task, feature branch, controller merges, `cairntest.NewEngine(t)` for storage tests, Forgejo upstream patches stay minimal.

**Approach for markdown handlers:** They consume already-loaded Forgejo data structs (passed in from middleware that's already populated `ctx.Repo`, `ctx.Issue`, `ctx.PullRequest`). The handlers don't fetch new data — they render what Forgejo's existing context loader already prepared. This keeps the touch-points narrow.

---

## Task 1: Markdown rendering scaffold + commit handler

**Files:**
- Create: `routers/web/cairn/markdown.go` — `Handler` struct + `RenderCommit(w, r, commit, repo)` method
- Create: `routers/web/cairn/markdown_test.go` — uses httptest with synthetic Commit data
- Create: `routers/web/cairn/templates/md/commit.tmpl` — markdown template

**Why:** Establishes the rendering scaffold (content negotiation via `?format=md` or `Accept: text/markdown`) and the commit page — the most-cited page type in the spec example. PR/Issue/File handlers in Tasks 2-3 reuse the scaffold.

**Approach:**
- `Handler` is a thin orchestrator that selects between a passed-in vanilla Forgejo handler and the markdown renderer based on content negotiation
- Markdown templates are embedded via `//go:embed` to keep deployment simple (one binary, no separate template files)
- The commit template renders: header (SHA, author, date, signed-status, agent-badge if applicable), commit message, structured diff
- Handler does NO direct Forgejo data access — caller passes the already-loaded structs

**Step 1: Branch**

```bash
cd ~/Source/cairn && git checkout cairn && git pull
git checkout -b cairn-markdown-commit
git config user.name "nexus-cw"
git config user.email "nexus@darksoft.co.nz"
```

**Step 2: Failing tests in `markdown_test.go`**

```go
package cairn

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// CommitData is the minimal struct the renderer needs. The hook
// handler in Forgejo will adapt from *git.Commit to CommitData.
// Defining it here keeps the test independent of Forgejo's heavy
// types.
//
// (This type lives in markdown.go; the test imports it.)

func TestWantsMarkdown_QueryParam(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/?format=md", nil)
	if !WantsMarkdown(req) {
		t.Error("expected WantsMarkdown=true for ?format=md")
	}
}

func TestWantsMarkdown_AcceptHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/markdown")
	if !WantsMarkdown(req) {
		t.Error("expected WantsMarkdown=true for Accept: text/markdown")
	}
}

func TestWantsMarkdown_None(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if WantsMarkdown(req) {
		t.Error("expected WantsMarkdown=false for plain GET")
	}
}

func TestRenderCommit_AgentSigned(t *testing.T) {
	c := CommitData{
		SHA:         "abc123def456",
		AuthorName:  "nexus-plumb",
		AuthorEmail: "nexus-plumb@darksoft.co.nz",
		Time:        time.Date(2026, 5, 10, 14, 23, 0, 0, time.UTC),
		Subject:     "Add agent registration endpoint",
		Body:        "",
		Signed:      true,
		Verified:    true,
		Diff:        "diff --git a/foo b/foo\n+new line",
	}
	repo := RepoData{Owner: "alice", Name: "cairn"}

	w := httptest.NewRecorder()
	if err := RenderCommit(w, c, repo); err != nil {
		t.Fatal(err)
	}

	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/markdown") {
		t.Errorf("Content-Type = %q, want text/markdown", got)
	}

	body := w.Body.String()
	for _, want := range []string{
		"# Commit abc123def456",
		"nexus-plumb",
		"agent:plumb",       // badge
		"under alice",     // owner attribution
		"Signed:** verified",
		"Add agent registration endpoint",
		"```diff",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\nbody=%s", want, body)
		}
	}
}

func TestRenderCommit_HumanCommit(t *testing.T) {
	c := CommitData{
		SHA:         "fedcba",
		AuthorName:  "Jacinta",
		AuthorEmail: "nexus@darksoft.co.nz",
		Time:        time.Date(2026, 5, 10, 9, 0, 0, 0, time.UTC),
		Subject:     "Vanilla human commit",
	}
	repo := RepoData{Owner: "alice", Name: "cairn"}

	w := httptest.NewRecorder()
	if err := RenderCommit(w, c, repo); err != nil {
		t.Fatal(err)
	}

	body := w.Body.String()
	if strings.Contains(body, "agent:") {
		t.Error("human commit should NOT include agent badge")
	}
	if !strings.Contains(body, "Jacinta") {
		t.Error("body missing author name")
	}
}

func TestRenderCommit_UnsignedAgent(t *testing.T) {
	c := CommitData{
		SHA:         "1234",
		AuthorName:  "nexus-plumb",
		AuthorEmail: "nexus-plumb@darksoft.co.nz",
		Time:        time.Now(),
		Subject:     "Unsigned by agent — shouldn't normally happen with enforce on",
		Signed:      false,
	}
	repo := RepoData{Owner: "alice", Name: "cairn"}

	w := httptest.NewRecorder()
	if err := RenderCommit(w, c, repo); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(w.Body.String(), "Signed:** no") {
		t.Errorf("expected unsigned indicator\nbody=%s", w.Body.String())
	}
	_ = bytes.Buffer{} // keep import
}
```

**Step 3: Implement `markdown.go`**

```go
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
		Commit       CommitData
		Repo         RepoData
		IsAgent      bool
		AgentSlug    string
		AgentBadge   string
		AuthorOwner  string // for "under <owner>" rendering
	}{
		Commit:     c,
		Repo:       repo,
		IsAgent:    IsAgentAuthor(c.AuthorEmail),
		AgentSlug:  AgentAuthorSlug(c.AuthorEmail),
		AgentBadge: AgentAuthorBadge(c.AuthorEmail),
	}
	if data.IsAgent {
		// Owner is the domain in the agent email (best-effort; full
		// resolution requires a service lookup which the renderer
		// doesn't do — caller can populate AuthorOwner explicitly).
		_, domain, _ := splitAgentEmail(c.AuthorEmail)
		data.AuthorOwner = domain
	}
	return tmpl.Execute(w, data)
}

// splitAgentEmail is a local helper — same parse as
// cairnidentity.ParseAgentEmail but doesn't require importing the
// identity package for one regex. (Avoids circular deps if the
// identity package ever needs a web helper.)
func splitAgentEmail(email string) (slug, domain string, ok bool) {
	at := strings.IndexByte(email, '@')
	if at <= 0 || !strings.HasPrefix(email, "nexus-") {
		return "", "", false
	}
	return email[len("nexus-"):at], email[at+1:], true
}
```

**Step 4: Implement `templates/md/commit.tmpl`**

```
# Commit {{.Commit.SHA}}

**Author:** {{.Commit.AuthorName}} <{{.Commit.AuthorEmail}}>{{if .IsAgent}}
**Agent:** {{.AgentBadge}} (under {{if .AuthorOwner}}{{.AuthorOwner}}{{else}}unknown{{end}}){{end}}
**Date:** {{.Commit.Time.UTC.Format "2006-01-02T15:04:05Z"}}
**Signed:** {{if and .Commit.Signed .Commit.Verified}}verified{{else if .Commit.Signed}}yes (unverified){{else}}no{{end}}
**Repo:** {{.Repo.Owner}}/{{.Repo.Name}}

## Message

{{.Commit.Subject}}
{{if .Commit.Body}}
{{.Commit.Body}}
{{end}}

{{if .Commit.Diff}}## Diff

```diff
{{.Commit.Diff}}
```
{{end}}
```

NOTE: the template uses Go template syntax. Use `text/template` (already imported) since we're producing markdown not HTML. The agent slug grammar is already restricted so XSS isn't a concern at the markdown layer (downstream HTML rendering is a separate concern — markdown's job is to be clean source).

**Step 5: Run tests, expect PASS**

```bash
go test ./routers/web/cairn/...
```

Expected: 5 tests pass (3 WantsMarkdown + 3 RenderCommit). All previous tests still pass.

**Step 6: Commit**

```bash
git add routers/web/cairn/markdown.go routers/web/cairn/markdown_test.go routers/web/cairn/templates/
git commit -m "$(cat <<'EOF'
feat(cairn): markdown rendering scaffold + commit page handler

Establishes the ?format=md / Accept: text/markdown content negotiation
and the commit-page renderer — the most-cited page type per spec.
Subsequent tasks reuse the scaffold for PR/Issue/File renderers.

Files:
- routers/web/cairn/markdown.go — Handler, WantsMarkdown(),
  CommitData, RepoData, RenderCommit, splitAgentEmail
- routers/web/cairn/markdown_test.go — content-negotiation tests +
  three RenderCommit cases (agent-signed, human, unsigned-agent)
- routers/web/cairn/templates/md/commit.tmpl — embedded via go:embed

Templates use text/template since the output IS markdown (not HTML).
Agent slug grammar already restricts to safe chars, so no XSS at the
markdown layer.

Refs: docs/cairn/specs/2026-05-09-cairn-foundation-design.md §4.5

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
git push -u origin cairn-markdown-commit
```

Don't merge — controller will do it.

---

## Task 2: PR + Issue markdown handlers

**Files:**
- Modify: `routers/web/cairn/markdown.go` — add `PullRequestData`, `IssueData`, `RenderPullRequest`, `RenderIssue`
- Create: `routers/web/cairn/templates/md/pull_request.tmpl`
- Create: `routers/web/cairn/templates/md/issue.tmpl`
- Modify: `routers/web/cairn/markdown_test.go` — add tests

**Why:** PR and Issue pages are the second and third most-cited markdown read targets in the spec. Their structure is similar (title, body, comments, status), so they share a template skeleton.

**Sketch of the structs:**

```go
type PullRequestData struct {
	Number      int
	Title       string
	State       string // "open"|"merged"|"closed"
	Author      string
	AuthorEmail string
	BaseBranch  string
	HeadBranch  string
	Body        string
	Comments    []CommentData
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type IssueData struct {
	Number      int
	Title       string
	State       string // "open"|"closed"
	Author      string
	AuthorEmail string
	Body        string
	Labels      []string
	Comments    []CommentData
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type CommentData struct {
	Author      string
	AuthorEmail string
	Body        string
	CreatedAt   time.Time
}
```

`RenderPullRequest` and `RenderIssue` follow the same shape as `RenderCommit`:
1. Parse the embedded template
2. Set `Content-Type: text/markdown`
3. Render with author + comment-author agent badges where applicable

**Templates:** straightforward header + body + comments. Each comment author gets the same agent-badge treatment as the top-level author.

Tests: parallel to RenderCommit — agent-authored PR, human-authored issue, mix.

**Commit message starts:** `feat(cairn): PR + Issue markdown renderers`

---

## Task 3: File-content markdown handler

**Files:**
- Modify: `routers/web/cairn/markdown.go` — add `FileData` + `RenderFile`
- Create: `routers/web/cairn/templates/md/file.tmpl`
- Modify: `routers/web/cairn/markdown_test.go` — add tests

**Why:** File-view markdown rendering is structurally different — content is the file's bytes (rendered as code-block) plus path/branch metadata. Smaller than PR/Issue.

**Sketch:**

```go
type FileData struct {
	Path        string
	Branch      string
	Size        int64
	IsBinary    bool
	Content     []byte // skipped if IsBinary
	LastCommit  CommitData // last commit that touched the file
}
```

`RenderFile` renders:
- Header with path, branch, size, last-commit attribution
- If binary: a placeholder line "(binary file, {{Size}} bytes)"
- Otherwise: language-fenced code block (try to infer language from extension; default to `text`)

Tests: text file with agent last-commit, binary file (no content rendered), missing-extension fallback.

**Commit message starts:** `feat(cairn): file-content markdown renderer`

---

## Task 4: `.well-known/` discovery handlers

**Files:**
- Create: `routers/web/cairn/wellknown.go`
- Create: `routers/web/cairn/wellknown_test.go`
- Create: `routers/web/cairn/templates/wellknown/llms.txt.tmpl`
- Create: `routers/web/cairn/templates/wellknown/security.txt`

**Why:** Spec §4.6 — `.well-known/llms.txt` (markdown for AI consumption), `.well-known/cairn.json` (machine-readable manifest), `.well-known/security.txt` (RFC 9116 vulnerability reporting).

**Three handlers:**

```go
// LLMsTxt serves /.well-known/llms.txt — a hand-curated markdown
// description of this Cairn instance for AI agents that land here.
// Includes pointers to ?format=md endpoints, identity API, manifest.
func LLMsTxt(w http.ResponseWriter, r *http.Request) error

// CairnManifest serves /.well-known/cairn.json — machine-readable
// capability manifest. Programmatic clients (CLI, MCP, SDKs) read
// this at startup to discover endpoints, algorithms, features.
func CairnManifest(w http.ResponseWriter, r *http.Request) error

// SecurityTxt serves /.well-known/security.txt per RFC 9116.
func SecurityTxt(w http.ResponseWriter, r *http.Request) error
```

**Manifest content** (matches spec §7):

```go
type Manifest struct {
	CairnVersion        string            `json:"cairn_version"`
	ForgejoVersion      string            `json:"forgejo_version"`
	InstanceName        string            `json:"instance_name"`
	FingerprintAlgo     string            `json:"fingerprint_algo"`
	SigningAlgo         string            `json:"signing_algo"`
	DerivationAlgo      string            `json:"derivation_algo"`
	DerivationInfoPrefix string           `json:"derivation_info_prefix"`
	EmailConvention     string            `json:"email_convention"`
	Trailers            []string          `json:"trailers"`
	Endpoints           map[string]string `json:"endpoints"`
	Features            map[string]any    `json:"features"`
}
```

The instance HMAC key is **never** exposed.

The `cairn_version` and `forgejo_version` come from `setting.AppVer` and a Cairn build constant (start at `0.1.0`). `instance_name` from `setting.AppName` or `setting.Cairn.InstanceName` if added — Forgejo's existing `[server] DOMAIN` is fine for now.

Features map current state:
```go
"markdown_rendering": setting.Cairn.MarkdownEndpointsEnabled,
"agent_proposals":    true,
"mcp_server":         false,
"sdks":               []string{}, // empty until 2b's CLI ships SDKs
```

**llms.txt content** is largely static — hand-curated markdown with template-substituted instance metadata. Sample shape:

```markdown
# Cairn ({{.InstanceName}}, v{{.CairnVersion}})
Agent-native git platform. Fork of Forgejo with per-agent identity
multiplexing via HKDF-derived Ed25519 keypairs.

## Reading content
Any page is fetchable as clean markdown via `?format=md` or
`Accept: text/markdown`. Useful reads:
- /[owner]/[repo]/commits/[hash]?format=md — commit details
- /[owner]/[repo]/pulls/[id]?format=md — PR overview
- /[owner]/[repo]/issues/[id]?format=md — issue details
- /[owner]/[repo]/src/branch/[branch]/[path]?format=md — file content

## Identity
GET /api/cairn/v1/agents/[fingerprint]/identity → public key for an agent.
Derivation: (user_seed, agent_slug) → HKDF-SHA256 → Ed25519.
Email convention: nexus-{slug}@{domain}.

## Manifest
/.well-known/cairn.json — full machine-readable capability manifest.
```

**security.txt** is a static file with the team's vuln-reporting contact (use `security@darksoft.co.nz` as a placeholder; operator can override post-deploy):

```
Contact: mailto:security@darksoft.co.nz
Expires: 2027-12-31T00:00:00Z
Preferred-Languages: en
```

**Tests:** GET each endpoint, verify Content-Type, verify presence of key markers. JSON manifest must `json.Unmarshal` cleanly.

**Commit message starts:** `feat(cairn): .well-known/ discovery handlers`

---

## Task 5: Route mounting + Forgejo integration

**Files:**
- Modify: `routers/init.go` (Forgejo upstream patch — extend Cairn route group)
- Possibly: `routers/api/cairn/v1/routes.go` if shared mounting helpers grow

**Why:** Wire the markdown + .well-known handlers into Forgejo's HTTP router. Markdown handlers attach to existing repo paths; `.well-known/` paths attach at the root.

**Approach:**

1. **Markdown handlers** intercept existing repo paths via middleware:
   - For each (owner)/(repo)/commits/(sha) request: check `WantsMarkdown(r)`. If yes, render markdown and short-circuit. Otherwise pass through to vanilla Forgejo handler.
   - Same for PR, Issue, File paths.
   
   This requires either:
   - **A. Wrap Forgejo's existing routes** — register Cairn middleware before the vanilla handler. Touch surface in `routers/init.go` is a few lines per route.
   - **B. Mount Cairn routes alongside Forgejo's** with the same path patterns — the router dispatches based on order or specificity. Risk: rule precedence tricky.
   
   **Recommend A.** Cleaner, less precedence guessing.

2. **`.well-known/` handlers** mount at root paths:
   - `GET /.well-known/llms.txt` → `LLMsTxt`
   - `GET /.well-known/cairn.json` → `CairnManifest`
   - `GET /.well-known/security.txt` → `SecurityTxt`

   These attach to Forgejo's root route group, not the API group.

3. Both gated by `setting.Cairn.Enabled` (master) and `setting.Cairn.MarkdownEndpointsEnabled` (markdown only).

The markdown wrappers need access to Forgejo's loaded data (`ctx.Repo`, `ctx.Issue`, etc.). Read Forgejo's existing handler code to see what's available in context, then build adapter functions in `routers/web/cairn/forgejo_bind.go` similar to Plan 3 Task 6's `services/cairn/hook/forgejo_bind.go`:

```go
// Adapter functions translate Forgejo's loaded data into Cairn's
// rendering structs. These are the only Cairn code that imports
// Forgejo's heavy types.

func commitDataFromForgejo(ctx *services_context.Context) CommitData {...}
func pullRequestDataFromForgejo(ctx *services_context.Context) PullRequestData {...}
// etc.
```

Verify the actual Forgejo APIs before writing.

**Step 1 (after investigation): wire each markdown route**

Inside Forgejo's commit route handler, BEFORE rendering the vanilla template:

```go
if cairnweb.WantsMarkdown(r) && setting.Cairn.MarkdownEndpointsEnabled {
    cairnweb.RenderCommit(w, cairnweb.CommitDataFromForgejo(ctx), cairnweb.RepoDataFromForgejo(ctx))
    return // short-circuit
}
// ... vanilla Forgejo rendering
```

This is ~5 lines per route, four routes (commit, pr, issue, file) = ~20 lines of upstream patch total.

**Step 2: mount `.well-known/` at root**

Find the place in `routers/init.go` where root-level routes are registered (before the catch-all). Add:

```go
if setting.Cairn.Enabled {
    m.Get("/.well-known/llms.txt", cairnweb.LLMsTxt)
    m.Get("/.well-known/cairn.json", cairnweb.CairnManifest)
    m.Get("/.well-known/security.txt", cairnweb.SecurityTxt)
}
```

**Step 3: smoke test**

```bash
# Build, run, curl
go build -o /tmp/cairn-task5-test .
# ... standard cairn setup ...

curl -s http://localhost:3000/.well-known/cairn.json | jq .
curl -s http://localhost:3000/.well-known/llms.txt | head -20
curl -sI http://localhost:3000/.well-known/security.txt
```

Plus markdown smoke test against a synthetic commit.

**Commit message starts:** `feat(cairn): mount markdown + .well-known routes`

---

## End-of-plan verification

```bash
cd ~/Source/cairn && git checkout cairn && git pull
go test ./routers/web/cairn/... ./services/cairn/...
```

Expected: all pass. ~25 new tests across the plan.

Live smoke test (per Task 5 — repeated for completeness):

```bash
go build -o /tmp/cairn-plan4-test .
# ... start binary, populate test data ...

# .well-known
curl -s http://localhost:3000/.well-known/cairn.json | jq .
curl -s http://localhost:3000/.well-known/llms.txt
curl -s http://localhost:3000/.well-known/security.txt

# markdown rendering against a real commit
curl -s "http://localhost:3000/alice/test-repo/commits/HEAD?format=md"
```

Plan 4 produces a Cairn instance that's discoverable to AI agents and renders structured-data-as-markdown for the four key page types.

---

## Notes for the executing agent

- Forgejo's middleware chain populates `ctx.Repo`, `ctx.Issue`, `ctx.PullRequest` before page handlers run. The Cairn wrappers consume those — the only Forgejo upstream touch is at the route registration layer.
- Templates embedded via `//go:embed` keep the binary self-contained.
- Agent author rendering reuses `IsAgentAuthor`/`AgentAuthorBadge` from Plan 3 Task 7 — no duplication.
- The instance HMAC key is NEVER in `cairn.json`. Algorithm name only.
- `security.txt` is a static file (no template substitution); the operator updates it post-deploy if the contact changes. Document in the deployment runbook.
