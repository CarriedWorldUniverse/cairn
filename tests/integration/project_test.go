// Copyright 2023 The Gitea Authors. All rights reserved.
// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"testing"

	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	org_model "forgejo.org/models/organization"
	project_model "forgejo.org/models/project"
	repo_model "forgejo.org/models/repo"
	unit_model "forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	repo_service "forgejo.org/services/repository"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrivateRepoProject(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	// not logged in user
	req := NewRequest(t, "GET", "/user31/-/projects")
	MakeRequest(t, req, http.StatusNotFound)

	sess := loginUser(t, "user1")
	req = NewRequest(t, "GET", "/user31/-/projects")
	sess.MakeRequest(t, req, http.StatusOK)
}

func TestMoveRepoProjectColumns(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo2 := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 2})

	project1 := project_model.Project{
		Title:        "new created project",
		RepoID:       repo2.ID,
		Type:         project_model.TypeRepository,
		TemplateType: project_model.TemplateTypeNone,
	}
	err := project_model.NewProject(db.DefaultContext, &project1)
	require.NoError(t, err)

	for i := range 3 {
		err = project_model.NewColumn(db.DefaultContext, &project_model.Column{
			Title:     fmt.Sprintf("column %d", i+1),
			ProjectID: project1.ID,
		})
		require.NoError(t, err)
	}

	columns, err := project1.GetColumns(db.DefaultContext)
	require.NoError(t, err)
	assert.Len(t, columns, 3)
	assert.EqualValues(t, 0, columns[0].Sorting)
	assert.EqualValues(t, 1, columns[1].Sorting)
	assert.EqualValues(t, 2, columns[2].Sorting)

	sess := loginUser(t, "user1")
	req := NewRequestWithJSON(t, "POST", fmt.Sprintf("/%s/projects/%d/move", repo2.FullName(), project1.ID), map[string]any{
		"columns": []map[string]any{
			{"columnID": columns[1].ID, "sorting": 0},
			{"columnID": columns[2].ID, "sorting": 1},
			{"columnID": columns[0].ID, "sorting": 2},
		},
	})
	sess.MakeRequest(t, req, http.StatusOK)

	columnsAfter, err := project1.GetColumns(db.DefaultContext)
	require.NoError(t, err)
	assert.Len(t, columns, 3)
	assert.Equal(t, columns[1].ID, columnsAfter[0].ID)
	assert.Equal(t, columns[2].ID, columnsAfter[1].ID)
	assert.Equal(t, columns[0].ID, columnsAfter[2].ID)

	require.NoError(t, project_model.DeleteProjectByID(db.DefaultContext, project1.ID))
}

func TestChangeStatusProject(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	user5 := loginUser(t, "user5")
	user2 := loginUser(t, "user2")

	t.Run("User", func(t *testing.T) {
		project4CloseURL := "/user2/-/projects/4/close"

		t.Run("Doer is not context user", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()

			user5.MakeRequest(t, NewRequest(t, "POST", project4CloseURL), http.StatusNotFound)
			unittest.AssertExistsIf(t, true, &project_model.Project{ID: 4}, "is_closed = false")
		})

		t.Run("Wrong ID", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()

			user5.MakeRequest(t, NewRequest(t, "POST", "/user5/-/projects/4/close"), http.StatusNotFound)
			unittest.AssertExistsIf(t, true, &project_model.Project{ID: 4}, "is_closed = false")

			user5.MakeRequest(t, NewRequest(t, "POST", "/user5/-/projects/1/close"), http.StatusNotFound)
			unittest.AssertExistsIf(t, true, &project_model.Project{ID: 1}, "is_closed = false")

			user5.MakeRequest(t, NewRequest(t, "POST", "/user5/-/projects/7/close"), http.StatusNotFound)
			unittest.AssertExistsIf(t, true, &project_model.Project{ID: 7}, "is_closed = false")
		})

		t.Run("Normal", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()

			user2.MakeRequest(t, NewRequest(t, "POST", project4CloseURL), http.StatusOK)
			unittest.AssertExistsIf(t, true, &project_model.Project{ID: 4}, "is_closed = true")
		})
	})

	t.Run("Organization", func(t *testing.T) {
		project7CloseURL := "/org3/-/projects/7/close"

		t.Run("Doer does not have permission", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()

			user5.MakeRequest(t, NewRequest(t, "POST", project7CloseURL), http.StatusNotFound)
			unittest.AssertExistsIf(t, true, &project_model.Project{ID: 7}, "is_closed = false")
		})

		t.Run("Normal", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()

			user2.MakeRequest(t, NewRequest(t, "POST", project7CloseURL), http.StatusOK)
			unittest.AssertExistsIf(t, true, &project_model.Project{ID: 7}, "is_closed = true")
		})
	})

	t.Run("Repository", func(t *testing.T) {
		project1CloseURL := "/user2/repo1/projects/1/close"

		t.Run("Doer does not have permission", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()

			user5.MakeRequest(t, NewRequest(t, "POST", project1CloseURL), http.StatusNotFound)
			unittest.AssertExistsIf(t, true, &project_model.Project{ID: 1}, "is_closed = false")
		})

		t.Run("Wrong ID", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()

			user5.MakeRequest(t, NewRequest(t, "POST", "/user5/repo4/projects/1/close"), http.StatusNotFound)
			unittest.AssertExistsIf(t, true, &project_model.Project{ID: 1}, "is_closed = false")
		})

		t.Run("Normal", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()

			user2.MakeRequest(t, NewRequest(t, "POST", project1CloseURL), http.StatusOK)
			unittest.AssertExistsIf(t, true, &project_model.Project{ID: 1}, "is_closed = true")
		})
	})
}

