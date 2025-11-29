// SPDX-License-Identifier: MIT

package actions

import (
	"fmt"
	"html/template"
	"testing"

	"forgejo.org/models/db"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/translation"

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

func TestActionRunJob_StatusDiagnostics(t *testing.T) {
	translation.InitLocales(t.Context())
	english := translation.NewLocale("en-US")

	tests := []struct {
		name     string
		job      ActionRunJob
		expected []template.HTML
	}{
		{
			name:     "Unknown status",
			job:      ActionRunJob{RunsOn: []string{"windows"}, Status: StatusUnknown, Run: &ActionRun{NeedApproval: false}},
			expected: []template.HTML{"Unknown"},
		},
		{
			name:     "Waiting without labels",
			job:      ActionRunJob{RunsOn: []string{}, Status: StatusWaiting, Run: &ActionRun{NeedApproval: false}},
			expected: []template.HTML{"Waiting for a runner with the following labels: "},
		},
		{
			name:     "Waiting with one label",
			job:      ActionRunJob{RunsOn: []string{"freebsd"}, Status: StatusWaiting, Run: &ActionRun{NeedApproval: false}},
			expected: []template.HTML{"Waiting for a runner with the following label: freebsd"},
		},
		{
			name:     "Waiting with labels, no approval",
			job:      ActionRunJob{RunsOn: []string{"docker", "ubuntu"}, Status: StatusWaiting, Run: &ActionRun{NeedApproval: false}},
			expected: []template.HTML{"Waiting for a runner with the following labels: docker, ubuntu"},
		},
		{
			name: "Waiting with labels, approval",
			job:  ActionRunJob{RunsOn: []string{"docker", "ubuntu"}, Status: StatusWaiting, Run: &ActionRun{NeedApproval: true}},
			expected: []template.HTML{
				"Waiting for a runner with the following labels: docker, ubuntu",
				"Need approval to run workflows for fork pull request.",
			},
		},
		{
			name:     "Running",
			job:      ActionRunJob{RunsOn: []string{"debian"}, Status: StatusRunning, Run: &ActionRun{NeedApproval: false}},
			expected: []template.HTML{"Running"},
		},
		{
			name:     "Success",
			job:      ActionRunJob{RunsOn: []string{"debian"}, Status: StatusSuccess, Run: &ActionRun{NeedApproval: false}},
			expected: []template.HTML{"Success"},
		},
		{
			name:     "Failure",
			job:      ActionRunJob{RunsOn: []string{"debian"}, Status: StatusFailure, Run: &ActionRun{NeedApproval: false}},
			expected: []template.HTML{"Failure"},
		},
		{
			name:     "Cancelled",
			job:      ActionRunJob{RunsOn: []string{"debian"}, Status: StatusCancelled, Run: &ActionRun{NeedApproval: false}},
			expected: []template.HTML{"Canceled"},
		},
		{
			name:     "Skipped",
			job:      ActionRunJob{RunsOn: []string{"debian"}, Status: StatusSkipped, Run: &ActionRun{NeedApproval: false}},
			expected: []template.HTML{"Skipped"},
		},
		{
			name:     "Blocked",
			job:      ActionRunJob{RunsOn: []string{"debian"}, Status: StatusBlocked, Run: &ActionRun{NeedApproval: false}},
			expected: []template.HTML{"Blocked"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.job.StatusDiagnostics(english))
		})
	}
}

func TestActionRunJob_IsIncompleteMatrix(t *testing.T) {
	tests := []struct {
		name         string
		job          ActionRunJob
		isIncomplete bool
		errContains  string
	}{
		{
			name:         "normal workflow",
			job:          ActionRunJob{WorkflowPayload: []byte("name: workflow")},
			isIncomplete: false,
		},
		{
			name:         "incomplete_matrix workflow",
			job:          ActionRunJob{WorkflowPayload: []byte("name: workflow\nincomplete_matrix: true")},
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
			isIncomplete, err := tt.job.IsIncompleteMatrix()
			if tt.errContains != "" {
				assert.ErrorContains(t, err, tt.errContains)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.isIncomplete, isIncomplete)
			}
		})
	}
}
