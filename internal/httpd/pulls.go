package httpd

import (
	"encoding/json"
	"errors"
	"net/http"

	ledgerclient "github.com/CarriedWorldUniverse/cairn/internal/ledger"
	"github.com/CarriedWorldUniverse/cairn/internal/repo"
)

type openPullBody struct {
	Source           string `json:"source"`
	Target           string `json:"target"`
	Title            string `json:"title"`
	Description      string `json:"description"`
	Project          string `json:"project"`
	DefinitionOfDone string `json:"definition_of_done"`
}

type pullResponse struct {
	ID             string `json:"id"`
	Repo           string `json:"repo"`
	Source         string `json:"source"`
	Target         string `json:"target"`
	Title          string `json:"title"`
	State          string `json:"state"`
	LedgerIssueKey string `json:"ledger_issue_key"`
	URL            string `json:"url,omitempty"`
}

func toPullResponse(p repo.Pull, slug, publicBase, org string) pullResponse {
	url := ""
	if publicBase != "" {
		url = publicBase + "/" + org + "/" + slug
	}
	return pullResponse{
		ID: p.ID, Repo: slug, Source: p.Source, Target: p.Target,
		Title: p.Title, State: p.State, LedgerIssueKey: p.LedgerIssueKey, URL: url,
	}
}

