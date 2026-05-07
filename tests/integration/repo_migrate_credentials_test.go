// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/models/admin"
	repo_model "github.com/CarriedWorldUniverse/cairn/models/repo"
	"github.com/CarriedWorldUniverse/cairn/models/unittest"
	user_model "github.com/CarriedWorldUniverse/cairn/models/user"
	"github.com/CarriedWorldUniverse/cairn/modules/setting"
	"github.com/CarriedWorldUniverse/cairn/modules/structs"
	"github.com/CarriedWorldUniverse/cairn/modules/test"
	"github.com/CarriedWorldUniverse/cairn/services/migrations"

	"github.com/stretchr/testify/require"
)

func TestRepoMigrateWithCredentials(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		defer test.MockVariableValue(&setting.Migrations.AllowLocalNetworks, true)()
		require.NoError(t, migrations.Init())

		user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		session := loginUser(t, "user2")

		t.Run("Incorrect credentials", func(t *testing.T) {
			session.MakeRequest(t, NewRequestWithValues(t, "POST", "/repo/migrate", map[string]string{
				"clone_addr":    u.JoinPath("/user2/repo2").String(),
				"auth_username": "user2",
				"auth_password": userPassword + "1",
				"uid":           "2",
				"repo_name":     "migrating-with-credentials",
				"service":       "1",
			}), http.StatusSeeOther)

			repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{Name: "migrating-with-credentials"}, "is_empty = true")
			unittest.AssertExistsAndLoadBean(t, &admin.Task{
				RepoID: repo.ID,
				Type:   structs.TaskTypeMigrateRepo,
				Status: structs.TaskStatusFailed,
			})
		})

		t.Run("Normal", func(t *testing.T) {
			session.MakeRequest(t, NewRequestWithValues(t, "POST", "/repo/migrate", map[string]string{
				"clone_addr":    u.JoinPath("/user2/repo2").String(),
				"auth_username": "user2",
				"auth_password": userPassword,
				"uid":           "2",
				"repo_name":     "migrating-with-credentials-2",
				"service":       "1",
			}), http.StatusSeeOther)

			repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{Name: "migrating-with-credentials-2"}, "is_empty = false")
			unittest.AssertExistsAndLoadBean(t, &admin.Task{
				RepoID: repo.ID,
				Type:   structs.TaskTypeMigrateRepo,
				Status: structs.TaskStatusFinished,
			})
		})

		t.Run("Dangerous credential", func(t *testing.T) {
			// Temporarily change the password
			dangerousPassword := "some`echo foo`thing"
			require.NoError(t, user2.SetPassword(dangerousPassword))
			require.NoError(t, user_model.UpdateUserCols(t.Context(), user2, "passwd", "passwd_hash_algo", "salt"))

			session = loginUserWithPassword(t, "user2", dangerousPassword)

			session.MakeRequest(t, NewRequestWithValues(t, "POST", "/repo/migrate", map[string]string{
				"clone_addr":    u.JoinPath("/user2/repo2").String(),
				"auth_username": "user2",
				"auth_password": dangerousPassword,
				"uid":           "2",
				"repo_name":     "migrating-with-credentials-3",
				"service":       "1",
			}), http.StatusSeeOther)

			repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{Name: "migrating-with-credentials-3"}, "is_empty = false")
			unittest.AssertExistsAndLoadBean(t, &admin.Task{
				RepoID: repo.ID,
				Type:   structs.TaskTypeMigrateRepo,
				Status: structs.TaskStatusFinished,
			})
		})
	})
}
