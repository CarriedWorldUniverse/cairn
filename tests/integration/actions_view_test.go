// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	actions_model "forgejo.org/models/actions"
	unit_model "forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	files_service "forgejo.org/services/repository/files"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActionViewsArtifactDeletion(t *testing.T) {
	onGiteaRun(t, func(t *testing.T, u *url.URL) {
		user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})

		// create the repo
		repo, _, f := tests.CreateDeclarativeRepo(t, user2, "",
			[]unit_model.Type{unit_model.TypeActions}, nil,
			[]*files_service.ChangeRepoFile{
				{
					Operation:     "create",
					TreePath:      ".gitea/workflows/pr.yml",
					ContentReader: strings.NewReader("name: test\non:\n  push:\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo helloworld\n"),
				},
			},
		)
		defer f()

		// a run has been created
		assert.Equal(t, 1, unittest.GetCount(t, &actions_model.ActionRun{RepoID: repo.ID}))

		// Load the run we just created
		run := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{RepoID: repo.ID})
		err := run.LoadAttributes(t.Context())
		require.NoError(t, err)

		// Visit it's web view
		req := NewRequest(t, "GET", run.HTMLURL())
		intermediateRedirect := MakeRequest(t, req, http.StatusTemporaryRedirect)

		finalURL := intermediateRedirect.Result().Header.Get("Location")
		req = NewRequest(t, "GET", finalURL)
		resp := MakeRequest(t, req, http.StatusOK)
		htmlDoc := NewHTMLParser(t, resp.Body)

		// Assert that the artifact deletion markup exists
		htmlDoc.AssertElement(t, "[data-locale-confirm-delete-artifact]", true)
	})
}

func TestActionViewsArtifactDownload(t *testing.T) {
	defer prepareTestEnvActionsArtifacts(t)()

	assertDataAttrs := func(t *testing.T, body *bytes.Buffer, runID int64) {
		t.Helper()
		run := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: runID})
		htmlDoc := NewHTMLParser(t, body)
		selector := "#repo-action-view"
		htmlDoc.AssertAttrEqual(t, selector, "data-run-id", fmt.Sprintf("%d", run.ID))
		htmlDoc.AssertAttrEqual(t, selector, "data-run-index", fmt.Sprintf("%d", run.Index))
	}

	t.Run("V3", func(t *testing.T) {
		runIndex := 187
		runID := int64(791)

		req := NewRequest(t, "GET", fmt.Sprintf("/user5/repo4/actions/runs/%d/artifacts", runIndex))
		resp := MakeRequest(t, req, http.StatusOK)
		assert.JSONEq(t, `{"artifacts":[{"name":"multi-file-download","size":2048,"status":"completed"}]}`, strings.TrimSuffix(resp.Body.String(), "\n"))

		req = NewRequest(t, "GET", fmt.Sprintf("/user5/repo4/actions/runs/%d", runIndex))
		intermediateRedirect := MakeRequest(t, req, http.StatusTemporaryRedirect)

		finalURL := intermediateRedirect.Result().Header.Get("Location")
		req = NewRequest(t, "GET", finalURL)
		resp = MakeRequest(t, req, http.StatusOK)
		assertDataAttrs(t, resp.Body, runID)

		req = NewRequest(t, "GET", fmt.Sprintf("/user5/repo4/actions/runs/%d/artifacts/multi-file-download", runID))
		resp = MakeRequest(t, req, http.StatusOK)
		assert.Contains(t, resp.Header().Get("content-disposition"), "multi-file-download.zip")
	})

	t.Run("V4", func(t *testing.T) {
		runIndex := 188
		runID := int64(792)

		req := NewRequest(t, "GET", fmt.Sprintf("/user5/repo4/actions/runs/%d/artifacts", runIndex))
		resp := MakeRequest(t, req, http.StatusOK)
		assert.JSONEq(t, `{"artifacts":[{"name":"artifact-v4-download","size":1024,"status":"completed"}]}`, strings.TrimSuffix(resp.Body.String(), "\n"))

		req = NewRequest(t, "GET", fmt.Sprintf("/user5/repo4/actions/runs/%d", runIndex))
		intermediateRedirect := MakeRequest(t, req, http.StatusTemporaryRedirect)

		finalURL := intermediateRedirect.Result().Header.Get("Location")
		req = NewRequest(t, "GET", finalURL)
		resp = MakeRequest(t, req, http.StatusOK)
		assertDataAttrs(t, resp.Body, runID)

		download := fmt.Sprintf("/user5/repo4/actions/runs/%d/artifacts/artifact-v4-download", runID)
		req = NewRequest(t, "GET", download)
		resp = MakeRequest(t, req, http.StatusOK)
		assert.Equal(t, "bytes", resp.Header().Get("accept-ranges"))
		assert.Contains(t, resp.Header().Get("content-disposition"), "artifact-v4-download.zip")
		assert.Equal(t, strings.Repeat("D", 1024), resp.Body.String())

		// Partial artifact download
		req = NewRequest(t, "GET", download).SetHeader("range", "bytes=0-99")
		resp = MakeRequest(t, req, http.StatusPartialContent)
		assert.Equal(t, "bytes 0-99/1024", resp.Header().Get("content-range"))
		assert.Equal(t, strings.Repeat("D", 100), resp.Body.String())
	})
}

