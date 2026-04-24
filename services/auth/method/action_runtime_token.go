// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package method

import (
	"context"
	"errors"
	"net/http"

	actions_model "forgejo.org/models/actions"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/optional"
	"forgejo.org/modules/web/middleware"
	"forgejo.org/services/actions"
	"forgejo.org/services/auth"
)

var _ auth.Method = &ActionRuntimeToken{}

type ActionRuntimeToken struct{}

func (a *ActionRuntimeToken) Verify(req *http.Request, w http.ResponseWriter, _ auth.SessionStore) auth.MethodOutput {
	// In the future this should be removed and migrated to route-specific middleware:
	if !middleware.IsAPIPath(req) && !isAttachmentDownload(req) && !isAuthenticatedTokenRequest(req) && !isGitRawOrAttachPath(req) && !isArchivePath(req) {
		return &auth.AuthenticationNotAttempted{}
	}

	maybeAuthToken := a.getTokenFromRequest(req)
	if !maybeAuthToken.Has() {
		return &auth.AuthenticationNotAttempted{}
	}
	_, authToken := maybeAuthToken.Get()

	taskID, err := actions.TokenToTaskID(authToken)
	if err != nil {
		return &auth.AuthenticationAttemptedIncorrectCredential{Error: err}
	}

	if !checkTaskIsRunning(req.Context(), taskID) {
		return &auth.AuthenticationAttemptedIncorrectCredential{Error: errors.New("failure to authenticate with action runtime token: task is no longer running")}
	}

	return &auth.AuthenticationSuccess{Result: &actionsTaskTokenAuthenticationResult{user: user_model.NewActionsUser(), taskID: taskID}}
}

func (a *ActionRuntimeToken) getTokenFromRequest(req *http.Request) optional.Option[string] {
	if has, token := tokenFromForm(req).Get(); has {
		return optional.Some(token)
	}
	if has, token := tokenFromAuthorizationBearer(req).Get(); has {
		return optional.Some(token)
	}
	return optional.None[string]()
}

// CheckTaskIsRunning verifies that the TaskID corresponds to a running task
func checkTaskIsRunning(ctx context.Context, taskID int64) bool {
	// Verify the task exists
	task, err := actions_model.GetTaskByID(ctx, taskID)
	if err != nil {
		return false
	}

	// Verify that it's running
	return task.Status == actions_model.StatusRunning
}
