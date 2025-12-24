// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package actions

import (
	"testing"

	"forgejo.org/models/db"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActionTask_GetAllAttempts(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	var task ActionTask
	has, err := db.GetEngine(t.Context()).Where("id=?", 47).Get(&task)
	require.NoError(t, err)
	require.True(t, has, "load ActionTask from fixture")

	allAttempts, err := task.GetAllAttempts(t.Context())
	require.NoError(t, err)
	require.Len(t, allAttempts, 3)
	assert.EqualValues(t, 47, allAttempts[0].ID, "ordered by attempt, 1")
	assert.EqualValues(t, 53, allAttempts[1].ID, "ordered by attempt, 2")
	assert.EqualValues(t, 52, allAttempts[2].ID, "ordered by attempt, 3")

	// GetAllAttempts doesn't populate all fields; so check expected fields from one of the records
	assert.EqualValues(t, 3, allAttempts[0].Attempt, "read Attempt field")
	assert.Equal(t, StatusRunning, allAttempts[0].Status, "read Status field")
	assert.Equal(t, timeutil.TimeStamp(1683636528), allAttempts[0].Started, "read Started field")
}

func TestActionTask_GetTaskByJobAttempt(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	task, err := GetTaskByJobAttempt(t.Context(), 192, 2)
	require.NoError(t, err)
	assert.EqualValues(t, 192, task.JobID)
	assert.EqualValues(t, 2, task.Attempt)

	_, err = GetTaskByJobAttempt(t.Context(), 192, 100)
	assert.ErrorContains(t, err, "task with job_id 192 and attempt 100: resource does not exist")
}

func TestActionTask_CreatePlaceholderTask(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	job := unittest.AssertExistsAndLoadBean(t, &ActionRunJob{ID: 396})
	assert.EqualValues(t, 0, job.TaskID)

	task, err := CreatePlaceholderTask(t.Context(), job, map[string]string{"output1": "value1", "output2": "value2"})
	require.NoError(t, err)

	assert.NotEqualValues(t, 0, task.ID)
	assert.Equal(t, job.ID, task.JobID)
	assert.EqualValues(t, 1, task.Attempt)
	assert.NotEqualValues(t, 0, task.Started)
	assert.NotEqualValues(t, 0, task.Stopped)
	assert.Equal(t, job.Status, task.Status)
	assert.Equal(t, job.RepoID, task.RepoID)
	assert.Equal(t, job.OwnerID, task.OwnerID)
	assert.Equal(t, job.CommitSHA, task.CommitSHA)
	assert.Equal(t, job.IsForkPullRequest, task.IsForkPullRequest)

	taskOutputs, err := FindTaskOutputByTaskID(t.Context(), task.ID)
	require.NoError(t, err)
	require.Len(t, taskOutputs, 2)
	finalOutputs := map[string]string{}
	for _, to := range taskOutputs {
		finalOutputs[to.OutputKey] = to.OutputValue
	}
	assert.Equal(t, map[string]string{"output1": "value1", "output2": "value2"}, finalOutputs)
}
