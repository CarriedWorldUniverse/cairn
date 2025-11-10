// Copyright 2024 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"fmt"
	"net/http"
	"testing"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	api "forgejo.org/modules/structs"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
)

func TestAPIUserVariablesCreateUserVariable(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	user1 := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "user1"})

	session := loginUser(t, user1.Name)
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteUser)

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
		req := NewRequestWithJSON(t, "POST", fmt.Sprintf("/api/v1/user/actions/variables/%s", c.Name), api.CreateVariableOption{
			Value: "value",
		}).AddTokenAuth(token)
		MakeRequest(t, req, c.ExpectedStatus)
	}
}

func TestAPIUserVariablesUpdateUserVariable(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	user1 := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "user1"})

	session := loginUser(t, user1.Name)
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteUser)

	variableName := "test_update_var"
	url := fmt.Sprintf("/api/v1/user/actions/variables/%s", variableName)
	req := NewRequestWithJSON(t, "POST", url, api.CreateVariableOption{
		Value: "initial_val",
	}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusNoContent)

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
		req := NewRequestWithJSON(t, "PUT", fmt.Sprintf("/api/v1/user/actions/variables/%s", c.Name), api.UpdateVariableOption{
			Name:  c.UpdateName,
			Value: "updated_val",
		}).AddTokenAuth(token)
		MakeRequest(t, req, c.ExpectedStatus)
	}
}

func TestAPIUserVariablesDeleteUserVariable(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	user1 := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "user1"})

	session := loginUser(t, user1.Name)
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteUser)

	variableName := "test_delete_var"
	url := fmt.Sprintf("/api/v1/user/actions/variables/%s", variableName)

	req := NewRequestWithJSON(t, "POST", url, api.CreateVariableOption{
		Value: "initial_val",
	}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusNoContent)

	req = NewRequest(t, "DELETE", url).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusNoContent)

	req = NewRequest(t, "DELETE", url).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusNotFound)
}

func TestAPIUserVariablesGetSingleUserVariable(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	user1 := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "user1"})

	session := loginUser(t, user1.Name)
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteUser)

	createURL := fmt.Sprintf("/api/v1/user/actions/variables/%s", "some_variable")

	createRequest := NewRequestWithJSON(t, "POST", createURL, api.CreateVariableOption{Value: "true"})
	createRequest.AddTokenAuth(token)

	MakeRequest(t, createRequest, http.StatusNoContent)

	variableRequest := NewRequest(t, "GET", "/api/v1/user/actions/variables/some_variable")
	variableRequest.AddTokenAuth(token)

	variableResponse := MakeRequest(t, variableRequest, http.StatusOK)

	var actionVariable api.ActionVariable
	DecodeJSON(t, variableResponse, &actionVariable)

	assert.NotNil(t, actionVariable)

	assert.Equal(t, user1.ID, actionVariable.OwnerID)
	assert.Equal(t, int64(0), actionVariable.RepoID)
	assert.Equal(t, "SOME_VARIABLE", actionVariable.Name)
	assert.Equal(t, "true", actionVariable.Data)
}

func TestAPIUserVariablesGetAllUserVariables(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	user1 := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "user1"})

	session := loginUser(t, user1.Name)
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteUser)

	variables := map[string]string{"second": "Dolor sit amet", "first": "Lorem ipsum"}
	for name, value := range variables {
		createURL := fmt.Sprintf("/api/v1/user/actions/variables/%s", name)

		createRequest := NewRequestWithJSON(t, "POST", createURL, api.CreateVariableOption{Value: value})
		createRequest.AddTokenAuth(token)

		MakeRequest(t, createRequest, http.StatusNoContent)
	}

	listRequest := NewRequest(t, "GET", "/api/v1/user/actions/variables")
	listRequest.AddTokenAuth(token)

	listResponse := MakeRequest(t, listRequest, http.StatusOK)

	var actionVariables []api.ActionVariable
	DecodeJSON(t, listResponse, &actionVariables)

	assert.Len(t, actionVariables, len(variables))

	assert.Equal(t, user1.ID, actionVariables[0].OwnerID)
	assert.Equal(t, int64(0), actionVariables[0].RepoID)
	assert.Equal(t, "FIRST", actionVariables[0].Name)
	assert.Equal(t, "Lorem ipsum", actionVariables[0].Data)

	assert.Equal(t, user1.ID, actionVariables[1].OwnerID)
	assert.Equal(t, int64(0), actionVariables[1].RepoID)
	assert.Equal(t, "SECOND", actionVariables[1].Name)
	assert.Equal(t, "Dolor sit amet", actionVariables[1].Data)
}
