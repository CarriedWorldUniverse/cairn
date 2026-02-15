// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package auth_test

import (
	"testing"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	"forgejo.org/models/unittest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetRepositoriesAccessibleWithToken(t *testing.T) {
	defer unittest.OverrideFixtures("models/auth/TestGetRepositoriesAccessibleWithToken")()
	require.NoError(t, unittest.PrepareTestDatabase())

	t.Run("No Resources", func(t *testing.T) {
		resources, err := auth_model.GetRepositoriesAccessibleWithToken(db.DefaultContext, 999)
		require.NoError(t, err)
		assert.Empty(t, resources)
	})

	t.Run("Has Resources", func(t *testing.T) {
		resources, err := auth_model.GetRepositoriesAccessibleWithToken(db.DefaultContext, 3)
		require.NoError(t, err)
		require.Len(t, resources, 3)

		// Verify all expected repo IDs are present
		repoIDs := make([]int64, len(resources))
		for i, res := range resources {
			repoIDs[i] = res.RepoID
		}
		assert.Contains(t, repoIDs, int64(1))
		assert.Contains(t, repoIDs, int64(2))
		assert.Contains(t, repoIDs, int64(3))
	})
}
