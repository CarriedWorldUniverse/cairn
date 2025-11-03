package task

import (
	"testing"

	admin_model "forgejo.org/models/admin"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/migration"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateMigrateTask(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})

	t.Run("Transaction failure", func(t *testing.T) {
		defer unittest.SetFaultInjector(2)()

		task, err := CreateMigrateTask(t.Context(), user, user, migration.MigrateOptions{
			CloneAddr:    "https://admin:password2@example.com",
			AuthPassword: "password",
			AuthToken:    "token",
			RepoName:     "migrate-test-2",
		})
		require.ErrorIs(t, err, unittest.ErrFaultInjected)
		require.Nil(t, task)

		unittest.AssertExistsIf(t, false, &admin_model.Task{})
	})

	t.Run("Normal", func(t *testing.T) {
		task, err := CreateMigrateTask(t.Context(), user, user, migration.MigrateOptions{
			CloneAddr:    "https://admin:password@example.com",
			AuthPassword: "password",
			AuthToken:    "token",
			RepoName:     "migrate-test",
		})
		require.NoError(t, err)
		require.NotNil(t, task)

		config, err := task.MigrateConfig()
		require.NoError(t, err)
		require.NotNil(t, config)

		assert.Equal(t, "token", config.AuthToken)
		assert.Equal(t, "password", config.AuthPassword)
		assert.Equal(t, "https://admin:password@example.com", config.CloneAddr)
	})
}
