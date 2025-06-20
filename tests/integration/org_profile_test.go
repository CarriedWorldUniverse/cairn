// Copyright 2024 The Forgejo Authors c/o Codeberg e.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"
	files_service "forgejo.org/services/repository/files"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
)

func TestOrgProfile(t *testing.T) {
	onGiteaRun(t, func(t *testing.T, u *url.URL) {
		checkReadme := func(t *testing.T, title, readmeFilename string, expectedCount int) {
			t.Run(title, func(t *testing.T) {
				defer tests.PrintCurrentTest(t)()

				// Prepare the test repository
				org3 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 3})

				var ops []*files_service.ChangeRepoFile
				op := "create"
				if readmeFilename != "README.md" {
					ops = append(ops, &files_service.ChangeRepoFile{
						Operation: "delete",
						TreePath:  "README.md",
					})
				} else {
					op = "update"
				}
				if readmeFilename != "" {
					ops = append(ops, &files_service.ChangeRepoFile{
						Operation:     op,
						TreePath:      readmeFilename,
						ContentReader: strings.NewReader("# Hi!\n"),
					})
				}

				_, _, f := tests.CreateDeclarativeRepo(t, org3, ".profile", nil, nil, ops)
				defer f()

				// Perform the test
				req := NewRequest(t, "GET", "/org3")
				resp := MakeRequest(t, req, http.StatusOK)

				doc := NewHTMLParser(t, resp.Body)
				readmeCount := doc.Find("#readme_profile").Length()

				assert.Equal(t, expectedCount, readmeCount)
			})
		}

		checkReadme(t, "No readme", "", 0)
		checkReadme(t, "README.md", "README.md", 1)
		checkReadme(t, "readme.md", "readme.md", 1)
		checkReadme(t, "ReadMe.mD", "ReadMe.mD", 1)
		checkReadme(t, "readme.org does not render", "README.org", 0)

		t.Run("readme-size", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()

			// Prepare the test repository
			org3 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 3})

			_, _, f := tests.CreateDeclarativeRepo(t, org3, ".profile", nil, nil, []*files_service.ChangeRepoFile{
				{
					Operation: "update",
					TreePath:  "README.md",
					ContentReader: strings.NewReader(`## Lorem ipsum
dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor incididunt ut labore et dolore magna aliqua.
## Ut enim ad minim veniam
quis nostrud exercitation ullamco laboris nisi ut aliquip ex ea commodo consequat. Duis aute irure dolor in reprehenderit in voluptate velit esse cillum dolore eu fugiat nulla pariatur. Excepteur sint occaecat cupidatat non proident, sunt in culpa qui officia deserunt mollit anim id est laborum`),
				},
			})
			defer f()

			t.Run("full", func(t *testing.T) {
				defer tests.PrintCurrentTest(t)()
				defer test.MockVariableValue(&setting.UI.MaxDisplayFileSize, 500)()

				req := NewRequest(t, "GET", "/org3")
				resp := MakeRequest(t, req, http.StatusOK)
				assert.Contains(t, resp.Body.String(), "Ut enim ad minim veniam")
				assert.Contains(t, resp.Body.String(), "mollit anim id est laborum")
			})

			t.Run("truncated", func(t *testing.T) {
				defer tests.PrintCurrentTest(t)()
				defer test.MockVariableValue(&setting.UI.MaxDisplayFileSize, 146)()

				req := NewRequest(t, "GET", "/org3")
				resp := MakeRequest(t, req, http.StatusOK)
				assert.Contains(t, resp.Body.String(), "Ut enim ad minim")
				assert.NotContains(t, resp.Body.String(), "veniam")
			})
		})
	})
}
