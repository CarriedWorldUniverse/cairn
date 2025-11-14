// Copyright 2024 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	repo_model "forgejo.org/models/repo"
	unit_model "forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	files_service "forgejo.org/services/repository/files"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
)

func TestCitation(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})

		session := loginUser(t, user.LoginName)

		t.Run("No citation", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()

			repo, _, f := tests.CreateDeclarativeRepo(t, user, "citation-no-citation", []unit_model.Type{unit_model.TypeCode}, nil, nil)
			defer f()

			testCitationButtonExists(t, session, repo, "")
		})

		t.Run("cff citation", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()

			repo, f := createRepoWithDummyFile(t, user, "citation-cff", "CITATION.cff")
			defer f()

			testCitationButtonExists(t, session, repo, "CITATION.cff")
		})

		t.Run("bib citation", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()

			repo, f := createRepoWithDummyFile(t, user, "citation-bib", "CITATION.bib")
			defer f()

			testCitationButtonExists(t, session, repo, "CITATION.bib")
		})
	})
}

func testCitationButtonExists(t *testing.T, session *TestSession, repo *repo_model.Repository, file string) {
	req := NewRequest(t, "GET", repo.HTMLURL())
	resp := session.MakeRequest(t, req, http.StatusOK)
	doc := NewHTMLParser(t, resp.Body)

	links := doc.Find("a.citation-link")
	if file == "" {
		assert.Equal(t, 0, links.Length())
		return
	}

	assert.Equal(t, 1, links.Length())
	href, exists := links.Attr("href")
	assert.True(t, exists)
	assert.True(t, strings.HasSuffix(href, file))

	// request the citation file to check for webcomponent presence
	req = NewRequest(t, "GET", href)
	resp = session.MakeRequest(t, req, http.StatusOK)
	doc = NewHTMLParser(t, resp.Body)
	doc.AssertElement(t, `lazy-webc[tag="citation-information"]`, true)
}

func createRepoWithDummyFile(t *testing.T, user *user_model.User, repoName, fileName string) (*repo_model.Repository, func()) {
	repo, _, f := tests.CreateDeclarativeRepo(t, user, repoName, []unit_model.Type{unit_model.TypeCode}, nil, []*files_service.ChangeRepoFile{
		{
			Operation:     "create",
			TreePath:      fileName,
			ContentReader: strings.NewReader("citation-content"), // viewer requires some content
		},
	})

	return repo, f
}
