// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package git

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLineBlame(t *testing.T) {
	t.Run("SHA1", func(t *testing.T) {
		repo, err := OpenRepository(t.Context(), filepath.Join(testReposDir, "repo1_bare"))
		require.NoError(t, err)
		defer repo.Close()

		commit, err := repo.LineBlame("HEAD", "foo/link_short", 1)
		require.NoError(t, err)
		assert.Equal(t, "37991dec2c8e592043f47155ce4808d4580f9123", commit.ID.String())

		commit, err = repo.LineBlame("HEAD", "foo/link_short", 512)
		require.ErrorIs(t, err, ErrBlameFileNotEnoughLines)
		assert.Nil(t, commit)

		commit, err = repo.LineBlame("HEAD", "non-existent/path", 512)
		require.ErrorIs(t, err, ErrBlameFileDoesNotExist)
		assert.Nil(t, commit)
	})

	t.Run("SHA256", func(t *testing.T) {
		skipIfSHA256NotSupported(t)

		repo, err := OpenRepository(t.Context(), filepath.Join(testReposDir, "repo1_bare_sha256"))
		require.NoError(t, err)
		defer repo.Close()

		commit, err := repo.LineBlame("HEAD", "foo/link_short", 1)
		require.NoError(t, err)
		assert.Equal(t, "6aae864a3d1d0d6a5be0cc64028c1e7021e2632b031fd8eb82afc5a283d1c3d1", commit.ID.String())

		commit, err = repo.LineBlame("HEAD", "foo/link_short", 512)
		require.ErrorIs(t, err, ErrBlameFileNotEnoughLines)
		assert.Nil(t, commit)

		commit, err = repo.LineBlame("HEAD", "non-existent/path", 512)
		require.ErrorIs(t, err, ErrBlameFileDoesNotExist)
		assert.Nil(t, commit)
	})
}
