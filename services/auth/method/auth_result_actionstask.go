// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package method

import (
	auth_model "github.com/CarriedWorldUniverse/cairn/models/auth"
	user_model "github.com/CarriedWorldUniverse/cairn/models/user"
	"github.com/CarriedWorldUniverse/cairn/modules/optional"
	"github.com/CarriedWorldUniverse/cairn/services/auth"
)

var _ auth.AuthenticationResult = &actionsTaskTokenAuthenticationResult{}

type actionsTaskTokenAuthenticationResult struct {
	*auth.BaseAuthenticationResult
	user   *user_model.User
	taskID int64
}

func (r *actionsTaskTokenAuthenticationResult) Scope() optional.Option[auth_model.AccessTokenScope] {
	return optional.None[auth_model.AccessTokenScope]()
}

func (r *actionsTaskTokenAuthenticationResult) User() *user_model.User {
	return r.user
}

func (r *actionsTaskTokenAuthenticationResult) ActionsTaskID() optional.Option[int64] {
	return optional.Some(r.taskID)
}
