package cairn

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
		"agent:plumb",
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
