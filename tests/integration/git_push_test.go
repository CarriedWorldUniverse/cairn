// Copyright 2024 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/url"
	"testing"

	"code.gitea.io/gitea/models/db"
	repo_model "code.gitea.io/gitea/models/repo"
	"code.gitea.io/gitea/models/unittest"
	user_model "code.gitea.io/gitea/models/user"
	repo_module "code.gitea.io/gitea/modules/repository"
	repo_service "code.gitea.io/gitea/services/repository"

	"github.com/stretchr/testify/require"
)

func TestOptionsGitPush(t *testing.T) {
	onGiteaRun(t, testOptionsGitPush)
}

func testOptionsGitPush(t *testing.T, u *url.URL) {
	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	repo, err := repo_service.CreateRepository(db.DefaultContext, user, user, repo_service.CreateRepoOptions{
		Name:          "repo-to-push",
		Description:   "test git push",
		AutoInit:      false,
		DefaultBranch: "main",
		IsPrivate:     false,
	})
	require.NoError(t, err)
	require.NotEmpty(t, repo)

	gitPath := t.TempDir()

	doGitInitTestRepository(gitPath)(t)

	u.Path = repo.FullName() + ".git"
	u.User = url.UserPassword(user.LowerName, userPassword)
	doGitAddRemote(gitPath, "origin", u)(t)

	{
		// owner sets private & template to true via push options
		branchName := "branch1"
		doGitCreateBranch(gitPath, branchName)(t)
		doGitPushTestRepository(gitPath, "origin", branchName, "-o", "repo.private=true", "-o", "repo.template=true")(t)
		repo, err := repo_model.GetRepositoryByOwnerAndName(db.DefaultContext, user.Name, "repo-to-push")
		require.NoError(t, err)
		require.True(t, repo.IsPrivate)
		require.True(t, repo.IsTemplate)
	}

	{
		// owner sets private & template to false via push options
		branchName := "branch2"
		doGitCreateBranch(gitPath, branchName)(t)
		doGitPushTestRepository(gitPath, "origin", branchName, "-o", "repo.private=false", "-o", "repo.template=false")(t)
		repo, err = repo_model.GetRepositoryByOwnerAndName(db.DefaultContext, user.Name, "repo-to-push")
		require.NoError(t, err)
		require.False(t, repo.IsPrivate)
		require.False(t, repo.IsTemplate)
	}

	{
		// create a collaborator with write access
		collaborator := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 5})
		u.User = url.UserPassword(collaborator.LowerName, userPassword)
		doGitAddRemote(gitPath, "collaborator", u)(t)
		repo, err := repo_model.GetRepositoryByOwnerAndName(db.DefaultContext, user.Name, "repo-to-push")
		require.NoError(t, err)
		repo_module.AddCollaborator(db.DefaultContext, repo, collaborator)
	}

	{
		// collaborator with write access is allowed to push
		branchName := "branch3"
		doGitCreateBranch(gitPath, branchName)(t)
		doGitPushTestRepository(gitPath, "collaborator", branchName)(t)
	}

	{
		// collaborator with write access fails to change private & template via push options
		branchName := "branch4"
		doGitCreateBranch(gitPath, branchName)(t)
		doGitPushTestRepositoryFail(gitPath, "collaborator", branchName, "-o", "repo.private=true", "-o", "repo.template=true")(t)
		repo, err = repo_model.GetRepositoryByOwnerAndName(db.DefaultContext, user.Name, "repo-to-push")
		require.NoError(t, err)
		require.False(t, repo.IsPrivate)
		require.False(t, repo.IsTemplate)
	}

	require.NoError(t, repo_service.DeleteRepositoryDirectly(db.DefaultContext, user, user.ID, repo.ID))
}
