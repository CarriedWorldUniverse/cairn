// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package nuget

import (
	"fmt"
	"net/http"

	auth_model "github.com/CarriedWorldUniverse/cairn/models/auth"
	user_model "github.com/CarriedWorldUniverse/cairn/models/user"
	"github.com/CarriedWorldUniverse/cairn/modules/log"
	"github.com/CarriedWorldUniverse/cairn/services/auth"
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

type Auth struct{}

// https://docs.microsoft.com/en-us/nuget/api/package-publish-resource#request-parameters
func (a *Auth) Verify(req *http.Request, w http.ResponseWriter, sess auth.SessionStore) auth.MethodOutput {
	apiKey := req.Header.Get("X-NuGet-ApiKey")
	if apiKey == "" {
		return &auth.AuthenticationNotAttempted{}
	}
	token, err := auth_model.GetAccessTokenBySHA(req.Context(), apiKey)
	if err != nil {
		if !auth_model.IsErrAccessTokenNotExist(err) && !auth_model.IsErrAccessTokenEmpty(err) {
			return &auth.AuthenticationError{Error: fmt.Errorf("nuget auth GetAccessTokenBySHA: %w", err)}
		}
		return &auth.AuthenticationAttemptedIncorrectCredential{Error: err}
	}

	u, err := user_model.GetUserByID(req.Context(), token.UID)
	if err != nil {
		return &auth.AuthenticationError{Error: fmt.Errorf("nuget auth GetUserByID: %w", err)}
	}

	if err := token.UpdateLastUsed(req.Context()); err != nil {
		log.Error("UpdateLastUsed:  %v", err)
	}

	return &auth.AuthenticationSuccess{Result: &nugetAuthenticationResult{user: u}}
}
