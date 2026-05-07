// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/modules/test"
	"github.com/CarriedWorldUniverse/cairn/tests"

	"github.com/stretchr/testify/assert"
)

func TestAdminConfig(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user1")
	req := NewRequest(t, "GET", "/admin/config")
	resp := session.MakeRequest(t, req, http.StatusOK)
	assert.True(t, test.IsNormalPageCompleted(resp.Body.String()))
}
