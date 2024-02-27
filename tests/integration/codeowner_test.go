// Copyright 2024 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"code.gitea.io/gitea/models/db"
	issues_model "code.gitea.io/gitea/models/issues"
	repo_model "code.gitea.io/gitea/models/repo"
	"code.gitea.io/gitea/models/unit"
	"code.gitea.io/gitea/models/unittest"
	user_model "code.gitea.io/gitea/models/user"
	"code.gitea.io/gitea/modules/git"
	repo_service "code.gitea.io/gitea/services/repository"
	files_service "code.gitea.io/gitea/services/repository/files"
	"code.gitea.io/gitea/tests"

	"github.com/stretchr/testify/assert"
)

func TestCodeOwner(t *testing.T) {
	onGiteaRun(t, func(t *testing.T, u *url.URL) {
		user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})

		// Create a new repository
		repo, err := repo_service.CreateRepository(db.DefaultContext, user2, user2, repo_service.CreateRepoOptions{
			Name:          "code-owner",
			Description:   "Temporary Repo",
			AutoInit:      true,
			Gitignores:    "",
			License:       "WTFPL",
			Readme:        "Default",
			DefaultBranch: "main",
		})
		assert.NoError(t, err)
		assert.NotEmpty(t, repo)

		err = repo_model.UpdateRepositoryUnits(repo, []repo_model.RepoUnit{{RepoID: repo.ID, Type: unit.TypePullRequests}}, nil)
		assert.NoError(t, err)

		resp, err := files_service.ChangeRepoFiles(git.DefaultContext, repo, user2, &files_service.ChangeRepoFilesOptions{
			Files: []*files_service.ChangeRepoFile{
				{
					Operation:     "create",
					TreePath:      "CODEOWNERS",
					ContentReader: strings.NewReader("README.md @user5\ntest-file @user4"),
				},
			},
			Message:   "add files",
			OldBranch: "main",
			NewBranch: "main",
			Author: &files_service.IdentityOptions{
				Name:  user2.Name,
				Email: user2.Email,
			},
			Committer: &files_service.IdentityOptions{
				Name:  user2.Name,
				Email: user2.Email,
			},
			Dates: &files_service.CommitDateOptions{
				Author:    time.Now(),
				Committer: time.Now(),
			},
		})
		assert.NoError(t, err)
		assert.NotEmpty(t, resp)

		dstPath := t.TempDir()
		r := fmt.Sprintf("%suser2/%s.git", u.String(), repo.Name)
		u, _ = url.Parse(r)
		u.User = url.UserPassword("user2", userPassword)
		assert.NoError(t, git.CloneWithArgs(context.Background(), nil, u.String(), dstPath, git.CloneRepoOptions{}))

		t.Run("Normal", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()

			err := os.WriteFile(path.Join(dstPath, "README.md"), []byte("## test content"), 0o666)
			assert.NoError(t, err)

			err = git.AddChanges(dstPath, true)
			assert.NoError(t, err)

			err = git.CommitChanges(dstPath, git.CommitChangesOptions{
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
				Message: "Add README.",
			})
			assert.NoError(t, err)

			err = git.NewCommand(git.DefaultContext, "push", "origin", "HEAD:refs/for/main", "-o", "topic=codeowner-normal").Run(&git.RunOpts{Dir: dstPath})
			assert.NoError(t, err)

			pr := unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{BaseRepoID: repo.ID, HeadBranch: "user2/codeowner-normal"})
			unittest.AssertExistsIf(t, true, &issues_model.Review{IssueID: pr.IssueID, Type: issues_model.ReviewTypeRequest, ReviewerID: 5})
		})

		t.Run("Out of date", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()

			// Push the changes made from the previous subtest.
			assert.NoError(t, git.NewCommand(git.DefaultContext, "push", "origin").Run(&git.RunOpts{Dir: dstPath}))

			// Reset the tree to the previous commit.
			assert.NoError(t, git.NewCommand(git.DefaultContext, "reset", "--hard", "HEAD~1").Run(&git.RunOpts{Dir: dstPath}))

			err := os.WriteFile(path.Join(dstPath, "test-file"), []byte("## test content"), 0o666)
			assert.NoError(t, err)

			err = git.AddChanges(dstPath, true)
			assert.NoError(t, err)

			err = git.CommitChanges(dstPath, git.CommitChangesOptions{
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
				Message: "Add test-file.",
			})
			assert.NoError(t, err)

			err = git.NewCommand(git.DefaultContext, "push", "origin", "HEAD:refs/for/main", "-o", "topic=codeowner-out-of-date").Run(&git.RunOpts{Dir: dstPath})
			assert.NoError(t, err)

			pr := unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{BaseRepoID: repo.ID, HeadBranch: "user2/codeowner-out-of-date"})
			unittest.AssertExistsIf(t, true, &issues_model.Review{IssueID: pr.IssueID, Type: issues_model.ReviewTypeRequest, ReviewerID: 4})
			unittest.AssertExistsIf(t, false, &issues_model.Review{IssueID: pr.IssueID, Type: issues_model.ReviewTypeRequest, ReviewerID: 5})
		})
	})
}
