// Copyright The Forgejo Authors.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"testing"

	auth_model "github.com/CarriedWorldUniverse/cairn/models/auth"
	"github.com/CarriedWorldUniverse/cairn/modules/setting"
	"github.com/CarriedWorldUniverse/cairn/modules/test"
	"github.com/CarriedWorldUniverse/cairn/routers"
	v1 "github.com/CarriedWorldUniverse/cairn/routers/api/forgejo/v1"
	"github.com/CarriedWorldUniverse/cairn/tests"

	"github.com/stretchr/testify/assert"
)

func TestAPIForgejoVersion(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	t.Run("Version", func(t *testing.T) {
		req := NewRequest(t, "GET", "/api/forgejo/v1/version")
		resp := MakeRequest(t, req, http.StatusOK)

		var version v1.Version
		DecodeJSON(t, resp, &version)
		assert.Equal(t, "1.0.0", *version.Version)
	})

	t.Run("Version with Content-Type is json", func(t *testing.T) {
		req := NewRequest(t, "GET", "/api/forgejo/v1/version")
		resp := MakeRequest(t, req, http.StatusOK)

		assert.Equal(t, "application/json; charset=utf-8", resp.Header().Get("Content-Type"))
	})

	t.Run("Versions with REQUIRE_SIGNIN_VIEW enabled", func(t *testing.T) {
		defer test.MockVariableValue(&setting.Service.RequireSignInView, true)()
		defer test.MockVariableValue(&testWebRoutes, routers.NormalRoutes())()

		t.Run("Get forgejo version without auth", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()

			// GET api without auth
			req := NewRequest(t, "GET", "/api/forgejo/v1/version")
			MakeRequest(t, req, http.StatusForbidden)
		})

		t.Run("Get forgejo version without auth", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			username := "user1"
			session := loginUser(t, username)
			token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository)

			// GET api with auth
			req := NewRequest(t, "GET", "/api/forgejo/v1/version").AddTokenAuth(token)
			resp := MakeRequest(t, req, http.StatusOK)

			var version v1.Version
			DecodeJSON(t, resp, &version)
			assert.Equal(t, "1.0.0", *version.Version)
		})
	})
}
