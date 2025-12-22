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

func TestActionsAPISearchActionJobs_OrgRunner(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user1")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteOrganization)

	job := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 395})

	req := NewRequest(t, "GET",
		fmt.Sprintf("/api/v1/orgs/org3/actions/runners/jobs?labels=%s", "fedora")).
		AddTokenAuth(token)
	res := MakeRequest(t, req, http.StatusOK)

	var jobs []*api.ActionRunJob
	DecodeJSON(t, res, &jobs)

	assert.Len(t, jobs, 1)
	assert.Equal(t, job.ID, jobs[0].ID)
}

func TestActionsAPISearchActionJobs_OrgRunnerAllPendingJobsWithoutLabels(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user1")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteOrganization)

	job := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 397})

	req := NewRequest(t, "GET", "/api/v1/orgs/org3/actions/runners/jobs?labels=").
		AddTokenAuth(token)
	res := MakeRequest(t, req, http.StatusOK)

	var jobs []*api.ActionRunJob
	DecodeJSON(t, res, &jobs)

	assert.Len(t, jobs, 1)
	assert.Equal(t, job.ID, jobs[0].ID)
}

func TestActionsAPISearchActionJobs_OrgRunnerAllPendingJobs(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user1")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteOrganization)

	job395 := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 395})
	job397 := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 397})

	req := NewRequest(t, "GET", "/api/v1/orgs/org3/actions/runners/jobs").
		AddTokenAuth(token)
	res := MakeRequest(t, req, http.StatusOK)

	var jobs []*api.ActionRunJob
	DecodeJSON(t, res, &jobs)

	assert.Len(t, jobs, 2)
	assert.Equal(t, job397.ID, jobs[0].ID)
	assert.Equal(t, job395.ID, jobs[1].ID)
}

func TestAPIOrgActionsRunnerRegistrationTokenOperations(t *testing.T) {
	defer unittest.OverrideFixtures("tests/integration/fixtures/TestAPIOrgActionsRunnerRegistrationTokenOperations")()
	require.NoError(t, unittest.PrepareTestDatabase())

	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	session := loginUser(t, user2.Name)
	readToken := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadOrganization)

	t.Run("GetRegistrationToken", func(t *testing.T) {
		request := NewRequest(t, "GET", "/api/v1/orgs/org3/actions/runners/registration-token")
		request.AddTokenAuth(readToken)
		response := MakeRequest(t, request, http.StatusOK)

		var registrationToken shared.RegistrationToken
		DecodeJSON(t, response, &registrationToken)

		expected := shared.RegistrationToken{Token: "Sk9wHjBHelH4n1ckQy-mo3KVYRdoaPZ_aaH1ATfgI05"}

		assert.Equal(t, expected, registrationToken)
	})
}

func TestAPIOrgActionsRunnerOperations(t *testing.T) {
	defer unittest.OverrideFixtures("tests/integration/fixtures/TestAPIOrgActionsRunnerOperations")()
	require.NoError(t, unittest.PrepareTestDatabase())

	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	session := loginUser(t, user2.Name)
	readToken := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadOrganization)
	writeToken := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteOrganization)

	t.Run("GetRunners", func(t *testing.T) {
		request := NewRequest(t, "GET", "/api/v1/orgs/org3/actions/runners")
		request.AddTokenAuth(readToken)
		response := MakeRequest(t, request, http.StatusOK)

		assert.Equal(t, "2", response.Header().Get("X-Total-Count"))

		var runners []*api.ActionRunner
		DecodeJSON(t, response, &runners)

		runnerOne := &api.ActionRunner{
			ID:          655691,
			UUID:        "a3297f3a-ba5c-4a0f-878e-6cc8b8ac79ec",
			Name:        "runner-1-organization",
			Version:     "dev",
			OwnerID:     3,
			RepoID:      0,
			Description: "A superb runner",
			Labels:      []string{"debian", "gpu"},
			Status:      "offline",
		}
		runnerThree := &api.ActionRunner{
			ID:          655693,
			UUID:        "0a7e5e05-2da4-44d5-a72a-615da120cef6",
			Name:        "runner-3-organization",
			Version:     "11.3.1",
			OwnerID:     3,
			RepoID:      0,
			Description: "Another fine runner",
			Labels:      []string{"fedora"},
			Status:      "offline",
		}

		assert.ElementsMatch(t, []*api.ActionRunner{runnerOne, runnerThree}, runners)
	})

	t.Run("GetRunnersPaginated", func(t *testing.T) {
		request := NewRequest(t, "GET", "/api/v1/orgs/org3/actions/runners?page=1&limit=1")
		request.AddTokenAuth(readToken)
		response := MakeRequest(t, request, http.StatusOK)

		var runners []*api.ActionRunner
		DecodeJSON(t, response, &runners)

		assert.NotEmpty(t, response.Header().Get("Link"))
		assert.NotEmpty(t, response.Header().Get("X-Total-Count"))
		assert.Len(t, runners, 1)
	})

	t.Run("GetRunner", func(t *testing.T) {
		request := NewRequest(t, "GET", "/api/v1/orgs/org3/actions/runners/655691")
		request.AddTokenAuth(readToken)
		response := MakeRequest(t, request, http.StatusOK)

		var runner *api.ActionRunner
		DecodeJSON(t, response, &runner)

		runnerOne := &api.ActionRunner{
			ID:          655691,
			UUID:        "a3297f3a-ba5c-4a0f-878e-6cc8b8ac79ec",
			Name:        "runner-1-organization",
			Version:     "dev",
			OwnerID:     3,
			RepoID:      0,
			Description: "A superb runner",
			Labels:      []string{"debian", "gpu"},
			Status:      "offline",
		}

		assert.Equal(t, runnerOne, runner)
	})

	t.Run("DeleteRunner", func(t *testing.T) {
		url := "/api/v1/orgs/org3/actions/runners/655691"

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
