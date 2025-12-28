// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"net/url"
	"strings"
	"testing"

	unit_model "forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/setting"
	files_service "forgejo.org/services/repository/files"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActionFetchTask_TaskCapacity(t *testing.T) {
	if !setting.Database.Type.IsSQLite3() {
		// mock repo runner only supported on SQLite testing
		t.Skip()
	}

	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})

		// create the repo
		repo, _, f := tests.CreateDeclarativeRepo(t, user2, "repo-many-tasks",
			[]unit_model.Type{unit_model.TypeActions}, nil,
			[]*files_service.ChangeRepoFile{
				{
					Operation: "create",
					TreePath:  ".forgejo/workflows/matrix.yml",
					ContentReader: strings.NewReader(`
on:
  push:
jobs:
  job1:
    strategy:
      # matrix creates 125 different jobs from one push...
      matrix:
        d1: [a, b, c, d, e]
        d2: [a, b, c, d, e]
        d3: [a, b, c, d, e]
    runs-on: ubuntu-latest
    steps:
      - run: echo ${{ matrix.d1 }} ${{ matrix.d2 }} ${{ matrix.d3 }}
      - run: sleep 2
`),
				},
			},
		)
		defer f()

		runner := newMockRunner()
		runner.registerAsRepoRunner(t, user2.Name, repo.Name, "mock-runner", []string{"ubuntu-latest"})

		// Fetch with TaskCapacity undefined, set to nil, should return a single pending task
		task := runner.fetchTask(t)
		require.NotNil(t, task)
		assert.Contains(t, string(task.GetWorkflowPayload()), "name: job1 (a, a, a)")

		// After successfully fetching a task, the runner sets their next requested version to 0.  This allows it to
		// fetch back-to-back tasks without requiring that a server-side state change occurs.  That behaviour is
		// replicated here:
		runner.lastTasksVersion = 0

		// Fetch with TaskCapacity set to 1; additional should be nil
		capacity := int64(1)
		task, addt := runner.fetchMultipleTasks(t, &capacity)
		require.NotNil(t, task, "task")
		assert.Nil(t, addt, "addt")
		assert.Contains(t, string(task.GetWorkflowPayload()), "name: job1 (a, a, b)")

		runner.lastTasksVersion = 0

		capacity = 10
		task, addt = runner.fetchMultipleTasks(t, &capacity)
		require.NotNil(t, task, "task")
		require.NotNil(t, addt, "addt")
		assert.Contains(t, string(task.GetWorkflowPayload()), "name: job1 (a, a, c)")
		require.Len(t, addt, 9)
		assert.Contains(t, string(addt[0].GetWorkflowPayload()), "name: job1 (a, a, d)")
	})
}
