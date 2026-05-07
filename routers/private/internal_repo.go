// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package private

import (
	"context"
	"fmt"
	"net/http"

	repo_model "github.com/CarriedWorldUniverse/cairn/models/repo"
	"github.com/CarriedWorldUniverse/cairn/modules/gitrepo"
	"github.com/CarriedWorldUniverse/cairn/modules/log"
	"github.com/CarriedWorldUniverse/cairn/modules/private"
	app_context "github.com/CarriedWorldUniverse/cairn/services/context"
)

// This file contains common functions relating to setting the Repository for the internal routes

// RepoAssignment assigns the repository and gitrepository to the private context
func RepoAssignment(ctx *app_context.PrivateContext) context.CancelFunc {
	ownerName := ctx.Params(":owner")
	repoName := ctx.Params(":repo")

	repo := loadRepository(ctx, ownerName, repoName)
	if ctx.Written() {
		// Error handled in loadRepository
		return nil
	}

	gitRepo, err := gitrepo.OpenRepository(ctx, repo)
	if err != nil {
		log.Error("Failed to open repository: %s/%s Error: %v", ownerName, repoName, err)
		ctx.JSON(http.StatusInternalServerError, private.Response{
			Err: fmt.Sprintf("Failed to open repository: %s/%s Error: %v", ownerName, repoName, err),
		})
		return nil
	}

	ctx.Repo = &app_context.Repository{
		Repository: repo,
		GitRepo:    gitRepo,
	}

	// We opened it, we should close it
	cancel := func() {
		// If it's been set to nil then assume someone else has closed it.
		if ctx.Repo.GitRepo != nil {
			ctx.Repo.GitRepo.Close()
		}
	}

	return cancel
}

func loadRepository(ctx *app_context.PrivateContext, ownerName, repoName string) *repo_model.Repository {
	repo, err := repo_model.GetRepositoryByOwnerAndName(ctx, ownerName, repoName)
	if err != nil {
		log.Error("Failed to get repository: %s/%s Error: %v", ownerName, repoName, err)
		ctx.JSON(http.StatusInternalServerError, private.Response{
			Err: fmt.Sprintf("Failed to get repository: %s/%s Error: %v", ownerName, repoName, err),
		})
		return nil
	}
	if repo.OwnerName == "" {
		repo.OwnerName = ownerName
	}
	return repo
}
