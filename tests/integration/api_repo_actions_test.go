// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	actions_model "forgejo.org/models/actions"
	auth_model "forgejo.org/models/auth"
	repo_model "forgejo.org/models/repo"
	unit_model "forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	api "forgejo.org/modules/structs"
	"forgejo.org/modules/webhook"
	"forgejo.org/routers/api/v1/shared"
	files_service "forgejo.org/services/repository/files"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActionsAPISearchActionJobs_RepoRunner(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	token := getUserToken(t, user2.LowerName, auth_model.AccessTokenScopeWriteRepository)
	job := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 393})

	req := NewRequestf(
		t,
		"GET",
		"/api/v1/repos/%s/%s/actions/runners/jobs?labels=%s",
		repo.OwnerName, repo.Name,
		"ubuntu-latest",
	).AddTokenAuth(token)
	res := MakeRequest(t, req, http.StatusOK)

	var jobs []*api.ActionRunJob
	DecodeJSON(t, res, &jobs)

	assert.Len(t, jobs, 1)
	assert.Equal(t, job.ID, jobs[0].ID)
}

func TestActionsAPISearchActionJobs_RepoRunnerAllPendingJobsWithoutLabels(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 4})
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
	token := getUserToken(t, user2.LowerName, auth_model.AccessTokenScopeWriteRepository)
	job := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 196})

	req := NewRequestf(
		t,
		"GET",
		"/api/v1/repos/%s/%s/actions/runners/jobs?labels=",
		repo.OwnerName, repo.Name,
	).AddTokenAuth(token)
	res := MakeRequest(t, req, http.StatusOK)

	var jobs []*api.ActionRunJob
	DecodeJSON(t, res, &jobs)

	assert.Len(t, jobs, 1)
	assert.Equal(t, job.ID, jobs[0].ID)
}

func TestActionsAPISearchActionJobs_RepoRunnerAllPendingJobs(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	token := getUserToken(t, user2.LowerName, auth_model.AccessTokenScopeWriteRepository)
	job393 := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 393})
	job394 := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 394})
	job395 := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 395})
	job397 := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 397})

	req := NewRequestf(
		t,
		"GET",
		"/api/v1/repos/%s/%s/actions/runners/jobs",
		repo.OwnerName, repo.Name,
	).AddTokenAuth(token)
	res := MakeRequest(t, req, http.StatusOK)

	var jobs []*api.ActionRunJob
	DecodeJSON(t, res, &jobs)

	assert.Len(t, jobs, 4)
	assert.Equal(t, job397.ID, jobs[0].ID)
	assert.Equal(t, job395.ID, jobs[1].ID)
	assert.Equal(t, job394.ID, jobs[2].ID)
	assert.Equal(t, job393.ID, jobs[3].ID)
}

func TestActionsAPIWorkflowDispatchReturnInfo(t *testing.T) {
	testCases := []struct {
		name              string
		workflowID        string
		workflowDirectory string
	}{
		{
			name:              "GitHub",
			workflowID:        "dispatch.yml",
			workflowDirectory: ".github/workflows",
		},
		{
			name:              "Gitea",
			workflowID:        "test.yml",
			workflowDirectory: ".gitea/workflows",
		},
		{
			name:              "Forgejo",
			workflowID:        "build.yml",
			workflowDirectory: ".forgejo/workflows",
		},
	}

	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		for _, testCase := range testCases {
			t.Run(testCase.name, func(t *testing.T) {
				user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
				token := getUserToken(t, user2.LowerName, auth_model.AccessTokenScopeWriteRepository)

				// create the repo
				repo, _, f := tests.CreateDeclarativeRepo(t, user2, "api-repo-workflow-dispatch",
					[]unit_model.Type{unit_model.TypeActions}, nil,
					[]*files_service.ChangeRepoFile{
						{
							Operation: "create",
							TreePath:  fmt.Sprintf("%s/%s", testCase.workflowDirectory, testCase.workflowID),
							ContentReader: strings.NewReader(`name: WD
on: [workflow-dispatch]
jobs:
  t1:
    runs-on: docker
    steps:
      - run: echo "test 1"
  t2:
    runs-on: docker
    steps:
      - run: echo "test 2"
`,
							),
						},
					},
				)
				defer f()

				req := NewRequestWithJSON(
					t,
					http.MethodPost,
					fmt.Sprintf(
						"/api/v1/repos/%s/%s/actions/workflows/%s/dispatches",
						repo.OwnerName, repo.Name, testCase.workflowID,
					),
					&api.DispatchWorkflowOption{
						Ref:           repo.DefaultBranch,
						ReturnRunInfo: true,
					},
				)
				req.AddTokenAuth(token)

				res := MakeRequest(t, req, http.StatusCreated)
				run := new(api.DispatchWorkflowRun)
				DecodeJSON(t, res, run)

				assert.NotZero(t, run.ID)
				assert.NotZero(t, run.RunNumber)
				assert.Len(t, run.Jobs, 2)

				actionRun := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: run.ID})
				assert.Equal(t, "WD", actionRun.Title)
				assert.Equal(t, repo.ID, actionRun.RepoID)
				assert.Equal(t, repo.OwnerID, actionRun.OwnerID)
				assert.Equal(t, testCase.workflowID, actionRun.WorkflowID)
				assert.Equal(t, testCase.workflowDirectory, actionRun.WorkflowDirectory)
				assert.Equal(t, user2.ID, actionRun.TriggerUserID)
				assert.Zero(t, actionRun.ScheduleID)
				assert.Equal(t, "refs/heads/main", actionRun.Ref)
				assert.Equal(t, webhook.HookEventType("workflow_dispatch"), actionRun.Event)
				assert.Equal(t, "workflow_dispatch", actionRun.TriggerEvent)

				req = NewRequestWithJSON(
					t,
					http.MethodPost,
					fmt.Sprintf(
						"/api/v1/repos/%s/%s/actions/workflows/%s/dispatches",
						repo.OwnerName, repo.Name, testCase.workflowID,
					),
					&api.DispatchWorkflowOption{
						Ref:           repo.DefaultBranch,
						ReturnRunInfo: false,
					},
				)
				req.AddTokenAuth(token)
				res = MakeRequest(t, req, http.StatusNoContent)
				body, err := io.ReadAll(res.Body)
				require.NoError(t, err)
				assert.Empty(t, body) // 204 No Content doesn't support a body, so should be empty
			})
		}
	})
}

