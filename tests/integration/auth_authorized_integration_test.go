// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"net/http"
	"testing"

	auth_model "forgejo.org/models/auth"
	api "forgejo.org/modules/structs"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
)

func TestAPIAuthWithAuthorizedIntegration(t *testing.T) {
	t.Run("all access token", func(t *testing.T) {
		defer tests.PrintCurrentTest(t)()

		ait := newAITester(t)
		defer ait.close()
		token := ait.signedJWT()

		req := NewRequest(t, "GET", "/api/v1/user").AddTokenAuth(token)
		resp := MakeRequest(t, req, http.StatusOK)
		var user api.User
		DecodeJSON(t, resp, &user)
		assert.Equal(t, "user2", user.LoginName)

		req = NewRequest(t, "GET", "/api/v1/repos/user2/repo1").AddTokenAuth(token)
		MakeRequest(t, req, http.StatusOK)
	})

	t.Run("scope-limited access token", func(t *testing.T) {
		defer tests.PrintCurrentTest(t)()

		ait := newAITester(t, func(ai *auth_model.AuthorizedIntegration) {
			ai.Scope = auth_model.AccessTokenScopeReadUser
		})
		defer ait.close()
		token := ait.signedJWT()

		req := NewRequest(t, "GET", "/api/v1/user").AddTokenAuth(token)
		resp := MakeRequest(t, req, http.StatusOK)
		var user api.User
		DecodeJSON(t, resp, &user)
		assert.Equal(t, "user2", user.LoginName)

		req = NewRequest(t, "GET", "/api/v1/repos/user2/repo1").AddTokenAuth(token)
		MakeRequest(t, req, http.StatusForbidden)
	})
}
