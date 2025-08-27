// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
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

func TestActionsViewArtifactDeletion(t *testing.T) {
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
