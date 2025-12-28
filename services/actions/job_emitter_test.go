// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"slices"
	"testing"

	actions_model "forgejo.org/models/actions"
	"forgejo.org/models/db"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/test"

	"code.forgejo.org/forgejo/runner/v12/act/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
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
		{
			name: "unblocked workflow call outer job with success",
			jobs: actions_model.ActionJobList{
				{ID: 1, JobID: "job1.innerjob1", Status: actions_model.StatusSuccess, Needs: []string{}},
				{ID: 2, JobID: "job1.innerjob2", Status: actions_model.StatusSuccess, Needs: []string{}},
				{ID: 3, JobID: "job1", Status: actions_model.StatusBlocked, Needs: []string{"job1.innerjob1", "job1.innerjob2"}, WorkflowPayload: []byte(
					`
name: test
on: push
jobs:
  job2:
    if: false
    uses: ./.forgejo/workflows/reusable.yml
__metadata:
  workflow_call_id: b5a9f46f1f2513d7777fde50b169d323a6519e349cc175484c947ac315a209ed
`)},
			},
			want: map[int64]actions_model.Status{
				3: actions_model.StatusSuccess,
			},
		},
		{
			name: "unblocked workflow call outer job with internal failure",
			jobs: actions_model.ActionJobList{
				{ID: 1, JobID: "job1.innerjob1", Status: actions_model.StatusSuccess, Needs: []string{}},
				{ID: 2, JobID: "job1.innerjob2", Status: actions_model.StatusFailure, Needs: []string{}},
				{ID: 3, JobID: "job1", Status: actions_model.StatusBlocked, Needs: []string{"job1.innerjob1", "job1.innerjob2"}, WorkflowPayload: []byte(
					`
name: test
on: push
jobs:
  job2:
    if: false
    uses: ./.forgejo/workflows/reusable.yml
__metadata:
  workflow_call_id: b5a9f46f1f2513d7777fde50b169d323a6519e349cc175484c947ac315a209ed
`)},
			},
			want: map[int64]actions_model.Status{
				3: actions_model.StatusFailure,
			},
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
	// Shouldn't get any decoding errors during this test -- pop them up from a log warning to a test fatal error.
	defer test.MockVariableValue(&model.OnDecodeNodeError, func(node yaml.Node, out any, err error) {
		t.Fatalf("Failed to decode node %v into %T: %v", node, out, err)
	})()

	tests := []struct {
		name                     string
		runJobID                 int64
		errContains              string
		consumed                 bool
		runJobNames              []string
		preExecutionError        actions_model.PreExecutionError
		preExecutionErrorDetails []any
		runsOn                   map[string][]string
	}{
		{
			name:     "not incomplete",
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
			name:                     "missing needs for strategy.matrix evaluation",
			runJobID:                 605,
			preExecutionError:        actions_model.ErrorCodeIncompleteMatrixMissingJob,
			preExecutionErrorDetails: []any{"job_1", "define-matrix-2", "define-matrix-1"},
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
		{
			name:                     "missing needs output for strategy.matrix evaluation",
			runJobID:                 615,
			preExecutionError:        actions_model.ErrorCodeIncompleteMatrixMissingOutput,
			preExecutionErrorDetails: []any{"job_1", "define-matrix-1", "colours-intentional-mistake"},
		},
		{
			name:     "runs-on evaluation with needs",
			runJobID: 617,
			consumed: true,
			runJobNames: []string{
				"consume-runs-on",
				"define-runs-on",
			},
			runsOn: map[string][]string{
				"define-runs-on":  {"fedora"},
				"consume-runs-on": {"nixos-25.11"},
			},
		},
		{
			name:     "runs-on evaluation with needs dynamic matrix",
			runJobID: 619,
			consumed: true,
			runJobNames: []string{
				"consume-runs-on (site-a, 12.x, 17)",
				"consume-runs-on (site-a, 12.x, 18)",
				"consume-runs-on (site-a, 14.x, 17)",
				"consume-runs-on (site-a, 14.x, 18)",
				"consume-runs-on (site-b, 12.x, 17)",
				"consume-runs-on (site-b, 12.x, 18)",
				"consume-runs-on (site-b, 14.x, 17)",
				"consume-runs-on (site-b, 14.x, 18)",
				"define-matrix",
			},
			runsOn: map[string][]string{
				"consume-runs-on (site-a, 12.x, 17)": {"node-12.x"},
				"consume-runs-on (site-a, 12.x, 18)": {"node-12.x"},
				"consume-runs-on (site-a, 14.x, 17)": {"node-14.x"},
				"consume-runs-on (site-a, 14.x, 18)": {"node-14.x"},
				"consume-runs-on (site-b, 12.x, 17)": {"node-12.x"},
				"consume-runs-on (site-b, 12.x, 18)": {"node-12.x"},
				"consume-runs-on (site-b, 14.x, 17)": {"node-14.x"},
				"consume-runs-on (site-b, 14.x, 18)": {"node-14.x"},
				"define-matrix":                      {"fedora"},
			},
		},
		{
			name:     "runs-on evaluation to part of array",
			runJobID: 621,
			consumed: true,
			runJobNames: []string{
				"consume-runs-on",
				"define-runs-on",
			},
			runsOn: map[string][]string{
				"define-runs-on": {"fedora"},
				"consume-runs-on": {
					"datacenter-alpha",
					"nixos-25.11",
					"node-27.x",
				},
			},
		},
		{
			name:                     "missing needs job for runs-on evaluation",
			runJobID:                 623,
			preExecutionError:        actions_model.ErrorCodeIncompleteRunsOnMissingJob,
			preExecutionErrorDetails: []any{"consume-runs-on", "oops-i-misspelt-the-job-id", "define-runs-on, another-needs"},
		},
		{
			name:                     "missing needs output for runs-on evaluation",
			runJobID:                 625,
			preExecutionError:        actions_model.ErrorCodeIncompleteRunsOnMissingOutput,
			preExecutionErrorDetails: []any{"consume-runs-on", "define-runs-on", "output-doesnt-exist"},
		},
		{
			name:                     "missing matrix dimension for runs-on evaluation",
			runJobID:                 627,
			preExecutionError:        actions_model.ErrorCodeIncompleteRunsOnMissingMatrixDimension,
			preExecutionErrorDetails: []any{"consume-runs-on", "dimension-oops-error"},
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
					assert.EqualValues(t, 0, actionRun.PreExecutionErrorCode)

					// compare jobs that exist with `runJobNames` to ensure new jobs are inserted:
					allJobsInRun, err := db.Find[actions_model.ActionRunJob](t.Context(), actions_model.FindRunJobOptions{RunID: blockedJob.RunID})
					require.NoError(t, err)
					allJobNames := []string{}
					for _, j := range allJobsInRun {
						allJobNames = append(allJobNames, j.Name)
					}
					slices.Sort(allJobNames)
					assert.Equal(t, tt.runJobNames, allJobNames)

					// Check the runs-on of all jobs
					if tt.runsOn != nil {
						for _, j := range allJobsInRun {
							expected, ok := tt.runsOn[j.Name]
							if assert.Truef(t, ok, "unable to find runsOn[%q] in test case", j.Name) {
								slices.Sort(j.RunsOn)
								slices.Sort(expected)
								assert.Equalf(t, expected, j.RunsOn, "comparing runsOn expectations for job %q", j.Name)
							}
						}
					}
				} else if tt.preExecutionError != 0 {
					// expectations are that the ActionRun has a populated PreExecutionError, is marked as failed
					actionRun := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: blockedJob.RunID})
					assert.Equal(t, tt.preExecutionError, actionRun.PreExecutionErrorCode)
					assert.Equal(t, tt.preExecutionErrorDetails, actionRun.PreExecutionErrorDetails)
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

func Test_tryHandleWorkflowCallOuterJob(t *testing.T) {
	tests := []struct {
		name         string
		runJobID     int64
		updateFields []string
		outputs      map[string]string
	}{
		{
			name:     "not workflow call outer job",
			runJobID: 600,
		},
		{
			name:         "outputs for every context",
			runJobID:     601,
			updateFields: []string{"task_id"},
			outputs: map[string]string{
				"from_inner_job":        "abcdefghijklmnopqrstuvwxyz",
				"from_inner_job_result": "success",
				"from_forgejo_ctx":      "refs/heads/main",
				"from_input_ctx":        "hello, world!",
				"from_vars_repo":        "this is a repo variable",
				"from_vars_org":         "this is an org variable",
				"from_vars_global":      "this is a global variable",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer unittest.OverrideFixtures("services/actions/Test_tryHandleWorkflowCallOuterJob")()
			require.NoError(t, unittest.PrepareTestDatabase())

			outerJob := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: tt.runJobID})
			require.EqualValues(t, 0, outerJob.TaskID)

			updateFields, err := tryHandleWorkflowCallOuterJob(t.Context(), outerJob)
			require.NoError(t, err)
			assert.Equal(t, tt.updateFields, updateFields)

			if tt.updateFields != nil {
				// TaskID expected to be set by tryHandleWorkflowCallOuterJob
				require.NotEqualValues(t, 0, outerJob.TaskID)

				taskOutputs, err := actions_model.FindTaskOutputByTaskID(t.Context(), outerJob.TaskID)
				require.NoError(t, err)
				outputMap := map[string]string{}
				for _, to := range taskOutputs {
					outputMap[to.OutputKey] = to.OutputValue
				}
				assert.Equal(t, tt.outputs, outputMap)
			}
		})
	}
}
