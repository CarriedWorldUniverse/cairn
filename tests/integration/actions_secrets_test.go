// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"fmt"
	"net/http"
	"testing"

	repo_model "forgejo.org/models/repo"
	secret_model "forgejo.org/models/secret"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	app_context "forgejo.org/services/context"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActionsSecretsManageUserSecrets(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	url := "/user/settings/actions/secrets"
	session := loginUser(t, user.Name)

	t.Run("Create secret", func(t *testing.T) {
		req := NewRequestWithValues(t, "POST", url, map[string]string{
			"name": "my_secret",
			"data": "   \r\n\tSecrët dåtä\\   \r\n",
		})
		session.MakeRequest(t, req, http.StatusOK)

		flashCookie := session.GetCookie(app_context.CookieNameFlash)
		assert.NotNil(t, flashCookie)
		assert.Equal(t, "success%3DThe%2Bsecret%2B%2522MY_SECRET%2522%2Bhas%2Bbeen%2Badded.", flashCookie.Value)

		secret := unittest.AssertExistsAndLoadBean(t, &secret_model.Secret{OwnerID: user.ID, RepoID: 0, Name: "MY_SECRET"})
		assert.Equal(t, "MY_SECRET", secret.Name)

		value, err := secret.GetDecryptedData()
		require.NoError(t, err)
		assert.Equal(t, "   \n\tSecrët dåtä\\   \n", value)
	})

	t.Run("Remove secret", func(t *testing.T) {
		req := NewRequestWithValues(t, "POST", url, map[string]string{
			"name": "TEST_SECRET",
			"data": "value",
		})
		session.MakeRequest(t, req, http.StatusOK)

		flashCookie := session.GetCookie(app_context.CookieNameFlash)
		assert.NotNil(t, flashCookie)
		assert.Equal(t, "success%3DThe%2Bsecret%2B%2522TEST_SECRET%2522%2Bhas%2Bbeen%2Badded.", flashCookie.Value)

		secret := unittest.AssertExistsAndLoadBean(t, &secret_model.Secret{OwnerID: user.ID, RepoID: 0, Name: "TEST_SECRET"})

		req = NewRequest(t, "POST", fmt.Sprintf("%s/delete?id=%d", url, secret.ID))
		session.MakeRequest(t, req, http.StatusOK)

		flashCookie = session.GetCookie(app_context.CookieNameFlash)
		assert.NotNil(t, flashCookie)
		assert.Equal(t, "success%3DThe%2Bsecret%2Bhas%2Bbeen%2Bremoved.", flashCookie.Value)

		unittest.AssertNotExistsBean(t, secret)
	})
}

func TestActionsSecretsManageRepositorySecrets(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1, OwnerID: user.ID})
	url := "/" + repo.FullName() + "/settings/actions/secrets"

	session := loginUser(t, user.Name)

	t.Run("Create secret", func(t *testing.T) {
		req := NewRequestWithValues(t, "POST", url, map[string]string{
			"name": "my_secret",
			"data": "   \r\n\tSecrët dåtä\\   \r\n",
		})
		session.MakeRequest(t, req, http.StatusOK)

		flashCookie := session.GetCookie(app_context.CookieNameFlash)
		assert.NotNil(t, flashCookie)
		assert.Equal(t, "success%3DThe%2Bsecret%2B%2522MY_SECRET%2522%2Bhas%2Bbeen%2Badded.", flashCookie.Value)

		secret := unittest.AssertExistsAndLoadBean(t, &secret_model.Secret{OwnerID: 0, RepoID: repo.ID, Name: "MY_SECRET"})
		assert.Equal(t, "MY_SECRET", secret.Name)

		value, err := secret.GetDecryptedData()
		require.NoError(t, err)
		assert.Equal(t, "   \n\tSecrët dåtä\\   \n", value)
	})

	t.Run("Remove secret", func(t *testing.T) {
		req := NewRequestWithValues(t, "POST", url, map[string]string{
			"name": "TEST_SECRET",
			"data": "value",
		})
		session.MakeRequest(t, req, http.StatusOK)

		flashCookie := session.GetCookie(app_context.CookieNameFlash)
		assert.NotNil(t, flashCookie)
		assert.Equal(t, "success%3DThe%2Bsecret%2B%2522TEST_SECRET%2522%2Bhas%2Bbeen%2Badded.", flashCookie.Value)

		secret := unittest.AssertExistsAndLoadBean(t, &secret_model.Secret{OwnerID: 0, RepoID: repo.ID, Name: "TEST_SECRET"})

		req = NewRequest(t, "POST", fmt.Sprintf("%s/delete?id=%d", url, secret.ID))
		session.MakeRequest(t, req, http.StatusOK)

		flashCookie = session.GetCookie(app_context.CookieNameFlash)
		assert.NotNil(t, flashCookie)
		assert.Equal(t, "success%3DThe%2Bsecret%2Bhas%2Bbeen%2Bremoved.", flashCookie.Value)

		unittest.AssertNotExistsBean(t, secret)
	})
}

func TestActionsSecretsManageOrganizationSecrets(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	org := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 3, Type: user_model.UserTypeOrganization})
	url := "/org/" + org.Name + "/settings/actions/secrets"

	session := loginUser(t, user.Name)

	t.Run("Create secret", func(t *testing.T) {
		req := NewRequestWithValues(t, "POST", url, map[string]string{
			"name": "my_secret",
			"data": "   \r\n\tSecrët dåtä\\   \r\n",
		})
		session.MakeRequest(t, req, http.StatusOK)

		flashCookie := session.GetCookie(app_context.CookieNameFlash)
		assert.NotNil(t, flashCookie)
		assert.Equal(t, "success%3DThe%2Bsecret%2B%2522MY_SECRET%2522%2Bhas%2Bbeen%2Badded.", flashCookie.Value)

		secret := unittest.AssertExistsAndLoadBean(t, &secret_model.Secret{OwnerID: org.ID, RepoID: 0, Name: "MY_SECRET"})
		assert.Equal(t, "MY_SECRET", secret.Name)

		value, err := secret.GetDecryptedData()
		require.NoError(t, err)
		assert.Equal(t, "   \n\tSecrët dåtä\\   \n", value)
	})

	t.Run("Remove secret", func(t *testing.T) {
		req := NewRequestWithValues(t, "POST", url, map[string]string{
			"name": "TEST_SECRET",
			"data": "value",
		})
		session.MakeRequest(t, req, http.StatusOK)

		flashCookie := session.GetCookie(app_context.CookieNameFlash)
		assert.NotNil(t, flashCookie)
		assert.Equal(t, "success%3DThe%2Bsecret%2B%2522TEST_SECRET%2522%2Bhas%2Bbeen%2Badded.", flashCookie.Value)

		secret := unittest.AssertExistsAndLoadBean(t, &secret_model.Secret{OwnerID: org.ID, RepoID: 0, Name: "TEST_SECRET"})

		req = NewRequest(t, "POST", fmt.Sprintf("%s/delete?id=%d", url, secret.ID))
		session.MakeRequest(t, req, http.StatusOK)

		flashCookie = session.GetCookie(app_context.CookieNameFlash)
		assert.NotNil(t, flashCookie)
		assert.Equal(t, "success%3DThe%2Bsecret%2Bhas%2Bbeen%2Bremoved.", flashCookie.Value)

		unittest.AssertNotExistsBean(t, secret)
	})
}
