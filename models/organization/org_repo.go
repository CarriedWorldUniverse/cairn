// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package organization

import (
	"context"

	"github.com/CarriedWorldUniverse/cairn/models/db"
	repo_model "github.com/CarriedWorldUniverse/cairn/models/repo"
)

// GetOrgRepositories get repos belonging to the given organization
func GetOrgRepositories(ctx context.Context, orgID int64) (repo_model.RepositoryList, error) {
	var orgRepos []*repo_model.Repository
	return orgRepos, db.GetEngine(ctx).Where("owner_id = ?", orgID).Find(&orgRepos)
}
