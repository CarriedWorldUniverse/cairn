// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	auth_model "forgejo.org/models/auth"
	org_model "forgejo.org/models/organization"
	secret_model "forgejo.org/models/secret"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/keying"
	api "forgejo.org/modules/structs"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAPIOrgSecrets(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	org := unittest.AssertExistsAndLoadBean(t, &org_model.Organization{Name: "org3"})
	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteOrganization)

	t.Run("List", func(t *testing.T) {
		listURL := fmt.Sprintf("/api/v1/orgs/%s/actions/secrets", org.Name)
		req := NewRequest(t, "GET", listURL).AddTokenAuth(token)
		res := MakeRequest(t, req, http.StatusOK)
		secrets := []*api.Secret{}
		DecodeJSON(t, res, &secrets)
		assert.Empty(t, secrets)

		createData := api.CreateOrUpdateSecretOption{Data: "a secret to create"}
		req = NewRequestWithJSON(t, "PUT", listURL+"/first", createData).AddTokenAuth(token)
		MakeRequest(t, req, http.StatusCreated)
		req = NewRequestWithJSON(t, "PUT", listURL+"/sec2", createData).AddTokenAuth(token)
		MakeRequest(t, req, http.StatusCreated)
		req = NewRequestWithJSON(t, "PUT", listURL+"/last", createData).AddTokenAuth(token)
		MakeRequest(t, req, http.StatusCreated)

		req = NewRequest(t, "GET", listURL).AddTokenAuth(token)
		res = MakeRequest(t, req, http.StatusOK)
		DecodeJSON(t, res, &secrets)
		assert.Len(t, secrets, 3)
		expectedValues := []string{"FIRST", "SEC2", "LAST"}
		for _, secret := range secrets {
			assert.Contains(t, expectedValues, secret.Name)
		}
	})

	t.Run("Create", func(t *testing.T) {
		cases := []struct {
			Name           string
			ExpectedStatus int
		}{
			{
				Name:           "",
				ExpectedStatus: http.StatusMethodNotAllowed,
			},
			{
				Name:           "-",
				ExpectedStatus: http.StatusBadRequest,
			},
			{
				Name:           "_",
				ExpectedStatus: http.StatusCreated,
			},
			{
				Name:           "ci",
				ExpectedStatus: http.StatusCreated,
			},
			{
				Name:           "secret",
				ExpectedStatus: http.StatusCreated,
			},
			{
				Name:           "2secret",
				ExpectedStatus: http.StatusBadRequest,
			},
			{
				Name:           "FORGEJO_secret",
				ExpectedStatus: http.StatusBadRequest,
			},
			{
				Name:           "GITEA_secret",
				ExpectedStatus: http.StatusBadRequest,
			},
			{
				Name:           "GITHUB_secret",
				ExpectedStatus: http.StatusBadRequest,
			},
		}

		for _, c := range cases {
			req := NewRequestWithJSON(t, "PUT", fmt.Sprintf("/api/v1/orgs/%s/actions/secrets/%s", org.Name, c.Name), api.CreateOrUpdateSecretOption{
				Data: "data",
			}).AddTokenAuth(token)
			MakeRequest(t, req, c.ExpectedStatus)
		}
	})

	t.Run("Update", func(t *testing.T) {
		name := "update_org_secret_and_test_data"
		url := fmt.Sprintf("/api/v1/orgs/%s/actions/secrets/%s", org.Name, name)

		req := NewRequestWithJSON(t, "PUT", url, api.CreateOrUpdateSecretOption{
			Data: "initial",
		}).AddTokenAuth(token)
		MakeRequest(t, req, http.StatusCreated)

		req = NewRequestWithJSON(t, "PUT", url, api.CreateOrUpdateSecretOption{
			Data: "changed data",
		}).AddTokenAuth(token)
		MakeRequest(t, req, http.StatusNoContent)

		secret := unittest.AssertExistsAndLoadBean(t, &secret_model.Secret{Name: strings.ToUpper(name)})
		data, err := keying.ActionSecret.Decrypt(secret.Data, keying.ColumnAndID("data", secret.ID))
		require.NoError(t, err)
		assert.Equal(t, "changed data", string(data))
	})

	t.Run("Delete", func(t *testing.T) {
		name := "delete_secret"
		url := fmt.Sprintf("/api/v1/orgs/%s/actions/secrets/%s", org.Name, name)

		req := NewRequestWithJSON(t, "PUT", url, api.CreateOrUpdateSecretOption{
			Data: "initial",
		}).AddTokenAuth(token)
		MakeRequest(t, req, http.StatusCreated)

		req = NewRequest(t, "DELETE", url).
			AddTokenAuth(token)
		MakeRequest(t, req, http.StatusNoContent)

		req = NewRequest(t, "DELETE", url).
			AddTokenAuth(token)
		MakeRequest(t, req, http.StatusNotFound)
	})

	t.Run("Delete with forbidden names", func(t *testing.T) {
		secret, err := secret_model.InsertEncryptedSecret(t.Context(), org.ID, 0, "FORGEJO_FORBIDDEN", "illegal")
		require.NoError(t, err)

		url := fmt.Sprintf("/api/v1/orgs/%s/actions/secrets/%s", org.Name, secret.Name)

		req := NewRequest(t, "DELETE", url).
			AddTokenAuth(token)
		MakeRequest(t, req, http.StatusNoContent)

		req = NewRequest(t, "DELETE", url).
			AddTokenAuth(token)
		MakeRequest(t, req, http.StatusNotFound)
	})
}