// handleOpenPull opens a PR: validate, dedupe, create the ledger issue on behalf
// of the opener, persist the PR. See the spec §7 flow.
func (s *Server) handleOpenPull(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFromHeaders(r)
	if !ok {
		httpErr(w, http.StatusUnauthorized, "missing identity")
		return
	}
	org := r.PathValue("org")
	slug := r.PathValue("slug")
	if id.Org != org {
		httpErr(w, http.StatusForbidden, "org mismatch")
		return
	}
	if !id.HasScope("repo:write") {
		httpErr(w, http.StatusForbidden, "missing scope repo:write")
		return
	}

	var body openPullBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.Source == "" || body.Target == "" || body.Title == "" || body.Project == "" {
		httpErr(w, http.StatusBadRequest, "source, target, title, project required")
		return
	}
	if body.Source == body.Target {
		httpErr(w, http.StatusBadRequest, "source and target must differ")
		return
	}

	rp, err := s.cfg.Core.GetRepo(r.Context(), org, slug)
	if err != nil {
		httpErr(w, http.StatusNotFound, "repo not found")
		return
	}
	srcRef, err := s.cfg.Core.GetRef(r.Context(), rp.ID, "refs/heads/"+body.Source)
	if err != nil {
		httpErr(w, http.StatusNotFound, "source branch not found")
		return
	}
	if _, err := s.cfg.Core.GetRef(r.Context(), rp.ID, "refs/heads/"+body.Target); err != nil {
		httpErr(w, http.StatusNotFound, "target branch not found")
		return
	}

	// Idempotency: an open PR for this (repo, source, target) already exists.
	if existing, err := s.cfg.Core.FindOpenPull(r.Context(), rp.ID, body.Source, body.Target); err == nil {
		writeJSON(w, http.StatusOK, toPullResponse(existing, slug, s.cfg.PublicBase, org))
		return
	} else if !errors.Is(err, repo.ErrPullNotFound) {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Create the ledger issue on behalf of the opener.
	headSHA := srcRef.Hash
	if len(headSHA) > 12 {
		headSHA = headSHA[:12]
	}
	ref := ledgerclient.ExternalRef{
		Tracker:     "cairn",
		Key:         org + "/" + slug + "@" + body.Source,
		Description: body.Source + "→" + body.Target + " @ " + headSHA,
	}
	if s.cfg.PublicBase != "" {
		ref.URL = s.cfg.PublicBase + "/" + org + "/" + slug
	}
	res, err := s.cfg.Ledger.CreateIssue(r.Context(), forwardCWB(r), ledgerclient.IssueInput{
		Project: body.Project, Type: "Story", Summary: body.Title,
		Description: body.Description, DefinitionOfDone: body.DefinitionOfDone,
		ExternalRefs: []ledgerclient.ExternalRef{ref},
	})
	if err != nil {
		var apiErr *ledgerclient.APIError
		if errors.As(err, &apiErr) {
			httpErr(w, apiErr.Status, "ledger rejected issue: "+apiErr.Body)
			return
		}
		httpErr(w, http.StatusBadGateway, "ledger unreachable: "+err.Error())
		return
	}

	p := repo.Pull{
		RepoID: rp.ID, Source: body.Source, Target: body.Target,
		Title: body.Title, LedgerIssueKey: res.Key, OpenedBy: id.Subject,
	}
	if err := s.cfg.Core.CreatePull(r.Context(), &p); err != nil {
		httpErr(w, http.StatusInternalServerError, "persist pull: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toPullResponse(p, slug, s.cfg.PublicBase, org))
}

// handleGetPull returns a PR by id.
func (s *Server) handleGetPull(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFromHeaders(r)
	if !ok {
		httpErr(w, http.StatusUnauthorized, "missing identity")
		return
	}
	org := r.PathValue("org")
	slug := r.PathValue("slug")
	if id.Org != org {
		httpErr(w, http.StatusForbidden, "org mismatch")
		return
	}
	if !id.HasScope("repo:read") {
		httpErr(w, http.StatusForbidden, "missing scope repo:read")
		return
	}
	rp, err := s.cfg.Core.GetRepo(r.Context(), org, slug)
	if err != nil {
		httpErr(w, http.StatusNotFound, "repo not found")
		return
	}
	p, err := s.cfg.Core.GetPull(r.Context(), rp.ID, r.PathValue("id"))
	if err != nil {
		httpErr(w, http.StatusNotFound, "pull not found")
		return
	}
	writeJSON(w, http.StatusOK, toPullResponse(p, slug, s.cfg.PublicBase, org))
}

// forwardCWB copies the trusted X-CWB-* identity headers from the inbound
// request so cairn can act on behalf of the caller against ledger.
func forwardCWB(r *http.Request) http.Header {
	out := http.Header{}
	for _, h := range []string{"X-Cwb-Subject", "X-Cwb-Org", "X-Cwb-Kind", "X-Cwb-Scopes", "X-Cwb-Responsible-Human"} {
		if v := r.Header.Get(h); v != "" {
			out.Set(h, v)
		}
	}
	return out
}

// writeJSON writes v as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type mergeResponse struct {
	ID                 string `json:"id"`
	State              string `json:"state"`
	Target             string `json:"target"`
	MergedSHA          string `json:"merged_sha"`
	LedgerCommentError string `json:"ledger_comment_error,omitempty"`
}

// handleMergePull fast-forward-merges an open PR's source into its target, marks
// the PR merged, and best-effort comments the linked ledger issue. See spec §4.
func (s *Server) handleMergePull(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFromHeaders(r)
	if !ok {
		httpErr(w, http.StatusUnauthorized, "missing identity")
		return
	}
	org := r.PathValue("org")
	slug := r.PathValue("slug")
	if id.Org != org {
		httpErr(w, http.StatusForbidden, "org mismatch")
		return
	}
	if !id.HasScope("repo:write") {
		httpErr(w, http.StatusForbidden, "missing scope repo:write")
		return
	}

	rp, err := s.cfg.Core.GetRepo(r.Context(), org, slug)
	if err != nil {
		httpErr(w, http.StatusNotFound, "repo not found")
		return
	}
	pull, err := s.cfg.Core.GetPull(r.Context(), rp.ID, r.PathValue("id"))
	if err != nil {
		httpErr(w, http.StatusNotFound, "pull not found")
		return
	}
	if pull.State != repo.PullStateOpen {
		httpErr(w, http.StatusConflict, "pull is not open")
		return
	}

	mergedSHA, ffErr := s.cfg.Core.FastForward(r.Context(), rp.ID, pull.Source, pull.Target)
	switch {
	case errors.Is(ffErr, repo.ErrAlreadyUpToDate):
		// already merged content; fall through to mark merged.
	case errors.Is(ffErr, repo.ErrNotFastForward):
		httpErr(w, http.StatusConflict, "not fast-forwardable; rebase "+pull.Source+" onto "+pull.Target)
		return
	case errors.Is(ffErr, repo.ErrNotFound):
		httpErr(w, http.StatusConflict, "source or target branch missing")
		return
	case ffErr != nil:
		httpErr(w, http.StatusInternalServerError, "merge: "+ffErr.Error())
		return
	}

	if err := s.cfg.Core.SetPullState(r.Context(), rp.ID, pull.ID, "merged"); err != nil {
		httpErr(w, http.StatusInternalServerError, "set state: "+err.Error())
		return
	}

	resp := mergeResponse{ID: pull.ID, State: "merged", Target: pull.Target, MergedSHA: mergedSHA}
	sha12 := mergedSHA
	if len(sha12) > 12 {
		sha12 = sha12[:12]
	}
	body := "merged " + pull.Source + " into " + pull.Target + " @ " + sha12
	if cErr := s.cfg.Ledger.CommentIssue(r.Context(), forwardCWB(r), pull.LedgerIssueKey, body); cErr != nil {
		resp.LedgerCommentError = cErr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}
