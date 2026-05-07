// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repository

import (
	"context"

	"github.com/CarriedWorldUniverse/cairn/models/organization"
	"github.com/CarriedWorldUniverse/cairn/models/perm"
	repo_model "github.com/CarriedWorldUniverse/cairn/models/repo"
)

// GetReviewerTeams get all teams can be requested to review
func GetReviewerTeams(ctx context.Context, repo *repo_model.Repository) ([]*organization.Team, error) {
	if err := repo.LoadOwner(ctx); err != nil {
		return nil, err
	}
	if !repo.Owner.IsOrganization() {
		return nil, nil
	}

	return organization.GetTeamsWithAccessToRepo(ctx, repo.OwnerID, repo.ID, perm.AccessModeRead)
}
