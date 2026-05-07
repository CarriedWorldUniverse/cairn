// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package private

import (
	"net/http"

	repo_model "github.com/CarriedWorldUniverse/cairn/models/repo"
	"github.com/CarriedWorldUniverse/cairn/modules/log"
	"github.com/CarriedWorldUniverse/cairn/modules/private"
	"github.com/CarriedWorldUniverse/cairn/modules/web"
	"github.com/CarriedWorldUniverse/cairn/services/agit"
	app_context "github.com/CarriedWorldUniverse/cairn/services/context"
)

// HookProcReceive proc-receive hook - only handles agit Proc-Receive requests at present
func HookProcReceive(ctx *app_context.PrivateContext) {
	opts := web.GetForm(ctx).(*private.HookOptions)

	results, err := agit.ProcReceive(ctx, ctx.Repo.Repository, ctx.Repo.GitRepo, opts)
	if err != nil {
		if repo_model.IsErrUserDoesNotHaveAccessToRepo(err) {
			ctx.Error(http.StatusBadRequest, "UserDoesNotHaveAccessToRepo", err.Error())
		} else {
			log.Error(err.Error())
			ctx.JSON(http.StatusInternalServerError, private.Response{
				Err: err.Error(),
			})
		}

		return
	}

	ctx.JSON(http.StatusOK, private.HookProcReceiveResult{
		Results: results,
	})
}
