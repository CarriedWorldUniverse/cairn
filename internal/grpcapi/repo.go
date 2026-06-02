package grpcapi

import (
	"context"

	cairnv1 "github.com/CarriedWorldUniverse/cwb-proto/gen/go/cwb/cairn/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// repoServer implements cairnv1.RepoServiceServer over the repo core.
type repoServer struct {
	cairnv1.UnimplementedRepoServiceServer
	s *Servers
}

// CreateRepo mirrors the former POST /api/orgs/{org}/repos handler: scope
// repo:write, org-bound, slug required.
func (r *repoServer) CreateRepo(ctx context.Context, req *cairnv1.CreateRepoRequest) (*cairnv1.CreateRepoResponse, error) {
	if _, err := authed(ctx, req.Org, "repo:write"); err != nil {
		return nil, err
	}
	if req.Slug == "" {
		return nil, status.Error(codes.InvalidArgument, "slug required")
	}
	rp, err := r.s.core.CreateRepo(ctx, req.Org, req.Slug)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return &cairnv1.CreateRepoResponse{Repo: &cairnv1.Repo{
		Id:            rp.ID,
		Org:           rp.OrgID,
		Slug:          rp.Slug,
		DefaultBranch: rp.DefaultBranch,
	}}, nil
}

// ListRepos lists the caller-org's repos (scope repo:read).
func (r *repoServer) ListRepos(ctx context.Context, req *cairnv1.ListReposRequest) (*cairnv1.ListReposResponse, error) {
	if _, err := authed(ctx, req.Org, "repo:read"); err != nil {
		return nil, err
	}
	repos, err := r.s.core.ListRepos(ctx, req.Org)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	out := make([]*cairnv1.Repo, 0, len(repos))
	for _, rp := range repos {
		out = append(out, &cairnv1.Repo{Id: rp.ID, Org: rp.OrgID, Slug: rp.Slug, DefaultBranch: rp.DefaultBranch})
	}
	return &cairnv1.ListReposResponse{Repos: out}, nil
}
