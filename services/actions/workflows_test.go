// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package actions

import (
	"testing"

	actions_model "forgejo.org/models/actions"
	"forgejo.org/models/repo"
	"forgejo.org/modules/webhook"

	act_model "code.forgejo.org/forgejo/runner/v11/act/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigureActionRunConcurrency(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		concurrency             *act_model.RawConcurrency
		vars                    map[string]string
		inputs                  map[string]any
		runEvent                webhook.HookEventType
		expectedConcurrencyType actions_model.ConcurrencyMode
	}{
		// Before the introduction of concurrency groups, push & pull_request_sync would cancel runs on the same repo,
		// reference, workflow, and event -- these cases cover undefined concurrency group and backwards compatibility
		// checks.
		{
			name:                    "backwards compatibility push",
			runEvent:                webhook.HookEventPush,
			expectedConcurrencyType: actions_model.CancelInProgress,
		},
		{
			name:                    "backwards compatibility pull_request_sync",
			runEvent:                webhook.HookEventPullRequestSync,
			expectedConcurrencyType: actions_model.CancelInProgress,
		},
		{
			name:                    "backwards compatibility other event",
			runEvent:                webhook.HookEventWorkflowDispatch,
			expectedConcurrencyType: actions_model.UnlimitedConcurrency,
		},

		{
			name: "fully-specified cancel-in-progress",
			concurrency: &act_model.RawConcurrency{
				Group:            "abc",
				CancelInProgress: "true",
			},
			runEvent:                webhook.HookEventPullRequestSync,
			expectedConcurrencyType: actions_model.CancelInProgress,
		},
		{
			name: "no concurrency group, cancel-in-progress: false",
			concurrency: &act_model.RawConcurrency{
				CancelInProgress: "false",
			},
			runEvent:                webhook.HookEventPullRequestSync,
			expectedConcurrencyType: actions_model.UnlimitedConcurrency,
		},

		{
			name: "interpreted values",
			concurrency: &act_model.RawConcurrency{
				Group:            "${{ github.workflow }}-${{ github.ref }}",
				CancelInProgress: "${{ !contains(github.ref, 'release/')}}",
			},
			runEvent:                webhook.HookEventPullRequestSync,
			expectedConcurrencyType: actions_model.CancelInProgress,
		},
		{
			name: "interpreted values with inputs and vars",
			concurrency: &act_model.RawConcurrency{
				Group: "${{ inputs.abc }}-${{ vars.def }}",
			},
			inputs:                  map[string]any{"abc": "123"},
			vars:                    map[string]string{"def": "456"},
			runEvent:                webhook.HookEventPullRequestSync,
			expectedConcurrencyType: actions_model.CancelInProgress,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workflow := &act_model.Workflow{RawConcurrency: tc.concurrency}
			run := &actions_model.ActionRun{
				Ref:          "refs/head/main",
				WorkflowID:   "testing.yml",
				Event:        tc.runEvent,
				TriggerEvent: string(tc.runEvent),
				Repo:         &repo.Repository{},
			}

			err := ConfigureActionRunConcurrency(workflow, run, tc.vars, tc.inputs)
			require.NoError(t, err)

			assert.Equal(t, tc.expectedConcurrencyType, run.ConcurrencyType)
		})
	}
}
