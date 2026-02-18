// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"fmt"
	"net/http"
	"testing"

	actions_model "forgejo.org/models/actions"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	app_context "forgejo.org/services/context"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
)

func TestRunnerModification(t *testing.T) {
	defer unittest.OverrideFixtures("tests/integration/fixtures/TestRunnerModification")()
	defer tests.PrepareTestEnv(t)()

	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	userRunner := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{ID: 1001, OwnerID: user.ID})
	userURL := "/user/settings/actions/runners"
	org := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 3, Type: user_model.UserTypeOrganization})
	orgRunner := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{ID: 1002, OwnerID: org.ID})
	orgURL := "/org/" + org.Name + "/settings/actions/runners"
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1, OwnerID: user.ID})
	repoRunner := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{ID: 1003, RepoID: repo.ID})
	repoURL := "/" + repo.FullName() + "/settings/actions/runners"
	admin := unittest.AssertExistsAndLoadBean(t, &user_model.User{IsAdmin: true})
	globalRunner := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{ID: 1004}, "owner_id = 0 AND repo_id = 0")
	adminURL := "/admin/actions/runners"

	adminSess := loginUser(t, admin.Name)
	sess := loginUser(t, user.Name)

	test := func(t *testing.T, fail bool, baseURL string, id int64) {
		defer tests.PrintCurrentTest(t, 1)()
		t.Helper()

		sess := sess
		if baseURL == adminURL {
			sess = adminSess
		}

		req := NewRequestWithValues(t, "POST", baseURL+fmt.Sprintf("/%d", id), map[string]string{
			"description": "New Description",
		})
		if fail {
			sess.MakeRequest(t, req, http.StatusNotFound)
		} else {
			sess.MakeRequest(t, req, http.StatusSeeOther)
			flashCookie := sess.GetCookie(app_context.CookieNameFlash)
			assert.NotNil(t, flashCookie)
			assert.Equal(t, "success%3DRunner%2Bedited%2Bsuccessfully", flashCookie.Value)
		}

		req = NewRequest(t, "POST", baseURL+fmt.Sprintf("/%d/delete", id))
		if fail {
			sess.MakeRequest(t, req, http.StatusNotFound)
		} else {
			sess.MakeRequest(t, req, http.StatusOK)
			flashCookie := sess.GetCookie(app_context.CookieNameFlash)
			assert.NotNil(t, flashCookie)
			assert.Equal(t, "success%3DRunner%2Bdeleted%2Bsuccessfully", flashCookie.Value)
		}
	}

	t.Run("User runner", func(t *testing.T) {
		t.Run("Organisation", func(t *testing.T) {
			test(t, true, orgURL, userRunner.ID)
		})
		t.Run("Repository", func(t *testing.T) {
			test(t, true, repoURL, userRunner.ID)
		})
		t.Run("User", func(t *testing.T) {
			test(t, false, userURL, userRunner.ID)
		})
	})

	t.Run("Organisation runner", func(t *testing.T) {
		t.Run("Repository", func(t *testing.T) {
			test(t, true, repoURL, orgRunner.ID)
		})
		t.Run("User", func(t *testing.T) {
			test(t, true, userURL, orgRunner.ID)
		})
		t.Run("Organisation", func(t *testing.T) {
			test(t, false, orgURL, orgRunner.ID)
		})
	})

	t.Run("Repository runner", func(t *testing.T) {
		t.Run("Organisation", func(t *testing.T) {
			test(t, true, orgURL, repoRunner.ID)
		})
		t.Run("User", func(t *testing.T) {
			test(t, true, userURL, repoRunner.ID)
		})
		t.Run("Repository", func(t *testing.T) {
			test(t, false, repoURL, repoRunner.ID)
		})
	})

	t.Run("Global runner", func(t *testing.T) {
		t.Run("Organisation", func(t *testing.T) {
			test(t, true, orgURL, globalRunner.ID)
		})
		t.Run("User", func(t *testing.T) {
			test(t, true, userURL, globalRunner.ID)
		})
		t.Run("Repository", func(t *testing.T) {
			test(t, true, repoURL, globalRunner.ID)
		})
		t.Run("Admin", func(t *testing.T) {
			test(t, false, adminURL, globalRunner.ID)
		})
	})
}

func TestRunnerVisibility(t *testing.T) {
	defer unittest.OverrideFixtures("tests/integration/fixtures/TestRunnerVisibility")()
	defer tests.PrepareTestEnv(t)()

	admin := unittest.AssertExistsAndLoadBean(t, &user_model.User{IsAdmin: true})
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})

	runnerOne := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{ID: 719931})
	runnerTwo := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{ID: 719932})
	runnerThree := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{ID: 719933})
	runnerFour := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{ID: 719934})
	runnerFive := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{ID: 719935})
	runnerSix := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{ID: 719936})

	testCases := []struct {
		name              string
		user              *user_model.User
		url               string
		expectedRunners   []*actions_model.ActionRunner
		unexpectedRunners []*actions_model.ActionRunner
	}{
		{
			name:              "admin-sees-all",
			user:              admin,
			url:               "/admin/actions/runners",
			expectedRunners:   []*actions_model.ActionRunner{runnerOne, runnerTwo, runnerThree, runnerFour, runnerFive, runnerSix},
			unexpectedRunners: []*actions_model.ActionRunner{},
		},
		{
			name:              "user-sees-own-and-global",
			user:              user2,
			url:               "user/settings/actions/runners",
			expectedRunners:   []*actions_model.ActionRunner{runnerTwo, runnerFour},
			unexpectedRunners: []*actions_model.ActionRunner{runnerOne, runnerThree, runnerFive, runnerSix},
		},
		{
			name:              "org-sees-own-and-global",
			user:              user2,
			url:               "/org/org3/settings/actions/runners",
			expectedRunners:   []*actions_model.ActionRunner{runnerOne, runnerFour},
			unexpectedRunners: []*actions_model.ActionRunner{runnerTwo, runnerThree, runnerFive, runnerSix},
		},
		{
			name:              "user-repo-sees-own-and-users-and-global",
			user:              user2,
			url:               "/user2/test_workflows/settings/actions/runners",
			expectedRunners:   []*actions_model.ActionRunner{runnerTwo, runnerFour, runnerSix},
			unexpectedRunners: []*actions_model.ActionRunner{runnerOne, runnerThree, runnerFive},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			session := loginUser(t, testCase.user.Name)

			request := NewRequest(t, "GET", testCase.url)
			response := session.MakeRequest(t, request, http.StatusOK)

			htmlDoc := NewHTMLParser(t, response.Body)
			for _, expectedRunner := range testCase.expectedRunners {
				selector := fmt.Sprintf("td:contains('%s')", expectedRunner.Name)
				assert.Equal(t, 1, htmlDoc.Find(selector).Length(), "runner '%s' could not be found", expectedRunner.Name)
			}
			for _, unexpectedRunner := range testCase.unexpectedRunners {
				selector := fmt.Sprintf("td:contains('%s')", unexpectedRunner.Name)
				assert.Zero(t, htmlDoc.Find(selector).Length(), "runner '%s' is unexpectedly present", unexpectedRunner.Name)
			}
		})
	}
}
