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
	api "forgejo.org/modules/structs"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
)

func TestActionsAPISearchActionJobs_GlobalRunner(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	job := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 393})
	adminUsername := "user1"
	token := getUserToken(t, adminUsername, auth_model.AccessTokenScopeWriteAdmin)

	req := NewRequest(
		t,
		"GET",
		fmt.Sprintf("/api/v1/admin/runners/jobs?labels=%s", "ubuntu-latest"),
	).AddTokenAuth(token)
	res := MakeRequest(t, req, http.StatusOK)

	var jobs []*api.ActionRunJob
	DecodeJSON(t, res, &jobs)

	assert.Len(t, jobs, 1)
	assert.Equal(t, job.ID, jobs[0].ID)
}

func TestActionsAPISearchActionJobs_GlobalRunnerAllPendingJobsWithoutLabels(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	job196 := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 196})
	job397 := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 397})

	adminUsername := "user1"
	token := getUserToken(t, adminUsername, auth_model.AccessTokenScopeWriteAdmin)

	req := NewRequest(t, "GET", "/api/v1/admin/runners/jobs?labels=").AddTokenAuth(token)
	res := MakeRequest(t, req, http.StatusOK)

	var jobs []*api.ActionRunJob
	DecodeJSON(t, res, &jobs)

	assert.Len(t, jobs, 2)
	assert.Equal(t, job397.ID, jobs[0].ID)
	assert.Equal(t, job196.ID, jobs[1].ID)
}

func TestActionsAPISearchActionJobs_GlobalRunnerAllPendingJobs(t *testing.T) {
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

	req := NewRequest(
		t,
		"GET",
		"/api/v1/admin/runners/jobs",
	).AddTokenAuth(token)
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
}
