// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package method

import (
	user_model "github.com/CarriedWorldUniverse/cairn/models/user"
	"github.com/CarriedWorldUniverse/cairn/services/auth"
)

var _ auth.AuthenticationResult = &httpSignAuthenticationResult{}

type httpSignAuthenticationResult struct {
	*auth.BaseAuthenticationResult
	user *user_model.User
}

func (r *httpSignAuthenticationResult) User() *user_model.User {
	return r.user
}
