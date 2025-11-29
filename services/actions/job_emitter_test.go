// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"slices"
	"testing"

	actions_model "forgejo.org/models/actions"
	"forgejo.org/models/db"
	"forgejo.org/models/unittest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_jobStatusResolver_Resolve(t *testing.T) {
	tests := []struct {
		name string
		jobs actions_model.ActionJobList
		want map[int64]actions_model.Status
	}{
		{
			name: "no blocked",
			jobs: actions_model.ActionJobList{
				{ID: 1, JobID: "1", Status: actions_model.StatusWaiting, Needs: []string{}},
				{ID: 2, JobID: "2", Status: actions_model.StatusWaiting, Needs: []string{}},
				{ID: 3, JobID: "3", Status: actions_model.StatusWaiting, Needs: []string{}},
			},
			want: map[int64]actions_model.Status{},
		},
		{
			name: "single blocked",
			jobs: actions_model.ActionJobList{
				{ID: 1, JobID: "1", Status: actions_model.StatusSuccess, Needs: []string{}},
				{ID: 2, JobID: "2", Status: actions_model.StatusBlocked, Needs: []string{"1"}},
				{ID: 3, JobID: "3", Status: actions_model.StatusWaiting, Needs: []string{}},
			},
			want: map[int64]actions_model.Status{
				2: actions_model.StatusWaiting,
			},
		},
		{
			name: "multiple blocked",
			jobs: actions_model.ActionJobList{
				{ID: 1, JobID: "1", Status: actions_model.StatusSuccess, Needs: []string{}},
				{ID: 2, JobID: "2", Status: actions_model.StatusBlocked, Needs: []string{"1"}},
				{ID: 3, JobID: "3", Status: actions_model.StatusBlocked, Needs: []string{"1"}},
			},
			want: map[int64]actions_model.Status{
				2: actions_model.StatusWaiting,
				3: actions_model.StatusWaiting,
			},
		},
		{
			name: "chain blocked",
			jobs: actions_model.ActionJobList{
				{ID: 1, JobID: "1", Status: actions_model.StatusFailure, Needs: []string{}},
				{ID: 2, JobID: "2", Status: actions_model.StatusBlocked, Needs: []string{"1"}},
				{ID: 3, JobID: "3", Status: actions_model.StatusBlocked, Needs: []string{"2"}},
			},
			want: map[int64]actions_model.Status{
				2: actions_model.StatusSkipped,
				3: actions_model.StatusSkipped,
			},
		},
		{
			name: "loop need",
			jobs: actions_model.ActionJobList{
				{ID: 1, JobID: "1", Status: actions_model.StatusBlocked, Needs: []string{"3"}},
				{ID: 2, JobID: "2", Status: actions_model.StatusBlocked, Needs: []string{"1"}},
				{ID: 3, JobID: "3", Status: actions_model.StatusBlocked, Needs: []string{"2"}},
			},
			want: map[int64]actions_model.Status{},
		},
		{
			name: "`if` is not empty and all jobs in `needs` completed successfully",
			jobs: actions_model.ActionJobList{
				{ID: 1, JobID: "job1", Status: actions_model.StatusSuccess, Needs: []string{}},
				{ID: 2, JobID: "job2", Status: actions_model.StatusBlocked, Needs: []string{"job1"}, WorkflowPayload: []byte(
					`
name: test
on: push
jobs:
  job2:
    runs-on: ubuntu-latest
    needs: job1
    if: ${{ always() && needs.job1.result == 'success' }}
    steps:
      - run: echo "will be checked by act_runner"
`)},
			},
			want: map[int64]actions_model.Status{2: actions_model.StatusWaiting},
		},
		{
			name: "`if` is not empty and not all jobs in `needs` completed successfully",
			jobs: actions_model.ActionJobList{
				{ID: 1, JobID: "job1", Status: actions_model.StatusFailure, Needs: []string{}},
				{ID: 2, JobID: "job2", Status: actions_model.StatusBlocked, Needs: []string{"job1"}, WorkflowPayload: []byte(
					`
name: test
on: push
jobs:
  job2:
    runs-on: ubuntu-latest
    needs: job1
    if: ${{ always() && needs.job1.result == 'failure' }}
    steps:
      - run: echo "will be checked by act_runner"
`)},
			},
			want: map[int64]actions_model.Status{2: actions_model.StatusWaiting},
		},
		{
			name: "`if` is empty and not all jobs in `needs` completed successfully",
			jobs: actions_model.ActionJobList{
				{ID: 1, JobID: "job1", Status: actions_model.StatusFailure, Needs: []string{}},
				{ID: 2, JobID: "job2", Status: actions_model.StatusBlocked, Needs: []string{"job1"}, WorkflowPayload: []byte(
					`
name: test
on: push
jobs:
  job2:
    runs-on: ubuntu-latest
    needs: job1
    steps:
      - run: echo "should be skipped"
`)},
			},
			want: map[int64]actions_model.Status{2: actions_model.StatusSkipped},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newJobStatusResolver(tt.jobs)
			assert.Equal(t, tt.want, r.Resolve())
		})
	}
}

