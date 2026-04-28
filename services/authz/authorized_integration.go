// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package authz

import (
	"context"
	"fmt"

	auth_model "forgejo.org/models/auth"
)

func GetAuthorizationReducerForAuthorizedIntegration(ctx context.Context, ai *auth_model.AuthorizedIntegration) (AuthorizationReducer, error) {
	if ai.ResourceAllRepos {
		if publicOnly, err := ai.Scope.PublicOnly(); err != nil {
			return nil, fmt.Errorf("PublicOnly: %w", err)
		} else if publicOnly {
			return &PublicReposAuthorizationReducer{}, nil
		}
		return &AllAccessAuthorizationReducer{}, nil
	}

	repos, err := auth_model.GetRepositoriesAccessibleWithIntegration(ctx, ai.ID)
	if err != nil {
		return nil, fmt.Errorf("GetRepositoriesAccessibleWithIntegration: %w", err)
	}
	// Cast slice into []RepoGetter
	iface := make([]RepoGetter, len(repos))
	for i, r := range repos {
		iface[i] = r
	}
	return &SpecificReposAuthorizationReducer{resourceRepos: iface}, nil
}

// Validate that an authorized integration's state is valid for creation.  For example, that it doesn't have a
// conflicting set of resources (public-only and specific repositories), and other similar checks.
func ValidateAuthorizedIntegration(ai *auth_model.AuthorizedIntegration, repoResources []*auth_model.AuthorizedIntegResourceRepo) error {
	// Other validations may be added here in the future.
	return validateRepositoryResource(ai.ResourceAllRepos, ai.Scope, len(repoResources))
}
