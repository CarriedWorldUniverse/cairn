// Copyright 2024 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package private

import (
	"testing"

	"github.com/CarriedWorldUniverse/cairn/models/db"
	issues_model "github.com/CarriedWorldUniverse/cairn/models/issues"
	pull_model "github.com/CarriedWorldUniverse/cairn/models/pull"
	repo_model "github.com/CarriedWorldUniverse/cairn/models/repo"
	"github.com/CarriedWorldUniverse/cairn/models/unittest"
	user_model "github.com/CarriedWorldUniverse/cairn/models/user"
	"github.com/CarriedWorldUniverse/cairn/modules/private"
	repo_module "github.com/CarriedWorldUniverse/cairn/modules/repository"
	"github.com/CarriedWorldUniverse/cairn/services/contexttest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandlePullRequestMerging(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	pr, err := issues_model.GetUnmergedPullRequest(db.DefaultContext, 1, 1, "branch2", "master", issues_model.PullRequestFlowGithub)
	require.NoError(t, err)
	require.NoError(t, pr.LoadBaseRepo(db.DefaultContext))

	user1 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})

	err = pull_model.ScheduleAutoMerge(db.DefaultContext, user1, pr.ID, repo_model.MergeStyleSquash, "squash merge a pr", false)
	require.NoError(t, err)

	autoMerge := unittest.AssertExistsAndLoadBean(t, &pull_model.AutoMerge{PullID: pr.ID})

	ctx, resp := contexttest.MockPrivateContext(t, "/")
	handlePullRequestMerging(ctx, &private.HookOptions{
		PullRequestID: pr.ID,
		UserID:        2,
	}, pr.BaseRepo.OwnerName, pr.BaseRepo.Name, []*repo_module.PushUpdateOptions{
		{NewCommitID: "01234567"},
	})
	assert.Empty(t, resp.Body.String())
	pr, err = issues_model.GetPullRequestByID(db.DefaultContext, pr.ID)
	require.NoError(t, err)
	assert.True(t, pr.HasMerged)
	assert.Equal(t, "01234567", pr.MergedCommitID)

	unittest.AssertNotExistsBean(t, &pull_model.AutoMerge{ID: autoMerge.ID})
}
