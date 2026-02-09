// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secret

import (
	"testing"

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

	key := keying.ActionSecret

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

	t.Run("FetchActionSecrets", func(t *testing.T) {
		secrets, err := FetchActionSecrets(t.Context(), 2, 1)
		require.NoError(t, err)
		assert.Equal(t, "some owner secret", secrets["OWNER_SECRET"])
		assert.Equal(t, "some repository secret", secrets["REPO_SECRET"])
	})
}

func TestSecretDataIsNormalized(t *testing.T) {
	secret := Secret{ID: 494, OwnerID: 829, RepoID: 0, Name: "A_SECRET"}

	secret.SetData("  \r\ndatà\t  ")

	decryptedData, err := secret.GetDecryptedData()
	require.NoError(t, err)
	assert.Equal(t, "  \ndatà\t  ", decryptedData)
}

func TestSecretGetDecryptedData(t *testing.T) {
	t.Run("Recovers original data", func(t *testing.T) {
		secret := Secret{ID: 494, OwnerID: 829, RepoID: 0, Name: "A_SECRET"}
		secret.SetData("data")

		decryptedData, err := secret.GetDecryptedData()
		require.NoError(t, err)
		assert.Equal(t, "data", decryptedData)
	})

	t.Run("Returns error if data cannot be decrypted", func(t *testing.T) {
		secret := Secret{ID: 494, OwnerID: 829, RepoID: 0, Name: "A_SECRET"}
		secret.SetData("data")

		// Changing the ID without updating the secret makes the secret irrecoverable.
		secret.ID++

		decryptedData, err := secret.GetDecryptedData()
		assert.Empty(t, decryptedData)
		assert.ErrorContains(t, err, "unable to decrypt secret[id=495,name=\"A_SECRET\"]")
	})
}