func Test_tryHandleIncompleteMatrix(t *testing.T) {
	tests := []struct {
		name              string
		runJobID          int64
		errContains       string
		consumed          bool
		runJobNames       []string
		preExecutionError string
	}{
		{
			name:     "not incomplete_matrix",
			runJobID: 600,
		},
		{
			name:        "matrix expanded to 3 new jobs",
			runJobID:    601,
			consumed:    true,
			runJobNames: []string{"define-matrix", "produce-artifacts (blue)", "produce-artifacts (green)", "produce-artifacts (red)"},
		},
		{
			name:        "needs an incomplete job",
			runJobID:    603,
			errContains: "jobStatusResolver attempted to tryHandleIncompleteMatrix for a job (id=603) with an incomplete 'needs' job (id=604)",
		},
		{
			name:              "missing needs for strategy.matrix evaluation",
			runJobID:          605,
			preExecutionError: "Unable to evaluate `strategy.matrix` of job job_1 due to a `needs` expression that was invalid. It may reference a job that is not in it's 'needs' list (define-matrix-1), or an output that doesn't exist on one of those jobs.",
		},
		{
			name:        "matrix expanded to 0 jobs",
			runJobID:    607,
			consumed:    true,
			runJobNames: []string{"define-matrix"},
		},
		{
			name:     "matrix multiple dimensions from separate outputs",
			runJobID: 609,
			consumed: true,
			runJobNames: []string{
				"define-matrix",
				"run-tests (site-a, 12.x, 17)",
				"run-tests (site-a, 12.x, 18)",
				"run-tests (site-a, 14.x, 17)",
				"run-tests (site-a, 14.x, 18)",
				"run-tests (site-b, 12.x, 17)",
				"run-tests (site-b, 12.x, 18)",
				"run-tests (site-b, 14.x, 17)",
				"run-tests (site-b, 14.x, 18)",
			},
		},
		{
			name:     "matrix multiple dimensions from one output",
			runJobID: 611,
			consumed: true,
			runJobNames: []string{
				"define-matrix",
				"run-tests (site-a, 12.x, 17)",
				"run-tests (site-a, 12.x, 18)",
				"run-tests (site-a, 14.x, 17)",
				"run-tests (site-a, 14.x, 18)",
				"run-tests (site-b, 12.x, 17)",
				"run-tests (site-b, 12.x, 18)",
				"run-tests (site-b, 14.x, 17)",
				"run-tests (site-b, 14.x, 18)",
			},
		},
		{
			// This test case also includes `on: [push]` in the workflow_payload, which appears to trigger a regression
			// in go.yaml.in/yaml/v4 v4.0.0-rc.2 (which I had accidentally referenced in job_emitter.go), and so serves
			// as a regression prevention test for this case...
			//
			// unmarshal WorkflowPayload to SingleWorkflow failed: yaml: unmarshal errors: line 1: cannot unmarshal
			// !!seq into yaml.Node
			name:     "scalar expansion into matrix",
			runJobID: 613,
			consumed: true,
			runJobNames: []string{
				"define-matrix",
				"scalar-job (hard-coded value)",
				"scalar-job (just some value)",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer unittest.OverrideFixtures("services/actions/Test_tryHandleIncompleteMatrix")()
			require.NoError(t, unittest.PrepareTestDatabase())

			blockedJob := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: tt.runJobID})

			jobsInRun, err := db.Find[actions_model.ActionRunJob](t.Context(), actions_model.FindRunJobOptions{RunID: blockedJob.RunID})
			require.NoError(t, err)

			skip, err := tryHandleIncompleteMatrix(t.Context(), blockedJob, jobsInRun)

			if tt.errContains != "" {
				require.ErrorContains(t, err, tt.errContains)
			} else {
				require.NoError(t, err)
				if tt.consumed {
					assert.True(t, skip, "skip flag")

					// blockedJob should no longer exist in the database
					unittest.AssertNotExistsBean(t, &actions_model.ActionRunJob{ID: tt.runJobID})

					// expectations are that the ActionRun has an empty PreExecutionError
					actionRun := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: blockedJob.RunID})
					assert.Empty(t, actionRun.PreExecutionError)

					// compare jobs that exist with `runJobNames` to ensure new jobs are inserted:
					allJobsInRun, err := db.Find[actions_model.ActionRunJob](t.Context(), actions_model.FindRunJobOptions{RunID: blockedJob.RunID})
					require.NoError(t, err)
					allJobNames := []string{}
					for _, j := range allJobsInRun {
						allJobNames = append(allJobNames, j.Name)
					}
					slices.Sort(allJobNames)
					assert.Equal(t, tt.runJobNames, allJobNames)
				} else if tt.preExecutionError != "" {
					// expectations are that the ActionRun has a populated PreExecutionError, is marked as failed
					actionRun := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: blockedJob.RunID})
					assert.Equal(t, tt.preExecutionError, actionRun.PreExecutionError)
					assert.Equal(t, actions_model.StatusFailure, actionRun.Status)

					// ActionRunJob is marked as failed
					blockedJobReloaded := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: tt.runJobID})
					assert.Equal(t, actions_model.StatusFailure, blockedJobReloaded.Status)

					// skip is set to true
					assert.True(t, skip, "skip flag")
				} else {
					assert.False(t, skip, "skip flag")
				}
			}
		})
	}
}