func TestActionViewsView(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	req := NewRequest(t, "GET", "/user5/repo4/actions/runs/187")
	intermediateRedirect := MakeRequest(t, req, http.StatusTemporaryRedirect)

	finalURL := intermediateRedirect.Result().Header.Get("Location")
	req = NewRequest(t, "GET", finalURL)
	resp := MakeRequest(t, req, http.StatusOK)

	htmlDoc := NewHTMLParser(t, resp.Body)
	selector := "#repo-action-view"
	// Verify key properties going into the `repo-action-view` to initialize the Vue component.
	htmlDoc.AssertAttrEqual(t, selector, "data-run-index", "187")
	htmlDoc.AssertAttrEqual(t, selector, "data-job-index", "0")
	htmlDoc.AssertAttrEqual(t, selector, "data-attempt-number", "1")
	htmlDoc.AssertAttrPredicate(t, selector, "data-initial-post-response", func(actual string) bool {
		// Remove dynamic "duration" fields for comparison.
		pattern := `"duration":"[^"]*"`
		re := regexp.MustCompile(pattern)
		actualClean := re.ReplaceAllString(actual, `"duration":"_duration_"`)
		// Remove "time_since_started_html" fields for comparison since they're TZ-sensitive in the test
		pattern = `"time_since_started_html":".*?\\u003c/relative-time\\u003e"`
		re = regexp.MustCompile(pattern)
		actualClean = re.ReplaceAllString(actualClean, `"time_since_started_html":"_time_"`)

		return assert.JSONEq(t, "{\"state\":{\"run\":{\"preExecutionError\":\"\",\"link\":\"/user5/repo4/actions/runs/187\",\"title\":\"update actions\",\"titleHTML\":\"update actions\",\"status\":\"success\",\"canCancel\":false,\"canApprove\":false,\"canRerun\":false,\"canDeleteArtifact\":false,\"done\":true,\"jobs\":[{\"id\":192,\"name\":\"job_2\",\"status\":\"success\",\"canRerun\":false,\"duration\":\"_duration_\"}],\"commit\":{\"localeCommit\":\"Commit\",\"localePushedBy\":\"pushed by\",\"localeWorkflow\":\"Workflow\",\"shortSHA\":\"c2d72f5484\",\"link\":\"/user5/repo4/commit/c2d72f548424103f01ee1dc02889c1e2bff816b0\",\"pusher\":{\"displayName\":\"user1\",\"link\":\"/user1\"},\"branch\":{\"name\":\"master\",\"link\":\"/user5/repo4/src/branch/master\",\"isDeleted\":false}}},\"currentJob\":{\"title\":\"job_2\",\"detail\":\"Success\",\"steps\":[{\"summary\":\"Set up job\",\"duration\":\"_duration_\",\"status\":\"success\"},{\"summary\":\"Complete job\",\"duration\":\"_duration_\",\"status\":\"success\"}],\"allAttempts\":[{\"number\":3,\"time_since_started_html\":\"_time_\",\"status\":\"running\"},{\"number\":2,\"time_since_started_html\":\"_time_\",\"status\":\"success\"},{\"number\":1,\"time_since_started_html\":\"_time_\",\"status\":\"success\"}]}},\"logs\":{\"stepsLog\":[]}}\n", actualClean)
	})
	htmlDoc.AssertAttrEqual(t, selector, "data-initial-artifacts-response", "{\"artifacts\":[{\"name\":\"multi-file-download\",\"size\":2048,\"status\":\"completed\"}]}\n")
}

