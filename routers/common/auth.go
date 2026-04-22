// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package common

import (
	"errors"

	"forgejo.org/modules/web/middleware"
	auth_service "forgejo.org/services/auth"
	"forgejo.org/services/context"
)

func AuthShared(ctx *context.Base, sessionStore auth_service.SessionStore, authMethod auth_service.Method) (ar auth_service.AuthenticationResult, err error) {
	ar, err = authMethod.Verify(ctx.Req, ctx.Resp, sessionStore)
	if err != nil {
		return ar, err
	}
	if ar == nil {
		return nil, errors.New("failure to retrieve AuthenticationResult - nil value")
	}
	doer := ar.User()
	if doer != nil {
		if ctx.Locale.Language() != doer.Language {
			ctx.Locale = middleware.Locale(ctx.Resp, ctx.Req)
		}
		ctx.Data["IsSigned"] = true
		ctx.Data[middleware.ContextDataKeySignedUser] = doer
		ctx.Data["SignedUserID"] = doer.ID
		ctx.Data["IsAdmin"] = doer.IsAdmin
	} else {
		ctx.Data["IsSigned"] = false
		ctx.Data["SignedUserID"] = int64(0)
	}
	return ar, nil
}

// VerifyOptions contains required or check options
type VerifyOptions struct {
	SignInRequired  bool
	SignOutRequired bool
	AdminRequired   bool
	DisableCSRF     bool
}
