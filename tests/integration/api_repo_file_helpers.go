// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"strings"

	"github.com/CarriedWorldUniverse/cairn/models"
	repo_model "github.com/CarriedWorldUniverse/cairn/models/repo"
	user_model "github.com/CarriedWorldUniverse/cairn/models/user"
	"github.com/CarriedWorldUniverse/cairn/modules/git"
	"github.com/CarriedWorldUniverse/cairn/modules/gitrepo"
	api "github.com/CarriedWorldUniverse/cairn/modules/structs"
	files_service "github.com/CarriedWorldUniverse/cairn/services/repository/files"
)

func createFileInBranch(user *user_model.User, repo *repo_model.Repository, treePath, branchName, content string) (*api.FilesResponse, error) {
	opts := &files_service.ChangeRepoFilesOptions{
		Files: []*files_service.ChangeRepoFile{
			{
				Operation:     "create",
				TreePath:      treePath,
				ContentReader: strings.NewReader(content),
			},
		},
		OldBranch: branchName,
		Author:    nil,
		Committer: nil,
	}
	return files_service.ChangeRepoFiles(git.DefaultContext, repo, user, opts)
}

func deleteFileInBranch(user *user_model.User, repo *repo_model.Repository, treePath, branchName string) error {
	commitID, err := gitrepo.GetBranchCommitID(git.DefaultContext, repo, branchName)
	if err != nil {
		return err
	}

	opts := &files_service.ChangeRepoFilesOptions{
		Files: []*files_service.ChangeRepoFile{
			{
				Operation: "delete",
				TreePath:  treePath,
			},
		},
		OldBranch:    branchName,
		Author:       nil,
		Committer:    nil,
		LastCommitID: commitID,
	}
	_, err = files_service.ChangeRepoFiles(git.DefaultContext, repo, user, opts)
	return err
}

func createOrReplaceFileInBranch(user *user_model.User, repo *repo_model.Repository, treePath, branchName, content string) error {
	err := deleteFileInBranch(user, repo, treePath, branchName)
	if err != nil && !models.IsErrRepoFileDoesNotExist(err) {
		return err
	}

	_, err = createFileInBranch(user, repo, treePath, branchName, content)
	return err
}

func createFile(user *user_model.User, repo *repo_model.Repository, treePath string) (*api.FilesResponse, error) {
	return createFileInBranch(user, repo, treePath, repo.DefaultBranch, "This is a NEW file")
}