// Action re-run will redirect the user to an attempt that may not exist in the database yet, since attempts are only
// updated in the DB when jobs are picked up by runners.  This test is intended to ensure that a "future" attempt number
// can still be loaded into the repo-action-view, which will handle waiting & polling for it to have data.
func TestActionViewsViewAttemptOutOfRange(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	// For this test to accurately reflect an attempt not yet picked, it needs to be accessing an ActionRunJob with
	// TaskID: null... otherwise we can't fetch future unpersisted attempts.
	req := NewRequest(t, "GET", "/user5/repo4/actions/runs/190/jobs/0/attempt/100")
	resp := MakeRequest(t, req, http.StatusOK)

	htmlDoc := NewHTMLParser(t, resp.Body)
	selector := "#repo-action-view"
	// Verify key properties going into the `repo-action-view` to initialize the Vue component.
	htmlDoc.AssertAttrEqual(t, selector, "data-run-index", "190")
	htmlDoc.AssertAttrEqual(t, selector, "data-job-index", "0")
	htmlDoc.AssertAttrEqual(t, selector, "data-attempt-number", "100")
	htmlDoc.AssertAttrPredicate(t, selector, "data-initial-post-response", func(actual string) bool {
		// Remove dynamic "duration" fields for comparison.
		pattern := `"duration":"[^"]*"`
		re := regexp.MustCompile(pattern)
		actualClean := re.ReplaceAllString(actual, `"duration":"_duration_"`)
		// Remove "time_since_started_html" fields for comparison since they're TZ-sensitive in the test
		pattern = `"time_since_started_html":".*?\\u003c/relative-time\\u003e"`
		re = regexp.MustCompile(pattern)
		actualClean = re.ReplaceAllString(actualClean, `"time_since_started_html":"_time_"`)

		return assert.JSONEq(t, "{\"state\":{\"run\":{\"preExecutionError\":\"\",\"link\":\"/user5/repo4/actions/runs/190\",\"title\":\"job output\",\"titleHTML\":\"job output\",\"status\":\"success\",\"canCancel\":false,\"canApprove\":false,\"canRerun\":false,\"canDeleteArtifact\":false,\"done\":false,\"jobs\":[{\"id\":396,\"name\":\"job_2\",\"status\":\"waiting\",\"canRerun\":false,\"duration\":\"_duration_\"}],\"commit\":{\"localeCommit\":\"Commit\",\"localePushedBy\":\"pushed by\",\"localeWorkflow\":\"Workflow\",\"shortSHA\":\"c2d72f5484\",\"link\":\"/user5/repo4/commit/c2d72f548424103f01ee1dc02889c1e2bff816b0\",\"pusher\":{\"displayName\":\"user1\",\"link\":\"/user1\"},\"branch\":{\"name\":\"test\",\"link\":\"/user5/repo4/src/branch/test\",\"isDeleted\":true}}},\"currentJob\":{\"title\":\"job_2\",\"detail\":\"Waiting\",\"steps\":[],\"allAttempts\":null}},\"logs\":{\"stepsLog\":[]}}\n", actualClean)
	})
	htmlDoc.AssertAttrEqual(t, selector, "data-initial-artifacts-response", "{\"artifacts\":[]}\n")
}
