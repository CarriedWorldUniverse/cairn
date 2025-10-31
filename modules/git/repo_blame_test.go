// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package git

import (
	"bytes"
	"os"
	"path"
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

		commit, lineno, err := repo.LineBlame("HEAD", "foo/link_short", 1)
		require.NoError(t, err)
		assert.Equal(t, "37991dec2c8e592043f47155ce4808d4580f9123", commit.ID.String())
		assert.EqualValues(t, 1, lineno)

		commit, lineno, err = repo.LineBlame("HEAD", "foo/link_short", 512)
		require.ErrorIs(t, err, ErrBlameFileNotEnoughLines)
		assert.Nil(t, commit)
		assert.Zero(t, lineno)

		commit, lineno, err = repo.LineBlame("HEAD", "non-existent/path", 512)
		require.ErrorIs(t, err, ErrBlameFileDoesNotExist)
		assert.Nil(t, commit)
		assert.Zero(t, lineno)
	})

	t.Run("SHA256", func(t *testing.T) {
		skipIfSHA256NotSupported(t)

		repo, err := OpenRepository(t.Context(), filepath.Join(testReposDir, "repo1_bare_sha256"))
		require.NoError(t, err)
		defer repo.Close()

		commit, lineno, err := repo.LineBlame("HEAD", "foo/link_short", 1)
		require.NoError(t, err)
		assert.Equal(t, "6aae864a3d1d0d6a5be0cc64028c1e7021e2632b031fd8eb82afc5a283d1c3d1", commit.ID.String())
		assert.EqualValues(t, 1, lineno)

		commit, lineno, err = repo.LineBlame("HEAD", "foo/link_short", 512)
		require.ErrorIs(t, err, ErrBlameFileNotEnoughLines)
		assert.Nil(t, commit)
		assert.Zero(t, lineno)

		commit, lineno, err = repo.LineBlame("HEAD", "non-existent/path", 512)
		require.ErrorIs(t, err, ErrBlameFileDoesNotExist)
		assert.Nil(t, commit)
		assert.Zero(t, lineno)
	})

	t.Run("Moved line", func(t *testing.T) {
		test := func(t *testing.T, objectFormatName string) {
			t.Helper()
			tmpDir := t.TempDir()

			require.NoError(t, InitRepository(t.Context(), tmpDir, false, objectFormatName))

			gitRepo, err := OpenRepository(t.Context(), tmpDir)
			require.NoError(t, err)
			defer gitRepo.Close()

			require.NoError(t, os.WriteFile(path.Join(tmpDir, "ANSWER"), []byte("abba\n"), 0o666))
			require.NoError(t, AddChanges(tmpDir, true))
			require.NoError(t, CommitChanges(tmpDir, CommitChangesOptions{Message: "Favourite singer of everyone who follows a automata course"}))

			firstCommit, err := gitRepo.GetRefCommitID("HEAD")
			require.NoError(t, err)

			require.NoError(t, os.WriteFile(path.Join(tmpDir, "ANSWER"), append(bytes.Repeat([]byte("baba\n"), 9), []byte("abba\n")...), 0o666))
			require.NoError(t, AddChanges(tmpDir, true))
			require.NoError(t, CommitChanges(tmpDir, CommitChangesOptions{Message: "Now there's several of them"}))

			secondCommit, err := gitRepo.GetRefCommitID("HEAD")
			require.NoError(t, err)

			commit, lineno, err := gitRepo.LineBlame("HEAD", "ANSWER", 10)
			require.NoError(t, err)
			assert.Equal(t, firstCommit, commit.ID.String())
			assert.EqualValues(t, 1, lineno)

			for i := range uint64(9) {
				commit, lineno, err = gitRepo.LineBlame("HEAD", "ANSWER", i+1)
				require.NoError(t, err)
				assert.Equal(t, secondCommit, commit.ID.String())
				assert.Equal(t, i+1, lineno)
			}
		}

		t.Run("SHA1", func(t *testing.T) {
			test(t, Sha1ObjectFormat.Name())
		})

		t.Run("SHA256", func(t *testing.T) {
			skipIfSHA256NotSupported(t)

			test(t, Sha256ObjectFormat.Name())
		})
	})
}
