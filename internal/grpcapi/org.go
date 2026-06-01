package grpcapi

import (
	"context"

	cairnv1 "github.com/CarriedWorldUniverse/cwb-proto/gen/go/cwb/cairn/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// orgServer implements cairnv1.OrgServiceServer.
type orgServer struct {
	cairnv1.UnimplementedOrgServiceServer
	s *Servers
}

// PurgeOrg mirrors the former DELETE /api/org handler: scope org:purge, deletes
// all repos for the caller's verified org (NEX-402). Org-bound by construction —
// the target is id.Org from metadata, never a request field — so a caller can
// only ever purge its own org. Idempotent: zero repos still succeeds.
func (o *orgServer) PurgeOrg(ctx context.Context, _ *cairnv1.PurgeOrgRequest) (*cairnv1.PurgeOrgResponse, error) {
	id, err := authed(ctx, "", "org:purge")
	if err != nil {
		return nil, err
	}
	repos, err := o.s.core.ListRepos(ctx, id.Org)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list repos failed: %v", err)
	}
	for _, rp := range repos {
		if err := o.s.core.DeleteRepo(ctx, rp.ID); err != nil {
			return nil, status.Errorf(codes.Internal, "delete repo failed: %v", err)
		}
	}
	return &cairnv1.PurgeOrgResponse{Purged: id.Org, Repos: int32(len(repos))}, nil
}
