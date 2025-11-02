// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package actions

import (
	"testing"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/cache"

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

func TestRepoNumOpenActions(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	err := cache.Init()
	require.NoError(t, err)

	t.Run("Repo 1", func(t *testing.T) {
		repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
		clearRepoRunCountCache(repo)
		assert.Equal(t, 0, RepoNumOpenActions(t.Context(), repo.ID))
	})

	t.Run("Repo 4", func(t *testing.T) {
		repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 4})
		clearRepoRunCountCache(repo)
		assert.Equal(t, 0, RepoNumOpenActions(t.Context(), repo.ID))
	})

	t.Run("Repo 63", func(t *testing.T) {
		repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 63})
		clearRepoRunCountCache(repo)
		assert.Equal(t, 1, RepoNumOpenActions(t.Context(), repo.ID))
	})

	t.Run("Cache Invalidation", func(t *testing.T) {
		repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 63})
		assert.Equal(t, 1, RepoNumOpenActions(t.Context(), repo.ID))

		err = db.DeleteBeans(t.Context(), &ActionRun{RepoID: repo.ID})
		require.NoError(t, err)

		// Even though we've deleted ActionRun, expecting that the number of open runs is still 1 (cached)
		assert.Equal(t, 1, RepoNumOpenActions(t.Context(), repo.ID))

		// Now that we clear the cache, computation should be performed
		clearRepoRunCountCache(repo)
		assert.Equal(t, 0, RepoNumOpenActions(t.Context(), repo.ID))
	})
}
