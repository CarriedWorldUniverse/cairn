// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package actions

import (
	"testing"

	actions_model "forgejo.org/models/actions"
	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/timeutil"
	webhook_module "forgejo.org/modules/webhook"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceActions_startTask(t *testing.T) {
	defer unittest.OverrideFixtures("services/actions/TestServiceActions_startTask")()
	require.NoError(t, unittest.PrepareTestDatabase())

	// Load fixtures that are corrupted and create one valid scheduled workflow
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 4})

	workflowID := "some.yml"
	schedules := []*actions_model.ActionSchedule{
		{
			Title:         "scheduletitle1",
			RepoID:        repo.ID,
			OwnerID:       repo.OwnerID,
			WorkflowID:    workflowID,
			TriggerUserID: repo.OwnerID,
			Ref:           "branch",
			CommitSHA:     "fakeSHA",
			Event:         webhook_module.HookEventSchedule,
			EventPayload:  "fakepayload",
			Specs:         []string{"* * * * *"},
			Content: []byte(
				`
jobs:
  job2:
    runs-on: ubuntu-latest
    steps:
      - run: true
`),
		},
	}

	require.Equal(t, 2, unittest.GetCount(t, actions_model.ActionScheduleSpec{}))
	require.NoError(t, actions_model.CreateScheduleTask(t.Context(), schedules))
	require.Equal(t, 3, unittest.GetCount(t, actions_model.ActionScheduleSpec{}))
	_, err := db.GetEngine(db.DefaultContext).Exec("UPDATE `action_schedule_spec` SET next = 1")
	require.NoError(t, err)

	// After running startTasks an ActionRun row is created for the valid scheduled workflow
	require.Empty(t, unittest.GetCount(t, actions_model.ActionRun{WorkflowID: workflowID}))
	require.NoError(t, startTasks(t.Context()))
	require.NotEmpty(t, unittest.GetCount(t, actions_model.ActionRun{WorkflowID: workflowID}))

	// The invalid workflows loaded from the fixtures are disabled
	repo = unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 4})
	actionUnit, err := repo.GetUnit(t.Context(), unit.TypeActions)
	require.NoError(t, err)
	actionConfig := actionUnit.ActionsConfig()
	assert.True(t, actionConfig.IsWorkflowDisabled("workflow2.yml"))
	assert.True(t, actionConfig.IsWorkflowDisabled("workflow1.yml"))
	assert.False(t, actionConfig.IsWorkflowDisabled("some.yml"))
}

func TestCreateScheduleTask(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 2, OwnerID: 2})

	assertConstant := func(t *testing.T, cron *actions_model.ActionSchedule, run *actions_model.ActionRun) {
		t.Helper()
		assert.Equal(t, cron.Title, run.Title)
		assert.Equal(t, cron.RepoID, run.RepoID)
		assert.Equal(t, cron.OwnerID, run.OwnerID)
		assert.Equal(t, cron.WorkflowID, run.WorkflowID)
		assert.Equal(t, cron.TriggerUserID, run.TriggerUserID)
		assert.Equal(t, cron.Ref, run.Ref)
		assert.Equal(t, cron.CommitSHA, run.CommitSHA)
		assert.Equal(t, cron.Event, run.Event)
		assert.Equal(t, cron.EventPayload, run.EventPayload)
		assert.Equal(t, cron.ID, run.ScheduleID)
		assert.Equal(t, actions_model.StatusWaiting, run.Status)
		assert.Equal(t, "branch_some.yml_schedule__auto", run.ConcurrencyGroup)
		assert.Equal(t, actions_model.UnlimitedConcurrency, run.ConcurrencyType)
	}

	assertMutable := func(t *testing.T, expected, run *actions_model.ActionRun) {
		t.Helper()
		assert.Equal(t, expected.NotifyEmail, run.NotifyEmail)
	}

	testCases := []struct {
		name string
		cron actions_model.ActionSchedule
		want []actions_model.ActionRun
	}{
		{
			name: "simple",
			cron: actions_model.ActionSchedule{
				Title:         "scheduletitle1",
				RepoID:        repo.ID,
				OwnerID:       repo.OwnerID,
				WorkflowID:    "some.yml",
				TriggerUserID: repo.OwnerID,
				Ref:           "branch",
				CommitSHA:     "fakeSHA",
				Event:         webhook_module.HookEventSchedule,
				EventPayload:  "fakepayload",
				Content: []byte(
					`
name: test
on: push
jobs:
  job2:
    runs-on: ubuntu-latest
    steps:
      - run: true
`),
			},
			want: []actions_model.ActionRun{
				{
					Title:       "scheduletitle1",
					NotifyEmail: false,
				},
			},
		},
		{
			name: "enable-email-notifications is true",
			cron: actions_model.ActionSchedule{
				Title:         "scheduletitle2",
				RepoID:        repo.ID,
				OwnerID:       repo.OwnerID,
				WorkflowID:    "some.yml",
				TriggerUserID: repo.OwnerID,
				Ref:           "branch",
				CommitSHA:     "fakeSHA",
				Event:         webhook_module.HookEventSchedule,
				EventPayload:  "fakepayload",
				Content: []byte(
					`
name: test
enable-email-notifications: true
on: push
jobs:
  job2:
    runs-on: ubuntu-latest
    steps:
      - run: true
`),
			},
			want: []actions_model.ActionRun{
				{
					Title:       "scheduletitle2",
					NotifyEmail: true,
				},
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			require.NoError(t, CreateScheduleTask(t.Context(), &testCase.cron))
			require.Equal(t, len(testCase.want), unittest.GetCount(t, actions_model.ActionRun{RepoID: repo.ID}))
			for _, expected := range testCase.want {
				run := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{Title: expected.Title})
				assertConstant(t, &testCase.cron, run)
				assertMutable(t, &expected, run)
			}
			unittest.AssertSuccessfulDelete(t, actions_model.ActionRun{RepoID: repo.ID})
		})
	}
}