func TestAssignProject(t *testing.T) {
	defer unittest.OverrideFixtures("tests/integration/fixtures/TestAssignProject/")()
	defer tests.PrepareTestEnv(t)()

	ctx := t.Context()

	newTestIssue := func(t *testing.T, session *TestSession, owner *user_model.User, repo *repo_model.Repository) (*issues_model.Issue, string, string) {
		t.Helper()

		issueURL := testNewIssue(t, session, owner.Name, repo.Name, "Hello", "World")
		indexStr := issueURL[strings.LastIndexByte(issueURL, '/')+1:]
		index, err := strconv.Atoi(indexStr)
		require.NoError(t, err, "Invalid issue href: %s", issueURL)

		issue := &issues_model.Issue{RepoID: repo.ID, Index: int64(index)}
		unittest.AssertExistsAndLoadBean(t, issue)

		issueID := strconv.FormatInt(issue.ID, 10)
		return issue, indexStr, issueID
	}

	updateIssueProject := func(t *testing.T, session *TestSession, projectID, issueID, owner, repo string, expectedStatus int) {
		t.Helper()

		req := NewRequestWithValues(t, "POST", path.Join(owner, repo, "issues", "projects"), map[string]string{
			"issue_ids": issueID,
			"id":        projectID,
		})
		session.MakeRequest(t, req, expectedStatus)
	}

	// User
	t.Run("UserProjectOn+RepoProjectOff", func(tt *testing.T) {
		defer tests.PrintCurrentTest(tt)()
		repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 4})
		user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 5})

		session := loginUser(tt, user.Name)
		issue, _, issueID := newTestIssue(tt, session, user, repo)

		updateIssueProject(tt, session, "1003", issueID, user.Name, repo.Name, http.StatusOK)
		require.NoError(tt, issue.LoadProject(db.DefaultContext))
		require.NotNil(tt, issue.Project)
		require.Equal(tt, int64(1003), issue.Project.ID)
	})

	// Team 1001 - enabled project unit
	team := unittest.AssertExistsAndLoadBean(t, &org_model.Team{ID: 1001})
	require.NoError(t, team.LoadMembers(ctx))
	require.NoError(t, team.LoadRepositories(ctx))

	user := team.Members[0]
	repo := team.Repos[0]
	org := team.GetOrg(ctx)

	session := loginUser(t, user.Name)

	t.Run("OrgProjectOn+RepoProjectOn", func(tt *testing.T) {
		defer tests.PrintCurrentTest(tt)()
		issue, _, issueID := newTestIssue(tt, session, org.AsUser(), repo)

		updateIssueProject(tt, session, "1001", issueID, org.Name, repo.Name, http.StatusOK)

		require.NoError(tt, issue.LoadProject(db.DefaultContext))
		require.NotNil(tt, issue.Project)
		require.Equal(tt, int64(1001), issue.Project.ID)
	})

	// Disable repository project unit
	require.NoError(t, repo_service.UpdateRepositoryUnits(ctx, repo, nil, []unit_model.Type{unit_model.TypeProjects}))
	t.Run("OrgProjectOn+RepoProjectOff", func(tt *testing.T) {
		defer tests.PrintCurrentTest(tt)()
		issue, _, issueID := newTestIssue(tt, session, org.AsUser(), repo)

		updateIssueProject(tt, session, "1001", issueID, org.Name, repo.Name, http.StatusOK)
		require.NoError(tt, issue.LoadProject(db.DefaultContext))
		require.NotNil(tt, issue.Project)
		require.Equal(tt, int64(1001), issue.Project.ID)
	})

	// Team 1002 - disabled project unit
	team = unittest.AssertExistsAndLoadBean(t, &org_model.Team{ID: 1002})
	require.NoError(t, team.LoadMembers(ctx))
	require.NoError(t, team.LoadRepositories(ctx))

	user = team.Members[0]
	repo = team.Repos[0]
	org = team.GetOrg(ctx)

	session = loginUser(t, user.Name)

	t.Run("OrgProjectOff+RepoProjectOn", func(tt *testing.T) {
		defer tests.PrintCurrentTest(tt)()
		issue, _, issueID := newTestIssue(tt, session, org.AsUser(), repo)

		updateIssueProject(tt, session, "1002", issueID, org.Name, repo.Name, http.StatusOK)
		require.NoError(tt, issue.LoadProject(db.DefaultContext))
		require.NotNil(tt, issue.Project)
		require.Equal(tt, int64(1002), issue.Project.ID)
	})

	// Disable repository project unit
	require.NoError(t, repo_service.UpdateRepositoryUnits(ctx, repo, nil, []unit_model.Type{unit_model.TypeProjects}))
	t.Run("OrgProjectOff+RepoProjectOff", func(tt *testing.T) {
		defer tests.PrintCurrentTest(tt)()
		issue, _, issueID := newTestIssue(tt, session, org.AsUser(), repo)

		updateIssueProject(tt, session, "1002", issueID, org.Name, repo.Name, http.StatusOK)
		require.NoError(tt, issue.LoadProject(db.DefaultContext))
		require.NotNil(tt, issue.Project)
		require.Equal(tt, int64(1002), issue.Project.ID)
	})
}
