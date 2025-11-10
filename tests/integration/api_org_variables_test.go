// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"fmt"
	"net/http"
	"testing"

	auth_model "forgejo.org/models/auth"
	org_model "forgejo.org/models/organization"
	"forgejo.org/models/unittest"
	api "forgejo.org/modules/structs"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
)

func TestAPIOrgVariablesCreateOrganizationVariable(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	org := unittest.AssertExistsAndLoadBean(t, &org_model.Organization{Name: "org3"})
	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteOrganization)

	cases := []struct {
		Name           string
		ExpectedStatus int
	}{
		{
			Name:           "-",
			ExpectedStatus: http.StatusBadRequest,
		},
		{
			Name:           "_",
			ExpectedStatus: http.StatusNoContent,
		},
		{
			Name:           "TEST_VAR",
			ExpectedStatus: http.StatusNoContent,
		},
		{
			Name:           "test_var",
			ExpectedStatus: http.StatusConflict,
		},
		{
			Name:           "ci",
			ExpectedStatus: http.StatusBadRequest,
		},
		{
			Name:           "123var",
			ExpectedStatus: http.StatusBadRequest,
		},
		{
			Name:           "var@test",
			ExpectedStatus: http.StatusBadRequest,
		},
		{
			Name:           "github_var",
			ExpectedStatus: http.StatusBadRequest,
		},
		{
			Name:           "gitea_var",
			ExpectedStatus: http.StatusBadRequest,
		},
	}

	for _, c := range cases {
		requestURL := fmt.Sprintf("/api/v1/orgs/%s/actions/variables/%s", org.Name, c.Name)
		request := NewRequestWithJSON(t, "POST", requestURL, api.CreateVariableOption{Value: "value"})
		request.AddTokenAuth(token)
		MakeRequest(t, request, c.ExpectedStatus)
	}
}

func TestAPIOrgVariablesUpdateOrganizationVariable(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	org := unittest.AssertExistsAndLoadBean(t, &org_model.Organization{Name: "org3"})
	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteOrganization)

	variableName := "test_update_var"

	url := fmt.Sprintf("/api/v1/orgs/%s/actions/variables/%s", org.Name, variableName)

	request := NewRequestWithJSON(t, "POST", url, api.CreateVariableOption{Value: "initial_val"})
	request.AddTokenAuth(token)

	MakeRequest(t, request, http.StatusNoContent)

	cases := []struct {
		Name           string
		UpdateName     string
		ExpectedStatus int
	}{
		{
			Name:           "not_found_var",
			ExpectedStatus: http.StatusNotFound,
		},
		{
			Name:           variableName,
			UpdateName:     "1invalid",
			ExpectedStatus: http.StatusBadRequest,
		},
		{
			Name:           variableName,
			UpdateName:     "invalid@name",
			ExpectedStatus: http.StatusBadRequest,
		},
		{
			Name:           variableName,
			UpdateName:     "ci",
			ExpectedStatus: http.StatusBadRequest,
		},
		{
			Name:           variableName,
			UpdateName:     "updated_var_name",
			ExpectedStatus: http.StatusNoContent,
		},
		{
			Name:           variableName,
			ExpectedStatus: http.StatusNotFound,
		},
		{
			Name:           "updated_var_name",
			ExpectedStatus: http.StatusNoContent,
		},
	}

	for _, c := range cases {
		url := fmt.Sprintf("/api/v1/orgs/%s/actions/variables/%s", org.Name, c.Name)
		request := NewRequestWithJSON(t, "PUT", url, api.UpdateVariableOption{Name: c.UpdateName, Value: "updated_val"})
		request.AddTokenAuth(token)
		MakeRequest(t, request, c.ExpectedStatus)
	}
}

func TestAPIOrgVariablesDeleteOrganizationVariable(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	org := unittest.AssertExistsAndLoadBean(t, &org_model.Organization{Name: "org3"})
	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteOrganization)

	variableName := "test_delete_var"

	url := fmt.Sprintf("/api/v1/orgs/%s/actions/variables/%s", org.Name, variableName)

	request := NewRequestWithJSON(t, "POST", url, api.CreateVariableOption{Value: "initial_val"})
	request.AddTokenAuth(token)
	MakeRequest(t, request, http.StatusNoContent)

	request = NewRequest(t, "DELETE", url).AddTokenAuth(token)
	MakeRequest(t, request, http.StatusNoContent)

	request = NewRequest(t, "DELETE", url).AddTokenAuth(token)
	MakeRequest(t, request, http.StatusNotFound)
}

func TestAPIOrgVariablesGetSingleOrganizationVariable(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	org := unittest.AssertExistsAndLoadBean(t, &org_model.Organization{Name: "org3"})
	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteOrganization)

	name := "some_variable"
	value := "false"

	createURL := fmt.Sprintf("/api/v1/orgs/%s/actions/variables/%s", org.Name, name)

	createRequest := NewRequestWithJSON(t, "POST", createURL, api.CreateVariableOption{Value: value})
	createRequest.AddTokenAuth(token)
	MakeRequest(t, createRequest, http.StatusNoContent)

	getURL := fmt.Sprintf("/api/v1/orgs/%s/actions/variables/%s", org.Name, name)

	getRequest := NewRequest(t, "GET", getURL)
	getRequest.AddTokenAuth(token)
	getResponse := MakeRequest(t, getRequest, http.StatusOK)

	var actionVariable api.ActionVariable
	DecodeJSON(t, getResponse, &actionVariable)

	assert.NotNil(t, actionVariable)
	assert.Equal(t, org.ID, actionVariable.OwnerID)
	assert.Equal(t, int64(0), actionVariable.RepoID)
	assert.Equal(t, "SOME_VARIABLE", actionVariable.Name)
	assert.Equal(t, value, actionVariable.Data)
}

func TestAPIOrgVariablesGetAllOrganizationVariables(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	org := unittest.AssertExistsAndLoadBean(t, &org_model.Organization{Name: "org3"})
	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteOrganization)

	variables := map[string]string{"second": "Dolor sit amet", "first": "Lorem ipsum"}
	for name, value := range variables {
		createURL := fmt.Sprintf("/api/v1/orgs/%s/actions/variables/%s", org.Name, name)

		createRequest := NewRequestWithJSON(t, "POST", createURL, api.CreateVariableOption{Value: value})
		createRequest.AddTokenAuth(token)

		MakeRequest(t, createRequest, http.StatusNoContent)
	}

	getURL := fmt.Sprintf("/api/v1/orgs/%s/actions/variables", org.Name)

	getRequest := NewRequest(t, "GET", getURL)
	getRequest.AddTokenAuth(token)
	getResponse := MakeRequest(t, getRequest, http.StatusOK)

	var actionVariables []api.ActionVariable
	DecodeJSON(t, getResponse, &actionVariables)

	assert.Len(t, actionVariables, len(variables))

	assert.Equal(t, org.ID, actionVariables[0].OwnerID)
	assert.Equal(t, int64(0), actionVariables[0].RepoID)
	assert.Equal(t, "FIRST", actionVariables[0].Name)
	assert.Equal(t, "Lorem ipsum", actionVariables[0].Data)

	assert.Equal(t, org.ID, actionVariables[1].OwnerID)
	assert.Equal(t, int64(0), actionVariables[1].RepoID)
	assert.Equal(t, "SECOND", actionVariables[1].Name)
	assert.Equal(t, "Dolor sit amet", actionVariables[1].Data)
}
