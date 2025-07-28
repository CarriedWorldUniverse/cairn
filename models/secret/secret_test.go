// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secret

import (
	"testing"

	"forgejo.org/models/actions"
	"forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/keying"
	"forgejo.org/modules/util"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInsertEncryptedSecret(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	t.Run("Global secret", func(t *testing.T) {
		secret, err := InsertEncryptedSecret(t.Context(), 0, 0, "GLOBAL_SECRET", "some common secret")
		require.ErrorIs(t, err, util.ErrInvalidArgument)
		assert.Nil(t, secret)
	})

	key := keying.DeriveKey(keying.ContextActionSecret)

	t.Run("Insert repository secret", func(t *testing.T) {
		secret, err := InsertEncryptedSecret(t.Context(), 0, 1, "REPO_SECRET", "some repository secret")
		require.NoError(t, err)
		assert.NotNil(t, secret)
		assert.Equal(t, "REPO_SECRET", secret.Name)
		assert.EqualValues(t, 1, secret.RepoID)
		assert.NotEmpty(t, secret.Data)

		// Assert the secret is stored in the database.
		unittest.AssertExistsAndLoadBean(t, &Secret{RepoID: 1, Name: "REPO_SECRET", Data: secret.Data})

		t.Run("Keying", func(t *testing.T) {
			// Cannot decrypt with different ID.
			plainText, err := key.Decrypt(secret.Data, keying.ColumnAndID("data", secret.ID+1))
			require.Error(t, err)
			assert.Nil(t, plainText)

			// Cannot decrypt with different column.
			plainText, err = key.Decrypt(secret.Data, keying.ColumnAndID("metadata", secret.ID))
			require.Error(t, err)
			assert.Nil(t, plainText)

			// Can decrypt with correct column and ID.
			plainText, err = key.Decrypt(secret.Data, keying.ColumnAndID("data", secret.ID))
			require.NoError(t, err)
			assert.EqualValues(t, "some repository secret", plainText)
		})
	})

	t.Run("Insert owner secret", func(t *testing.T) {
		secret, err := InsertEncryptedSecret(t.Context(), 2, 0, "OWNER_SECRET", "some owner secret")
		require.NoError(t, err)
		assert.NotNil(t, secret)
		assert.Equal(t, "OWNER_SECRET", secret.Name)
		assert.EqualValues(t, 2, secret.OwnerID)
		assert.NotEmpty(t, secret.Data)

		// Assert the secret is stored in the database.
		unittest.AssertExistsAndLoadBean(t, &Secret{OwnerID: 2, Name: "OWNER_SECRET", Data: secret.Data})

		t.Run("Keying", func(t *testing.T) {
			// Cannot decrypt with different ID.
			plainText, err := key.Decrypt(secret.Data, keying.ColumnAndID("data", secret.ID+1))
			require.Error(t, err)
			assert.Nil(t, plainText)

			// Cannot decrypt with different column.
			plainText, err = key.Decrypt(secret.Data, keying.ColumnAndID("metadata", secret.ID))
			require.Error(t, err)
			assert.Nil(t, plainText)

			// Can decrypt with correct column and ID.
			plainText, err = key.Decrypt(secret.Data, keying.ColumnAndID("data", secret.ID))
			require.NoError(t, err)
			assert.EqualValues(t, "some owner secret", plainText)
		})
	})

	t.Run("Get secrets", func(t *testing.T) {
		secrets, err := GetSecretsOfTask(t.Context(), &actions.ActionTask{
			Job: &actions.ActionRunJob{
				Run: &actions.ActionRun{
					RepoID: 1,
					Repo: &repo.Repository{
						OwnerID: 2,
					},
				},
			},
		})
		require.NoError(t, err)
		assert.Equal(t, "some owner secret", secrets["OWNER_SECRET"])
		assert.Equal(t, "some repository secret", secrets["REPO_SECRET"])
	})
}
