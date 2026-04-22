// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package auth

import (
	"context"
	"net/http"

	auth_model "forgejo.org/models/auth"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/optional"
	"forgejo.org/modules/session"
	"forgejo.org/modules/web/middleware"
	"forgejo.org/services/authz"
)

// DataStore represents a data store
type DataStore middleware.ContextDataStore

// SessionStore represents a session store
type SessionStore session.Store

// Method represents an authentication method (plugin) for HTTP requests.
type Method interface {
	// Verify tries to verify the authentication data contained in the request. If verification is successful returns an
	// AuthenticationResult implementation with details about the authentication, or, may return an
	// AnonymousAuthentication if the authentication method doesn't indicate that the request is authenticated. An error
	// is only returned if a failure occurred while checking authentication.
	Verify(http *http.Request, w http.ResponseWriter, sess SessionStore) (AuthenticationResult, error)
}

// PasswordAuthenticator represents a source of authentication
type PasswordAuthenticator interface {
	Authenticate(ctx context.Context, user *user_model.User, login, password string) (*user_model.User, error)
}

// LocalTwoFASkipper represents a source of authentication that can skip local 2fa
type LocalTwoFASkipper interface {
	IsSkipLocalTwoFA() bool
}

// SynchronizableSource represents a source that can synchronize users
type SynchronizableSource interface {
	Sync(ctx context.Context, updateExisting bool) error
}

type AuthenticationResult interface {
	// May return `nil` to represent an anonymous, unauthenticated user.
	User() *user_model.User

	// Optional permission scope indicated by the authentication method
	Scope() optional.Option[auth_model.AccessTokenScope]
	// Optional authorization reducer indicated by the authentication method
	Reducer() authz.AuthorizationReducer

	// Identifies if the authentication involved the user's password. If so, and the user has 2FA enabled, some
	// restrictions may be applied.
	IsPasswordAuthentication() bool

	// Identifies if the authentication was performed by a reverse proxy.
	IsReverseProxyAuthentication() bool

	// Identifies specifically that the OAuth2 JWT authentication method was used. If so, some related OAuth2 API
	// endpoints may be accessible that otherwise wouldn't be.
	IsOAuth2JWTAuthentication() bool

	// If authenticated as an Actions task (using ${{ forgejo.token }}), then indicates the specific task that performed
	// the authentication.
	ActionsTaskID() optional.Option[int64]
}

type BaseAuthenticationResult struct{}

func (*BaseAuthenticationResult) IsOAuth2JWTAuthentication() bool {
	return false
}

func (*BaseAuthenticationResult) IsPasswordAuthentication() bool {
	return false
}

func (*BaseAuthenticationResult) IsReverseProxyAuthentication() bool {
	return false
}

func (*BaseAuthenticationResult) ActionsTaskID() optional.Option[int64] {
	return optional.None[int64]()
}

func (*BaseAuthenticationResult) Reducer() authz.AuthorizationReducer {
	return nil
}

func (*BaseAuthenticationResult) Scope() optional.Option[auth_model.AccessTokenScope] {
	return optional.None[auth_model.AccessTokenScope]()
}

type UnauthenticatedResult struct {
	*BaseAuthenticationResult
}

func (*UnauthenticatedResult) User() *user_model.User {
	return nil
}
