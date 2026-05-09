package cairn

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
	"github.com/CarriedWorldUniverse/cairn/services/cairn/summarizer"
)

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
		Signed:        true,
		Verified:      true,
		Diff:          "diff --git a/foo b/foo\n+new line",
		OwnerUsername: "alice",
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
		"agent:plumb",
		"under alice",
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
		Subject:     "Unsigned",
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
}

func TestRenderPullRequest_AgentAuthored(t *testing.T) {
	pr := PullRequestData{
		Number:      42,
		Title:       "Add identity layer",
		State:       "open",
		Author:      "nexus-plumb",
		AuthorEmail: "nexus-plumb@darksoft.co.nz",
		BaseBranch:  "main",
		HeadBranch:  "feat/identity",
		Body:        "Implements the agent registration endpoint.",
		Comments: []CommentData{
			{
				Author:      "Jacinta",
				AuthorEmail: "nexus@darksoft.co.nz",
				Body:        "LGTM, merging.",
				CreatedAt:   time.Now(),
			},
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	repo := RepoData{Owner: "alice", Name: "cairn"}

	w := httptest.NewRecorder()
	if err := RenderPullRequest(w, pr, repo); err != nil {
		t.Fatal(err)
	}

	body := w.Body.String()
	for _, want := range []string{
		"# PR #42: Add identity layer",
		"State:** open",
		"agent:plumb",
		"main",
		"feat/identity",
		"Implements the agent registration endpoint.",
		"Jacinta",
		"LGTM, merging.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\nbody=%s", want, body)
		}
	}
}

func TestRenderIssue_HumanAuthored(t *testing.T) {
	issue := IssueData{
		Number:      7,
		Title:       "Need better docs",
		State:       "open",
		Author:      "Jacinta",
		AuthorEmail: "nexus@darksoft.co.nz",
		Body:        "The README assumes too much.",
		Labels:      []string{"docs", "good-first-issue"},
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	repo := RepoData{Owner: "alice", Name: "cairn"}

	w := httptest.NewRecorder()
	if err := RenderIssue(w, issue, repo); err != nil {
		t.Fatal(err)
	}

	body := w.Body.String()
	if strings.Contains(body, "agent:") {
		t.Error("human-authored issue should NOT include agent badge")
	}
	for _, want := range []string{
		"# Issue #7: Need better docs",
		"State:** open",
		"docs, good-first-issue",
		"The README assumes too much.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\nbody=%s", want, body)
		}
	}
}

