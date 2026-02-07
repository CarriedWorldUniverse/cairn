// SPDX-License-Identifier: MIT

package actions

import (
	"fmt"
	"testing"

	"forgejo.org/models/db"
	"forgejo.org/models/unittest"

	"code.forgejo.org/forgejo/runner/v12/act/jobparser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActionRunJob_ItRunsOn(t *testing.T) {
	actionJob := ActionRunJob{RunsOn: []string{"ubuntu"}}
	agentLabels := []string{"ubuntu", "node-20"}

	assert.True(t, actionJob.ItRunsOn(agentLabels))
	assert.False(t, actionJob.ItRunsOn([]string{}))

	actionJob.RunsOn = append(actionJob.RunsOn, "node-20")

	assert.True(t, actionJob.ItRunsOn(agentLabels))

	agentLabels = []string{"ubuntu"}

	assert.False(t, actionJob.ItRunsOn(agentLabels))

	actionJob.RunsOn = []string{}

	assert.False(t, actionJob.ItRunsOn(agentLabels))
}

func TestActionRunJob_HTMLURL(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	tests := []struct {
		id       int64
		expected string
	}{
		{
			id:       192,
			expected: "https://try.gitea.io/user5/repo4/actions/runs/187/jobs/0/attempt/1",
		},
		{
			id:       393,
			expected: "https://try.gitea.io/user2/repo1/actions/runs/187/jobs/1/attempt/1",
		},
		{
			id:       394,
			expected: "https://try.gitea.io/user2/repo1/actions/runs/187/jobs/2/attempt/2",
		},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("id=%d", tt.id), func(t *testing.T) {
			var job ActionRunJob
			has, err := db.GetEngine(t.Context()).Where("id=?", tt.id).Get(&job)
			require.NoError(t, err)
			require.True(t, has, "load ActionRunJob from fixture")

			err = job.LoadAttributes(t.Context())
			require.NoError(t, err)

			url, err := job.HTMLURL(t.Context())
			require.NoError(t, err)
			assert.Equal(t, tt.expected, url)
		})
	}
}

func TestActionRunJob_HasIncompleteMatrix(t *testing.T) {
	tests := []struct {
		name         string
		job          ActionRunJob
		isIncomplete bool
		needs        *jobparser.IncompleteNeeds
		errContains  string
	}{
		{
			name:         "normal workflow",
			job:          ActionRunJob{WorkflowPayload: []byte("name: workflow")},
			isIncomplete: false,
		},
		{
			name:         "incomplete_matrix workflow",
			job:          ActionRunJob{WorkflowPayload: []byte("name: workflow\nincomplete_matrix: true\nincomplete_matrix_needs: { job: abc }")},
			needs:        &jobparser.IncompleteNeeds{Job: "abc"},
			isIncomplete: true,
		},
		{
			name:        "unparseable workflow",
			job:         ActionRunJob{WorkflowPayload: []byte("name: []\nincomplete_matrix: true")},
			errContains: "failure unmarshaling WorkflowPayload to SingleWorkflow: yaml: unmarshal errors",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isIncomplete, needs, err := tt.job.HasIncompleteMatrix()
			if tt.errContains != "" {
				assert.ErrorContains(t, err, tt.errContains)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.isIncomplete, isIncomplete)
				assert.Equal(t, tt.needs, needs)
			}
		})
	}
}

func TestActionRunJob_HasIncompleteRunsOn(t *testing.T) {
	tests := []struct {
		name         string
		job          ActionRunJob
		isIncomplete bool
		needs        *jobparser.IncompleteNeeds
		matrix       *jobparser.IncompleteMatrix
		errContains  string
	}{
		{
			name:         "normal workflow",
			job:          ActionRunJob{WorkflowPayload: []byte("name: workflow")},
			isIncomplete: false,
		},
		{
			name:         "nincomplete_runs_on workflow",
			job:          ActionRunJob{WorkflowPayload: []byte("name: workflow\nincomplete_runs_on: true\nincomplete_runs_on_needs: { job: abc }")},
			needs:        &jobparser.IncompleteNeeds{Job: "abc"},
			isIncomplete: true,
		},
		{
			name:         "nincomplete_runs_on workflow",
			job:          ActionRunJob{WorkflowPayload: []byte("name: workflow\nincomplete_runs_on: true\nincomplete_runs_on_matrix: { dimension: abc }")},
			matrix:       &jobparser.IncompleteMatrix{Dimension: "abc"},
			isIncomplete: true,
		},
		{
			name:        "unparseable workflow",
			job:         ActionRunJob{WorkflowPayload: []byte("name: []\nincomplete_runs_on: true")},
			errContains: "failure unmarshaling WorkflowPayload to SingleWorkflow: yaml: unmarshal errors",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isIncomplete, needs, matrix, err := tt.job.HasIncompleteRunsOn()
			if tt.errContains != "" {
				assert.ErrorContains(t, err, tt.errContains)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.isIncomplete, isIncomplete)
				assert.Equal(t, tt.needs, needs)
				assert.Equal(t, tt.matrix, matrix)
			}
		})
	}
}

