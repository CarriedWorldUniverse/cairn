// SPDX-License-Identifier: MIT

package actions

import (
	"fmt"
	"testing"

	"forgejo.org/models/db"
	"forgejo.org/models/unittest"

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