func TestRenderPullRequest_NoComments(t *testing.T) {
	pr := PullRequestData{
		Number:      1,
		Title:       "Initial",
		State:       "merged",
		Author:      "alice",
		AuthorEmail: "nexus@darksoft.co.nz",
		BaseBranch:  "main",
		HeadBranch:  "init",
		Body:        "",
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	repo := RepoData{Owner: "alice", Name: "cairn"}

	w := httptest.NewRecorder()
	if err := RenderPullRequest(w, pr, repo); err != nil {
		t.Fatal(err)
	}

	body := w.Body.String()
	if !strings.Contains(body, "_(no description)_") {
		t.Errorf("expected '_(no description)_' for empty body\nbody=%s", body)
	}
	if strings.Contains(body, "## Comments") {
		t.Errorf("should not render Comments section for empty list\nbody=%s", body)
	}
}

func TestRenderPullRequest_InlinesCachedSummary(t *testing.T) {
	eng := cairntest.NewEngine(t)
	svc := summarizer.NewService(eng, nil)
	summarizer.SetGlobal(svc)
	t.Cleanup(func() { summarizer.SetGlobal(nil) })

	if _, err := eng.Insert(&cairnmodels.PRSummary{
		RepoID:      42,
		PRNumber:    7,
		ContentHash: "h",
		SummaryMD:   "the summary text",
		ModelID:     "m",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	pr := PullRequestData{
		Number:      7,
		Title:       "x",
		State:       "open",
		Author:      "alice",
		AuthorEmail: "nexus@darksoft.co.nz",
		BaseBranch:  "main",
		HeadBranch:  "feat/x",
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	repo := RepoData{ID: 42, Owner: "o", Name: "r"}

	w := httptest.NewRecorder()
	if err := RenderPullRequest(w, pr, repo); err != nil {
		t.Fatal(err)
	}

	out := w.Body.String()
	if !strings.Contains(out, "## Summary by cairn") {
		t.Errorf("output missing summary header:\n%s", out)
	}
	if !strings.Contains(out, "the summary text") {
		t.Errorf("output missing summary body:\n%s", out)
	}
	headerIdx := strings.Index(out, "## Summary by cairn")
	prHeaderIdx := strings.Index(out, "# PR #7")
	if headerIdx < 0 || prHeaderIdx < 0 || headerIdx > prHeaderIdx {
		t.Errorf("summary block should appear before PR header (summary=%d, pr=%d)", headerIdx, prHeaderIdx)
	}
}

func TestRenderPullRequest_NoSummaryWhenServiceNil(t *testing.T) {
	summarizer.SetGlobal(nil)
	pr := PullRequestData{
		Number:      1,
		Title:       "x",
		State:       "open",
		Author:      "alice",
		AuthorEmail: "nexus@darksoft.co.nz",
		BaseBranch:  "main",
		HeadBranch:  "feat/x",
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	repo := RepoData{ID: 1, Owner: "o", Name: "r"}

	w := httptest.NewRecorder()
	if err := RenderPullRequest(w, pr, repo); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.Body.String(), "Summary by cairn") {
		t.Errorf("should not render summary block when service nil")
	}
}

func TestRenderFile_TextWithAgentLastCommit(t *testing.T) {
	f := FileData{
		Path:     "services/cairn/identity/agent_service.go",
		Branch:   "main",
		Size:     1234,
		IsBinary: false,
		Content:  []byte("package identity\n\nfunc Hello() {}\n"),
		LastCommit: CommitData{
			SHA:         "abc123",
			AuthorName:  "nexus-plumb",
			AuthorEmail: "nexus-plumb@darksoft.co.nz",
			Time:        time.Now(),
			Subject:     "Add identity primitives",
		},
	}
	repo := RepoData{Owner: "alice", Name: "cairn"}

	w := httptest.NewRecorder()
	if err := RenderFile(w, f, repo); err != nil {
		t.Fatal(err)
	}

	body := w.Body.String()
	for _, want := range []string{
		"# services/cairn/identity/agent_service.go",
		"Branch:** main",
		"Size:** 1234 bytes",
		"agent:plumb",
		"```go",
		"package identity",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\nbody=%s", want, body)
		}
	}
}

func TestRenderFile_BinaryFile(t *testing.T) {
	f := FileData{
		Path:     "logo.png",
		Branch:   "main",
		Size:     8192,
		IsBinary: true,
		LastCommit: CommitData{
			SHA:         "fed",
			AuthorName:  "Jacinta",
			AuthorEmail: "nexus@darksoft.co.nz",
			Time:        time.Now(),
			Subject:     "Add logo",
		},
	}
	repo := RepoData{Owner: "alice", Name: "cairn"}

	w := httptest.NewRecorder()
	if err := RenderFile(w, f, repo); err != nil {
		t.Fatal(err)
	}

	body := w.Body.String()
	if !strings.Contains(body, "_(binary file, 8192 bytes)_") {
		t.Errorf("expected binary placeholder\nbody=%s", body)
	}
	if strings.Contains(body, "```png") {
		t.Errorf("binary file should not produce a code fence\nbody=%s", body)
	}
}

func TestRenderFile_UnknownExtension(t *testing.T) {
	f := FileData{
		Path:     "Makefile",
		Branch:   "main",
		Size:     200,
		IsBinary: false,
		Content:  []byte("all:\n\techo hi\n"),
		LastCommit: CommitData{
			SHA:         "xyz",
			AuthorName:  "Jacinta",
			AuthorEmail: "nexus@darksoft.co.nz",
			Time:        time.Now(),
			Subject:     "Add Makefile",
		},
	}
	repo := RepoData{Owner: "alice", Name: "cairn"}

	w := httptest.NewRecorder()
	if err := RenderFile(w, f, repo); err != nil {
		t.Fatal(err)
	}

	body := w.Body.String()
	if !strings.Contains(body, "```text") {
		t.Errorf("expected fallback to text language\nbody=%s", body)
	}
}
