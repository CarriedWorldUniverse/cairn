// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package nuget

import (
	"net/http"

	auth_model "forgejo.org/models/auth"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/log"
	"forgejo.org/services/auth"
)

var (
	_ auth.Method               = &Auth{}
	_ auth.AuthenticationResult = &nugetAuthenticationResult{}
)

type nugetAuthenticationResult struct {
	*auth.BaseAuthenticationResult
	user *user_model.User
}

func (r *nugetAuthenticationResult) User() *user_model.User {
	return r.user
}

var _ auth.Method = &Auth{}

type Auth struct{}

func (a *Auth) Name() string {
	return "nuget"
}

// https://docs.microsoft.com/en-us/nuget/api/package-publish-resource#request-parameters
func (a *Auth) Verify(req *http.Request, w http.ResponseWriter, sess auth.SessionStore) (auth.AuthenticationResult, error) {
	token, err := auth_model.GetAccessTokenBySHA(req.Context(), req.Header.Get("X-NuGet-ApiKey"))
	if err != nil {
		if !auth_model.IsErrAccessTokenNotExist(err) && !auth_model.IsErrAccessTokenEmpty(err) {
			log.Error("GetAccessTokenBySHA: %v", err)
			return nil, err
		}
		return &auth.UnauthenticatedResult{}, nil
	}

	u, err := user_model.GetUserByID(req.Context(), token.UID)
	if err != nil {
		log.Error("GetUserByID:  %v", err)
		return nil, err
	}

	if err := token.UpdateLastUsed(req.Context()); err != nil {
		log.Error("UpdateLastUsed:  %v", err)
	}

	return &nugetAuthenticationResult{user: u}, nil
}
