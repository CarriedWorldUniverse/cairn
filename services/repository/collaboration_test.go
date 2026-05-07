// Copyright 2017 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repository

import (
	"testing"

	"github.com/CarriedWorldUniverse/cairn/models/db"
	repo_model "github.com/CarriedWorldUniverse/cairn/models/repo"
	"github.com/CarriedWorldUniverse/cairn/models/unittest"

	"github.com/stretchr/testify/require"
)

func TestRepository_DeleteCollaboration(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 4})
	require.NoError(t, repo.LoadOwner(db.DefaultContext))
	require.NoError(t, DeleteCollaboration(db.DefaultContext, repo, 4))
	unittest.AssertNotExistsBean(t, &repo_model.Collaboration{RepoID: repo.ID, UserID: 4})

	require.NoError(t, DeleteCollaboration(db.DefaultContext, repo, 4))
	unittest.AssertNotExistsBean(t, &repo_model.Collaboration{RepoID: repo.ID, UserID: 4})

	unittest.CheckConsistencyFor(t, &repo_model.Repository{ID: repo.ID})
}
