// Copyright 2024 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"testing"
	"time"

	actions_model "forgejo.org/models/actions"
	"forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRerun_GetAllRerunJobs(t *testing.T) {
	job1 := &actions_model.ActionRunJob{JobID: "job1"}
	job2 := &actions_model.ActionRunJob{JobID: "job2", Needs: []string{"job1"}}
	job3 := &actions_model.ActionRunJob{JobID: "job3", Needs: []string{"job2"}}
	job4 := &actions_model.ActionRunJob{JobID: "job4", Needs: []string{"job2", "job3"}}

	jobs := []*actions_model.ActionRunJob{job1, job2, job3, job4}

	testCases := []struct {
		job       *actions_model.ActionRunJob
		rerunJobs []*actions_model.ActionRunJob
	}{
		{
			job1,
			[]*actions_model.ActionRunJob{job1, job2, job3, job4},
		},
		{
			job2,
			[]*actions_model.ActionRunJob{job2, job3, job4},
		},
		{
			job3,
			[]*actions_model.ActionRunJob{job3, job4},
		},
		{
			job4,
			[]*actions_model.ActionRunJob{job4},
		},
	}

	for _, tc := range testCases {
		rerunJobs := GetAllRerunJobs(tc.job, jobs)
		assert.ElementsMatch(t, tc.rerunJobs, rerunJobs)
	}
}

func TestRerun_RerunAllJobs(t *testing.T) {
	t.Run("Reruns completed workflow", func(t *testing.T) {
		defer unittest.OverrideFixtures("services/actions/TestRerun_RerunAllJobs")()
		require.NoError(t, unittest.PrepareTestDatabase())

		run := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: 455620})

		rerunJobs, err := RerunAllJobs(t.Context(), run)
		require.NoError(t, err)

		run = unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: 455620})

		assert.Equal(t, actions_model.StatusWaiting, run.Status)
		assert.Equal(t, timeutil.TimeStamp(0), run.Started)
		assert.Equal(t, timeutil.TimeStamp(0), run.Stopped)
		assert.Equal(t, 11*time.Second, run.PreviousDuration)

		assert.Len(t, rerunJobs, 2)
		assert.Equal(t, int64(683880), rerunJobs[0].ID)
		assert.Equal(t, int64(2), rerunJobs[0].Attempt)
		assert.Equal(t, actions_model.StatusBlocked, rerunJobs[0].Status)
		assert.Equal(t, timeutil.TimeStamp(0), rerunJobs[0].Started)
		assert.Equal(t, timeutil.TimeStamp(0), rerunJobs[0].Stopped)

		assert.Equal(t, int64(683881), rerunJobs[1].ID)
		assert.Equal(t, int64(2), rerunJobs[1].Attempt)
		assert.Equal(t, actions_model.StatusWaiting, rerunJobs[1].Status)
		assert.Equal(t, timeutil.TimeStamp(0), rerunJobs[1].Started)
		assert.Equal(t, timeutil.TimeStamp(0), rerunJobs[1].Stopped)
	})

	t.Run("Error if workflow running", func(t *testing.T) {
		defer unittest.OverrideFixtures("services/actions/TestRerun_RerunAllJobs")()
		require.NoError(t, unittest.PrepareTestDatabase())

		run := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: 455630})

		rerunJobs, err := RerunAllJobs(t.Context(), run)
		require.ErrorContains(t, err, "workflow is still running")

		run = unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: 455630})

		assert.Equal(t, actions_model.StatusRunning, run.Status)
		assert.Equal(t, timeutil.TimeStamp(1776281360), run.Started)
		assert.Equal(t, timeutil.TimeStamp(0), run.Stopped)
		assert.Equal(t, time.Duration(0), run.PreviousDuration)

		assert.Empty(t, rerunJobs)
	})

	t.Run("Error if workflow invalid", func(t *testing.T) {
		defer unittest.OverrideFixtures("services/actions/TestRerun_RerunAllJobs")()
		require.NoError(t, unittest.PrepareTestDatabase())

		run := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: 455640})

		rerunJobs, err := RerunAllJobs(t.Context(), run)
		require.ErrorContains(t, err, "workflow is invalid")

		run = unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: 455640})

		assert.Equal(t, actions_model.StatusFailure, run.Status)
		assert.Equal(t, timeutil.TimeStamp(0), run.Started)
		assert.Equal(t, timeutil.TimeStamp(0), run.Stopped)
		assert.Equal(t, time.Duration(0), run.PreviousDuration)

		assert.Empty(t, rerunJobs)
	})

	t.Run("Error if workflow disabled", func(t *testing.T) {
		defer unittest.OverrideFixtures("services/actions/TestRerun_RerunAllJobs")()
		require.NoError(t, unittest.PrepareTestDatabase())

		run := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: 455620})

		// Disable workflow
		require.NoError(t, run.LoadAttributes(t.Context()))
		actionsConfig := run.Repo.MustGetUnit(t.Context(), unit.TypeActions).ActionsConfig()
		actionsConfig.DisableWorkflow(run.WorkflowID)

		rerunJobs, err := RerunAllJobs(t.Context(), run)
		require.ErrorContains(t, err, "workflow is disabled")

		run = unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: 455620})

		assert.Equal(t, actions_model.StatusSuccess, run.Status)
		assert.Equal(t, timeutil.TimeStamp(1776279254), run.Started)
		assert.Equal(t, timeutil.TimeStamp(1776279265), run.Stopped)
		assert.Equal(t, time.Duration(0), run.PreviousDuration)

		assert.Empty(t, rerunJobs)
	})
}

