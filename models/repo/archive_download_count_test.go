// Copyright 2024 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo_test

import (
	"testing"

	"github.com/CarriedWorldUniverse/cairn/models/db"
	repo_model "github.com/CarriedWorldUniverse/cairn/models/repo"
	"github.com/CarriedWorldUniverse/cairn/models/unittest"
	"github.com/CarriedWorldUniverse/cairn/modules/git"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepoArchiveDownloadCount(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	release, err := repo_model.GetReleaseByID(db.DefaultContext, 1)
	require.NoError(t, err)

	// We have no count, so it should return 0
	downloadCount, err := repo_model.GetArchiveDownloadCount(db.DefaultContext, release.RepoID, release.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), downloadCount.Zip)
	assert.Equal(t, int64(0), downloadCount.TarGz)

	// Set the TarGz counter to 1
	err = repo_model.CountArchiveDownload(db.DefaultContext, release.RepoID, release.ID, git.TARGZ)
	require.NoError(t, err)

	downloadCount, err = repo_model.GetArchiveDownloadCountForTagName(db.DefaultContext, release.RepoID, release.TagName)
	require.NoError(t, err)
	assert.Equal(t, int64(0), downloadCount.Zip)
	assert.Equal(t, int64(1), downloadCount.TarGz)

	// Set the TarGz counter to 2
	err = repo_model.CountArchiveDownload(db.DefaultContext, release.RepoID, release.ID, git.TARGZ)
	require.NoError(t, err)

	downloadCount, err = repo_model.GetArchiveDownloadCountForTagName(db.DefaultContext, release.RepoID, release.TagName)
	require.NoError(t, err)
	assert.Equal(t, int64(0), downloadCount.Zip)
	assert.Equal(t, int64(2), downloadCount.TarGz)

	// Set the Zip counter to 1
	err = repo_model.CountArchiveDownload(db.DefaultContext, release.RepoID, release.ID, git.ZIP)
	require.NoError(t, err)

	downloadCount, err = repo_model.GetArchiveDownloadCountForTagName(db.DefaultContext, release.RepoID, release.TagName)
	require.NoError(t, err)
	assert.Equal(t, int64(1), downloadCount.Zip)
	assert.Equal(t, int64(2), downloadCount.TarGz)

	// Delete the count
	err = repo_model.DeleteArchiveDownloadCountForRelease(db.DefaultContext, release.ID)
	require.NoError(t, err)

	downloadCount, err = repo_model.GetArchiveDownloadCountForTagName(db.DefaultContext, release.RepoID, release.TagName)
	require.NoError(t, err)
	assert.Equal(t, int64(0), downloadCount.Zip)
	assert.Equal(t, int64(0), downloadCount.TarGz)
}
