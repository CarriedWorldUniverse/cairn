// Copyright 2024 The Forgejo Authors c/o Codeberg e.V.. All rights reserved.
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

func TestAPIAdminActionsGetJobs(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	job196 := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 196})
	job198 := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 198})
	job393 := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 393})
	job394 := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 394})
	job395 := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 395})
	job396 := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 396})
	job397 := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 397})

	adminUsername := "user1"
	token := getUserToken(t, adminUsername, auth_model.AccessTokenScopeWriteAdmin)

	t.Run("jobs-with-label", func(t *testing.T) {
		url := fmt.Sprintf("/api/v1/admin/actions/runners/jobs?labels=%s", "ubuntu-latest")
		req := NewRequest(t, "GET", url)
		req.AddTokenAuth(token)
		res := MakeRequest(t, req, http.StatusOK)

		var jobs []*api.ActionRunJob
		DecodeJSON(t, res, &jobs)

		assert.Len(t, jobs, 1)
		assert.Equal(t, job393.ID, jobs[0].ID)
	})

	t.Run("jobs-without-labels", func(t *testing.T) {
		req := NewRequest(t, "GET", "/api/v1/admin/actions/runners/jobs?labels=")
		req.AddTokenAuth(token)
		res := MakeRequest(t, req, http.StatusOK)

		var jobs []*api.ActionRunJob
		DecodeJSON(t, res, &jobs)

		assert.Len(t, jobs, 2)
		assert.Equal(t, job397.ID, jobs[0].ID)
		assert.Equal(t, job196.ID, jobs[1].ID)
	})

	t.Run("all-jobs", func(t *testing.T) {
		req := NewRequest(t, "GET", "/api/v1/admin/actions/runners/jobs")
		req.AddTokenAuth(token)
		res := MakeRequest(t, req, http.StatusOK)

		var jobs []*api.ActionRunJob
		DecodeJSON(t, res, &jobs)

		assert.Len(t, jobs, 7)
		assert.Equal(t, job397.ID, jobs[0].ID)
		assert.Equal(t, job396.ID, jobs[1].ID)
		assert.Equal(t, job395.ID, jobs[2].ID)
		assert.Equal(t, job394.ID, jobs[3].ID)
		assert.Equal(t, job393.ID, jobs[4].ID)
		assert.Equal(t, job198.ID, jobs[5].ID)
		assert.Equal(t, job196.ID, jobs[6].ID)
	})
}

func TestAPIAdminActionsSearchJobs(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	job196 := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 196})
	job198 := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 198})
	job393 := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 393})
	job394 := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 394})
	job395 := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 395})
	job396 := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 396})
	job397 := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 397})

	adminUsername := "user1"
	token := getUserToken(t, adminUsername, auth_model.AccessTokenScopeWriteAdmin)

	t.Run("jobs-with-label", func(t *testing.T) {
		url := fmt.Sprintf("/api/v1/admin/runners/jobs?labels=%s", "ubuntu-latest")
		req := NewRequest(t, "GET", url)
		req.AddTokenAuth(token)
		res := MakeRequest(t, req, http.StatusOK)

		var jobs []*api.ActionRunJob
		DecodeJSON(t, res, &jobs)

		assert.Len(t, jobs, 1)
		assert.Equal(t, job393.ID, jobs[0].ID)
	})

	t.Run("jobs-without-labels", func(t *testing.T) {
		req := NewRequest(t, "GET", "/api/v1/admin/runners/jobs?labels=")
		req.AddTokenAuth(token)
		res := MakeRequest(t, req, http.StatusOK)

		var jobs []*api.ActionRunJob
		DecodeJSON(t, res, &jobs)

		assert.Len(t, jobs, 2)
		assert.Equal(t, job397.ID, jobs[0].ID)
		assert.Equal(t, job196.ID, jobs[1].ID)
	})

	t.Run("all-jobs", func(t *testing.T) {
		req := NewRequest(t, "GET", "/api/v1/admin/runners/jobs")
		req.AddTokenAuth(token)
		res := MakeRequest(t, req, http.StatusOK)

		var jobs []*api.ActionRunJob
		DecodeJSON(t, res, &jobs)

		assert.Len(t, jobs, 7)
		assert.Equal(t, job397.ID, jobs[0].ID)
		assert.Equal(t, job396.ID, jobs[1].ID)
		assert.Equal(t, job395.ID, jobs[2].ID)
		assert.Equal(t, job394.ID, jobs[3].ID)
		assert.Equal(t, job393.ID, jobs[4].ID)
		assert.Equal(t, job198.ID, jobs[5].ID)
		assert.Equal(t, job196.ID, jobs[6].ID)
	})
}

