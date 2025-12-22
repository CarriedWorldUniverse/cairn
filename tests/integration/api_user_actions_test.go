// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"fmt"
	"net/http"
	"testing"

	actions_model "forgejo.org/models/actions"
	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	api "forgejo.org/modules/structs"
	"forgejo.org/routers/api/v1/shared"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActionsAPISearchActionJobs_UserRunner(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	normalUsername := "user2"
	session := loginUser(t, normalUsername)
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteUser)
	job := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 394})

	req := NewRequest(t, "GET",
		fmt.Sprintf("/api/v1/user/actions/runners/jobs?labels=%s", "debian-latest")).
		AddTokenAuth(token)
	res := MakeRequest(t, req, http.StatusOK)

	var jobs []*api.ActionRunJob
	DecodeJSON(t, res, &jobs)

	assert.Len(t, jobs, 1)
	assert.Equal(t, job.ID, jobs[0].ID)
}

func TestActionsAPISearchActionJobs_UserRunnerAllPendingJobsWithoutLabels(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	normalUsername := "user1"
	session := loginUser(t, normalUsername)
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteUser)
	job := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 196})

	req := NewRequest(t, "GET", "/api/v1/user/actions/runners/jobs?labels=").
		AddTokenAuth(token)
	res := MakeRequest(t, req, http.StatusOK)

	var jobs []*api.ActionRunJob
	DecodeJSON(t, res, &jobs)

	assert.Len(t, jobs, 1)
	assert.Equal(t, job.ID, jobs[0].ID)
}

func TestActionsAPISearchActionJobs_UserRunnerAllPendingJobs(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	normalUsername := "user2"
	session := loginUser(t, normalUsername)
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteUser)
	job := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 394})

	req := NewRequest(t, "GET", "/api/v1/user/actions/runners/jobs").
		AddTokenAuth(token)
	res := MakeRequest(t, req, http.StatusOK)

	var jobs []*api.ActionRunJob
	DecodeJSON(t, res, &jobs)

	assert.Len(t, jobs, 1)
	assert.Equal(t, job.ID, jobs[0].ID)
}

func TestAPIUserActionsRunnerRegistrationTokenOperations(t *testing.T) {
	defer unittest.OverrideFixtures("tests/integration/fixtures/TestAPIUserActionsRunnerRegistrationTokenOperations")()
	require.NoError(t, unittest.PrepareTestDatabase())

	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	session := loginUser(t, user2.Name)
	readToken := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadUser)

	t.Run("GetRegistrationToken", func(t *testing.T) {
		request := NewRequest(t, "GET", "/api/v1/user/actions/runners/registration-token")
		request.AddTokenAuth(readToken)
		response := MakeRequest(t, request, http.StatusOK)

		var registrationToken shared.RegistrationToken
		DecodeJSON(t, response, &registrationToken)

		expected := shared.RegistrationToken{Token: "Xb3WmQBum2S0-WwFY399A0DhnPkgRdXzpEOJaMmL5UT"}

		assert.Equal(t, expected, registrationToken)
	})
}

func TestAPIUserActionsRunnerOperations(t *testing.T) {
	defer unittest.OverrideFixtures("tests/integration/fixtures/TestAPIUserActionsRunnerOperations")()
	require.NoError(t, unittest.PrepareTestDatabase())

	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	session := loginUser(t, user2.Name)
	readToken := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadUser)
	writeToken := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteUser)

	t.Run("GetRunners", func(t *testing.T) {
		request := NewRequest(t, "GET", "/api/v1/user/actions/runners")
		request.AddTokenAuth(readToken)
		response := MakeRequest(t, request, http.StatusOK)

		assert.Equal(t, "2", response.Header().Get("X-Total-Count"))

		var runners []*api.ActionRunner
		DecodeJSON(t, response, &runners)

		runnerOne := &api.ActionRunner{
			ID:          71301,
			UUID:        "99fc4a58-a25e-4dbe-b6ea-3d55dddcd216",
			Name:        "runner-1-user",
			Version:     "dev",
			OwnerID:     2,
			RepoID:      0,
			Description: "A superb runner",
			Labels:      []string{"debian", "gpu"},
			Status:      "offline",
		}
		runnerThree := &api.ActionRunner{
			ID:          71303,
			UUID:        "70bc0da3-35b2-4129-bbc9-4679dfdda4d0",
			Name:        "runner-3-user",
			Version:     "11.3.1",
			OwnerID:     2,
			RepoID:      0,
			Description: "Another fine runner",
			Labels:      []string{"fedora"},
			Status:      "offline",
		}

		assert.ElementsMatch(t, []*api.ActionRunner{runnerOne, runnerThree}, runners)
	})

	t.Run("GetRunnersPaginated", func(t *testing.T) {
		request := NewRequest(t, "GET", "/api/v1/user/actions/runners?page=1&limit=1")
		request.AddTokenAuth(readToken)
		response := MakeRequest(t, request, http.StatusOK)

		var runners []*api.ActionRunner
		DecodeJSON(t, response, &runners)

		assert.NotEmpty(t, response.Header().Get("Link"))
		assert.NotEmpty(t, response.Header().Get("X-Total-Count"))
		assert.Len(t, runners, 1)
	})

	t.Run("GetRunner", func(t *testing.T) {
		request := NewRequest(t, "GET", "/api/v1/user/actions/runners/71303")
		request.AddTokenAuth(readToken)
		response := MakeRequest(t, request, http.StatusOK)

		var runner *api.ActionRunner
		DecodeJSON(t, response, &runner)

		runnerThree := &api.ActionRunner{
			ID:          71303,
			UUID:        "70bc0da3-35b2-4129-bbc9-4679dfdda4d0",
			Name:        "runner-3-user",
			Version:     "11.3.1",
			OwnerID:     2,
			RepoID:      0,
			Description: "Another fine runner",
			Labels:      []string{"fedora"},
			Status:      "offline",
		}

		assert.Equal(t, runnerThree, runner)
	})

	t.Run("DeleteRunner", func(t *testing.T) {
		url := "/api/v1/user/actions/runners/71303"

		request := NewRequest(t, "GET", url)
		request.AddTokenAuth(readToken)
		MakeRequest(t, request, http.StatusOK)

		deleteRequest := NewRequest(t, "DELETE", url)
		deleteRequest.AddTokenAuth(writeToken)
		MakeRequest(t, deleteRequest, http.StatusNoContent)

		request = NewRequest(t, "GET", url)
		request.AddTokenAuth(readToken)
		MakeRequest(t, request, http.StatusNotFound)
	})
}
