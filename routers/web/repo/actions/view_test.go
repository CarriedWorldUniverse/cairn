// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package actions

import (
	"fmt"
	"html/template"
	"net/http"
	"testing"

	actions_model "forgejo.org/models/actions"
	repo_model "forgejo.org/models/repo"
	unittest "forgejo.org/models/unittest"
	"forgejo.org/modules/json"
	"forgejo.org/modules/web"
	"forgejo.org/services/contexttest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_getRunByID(t *testing.T) {
	unittest.PrepareTestEnv(t)

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{OwnerID: 5, ID: 4})

	for _, testCase := range []struct {
		name  string
		runID int64
		err   string
	}{
		{
			name:  "Found",
			runID: 792,
		},
		{
			name:  "NotFound",
			runID: 24344,
			err:   "no such run",
		},
		{
			name:  "ZeroNotFound",
			runID: 0,
			err:   "zero is not a valid run ID",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx, resp := contexttest.MockContext(t, fmt.Sprintf("user5/repo4/actions/runs/%v/artifacts/some-name", testCase.runID))
			ctx.Repo.Repository = repo
			run := getRunByID(ctx, testCase.runID)
			if testCase.err == "" {
				assert.NotNil(t, run)
				assert.False(t, ctx.Written(), resp.Body.String())
			} else {
				assert.Nil(t, run)
				assert.True(t, ctx.Written())
				assert.Contains(t, resp.Body.String(), testCase.err)
			}
		})
	}
}

func Test_artifactsFind(t *testing.T) {
	unittest.PrepareTestEnv(t)

	for _, testCase := range []struct {
		name         string
		artifactName string
		count        int
	}{
		{
			name:         "Found",
			artifactName: "artifact-v4-download",
			count:        1,
		},
		{
			name:         "NotFound",
			artifactName: "notexist",
			count:        0,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			runID := int64(792)
			ctx, _ := contexttest.MockContext(t, fmt.Sprintf("user5/repo4/actions/runs/%v/artifacts/%v", runID, testCase.artifactName))
			artifacts := artifactsFind(ctx, actions_model.FindArtifactsOptions{
				RunID:        runID,
				ArtifactName: testCase.artifactName,
			})
			assert.False(t, ctx.Written())
			assert.Len(t, artifacts, testCase.count)
		})
	}
}

func Test_artifactsFindByNameOrID(t *testing.T) {
	unittest.PrepareTestEnv(t)

	for _, testCase := range []struct {
		name     string
		nameOrID string
		err      string
	}{
		{
			name:     "NameFound",
			nameOrID: "artifact-v4-download",
		},
		{
			name:     "NameNotFound",
			nameOrID: "notexist",
			err:      "artifact name not found",
		},
		{
			name:     "IDFound",
			nameOrID: "22",
		},
		{
			name:     "IDNotFound",
			nameOrID: "666",
			err:      "artifact ID not found",
		},
		{
			name:     "IDZeroNotFound",
			nameOrID: "0",
			err:      "artifact name not found",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			runID := int64(792)
			ctx, resp := contexttest.MockContext(t, fmt.Sprintf("user5/repo4/actions/runs/%v/artifacts/%v", runID, testCase.nameOrID))
			artifacts := artifactsFindByNameOrID(ctx, runID, testCase.nameOrID)
			if testCase.err == "" {
				assert.NotEmpty(t, artifacts)
				assert.False(t, ctx.Written(), resp.Body.String())
			} else {
				assert.Empty(t, artifacts)
				assert.True(t, ctx.Written())
				assert.Contains(t, resp.Body.String(), testCase.err)
			}
		})
	}
}

