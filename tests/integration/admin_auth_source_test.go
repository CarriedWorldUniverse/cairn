// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later.

package integration

import (
	"fmt"
	"net/http"
	"testing"

	"forgejo.org/models/auth"
	"forgejo.org/tests"
)

func TestAdminAuthAllowUsernameChangeSetting(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user1")

	source := addAuthSource(t, map[string]string{
		"type":                  fmt.Sprintf("%d", auth.OAuth2),
		"name":                  "some-name",
		"is_active":             "on",
		"allow_username_change": "on",
		"oauth2_provider":       "gitlab",
	})

	response := session.MakeRequest(t, NewRequestf(t, "GET", "/admin/auths/%d", source.ID), http.StatusOK)
	htmlDoc := NewHTMLParser(t, response.Body)

	htmlDoc.AssertElement(t, "#allow_username_change[checked]", true)
}