func TestActionsAPIGetListActionRun(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	var (
		runIDs = []int64{892, 893, 894}
		dbRuns = make(map[int64]*actions_model.ActionRun, 3)
	)

	for _, id := range runIDs {
		dbRuns[id] = unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: id})
	}

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: dbRuns[runIDs[0]].RepoID})
	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: repo.OwnerID})
	token := getUserToken(t, user.LowerName, auth_model.AccessTokenScopeWriteRepository)

	testqueries := []struct {
		name        string
		query       string
		expectedIDs []int64
	}{
		{
			name:        "No query parameters",
			query:       "",
			expectedIDs: runIDs,
		},
		{
			name:        "Search for workflow_dispatch events",
			query:       "?event=workflow_dispatch",
			expectedIDs: []int64{894},
		},
		{
			name:        "Search for multiple events",
			query:       "?event=workflow_dispatch&event=push",
			expectedIDs: []int64{892, 894},
		},
		{
			name:        "Search for failed status",
			query:       "?status=failure",
			expectedIDs: []int64{893},
		},
		{
			name:        "Search for multiple statuses",
			query:       "?status=failure&status=running",
			expectedIDs: []int64{893, 894},
		},
		{
			name:        "Search for num_nr",
			query:       "?run_number=1",
			expectedIDs: []int64{892},
		},
		{
			name:        "Search for sha",
			query:       "?head_sha=97f29ee599c373c729132a5c46a046978311e0ee",
			expectedIDs: []int64{892, 894},
		},
	}

	for _, tt := range testqueries {
		t.Run(tt.name, func(t *testing.T) {
			req := NewRequest(t, http.MethodGet,
				fmt.Sprintf("/api/v1/repos/%s/%s/actions/runs%s",
					repo.OwnerName, repo.Name, tt.query,
				),
			)
			req.AddTokenAuth(token)

			res := MakeRequest(t, req, http.StatusOK)
			apiRuns := new(api.ListActionRunResponse)
			DecodeJSON(t, res, apiRuns)

			assert.Equal(t, int64(len(tt.expectedIDs)), apiRuns.TotalCount)
			assert.Len(t, apiRuns.Entries, len(tt.expectedIDs))

			resultIDs := make([]int64, apiRuns.TotalCount)
			for i, run := range apiRuns.Entries {
				resultIDs[i] = run.ID
			}

			assert.ElementsMatch(t, tt.expectedIDs, resultIDs)
		})
	}
}

func TestActionsAPIGetActionRun(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 63})
	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: repo.OwnerID})
	token := getUserToken(t, user.LowerName, auth_model.AccessTokenScopeWriteRepository)

	testqueries := []struct {
		name           string
		runID          int64
		expectedStatus int
	}{
		{
			name:           "existing return ok",
			runID:          892,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "non existing run",
			runID:          9876543210, // I hope this run will not exists, else just change it to another.
			expectedStatus: http.StatusNotFound,
		},
		{
			name:           "existing run but wrong repo should not be found",
			runID:          891,
			expectedStatus: http.StatusNotFound,
		},
	}

	for _, tt := range testqueries {
		t.Run(tt.name, func(t *testing.T) {
			req := NewRequest(t, http.MethodGet,
				fmt.Sprintf("/api/v1/repos/%s/%s/actions/runs/%d",
					repo.OwnerName, repo.Name, tt.runID,
				),
			)
			req.AddTokenAuth(token)

			res := MakeRequest(t, req, tt.expectedStatus)

			// Only interested in the data if 200 OK
			if tt.expectedStatus != http.StatusOK {
				return
			}

			dbRun := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: tt.runID})
			apiRun := new(api.ActionRun)
			DecodeJSON(t, res, apiRun)

			assert.Equal(t, dbRun.Index, apiRun.Index)
			assert.Equal(t, dbRun.Status.String(), apiRun.Status)
			assert.Equal(t, dbRun.CommitSHA, apiRun.CommitSHA)
			assert.Equal(t, dbRun.TriggerUserID, apiRun.TriggerUser.ID)
		})
	}
}

