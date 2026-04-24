// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package method

import (
	"fmt"
	"net/http"

	auth_model "forgejo.org/models/auth"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/log"
	"forgejo.org/modules/optional"
	"forgejo.org/modules/web/middleware"
	"forgejo.org/services/auth"
	"forgejo.org/services/authz"
)

var _ auth.Method = &AccessToken{}

type AccessToken struct{}

func (a *AccessToken) Verify(req *http.Request, w http.ResponseWriter, _ auth.SessionStore) auth.MethodOutput {
	// Authentication previously was performed in a single routine for `Authorization: Basic ...` and `Authorization:
	// Bearer ...`, and both routines had separate URL exclusion lists onto which they wouldn't apply.  That behaviour
	// is maintained by cloning those conditions here and deciding whether to look at basic/bearer auth, or not.  In the
	// future this should be removed and migrated to route-specific middleware.
	legacySkipBasic := !middleware.IsAPIPath(req) && !isContainerPath(req) && !isAttachmentDownload(req) && !isGitRawOrAttachOrLFSPath(req)
	legacySkipFormAndBearer := !middleware.IsAPIPath(req) && !isAttachmentDownload(req) && !isAuthenticatedTokenRequest(req) && !isGitRawOrAttachPath(req) && !isArchivePath(req)

	maybeAuthToken := a.getTokenFromRequest(req, legacySkipBasic, legacySkipFormAndBearer)
	if !maybeAuthToken.Has() {
		return &auth.AuthenticationNotAttempted{}
	}
	_, authToken := maybeAuthToken.Get()

	// check personal access token
	token, err := auth_model.GetAccessTokenBySHA(req.Context(), authToken)
	if auth_model.IsErrAccessTokenNotExist(err) || auth_model.IsErrAccessTokenEmpty(err) {
		return &auth.AuthenticationAttemptedIncorrectCredential{Error: err}
	} else if err != nil {
		return &auth.AuthenticationError{Error: fmt.Errorf("access token GetAccessTokenBySHA: %w", err)}
	}

	log.Trace("AccessToken: Valid AccessToken for user[%d]", token.UID)
	u, err := user_model.GetUserByID(req.Context(), token.UID)
	if err != nil {
		return &auth.AuthenticationError{Error: fmt.Errorf("access token GetUserByID: %w", err)}
	}

	if err = token.UpdateLastUsed(req.Context()); err != nil {
		log.Error("UpdateLastUsed:  %v", err)
	}

	reducer, err := authz.GetAuthorizationReducerForAccessToken(req.Context(), token)
	if err != nil {
		return &auth.AuthenticationError{Error: fmt.Errorf("access token GetAuthorizationReducerForAccessToken: %w", err)}
	}

	return &auth.AuthenticationSuccess{Result: &accessTokenAuthenticationResult{user: u, scope: token.Scope, reducer: reducer}}
}

func (a *AccessToken) getTokenFromRequest(req *http.Request, skipBasic, skipFormAndBearer bool) optional.Option[string] {
	if !skipFormAndBearer {
		if has, token := tokenFromForm(req).Get(); has {
			return optional.Some(token)
		}
		if has, token := tokenFromAuthorizationBearer(req).Get(); has {
			return optional.Some(token)
		}
	}
	if !skipBasic {
		if has, token := tokenFromAuthorizationBasic(req).Get(); has {
			return optional.Some(token)
		}
	}
	return optional.None[string]()
}
