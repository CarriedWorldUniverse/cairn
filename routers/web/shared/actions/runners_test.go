// Copyright 2025 The Forgejo Authors.
// SPDX-License-Identifier: GPL-3.0-or-later

package actions

import (
	"net/http"
	"testing"

	actions_model "forgejo.org/models/actions"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/services/contexttest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunnerDetails(t *testing.T) {
	defer unittest.OverrideFixtures("routers/web/shared/actions/fixtures/TestRunnerDetails")()
	require.NoError(t, unittest.PrepareTestDatabase())

	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	runner := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{ID: 1004})

	t.Run("permission denied", func(t *testing.T) {
		ctx, resp := contexttest.MockContext(t, "/admin/actions/runners")
		RunnerDetails(ctx, runner.ID, user.ID, 0, "admin/runners/details", 1)
		assert.Equal(t, http.StatusOK, resp.Code)
		assert.Empty(t, ctx.GetData()["Tasks"])
	})

	t.Run("first page", func(t *testing.T) {
		ctx, resp := contexttest.MockContext(t, "/admin/actions/runners")
		page := 1
		RunnerDetails(ctx, runner.ID, 0, 0, "admin/runners/details", page)
		require.Equal(t, http.StatusOK, resp.Code)
		assert.Len(t, ctx.GetData()["Tasks"], 30)
	})

	t.Run("second and last page", func(t *testing.T) {
		ctx, resp := contexttest.MockContext(t, "/admin/actions/runners")
		page := 2
		RunnerDetails(ctx, runner.ID, 0, 0, "admin/runners/details", page)
		require.Equal(t, http.StatusOK, resp.Code)
		assert.Len(t, ctx.GetData()["Tasks"], 10)
	})
}