func TestAPIAdminActionsRegistrationTokenOperations(t *testing.T) {
	defer unittest.OverrideFixtures("tests/integration/fixtures/TestAPIGlobalActionsRunnerRegistrationTokenOperations")()
	require.NoError(t, unittest.PrepareTestDatabase())

	user1 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
	session := loginUser(t, user1.Name)
	readToken := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadAdmin)

	t.Run("GetRegistrationToken", func(t *testing.T) {
		request := NewRequest(t, "GET", "/api/v1/admin/actions/runners/registration-token")
		request.AddTokenAuth(readToken)
		response := MakeRequest(t, request, http.StatusOK)

		var registrationToken shared.RegistrationToken
		DecodeJSON(t, response, &registrationToken)

		expected := shared.RegistrationToken{Token: "BzcgyhjWhLeKGA4ihJIigeRDrcxrFESd0yizEpb7xZJ"}

		assert.Equal(t, expected, registrationToken)
	})

	t.Run("DeprecatedGetRegistrationToken", func(t *testing.T) {
		request := NewRequest(t, "GET", "/api/v1/admin/runners/registration-token")
		request.AddTokenAuth(readToken)
		response := MakeRequest(t, request, http.StatusOK)

		var registrationToken shared.RegistrationToken
		DecodeJSON(t, response, &registrationToken)

		expected := shared.RegistrationToken{Token: "BzcgyhjWhLeKGA4ihJIigeRDrcxrFESd0yizEpb7xZJ"}

		assert.Equal(t, expected, registrationToken)
	})
}

func TestAPIAdminActionsRunnerOperations(t *testing.T) {
	defer unittest.OverrideFixtures("tests/integration/fixtures/TestAPIGlobalActionsRunnerOperations")()
	require.NoError(t, unittest.PrepareTestDatabase())

	user1 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
	session := loginUser(t, user1.Name)
	readToken := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadAdmin)
	writeToken := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteAdmin)

	t.Run("GetRunners", func(t *testing.T) {
		request := NewRequest(t, "GET", "/api/v1/admin/actions/runners")
		request.AddTokenAuth(readToken)
		response := MakeRequest(t, request, http.StatusOK)

		assert.NotEmpty(t, response.Header().Get("X-Total-Count"))

		var runners []*api.ActionRunner
		DecodeJSON(t, response, &runners)

		runnerOne := &api.ActionRunner{
			ID:          130791,
			UUID:        "8b0f6b98-fef8-430e-bfdc-dcbeeb58f3c8",
			Name:        "runner-1-global",
			Version:     "dev",
			OwnerID:     0,
			RepoID:      0,
			Description: "A superb runner",
			Labels:      []string{"debian", "gpu"},
			Status:      "offline",
		}
		runnerTwo := &api.ActionRunner{
			ID:          130792,
			UUID:        "61c48447-6e7d-42da-9dbe-d659ade77a56",
			Name:        "runner-2-user",
			Version:     "11.3.1",
			OwnerID:     1,
			RepoID:      0,
			Description: "A splendid runner",
			Labels:      []string{"docker"},
			Status:      "offline",
		}
		runnerThree := &api.ActionRunner{
			ID:          130793,
			UUID:        "9b92be13-b002-4fc0-b182-5e7cdbef0b8d",
			Name:        "runner-3-global",
			Version:     "11.3.1",
			OwnerID:     0,
			RepoID:      0,
			Description: "Another fine runner",
			Labels:      []string{"fedora"},
			Status:      "offline",
		}

		// There are more runners in the result that originate from the global fixtures. The test ignores them to limit
		// the impact of unrelated changes.
		assert.Contains(t, runners, runnerOne)
		assert.Contains(t, runners, runnerTwo)
		assert.Contains(t, runners, runnerThree)
	})

	t.Run("GetRunnersPaginated", func(t *testing.T) {
		request := NewRequest(t, "GET", "/api/v1/admin/actions/runners?page=1&limit=5")
		request.AddTokenAuth(readToken)
		response := MakeRequest(t, request, http.StatusOK)

		var runners []*api.ActionRunner
		DecodeJSON(t, response, &runners)

		assert.NotEmpty(t, response.Header().Get("Link"))
		assert.NotEmpty(t, response.Header().Get("X-Total-Count"))
		assert.Len(t, runners, 5)
	})

	t.Run("GetGlobalRunner", func(t *testing.T) {
		request := NewRequest(t, "GET", "/api/v1/admin/actions/runners/130793")
		request.AddTokenAuth(readToken)
		response := MakeRequest(t, request, http.StatusOK)

		var runner *api.ActionRunner
		DecodeJSON(t, response, &runner)

		runnerOne := &api.ActionRunner{
			ID:          130793,
			UUID:        "9b92be13-b002-4fc0-b182-5e7cdbef0b8d",
			Name:        "runner-3-global",
			Version:     "11.3.1",
			OwnerID:     0,
			RepoID:      0,
			Description: "Another fine runner",
			Labels:      []string{"fedora"},
			Status:      "offline",
		}

		assert.Equal(t, runnerOne, runner)
	})

	t.Run("GetRepositoryScopedRunner", func(t *testing.T) {
		request := NewRequest(t, "GET", "/api/v1/admin/actions/runners/130794")
		request.AddTokenAuth(readToken)
		response := MakeRequest(t, request, http.StatusOK)

		var runner *api.ActionRunner
		DecodeJSON(t, response, &runner)

		runnerFour := &api.ActionRunner{
			ID:          130794,
			UUID:        "44d595e9-b47d-42ef-b1b9-5869f8b8d501",
			Name:        "runner-4-repository",
			Version:     "12.2.0",
			OwnerID:     0,
			RepoID:      62,
			Description: "",
			Labels:      []string{"nixos"},
			Status:      "offline",
		}

		assert.Equal(t, runnerFour, runner)
	})

	t.Run("DeleteGlobalRunner", func(t *testing.T) {
		url := "/api/v1/admin/actions/runners/130791"

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

	t.Run("DeleteRepositoryScopedRunner", func(t *testing.T) {
		url := "/api/v1/admin/actions/runners/130794"

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
