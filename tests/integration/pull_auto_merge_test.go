// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	unit_model "forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	app_context "forgejo.org/services/context"
	files_service "forgejo.org/services/repository/files"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPullRemoveAutomerge(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		user5 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 5})
		user5Session := loginUser(t, user5.Name)
		user2Session := loginUser(t, "user2")

		repo, _, f := tests.CreateDeclarativeRepo(t, user5, "",
			[]unit_model.Type{unit_model.TypeCode, unit_model.TypePullRequests}, nil,
			[]*files_service.ChangeRepoFile{
				{
					Operation: "create",
					TreePath:  "FUNFACT",
					ContentReader: strings.NewReader(
						"The Netherlands got its first openly gay prime minister today."),
				},
			},
		)
		defer f()

		dstPath := t.TempDir()
		cloneURL, _ := url.Parse(fmt.Sprintf("%suser5/%s.git", u.String(), repo.Name))
		cloneURL.User = url.UserPassword("user5", userPassword)
		require.NoError(t, git.CloneWithArgs(t.Context(), nil, cloneURL.String(), dstPath, git.CloneRepoOptions{}))
		doGitSetRemoteURL(dstPath, "origin", cloneURL)(t)

		require.NoError(t, git.NewCommand(t.Context(), "switch", "-c", "new-fun-fact").Run(&git.RunOpts{Dir: dstPath}))

		require.NoError(t, os.WriteFile(path.Join(dstPath, "README.md"), []byte("The house of representative already had that in 1937."), 0o600))
		require.NoError(t, git.AddChanges(dstPath, true))
		require.NoError(t, git.CommitChanges(dstPath, git.CommitChangesOptions{
			Committer: &git.Signature{
				Email: "user2@example.com",
				Name:  "user2",
				When:  time.Now(),
			},
			Author: &git.Signature{
				Email: "user2@example.com",
				Name:  "user2",
				When:  time.Now(),
			},
			Message: "Update funfact.",
		}))

		require.NoError(t, git.NewCommand(t.Context(), "push", "origin", "HEAD:refs/for/main", "-o", "topic=new-fun-fact").Run(&git.RunOpts{Dir: dstPath}))

		// Create a protected branch rule for automerge.
		user5Session.MakeRequest(t, NewRequestWithValues(t, "POST", fmt.Sprintf("/%s/settings/branches/edit", repo.FullName()), map[string]string{
			"rule_name":          "main",
			"required_approvals": "1",
		}), http.StatusSeeOther)

		// Start a automerge for new pull request.
		user5Session.MakeRequest(t, NewRequestWithValues(t, "POST", fmt.Sprintf("/%s/pulls/1/merge", repo.FullName()), map[string]string{
			"merge_message_field":       "I love automation when it works",
			"do":                        "merge",
			"merge_when_checks_succeed": "true",
		}), http.StatusOK)

		t.Run("No permission", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()

			user2Session.MakeRequest(t, NewRequestWithValues(t, "POST", fmt.Sprintf("/%s/pulls/1/cancel_auto_merge", repo.FullName()), nil), http.StatusSeeOther)

			flashCookie := user2Session.GetCookie(app_context.CookieNameFlash)
			assert.NotNil(t, flashCookie)
			assert.Equal(t, "error%3DYou%2Bdo%2Bnot%2Bhave%2Bpermission%2Bto%2Bcancel%2Bthis%2Bpull%2Brequest%2527s%2Bauto%2Bmerge.", flashCookie.Value)
		})

		t.Run("Normal", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()

			user5Session.MakeRequest(t, NewRequestWithValues(t, "POST", fmt.Sprintf("/%s/pulls/1/cancel_auto_merge", repo.FullName()), nil), http.StatusSeeOther)

			flashCookie := user5Session.GetCookie(app_context.CookieNameFlash)
			assert.NotNil(t, flashCookie)
			assert.Equal(t, "success%3DThe%2Bauto%2Bmerge%2Bwas%2Bcanceled%2Bfor%2Bthis%2Bpull%2Brequest.", flashCookie.Value)
		})
	})
}
