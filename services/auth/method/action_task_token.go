// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package method

import (
	"errors"
	"fmt"
	"net/http"

	actions_model "forgejo.org/models/actions"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/log"
	"forgejo.org/modules/optional"
	"forgejo.org/modules/util"
	"forgejo.org/modules/web/middleware"
	"forgejo.org/services/auth"
)

var _ auth.Method = &ActionTaskToken{}

type ActionTaskToken struct{}

func (a *ActionTaskToken) Verify(req *http.Request, w http.ResponseWriter, _ auth.SessionStore) auth.MethodOutput {
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

	// check task token
	task, err := actions_model.GetRunningTaskByToken(req.Context(), authToken)
	if err != nil && errors.Is(err, util.ErrNotExist) {
		return &auth.AuthenticationAttemptedIncorrectCredential{Error: err}
	} else if err != nil {
		return &auth.AuthenticationError{Error: fmt.Errorf("action task token GetRunningTaskByToken: %w", err)}
	} else if task == nil {
		return &auth.AuthenticationError{Error: errors.New("failed to retrieve non-nil task")}
	}

	log.Trace("Basic Authorization: Valid AccessToken for task[%d]", task.ID)
	return &auth.AuthenticationSuccess{Result: &actionsTaskTokenAuthenticationResult{user: user_model.NewActionsUser(), taskID: task.ID}}
}

func (a *ActionTaskToken) getTokenFromRequest(req *http.Request, skipBasic, skipFormAndBearer bool) optional.Option[string] {
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