func TestCancelPreviousJobs(t *testing.T) {
	defer unittest.OverrideFixtures("services/actions/TestCancelPreviousJobs")()
	require.NoError(t, unittest.PrepareTestDatabase())

	run := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: 894})
	assert.Equal(t, actions_model.StatusRunning, run.Status)
	assert.EqualValues(t, 1683636626, run.Updated)
	runJob := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{RunID: 894})
	assert.Equal(t, actions_model.StatusRunning, runJob.Status)
	assert.EqualValues(t, 1683636528, runJob.Started)

	err := CancelPreviousJobs(t.Context(), 63, "refs/heads/main", "running.yaml", webhook_module.HookEventWorkflowDispatch)
	require.NoError(t, err)

	run = unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: 894})
	assert.Equal(t, actions_model.StatusCancelled, run.Status)
	assert.Greater(t, run.Updated, timeutil.TimeStamp(1683636626))
	runJob = unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{RunID: 894})
	assert.Equal(t, actions_model.StatusCancelled, runJob.Status)
	assert.Greater(t, runJob.Stopped, timeutil.TimeStamp(1683636528))
}

func TestCancelPreviousWithConcurrencyGroup(t *testing.T) {
	for _, tc := range []struct {
		name         string
		updateRun901 map[string]any
	}{
		// run 900 & 901 in the fixture data have almost the same data and so should both be cancelled by
		// TestCancelPreviousWithConcurrencyGroup -- but each test case will vary something different about 601 to
		// ensure that only run 600 is targeted by the cancellation
		{
			name:         "only cancels target repo",
			updateRun901: map[string]any{"repo_id": 2},
		},
		{
			name:         "only cancels target concurrency group",
			updateRun901: map[string]any{"concurrency_group": "321cba"},
		},
		{
			name:         "only cancels running",
			updateRun901: map[string]any{"status": actions_model.StatusSuccess},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer unittest.OverrideFixtures("services/actions/TestCancelPreviousWithConcurrencyGroup")()
			require.NoError(t, unittest.PrepareTestDatabase())

			e := db.GetEngine(t.Context())

			expected901Status := actions_model.StatusRunning
			if tc.updateRun901 != nil {
				affected, err := e.Table(&actions_model.ActionRun{}).Where("id = ?", 901).Update(tc.updateRun901)
				require.NoError(t, err)
				require.EqualValues(t, 1, affected)
				newStatus, ok := tc.updateRun901["status"]
				if ok {
					expected901Status = newStatus.(actions_model.Status)
				}
			}

			run := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: 900})
			assert.Equal(t, actions_model.StatusRunning, run.Status)
			assert.EqualValues(t, 1683636626, run.Updated)
			assert.Equal(t, "abc123", run.ConcurrencyGroup)
			run = unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: 901})
			assert.Equal(t, expected901Status, run.Status)
			runJob := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{RunID: 900})
			assert.Equal(t, actions_model.StatusRunning, runJob.Status)
			assert.EqualValues(t, 1683636528, runJob.Started)

			// Search for concurrency group should be case-insensitive, which we test here by using a different capitalization
			// than the fixture data
			err := CancelPreviousWithConcurrencyGroup(t.Context(), 63, "ABC123")
			require.NoError(t, err)

			run = unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: 900})
			assert.Equal(t, actions_model.StatusCancelled, run.Status)
			assert.Greater(t, run.Updated, timeutil.TimeStamp(1683636626))
			runJob = unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{RunID: 900})
			assert.Equal(t, actions_model.StatusCancelled, runJob.Status)
			assert.Greater(t, runJob.Stopped, timeutil.TimeStamp(1683636528))

			run = unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: 901})
			assert.Equal(t, expected901Status, run.Status)
		})
	}
}