func TestActionRunJob_IsWorkflowCallOuterJob(t *testing.T) {
	tests := []struct {
		name                   string
		job                    ActionRunJob
		isWorkflowCallOuterJob bool
		errContains            string
	}{
		{
			name:                   "normal workflow",
			job:                    ActionRunJob{WorkflowPayload: []byte("name: workflow")},
			isWorkflowCallOuterJob: false,
		},
		{
			name:                   "workflow_call outer job",
			job:                    ActionRunJob{WorkflowPayload: []byte("name: test\njobs:\n  job:\n    if: false\n__metadata:\n  workflow_call_id: b5a9f46f1f2513d7777fde50b169d323a6519e349cc175484c947ac315a209ed\n")},
			isWorkflowCallOuterJob: true,
		},
		{
			name:        "unparseable workflow",
			job:         ActionRunJob{WorkflowPayload: []byte("name: []\nincomplete_runs_on: true")},
			errContains: "failure unmarshaling WorkflowPayload to SingleWorkflow: yaml: unmarshal errors",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isWorkflowCallOuterJob, err := tt.job.IsWorkflowCallOuterJob()
			if tt.errContains != "" {
				assert.ErrorContains(t, err, tt.errContains)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.isWorkflowCallOuterJob, isWorkflowCallOuterJob)
			}
		})
	}
}

func TestActionRunJob_IsWorkflowCallInnerJob(t *testing.T) {
	tests := []struct {
		name                   string
		job                    ActionRunJob
		isWorkflowCallInnerJob bool
		errContains            string
	}{
		{
			name:                   "normal workflow",
			job:                    ActionRunJob{WorkflowPayload: []byte("on: [workflow_dispatch]\nname: workflow")},
			isWorkflowCallInnerJob: false,
		},
		{
			name:                   "inner job",
			job:                    ActionRunJob{WorkflowPayload: []byte("on:\n  workflow_call:\nname: workflow\n__metadata:\n  workflow_call_parent: b5a9f46f1f2513d7777fde50b169d323a6519e349cc175484c947ac315a209ed\n")},
			isWorkflowCallInnerJob: true,
		},
		{
			name:        "unparseable workflow",
			job:         ActionRunJob{WorkflowPayload: []byte("name: []\nincomplete_runs_on: true")},
			errContains: "failure unmarshaling WorkflowPayload to SingleWorkflow: yaml: unmarshal errors",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isWorkflowCallInnerJob, err := tt.job.IsWorkflowCallInnerJob()
			if tt.errContains != "" {
				assert.ErrorContains(t, err, tt.errContains)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.isWorkflowCallInnerJob, isWorkflowCallInnerJob)
			}
		})
	}
}

func TestActionRunJob_HasIncompleteWith(t *testing.T) {
	tests := []struct {
		name         string
		job          ActionRunJob
		isIncomplete bool
		needs        *jobparser.IncompleteNeeds
		matrix       *jobparser.IncompleteMatrix
		errContains  string
	}{
		{
			name:         "normal workflow",
			job:          ActionRunJob{WorkflowPayload: []byte("name: workflow")},
			isIncomplete: false,
		},
		{
			name:         "incomplete_with workflow",
			job:          ActionRunJob{WorkflowPayload: []byte("name: workflow\nincomplete_with: true\nincomplete_with_needs: { job: abc }")},
			needs:        &jobparser.IncompleteNeeds{Job: "abc"},
			isIncomplete: true,
		},
		{
			name:         "incomplete_with workflow",
			job:          ActionRunJob{WorkflowPayload: []byte("name: workflow\nincomplete_with: true\nincomplete_with_matrix: { dimension: abc }")},
			matrix:       &jobparser.IncompleteMatrix{Dimension: "abc"},
			isIncomplete: true,
		},
		{
			name:        "unparseable workflow",
			job:         ActionRunJob{WorkflowPayload: []byte("name: []\nincomplete_with: true")},
			errContains: "failure unmarshaling WorkflowPayload to SingleWorkflow: yaml: unmarshal errors",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isIncomplete, needs, matrix, err := tt.job.HasIncompleteWith()
			if tt.errContains != "" {
				assert.ErrorContains(t, err, tt.errContains)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.isIncomplete, isIncomplete)
				assert.Equal(t, tt.needs, needs)
				assert.Equal(t, tt.matrix, matrix)
			}
		})
	}
}

func TestRunHasOtherJobs(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	jobs, err := GetRunJobsByRunID(t.Context(), 791)
	require.NoError(t, err)
	assert.Len(t, jobs, 1)

	has, err := RunHasOtherJobs(t.Context(), 791, nil)
	require.NoError(t, err)
	assert.True(t, has)

	has, err = RunHasOtherJobs(t.Context(), 791, []*ActionRunJob{})
	require.NoError(t, err)
	assert.True(t, has)

	has, err = RunHasOtherJobs(t.Context(), 791, jobs)
	require.NoError(t, err)
	assert.False(t, has)
}
