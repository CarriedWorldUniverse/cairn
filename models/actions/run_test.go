// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package actions

import (
	"testing"

	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetRunBefore(t *testing.T) {
}

func TestSetConcurrencyGroup(t *testing.T) {
	run := ActionRun{}
	run.SetConcurrencyGroup("abc123")
	assert.Equal(t, "abc123", run.ConcurrencyGroup)
	run.SetConcurrencyGroup("ABC123") // case should collapse in SetConcurrencyGroup
	assert.Equal(t, "abc123", run.ConcurrencyGroup)
}

func TestSetDefaultConcurrencyGroup(t *testing.T) {
	run := ActionRun{
		Ref:          "refs/heads/main",
		WorkflowID:   "testing",
		TriggerEvent: "pull_request",
	}
	run.SetDefaultConcurrencyGroup()
	assert.Equal(t, "refs/heads/main_testing_pull_request__auto", run.ConcurrencyGroup)
	run = ActionRun{
		Ref:          "refs/heads/main",
		WorkflowID:   "TESTING", // case should collapse in SetDefaultConcurrencyGroup
		TriggerEvent: "pull_request",
	}
	run.SetDefaultConcurrencyGroup()
	assert.Equal(t, "refs/heads/main_testing_pull_request__auto", run.ConcurrencyGroup)
}

func TestUpdateRepoRunsNumbers(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	t.Run("Normal", func(t *testing.T) {
		t.Run("Repo 1", func(t *testing.T) {
			repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})

			require.NoError(t, updateRepoRunsNumbers(t.Context(), repo))

			repo = unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
			assert.Equal(t, 1, repo.NumActionRuns)
			assert.Equal(t, 1, repo.NumClosedActionRuns)
		})

		t.Run("Repo 4", func(t *testing.T) {
			repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 4})

			require.NoError(t, updateRepoRunsNumbers(t.Context(), repo))

			repo = unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 4})
			assert.Equal(t, 4, repo.NumActionRuns)
			assert.Equal(t, 4, repo.NumClosedActionRuns)
		})

		t.Run("Repo 63", func(t *testing.T) {
			repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 63})

			require.NoError(t, updateRepoRunsNumbers(t.Context(), repo))

			repo = unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 63})
			assert.Equal(t, 3, repo.NumActionRuns)
			assert.Equal(t, 2, repo.NumClosedActionRuns)
		})
	})

	t.Run("Columns specifc", func(t *testing.T) {
		repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
		repo.Name = "ishouldnotbeupdated"

		require.NoError(t, updateRepoRunsNumbers(t.Context(), repo))

		repo = unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
		assert.Equal(t, "repo1", repo.Name)
	})
}
