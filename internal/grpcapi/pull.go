package grpcapi

import (
	"context"
	"errors"

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

// MergePull mirrors POST .../pulls/{id}/merge: fast-forward-only merge, mark
// merged, best-effort comment the linked ledger issue.
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
