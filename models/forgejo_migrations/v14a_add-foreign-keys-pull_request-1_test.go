// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package forgejo_migrations

import (
	"testing"

	"github.com/CarriedWorldUniverse/cairn/models/db"
	migration_tests "github.com/CarriedWorldUniverse/cairn/models/gitea_migrations/test"
	"github.com/CarriedWorldUniverse/cairn/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_addForeignKeysPullRequest1(t *testing.T) {
	type PullRequestType int
	type PullRequestStatus int
	type PullRequestFlow int
	type PullRequest struct {
		ID                    int64 `xorm:"pk autoincr"`
		Type                  PullRequestType
		Status                PullRequestStatus
		ConflictedFiles       []string `xorm:"TEXT JSON"`
		CommitsAhead          int
		CommitsBehind         int
		ChangedProtectedFiles []string `xorm:"TEXT JSON"`
		IssueID               int64    `xorm:"INDEX"`
		Index                 int64
		HeadRepoID            int64 `xorm:"INDEX"`
		BaseRepoID            int64 `xorm:"INDEX"`
		HeadBranch            string
		BaseBranch            string
		MergeBase             string             `xorm:"VARCHAR(64)"`
		AllowMaintainerEdit   bool               `xorm:"NOT NULL DEFAULT false"`
		HasMerged             bool               `xorm:"INDEX"`
		MergedCommitID        string             `xorm:"VARCHAR(64)"`
		MergerID              int64              `xorm:"INDEX"`
		MergedUnix            timeutil.TimeStamp `xorm:"updated INDEX"`
		Flow                  PullRequestFlow    `xorm:"NOT NULL DEFAULT 0"`
	}
	type Repository struct {
		ID int64 `xorm:"pk autoincr"`
	}
	type Issue struct {
		ID int64 `xorm:"pk autoincr"`
	}
	x, deferable := migration_tests.PrepareTestEnv(t, 0, new(Issue), new(Repository), new(PullRequest))
	defer deferable()
	if x == nil || t.Failed() {
		return
	}

	require.NoError(t, addForeignKeysPullRequest1(x))

	var remainingRecords []*PullRequest
	require.NoError(t,
		db.GetEngine(t.Context()).
			Table("pull_request").
			Select("`id`, `issue_id`, `base_repo_id`").
			OrderBy("`id`").
			Find(&remainingRecords))
	assert.Equal(t,
		[]*PullRequest{
			{ID: 1, BaseRepoID: 1, IssueID: 1},
		},
		remainingRecords)
}