func TestRerun_RerunJob(t *testing.T) {
	t.Run("Rerun independent job", func(t *testing.T) {
		defer unittest.OverrideFixtures("services/actions/TestRerun_RerunJob")()
		require.NoError(t, unittest.PrepareTestDatabase())

		job := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 683910})

		rerunJobs, err := RerunJob(t.Context(), job)

		require.NoError(t, err)

		assert.Len(t, rerunJobs, 1)
		assert.Equal(t, job.ID, rerunJobs[0].ID)

		job = unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 683910})

		assert.Equal(t, int64(2), job.Attempt)
		assert.Equal(t, actions_model.StatusWaiting, job.Status)
		assert.Equal(t, timeutil.TimeStamp(0), job.Started)
		assert.Equal(t, timeutil.TimeStamp(0), job.Stopped)
	})

	t.Run("Rerun job needed by others", func(t *testing.T) {
		defer unittest.OverrideFixtures("services/actions/TestRerun_RerunJob")()
		require.NoError(t, unittest.PrepareTestDatabase())

		job := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 683911})

		rerunJobs, err := RerunJob(t.Context(), job)

		require.NoError(t, err)

		assert.Len(t, rerunJobs, 2)
		assert.Equal(t, int64(683911), rerunJobs[0].ID)
		assert.Equal(t, int64(683912), rerunJobs[1].ID)

		job = unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 683911})

		assert.Equal(t, int64(2), job.Attempt)
		assert.Equal(t, actions_model.StatusWaiting, job.Status)
		assert.Equal(t, timeutil.TimeStamp(0), job.Started)
		assert.Equal(t, timeutil.TimeStamp(0), job.Stopped)

		dependentJob := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 683912})

		assert.Equal(t, int64(2), dependentJob.Attempt)
		assert.Equal(t, actions_model.StatusBlocked, dependentJob.Status)
		assert.Equal(t, timeutil.TimeStamp(0), dependentJob.Started)
		assert.Equal(t, timeutil.TimeStamp(0), dependentJob.Stopped)
	})

	t.Run("Rerun job with needs", func(t *testing.T) {
		defer unittest.OverrideFixtures("services/actions/TestRerun_RerunJob")()
		require.NoError(t, unittest.PrepareTestDatabase())

		job := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 683912})

		rerunJobs, err := RerunJob(t.Context(), job)

		require.NoError(t, err)

		assert.Len(t, rerunJobs, 1)
		assert.Equal(t, int64(683912), rerunJobs[0].ID)

		job = unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 683912})

		assert.Len(t, job.Needs, 1)
		assert.Equal(t, int64(2), job.Attempt)
		assert.Equal(t, actions_model.StatusWaiting, job.Status)
		assert.Equal(t, timeutil.TimeStamp(0), job.Started)
		assert.Equal(t, timeutil.TimeStamp(0), job.Stopped)
	})

	t.Run("Error if workflow invalid", func(t *testing.T) {
		defer unittest.OverrideFixtures("services/actions/TestRerun_RerunJob")()
		require.NoError(t, unittest.PrepareTestDatabase())

		job := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 683900})

		rerunJobs, err := RerunJob(t.Context(), job)

		require.ErrorContains(t, err, "workflow is invalid")
		assert.Empty(t, rerunJobs)

		job = unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 683900})

		assert.Equal(t, int64(1), job.Attempt)
		assert.Equal(t, actions_model.StatusFailure, job.Status)
		assert.Equal(t, timeutil.TimeStamp(0), job.Started)
		assert.Equal(t, timeutil.TimeStamp(0), job.Stopped)
	})

	t.Run("Error if workflow disabled", func(t *testing.T) {
		defer unittest.OverrideFixtures("services/actions/TestRerun_RerunJob")()
		require.NoError(t, unittest.PrepareTestDatabase())

		job := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 683881})

		// Disable workflow
		require.NoError(t, job.LoadAttributes(t.Context()))
		actionsConfig := job.Run.Repo.MustGetUnit(t.Context(), unit.TypeActions).ActionsConfig()
		actionsConfig.DisableWorkflow(job.Run.WorkflowID)

		rerunJobs, err := RerunJob(t.Context(), job)

		require.ErrorContains(t, err, "workflow is disabled")
		assert.Empty(t, rerunJobs)

		job = unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 683881})

		assert.Equal(t, int64(1), job.Attempt)
		assert.Equal(t, actions_model.StatusSuccess, job.Status)
		assert.Equal(t, timeutil.TimeStamp(1776279254), job.Started)
		assert.Equal(t, timeutil.TimeStamp(1776279264), job.Stopped)
	})

	t.Run("Error if job still running", func(t *testing.T) {
		defer unittest.OverrideFixtures("services/actions/TestRerun_RerunJob")()
		require.NoError(t, unittest.PrepareTestDatabase())

		job := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 683592})

		rerunJobs, err := RerunJob(t.Context(), job)

		require.ErrorContains(t, err, "job is still running")
		assert.Empty(t, rerunJobs)

		job = unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 683592})

		assert.Equal(t, int64(1), job.Attempt)
		assert.Equal(t, actions_model.StatusRunning, job.Status)
		assert.Equal(t, timeutil.TimeStamp(1776331665), job.Started)
		assert.Equal(t, timeutil.TimeStamp(0), job.Stopped)
	})
}
