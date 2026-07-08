package grpcapi

import (
	"context"
	"errors"
	"time"

	ledgerclient "github.com/CarriedWorldUniverse/cairn/internal/ledger"
	"github.com/CarriedWorldUniverse/cairn/internal/repo"
	cairnv1 "github.com/CarriedWorldUniverse/cwb-proto/gen/go/cwb/cairn/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// pullServer implements cairnv1.PullServiceServer over the repo core + ledger.
type pullServer struct {
	cairnv1.UnimplementedPullServiceServer
	s *Servers
}

// OpenPull mirrors the former POST .../pulls handler: validate, dedupe, create
// the ledger issue on-behalf-of the opener, persist the PR.
func (p *pullServer) OpenPull(ctx context.Context, req *cairnv1.OpenPullRequest) (*cairnv1.OpenPullResponse, error) {
	id, err := authed(ctx, req.Org, "repo:write")
	if err != nil {
		return nil, err
	}
	if req.Source == "" || req.Target == "" || req.Title == "" || req.Project == "" {
		return nil, status.Error(codes.InvalidArgument, "source, target, title, project required")
	}
	if req.Source == req.Target {
		return nil, status.Error(codes.InvalidArgument, "source and target must differ")
	}

	rp, err := p.s.core.GetRepo(ctx, req.Org, req.Slug)
	if err != nil {
		return nil, status.Error(codes.NotFound, "repo not found")
	}
	srcRef, err := p.s.core.GetRef(ctx, rp.ID, "refs/heads/"+req.Source)
	if err != nil {
		return nil, status.Error(codes.NotFound, "source branch not found")
	}
	if _, err := p.s.core.GetRef(ctx, rp.ID, "refs/heads/"+req.Target); err != nil {
		return nil, status.Error(codes.NotFound, "target branch not found")
	}

	// Idempotency: an open PR for this (repo, source, target) already exists.
	if existing, err := p.s.core.FindOpenPull(ctx, rp.ID, req.Source, req.Target); err == nil {
		return &cairnv1.OpenPullResponse{Pull: toProtoPull(existing, req.Slug, p.s.publicBase, req.Org)}, nil
	} else if !errors.Is(err, repo.ErrPullNotFound) {
		return nil, status.Error(codes.Internal, err.Error())
	}

	headSHA := srcRef.Hash
	if len(headSHA) > 12 {
		headSHA = headSHA[:12]
	}
	ref := ledgerclient.ExternalRef{
		Tracker:     "cairn",
		Key:         req.Org + "/" + req.Slug + "@" + req.Source,
		Description: req.Source + "→" + req.Target + " @ " + headSHA,
	}
	if p.s.publicBase != "" {
		ref.URL = p.s.publicBase + "/" + req.Org + "/" + req.Slug
	}
	res, err := p.s.ledger.CreateIssue(ctx, id.forwardHeader(), ledgerclient.IssueInput{
		Project:          req.Project,
		Type:             "Story",
		Summary:          req.Title,
		Description:      req.Description,
		DefinitionOfDone: req.DefinitionOfDone,
		ExternalRefs:     []ledgerclient.ExternalRef{ref},
	})
	if err != nil {
		var apiErr *ledgerclient.APIError
		if errors.As(err, &apiErr) {
			return nil, status.Errorf(codes.FailedPrecondition, "ledger rejected issue: %s", apiErr.Body)
		}
		return nil, status.Errorf(codes.Unavailable, "ledger unreachable: %v", err)
	}

	pull := repo.Pull{
		RepoID:         rp.ID,
		Source:         req.Source,
		Target:         req.Target,
		Title:          req.Title,
		LedgerIssueKey: res.Key,
		OpenedBy:       id.Subject,
	}
	if err := p.s.core.CreatePull(ctx, &pull); err != nil {
		return nil, status.Errorf(codes.Internal, "persist pull: %v", err)
	}
	return &cairnv1.OpenPullResponse{Pull: toProtoPull(pull, req.Slug, p.s.publicBase, req.Org)}, nil
}

// GetPull mirrors GET .../pulls/{id}: scope repo:read, org-bound.
func (p *pullServer) GetPull(ctx context.Context, req *cairnv1.GetPullRequest) (*cairnv1.GetPullResponse, error) {
	if _, err := authed(ctx, req.Org, "repo:read"); err != nil {
		return nil, err
	}
	rp, err := p.s.core.GetRepo(ctx, req.Org, req.Slug)
	if err != nil {
		return nil, status.Error(codes.NotFound, "repo not found")
	}
	pull, err := p.s.core.GetPull(ctx, rp.ID, req.Id)
	if err != nil {
		return nil, status.Error(codes.NotFound, "pull not found")
	}
	return &cairnv1.GetPullResponse{Pull: toProtoPull(pull, req.Slug, p.s.publicBase, req.Org)}, nil
}

// ListPulls lists a repo's pulls (scope repo:read; optional state filter).
func (p *pullServer) ListPulls(ctx context.Context, req *cairnv1.ListPullsRequest) (*cairnv1.ListPullsResponse, error) {
	if _, err := authed(ctx, req.Org, "repo:read"); err != nil {
		return nil, err
	}
	rp, err := p.s.core.GetRepo(ctx, req.Org, req.Slug)
	if err != nil {
		return nil, status.Error(codes.NotFound, "repo not found")
	}
	pulls, err := p.s.core.ListPulls(ctx, rp.ID, req.State)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	out := make([]*cairnv1.Pull, 0, len(pulls))
	for _, pl := range pulls {
		out = append(out, toProtoPull(pl, req.Slug, p.s.publicBase, req.Org))
	}
	return &cairnv1.ListPullsResponse{Pulls: out}, nil
}

// RecordPullCheck mirrors POST .../pulls/{id}/checks: scope repo:write, pull
// must be open. Upserts by (pull, name); best-effort comments the linked
// ledger issue, matching MergePull's best-effort-comment pattern.
func (p *pullServer) RecordPullCheck(ctx context.Context, req *cairnv1.RecordPullCheckRequest) (*cairnv1.RecordPullCheckResponse, error) {
	id, err := authed(ctx, req.Org, "repo:write")
	if err != nil {
		return nil, err
	}
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name required")
	}
	switch req.State {
	case repo.CheckStatePass, repo.CheckStateFail, repo.CheckStatePending:
	default:
		return nil, status.Errorf(codes.InvalidArgument, "state must be one of pass|fail|pending, got %q", req.State)
	}

	rp, err := p.s.core.GetRepo(ctx, req.Org, req.Slug)
	if err != nil {
		return nil, status.Error(codes.NotFound, "repo not found")
	}
	pull, err := p.s.core.GetPull(ctx, rp.ID, req.Id)
	if err != nil {
		return nil, status.Error(codes.NotFound, "pull not found")
	}
	if pull.State != repo.PullStateOpen {
		return nil, status.Error(codes.Aborted, "pull is not open")
	}

	check := &repo.PullCheck{
		PullID:      pull.ID,
		Name:        req.Name,
		State:       req.State,
		Summary:     req.Summary,
		EvidenceURL: req.EvidenceUrl,
		RecordedBy:  id.Subject,
	}
	if err := p.s.core.RecordPullCheck(ctx, check); err != nil {
		return nil, status.Errorf(codes.Internal, "record check: %v", err)
	}

	body := "check " + req.Name + ": " + req.State + " — " + req.Summary
	if cErr := p.s.ledger.CommentIssue(ctx, id.forwardHeader(), pull.LedgerIssueKey, body); cErr != nil {
		// best-effort, matching MergePull: the check still records.
		_ = cErr
	}
	return &cairnv1.RecordPullCheckResponse{Check: toProtoPullCheck(*check)}, nil
}