func baseExpectedResponse() *ViewResponse {
	return &ViewResponse{
		State: ViewState{
			Run: ViewRunInfo{
				Link:              "/user5/repo4/actions/runs/187",
				Title:             "update actions",
				TitleHTML:         template.HTML("update actions"),
				Status:            "success",
				CanCancel:         false,
				CanApprove:        false,
				CanRerun:          false,
				CanDeleteArtifact: false,
				Done:              true,
				Jobs: []*ViewJob{
					{
						ID:       192,
						Name:     "job_2",
						Status:   "success",
						CanRerun: false,
						Duration: "1m38s",
					},
				},
				Commit: ViewCommit{
					LocaleCommit:   "actions.runs.commit",
					LocalePushedBy: "actions.runs.pushed_by",
					LocaleWorkflow: "actions.runs.workflow",
					ShortSha:       "c2d72f5484",
					Link:           "/user5/repo4/commit/c2d72f548424103f01ee1dc02889c1e2bff816b0",
					Pusher: ViewUser{
						DisplayName: "user1",
						Link:        "/user1",
					},
					Branch: ViewBranch{
						Name:      "master",
						Link:      "/user5/repo4/src/branch/master",
						IsDeleted: false,
					},
				},
			},
			CurrentJob: ViewCurrentJob{
				Title:  "job_2",
				Detail: "actions.status.success",
				Steps: []*ViewJobStep{
					{
						Summary: "Set up job",
						Status:  "running",
					},
					{
						Summary: "Complete job",
						Status:  "waiting",
					},
				},
			},
		},
		Logs: ViewLogs{
			StepsLog: []*ViewStepLog{},
		},
	}
}

func TestActionsViewViewPost(t *testing.T) {
	unittest.PrepareTestEnv(t)

	tests := []struct {
		name           string
		runIndex       int64
		jobIndex       int64
		expected       *ViewResponse
		expectedTweaks func(*ViewResponse)
	}{
		{
			name:     "base case",
			runIndex: 187,
			jobIndex: 0,
			expected: baseExpectedResponse(),
			expectedTweaks: func(resp *ViewResponse) {
			},
		},
		{
			name:     "run with waiting jobs",
			runIndex: 189,
			jobIndex: 0,
			expected: baseExpectedResponse(),
			expectedTweaks: func(resp *ViewResponse) {
				// Variations from runIndex 187 -> runIndex 189 that are not the subject of this test...
				resp.State.Run.Link = "/user5/repo4/actions/runs/189"
				resp.State.Run.Title = "job output"
				resp.State.Run.TitleHTML = "job output"
				resp.State.Run.Jobs = []*ViewJob{
					{
						ID:     194,
						Name:   "job1 (1)",
						Status: "success",
					},
					{
						ID:     195,
						Name:   "job1 (2)",
						Status: "success",
					},
					{
						ID:     196,
						Name:   "job2",
						Status: "waiting",
					},
				}
				resp.State.CurrentJob.Title = "job1 (1)"
				resp.State.CurrentJob.Steps = []*ViewJobStep{
					{
						Summary: "Set up job",
						Status:  "success",
					},
					{
						Summary: "Complete job",
						Status:  "success",
					},
				}

				// Under test in this case: verify that Done is set to false; in the fixture data, job.ID=195 is status
				// Success, but job.ID=196 is status Waiting, and so we expect to signal Done=false to indicate to the
				// UI to continue refreshing the page.
				resp.State.Run.Done = false
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, resp := contexttest.MockContext(t, "user2/repo1/actions/runs/0")
			contexttest.LoadUser(t, ctx, 2)
			contexttest.LoadRepo(t, ctx, 4)
			ctx.SetParams(":run", fmt.Sprintf("%d", tt.runIndex))
			ctx.SetParams(":job", fmt.Sprintf("%d", tt.jobIndex))
			web.SetForm(ctx, &ViewRequest{})

			ViewPost(ctx)
			require.Equal(t, http.StatusOK, resp.Result().StatusCode)

			var actual ViewResponse
			err := json.Unmarshal(resp.Body.Bytes(), &actual)
			require.NoError(t, err)

			// `Duration` field is dynamic based upon current time, so eliminate it from comparison -- but check that it
			// has the right format at least.
			zeroDurations := func(vr *ViewResponse) {
				for _, job := range vr.State.Run.Jobs {
					assert.Regexp(t, `^(\d+[hms]){1,3}$`, job.Duration)
					job.Duration = ""
				}
				for _, step := range vr.State.CurrentJob.Steps {
					step.Duration = ""
				}
			}
			zeroDurations(&actual)
			zeroDurations(tt.expected)
			tt.expectedTweaks(tt.expected)

			assert.Equal(t, *tt.expected, actual)
		})
	}
}
