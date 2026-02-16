// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"fmt"
	"testing"

	"forgejo.org/models/unittest"
	"forgejo.org/services/contexttest"

	"github.com/stretchr/testify/assert"
)

func TestNewIssueValidateProject(t *testing.T) {
	unittest.PrepareTestEnv(t)

	for _, testCase := range []struct {
		name      string
		projectID int64
		userName  string
		userID    int64
		repoName  string
		repoID    int64
		isFound   bool
	}{
		{
			name:      "Project belongs to repository",
			projectID: 1,
			userName:  "user2",
			userID:    2,
			repoName:  "repo1",
			repoID:    1,
			isFound:   true,
		},
		{
			name:      "Project belongs to user",
			projectID: 4,
			userName:  "user2",
			userID:    2,
			repoName:  "repo1",
			repoID:    1,
			isFound:   true,
		},
		{
			name:      "Project belongs to org",
			projectID: 7,
			userName:  "org3",
			userID:    3,
			repoName:  "repo3",
			repoID:    3,
			isFound:   true,
		},
		{
			name:      "Project neither belongs to repo nor the user",
			projectID: 2,
			userName:  "user2",
			userID:    2,
			repoName:  "repo1",
			repoID:    1,
			isFound:   false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx, _ := contexttest.MockContext(
				t, fmt.Sprintf(
					"/%s/%s/issues/new?project=%d",
					testCase.userName,
					testCase.repoName,
					testCase.projectID,
				),
			)
			contexttest.LoadUser(t, ctx, testCase.userID)
			contexttest.LoadRepo(t, ctx, testCase.repoID)
			contexttest.LoadGitRepo(t, ctx)

			NewIssue(ctx)

			if testCase.isFound {
				assert.Equal(t, testCase.projectID, ctx.Data["project_id"])
				assert.NotNil(t, ctx.Data["Project"])
			} else {
				assert.Nil(t, ctx.Data["project_id"])
				assert.Nil(t, ctx.Data["Project"])
			}
		})
	}
}
