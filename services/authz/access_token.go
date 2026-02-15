// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package authz

import (
	"context"
	"fmt"

	auth_model "forgejo.org/models/auth"
)

func GetAuthorizationReducerForAccessToken(ctx context.Context, token *auth_model.AccessToken) (AuthorizationReducer, error) {
	if token.ResourceAllRepos {
		if publicOnly, err := token.Scope.PublicOnly(); err != nil {
			return nil, fmt.Errorf("PublicOnly: %w", err)
		} else if publicOnly {
			return &PublicReposAuthorizationReducer{}, nil
		}
		return &AllAccessAuthorizationReducer{}, nil
	}

	repos, err := auth_model.GetRepositoriesAccessibleWithToken(ctx, token.ID)
	if err != nil {
		return nil, fmt.Errorf("GetRepositoriesAccessibleWithToken: %w", err)
	}
	return &SpecificReposAuthorizationReducer{resourceRepos: repos}, nil
}