// ListPullChecks mirrors GET .../pulls/{id}/checks: scope repo:read.
func (p *pullServer) ListPullChecks(ctx context.Context, req *cairnv1.ListPullChecksRequest) (*cairnv1.ListPullChecksResponse, error) {
	if _, err := authed(ctx, req.Org, "repo:read"); err != nil {
		return nil, err
	}
	rp, err := p.s.core.GetRepo(ctx, req.Org, req.Slug)
	if err != nil {
		return nil, status.Error(codes.NotFound, "repo not found")
	}
	pull, err := p.s.core.GetPull(ctx, rp.ID, req.Id)
	if err != nil {
		return nil, status.Error(codes.NotFound, "pull not found")
	}
	checks, err := p.s.core.ListPullChecks(ctx, pull.ID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	out := make([]*cairnv1.PullCheck, 0, len(checks))
	for _, c := range checks {
		out = append(out, toProtoPullCheck(c))
	}
	return &cairnv1.ListPullChecksResponse{Checks: out}, nil
}

// MergePull mirrors POST .../pulls/{id}/merge: fast-forward-only merge, mark
// merged, best-effort comment the linked ledger issue. Refuses to merge while
// any recorded check on the pull is not "pass" (zero recorded checks merges
// exactly as before this feature — back-compat).
func (p *pullServer) MergePull(ctx context.Context, req *cairnv1.MergePullRequest) (*cairnv1.MergePullResponse, error) {
	id, err := authed(ctx, req.Org, "repo:write")
	if err != nil {
		return nil, err
	}
	rp, err := p.s.core.GetRepo(ctx, req.Org, req.Slug)
	if err != nil {
		return nil, status.Error(codes.NotFound, "repo not found")
	}
	pull, err := p.s.core.GetPull(ctx, rp.ID, req.Id)
	if err != nil {
		return nil, status.Error(codes.NotFound, "pull not found")
	}
	if pull.State != repo.PullStateOpen {
		return nil, status.Error(codes.Aborted, "pull is not open")
	}

	checks, err := p.s.core.ListPullChecks(ctx, pull.ID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list checks: %v", err)
	}
	if failing := nonPassingChecks(checks); len(failing) > 0 {
		return nil, status.Errorf(codes.FailedPrecondition, "checks not passing: %s", failing)
	}

	mergedSHA, ffErr := p.s.core.FastForward(ctx, rp.ID, pull.Source, pull.Target)
	switch {
	case errors.Is(ffErr, repo.ErrAlreadyUpToDate):
		// already merged content; fall through to mark merged.
	case errors.Is(ffErr, repo.ErrNotFastForward):
		return nil, status.Errorf(codes.Aborted, "not fast-forwardable; rebase %s onto %s", pull.Source, pull.Target)
	case errors.Is(ffErr, repo.ErrNotFound):
		return nil, status.Error(codes.Aborted, "source or target branch missing")
	case ffErr != nil:
		return nil, status.Errorf(codes.Internal, "merge: %v", ffErr)
	}

	if err := p.s.core.SetPullState(ctx, rp.ID, pull.ID, "merged"); err != nil {
		return nil, status.Errorf(codes.Internal, "set state: %v", err)
	}

	result := &cairnv1.MergeResult{Id: pull.ID, State: "merged", Target: pull.Target, MergedSha: mergedSHA}
	sha12 := mergedSHA
	if len(sha12) > 12 {
		sha12 = sha12[:12]
	}
	body := "merged " + pull.Source + " into " + pull.Target + " @ " + sha12
	if cErr := p.s.ledger.CommentIssue(ctx, id.forwardHeader(), pull.LedgerIssueKey, body); cErr != nil {
		result.LedgerCommentError = cErr.Error()
	}
	return &cairnv1.MergePullResponse{Result: result}, nil
}

// nonPassingChecks names every recorded check that is not "pass", as
// "<name>=<state>" comma-joined, for the MergePull refusal message. Empty
// when there are zero recorded checks or all are "pass".
func nonPassingChecks(checks []repo.PullCheck) string {
	var out string
	for _, c := range checks {
		if c.State == repo.CheckStatePass {
			continue
		}
		if out != "" {
			out += ", "
		}
		out += c.Name + "=" + c.State
	}
	return out
}

// toProtoPullCheck maps a repo.PullCheck to the wire message.
func toProtoPullCheck(c repo.PullCheck) *cairnv1.PullCheck {
	return &cairnv1.PullCheck{
		Id:          c.ID,
		PullId:      c.PullID,
		Name:        c.Name,
		State:       c.State,
		Summary:     c.Summary,
		EvidenceUrl: c.EvidenceURL,
		RecordedBy:  c.RecordedBy,
		RecordedAt:  c.RecordedAt.Format(time.RFC3339),
	}
}