func TestAPIRepoActionsRunnerRegistrationTokenOperations(t *testing.T) {
	defer unittest.OverrideFixtures("tests/integration/fixtures/TestAPIRepoActionsRunnerRegistrationTokenOperations")()
	require.NoError(t, unittest.PrepareTestDatabase())

	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	session := loginUser(t, user2.Name)
	readToken := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadRepository)

	t.Run("GetRegistrationToken", func(t *testing.T) {
		request := NewRequest(t, "GET", "/api/v1/repos/user2/test_workflows/actions/runners/registration-token")
		request.AddTokenAuth(readToken)
		response := MakeRequest(t, request, http.StatusOK)

		var registrationToken shared.RegistrationToken
		DecodeJSON(t, response, &registrationToken)

		expected := shared.RegistrationToken{Token: "BzcgyhjWhLeKGA4ihJIigeRDrcxrFESd0yizEpb7xZJ"}

		assert.Equal(t, expected, registrationToken)
	})
}

func TestAPIRepoActionsRunnerOperations(t *testing.T) {
	defer unittest.OverrideFixtures("tests/integration/fixtures/TestAPIRepoActionsRunnerOperations")()
	require.NoError(t, unittest.PrepareTestDatabase())

	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	session := loginUser(t, user2.Name)
	readToken := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadRepository)
	writeToken := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository)

	t.Run("GetRunners", func(t *testing.T) {
		request := NewRequest(t, "GET", "/api/v1/repos/user2/test_workflows/actions/runners")
		request.AddTokenAuth(readToken)
		response := MakeRequest(t, request, http.StatusOK)

		assert.Equal(t, "2", response.Header().Get("X-Total-Count"))

		var runners []*api.ActionRunner
		DecodeJSON(t, response, &runners)

		runnerOne := &api.ActionRunner{
			ID:          899251,
			UUID:        "a3297f3a-ba5c-4a0f-878e-6cc8b8ac79ec",
			Name:        "runner-1-repository",
			Version:     "dev",
			OwnerID:     0,
			RepoID:      62,
			Description: "A superb runner",
			Labels:      []string{"debian", "gpu"},
			Status:      "offline",
		}
		runnerThree := &api.ActionRunner{
			ID:          899253,
			UUID:        "0a7e5e05-2da4-44d5-a72a-615da120cef6",
			Name:        "runner-3-repository",
			Version:     "11.3.1",
			OwnerID:     0,
			RepoID:      62,
			Description: "Another fine runner",
			Labels:      []string{"fedora"},
			Status:      "offline",
		}

		assert.ElementsMatch(t, []*api.ActionRunner{runnerOne, runnerThree}, runners)
	})

	t.Run("GetRunnersPaginated", func(t *testing.T) {
		request := NewRequest(t, "GET", "/api/v1/repos/user2/test_workflows/actions/runners?page=1&limit=1")
		request.AddTokenAuth(readToken)
		response := MakeRequest(t, request, http.StatusOK)

		var runners []*api.ActionRunner
		DecodeJSON(t, response, &runners)

		assert.NotEmpty(t, response.Header().Get("Link"))
		assert.NotEmpty(t, response.Header().Get("X-Total-Count"))
		assert.Len(t, runners, 1)
	})

	t.Run("GetRunner", func(t *testing.T) {
		request := NewRequest(t, "GET", "/api/v1/repos/user2/test_workflows/actions/runners/899251")
		request.AddTokenAuth(readToken)
		response := MakeRequest(t, request, http.StatusOK)

		var runner *api.ActionRunner
		DecodeJSON(t, response, &runner)

		runnerOne := &api.ActionRunner{
			ID:          899251,
			UUID:        "a3297f3a-ba5c-4a0f-878e-6cc8b8ac79ec",
			Name:        "runner-1-repository",
			Version:     "dev",
			OwnerID:     0,
			RepoID:      62,
			Description: "A superb runner",
			Labels:      []string{"debian", "gpu"},
			Status:      "offline",
		}

		assert.Equal(t, runnerOne, runner)
	})

	t.Run("DeleteRunner", func(t *testing.T) {
		url := "/api/v1/repos/user2/test_workflows/actions/runners/899253"

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
