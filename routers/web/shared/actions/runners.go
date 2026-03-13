// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"errors"
	"net/http"

	actions_model "forgejo.org/models/actions"
	"forgejo.org/models/db"
	"forgejo.org/modules/base"
	"forgejo.org/modules/log"
	"forgejo.org/modules/optional"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/util"
	"forgejo.org/modules/web"
	"forgejo.org/services/context"
	"forgejo.org/services/forms"

	gouuid "github.com/google/uuid"
)

// RunnersList renders the list of runners.
func RunnersList(ctx *context.Context, template base.TplName, opts actions_model.FindRunnerOptions) {
	runners, count, err := db.FindAndCount[actions_model.ActionRunner](ctx, opts)
	if err != nil {
		ctx.ServerError("CountRunners", err)
		return
	}

	if err := actions_model.RunnerList(runners).LoadAttributes(ctx); err != nil {
		ctx.ServerError("LoadAttributes", err)
		return
	}

	// ownid=0,repo_id=0,means this token is used for global
	ownerID := optional.None[int64]()
	if opts.OwnerID != 0 {
		ownerID = optional.Some(opts.OwnerID)
	}
	repoID := optional.None[int64]()
	if opts.RepoID != 0 {
		repoID = optional.Some(opts.RepoID)
	}

	var token *actions_model.ActionRunnerToken
	token, err = actions_model.GetLatestRunnerToken(ctx, ownerID, repoID)
	if errors.Is(err, util.ErrNotExist) || (token != nil && !token.IsActive) {
		token, err = actions_model.NewRunnerToken(ctx, ownerID, repoID)
		if err != nil {
			ctx.ServerError("CreateRunnerToken", err)
			return
		}
	} else if err != nil {
		ctx.ServerError("GetLatestRunnerToken", err)
		return
	}

	ctx.Data["PageIsSharedSettingsRunners"] = true
	ctx.Data["Title"] = ctx.Tr("actions.actions")
	ctx.Data["PageType"] = "runners"
	ctx.Data["Keyword"] = opts.Filter
	ctx.Data["Runners"] = runners
	ctx.Data["Total"] = count
	ctx.Data["RegistrationToken"] = token.Token
	ctx.Data["RunnerOwnerID"] = opts.OwnerID
	ctx.Data["RunnerRepoID"] = opts.RepoID
	ctx.Data["SortType"] = opts.Sort

	pager := context.NewPagination(int(count), opts.PageSize, opts.Page, 5)

	ctx.Data["Page"] = pager
	ctx.HTML(http.StatusOK, template)
}

// RunnerDetails displays detail information about each runner. The page is purely informational and visible to everyone
// who is allowed to use a runner.
func RunnerDetails(ctx *context.Context, runnerID, ownerID, repoID int64, template base.TplName, page int) {
	runner, err := actions_model.GetVisibleRunnerByID(ctx, runnerID, ownerID, repoID)
	if errors.Is(err, util.ErrNotExist) {
		ctx.NotFound("GetVisibleRunnerByID", err)
		return
	} else if err != nil {
		ctx.ServerError("GetVisibleRunnerByID", err)
		return
	}
	if err := runner.LoadAttributes(ctx); err != nil {
		ctx.ServerError("LoadAttributes", err)
		return
	}

	opts := actions_model.FindTaskOptions{
		ListOptions: db.ListOptions{
			Page:     page,
			PageSize: 30,
		},
		RunnerID: runner.ID,
		OwnerID:  ownerID,
		RepoID:   repoID,
	}

	tasks, count, err := db.FindAndCount[actions_model.ActionTask](ctx, opts)
	if err != nil {
		ctx.ServerError("CountTasks", err)
		return
	}

	if err = actions_model.TaskList(tasks).LoadAttributes(ctx); err != nil {
		ctx.ServerError("TasksLoadAttributes", err)
		return
	}

	ctx.Data["PageIsSharedSettingsRunners"] = true
	ctx.Data["RunnerOwnerID"] = ownerID
	ctx.Data["RunnerRepoID"] = repoID
	ctx.Data["Title"] = ctx.Tr("actions.runners.runner_details.page_title", runner.Name)
	ctx.Data["Runner"] = runner
	ctx.Data["Tasks"] = tasks
	pager := context.NewPagination(int(count), opts.PageSize, opts.Page, 5)
	ctx.Data["Page"] = pager
	ctx.HTML(http.StatusOK, template)
}

// RunnerCreate displays a form for creating a new runner.
func RunnerCreate(ctx *context.Context, template base.TplName) {
	ctx.Data["PageIsSharedSettingsRunners"] = true
	ctx.Data["Title"] = ctx.Tr("actions.runners.create_runner.page_title")
	ctx.HTML(http.StatusOK, template)
}

// RunnerCreatePost handles the form submitted by RunnerCreate.
func RunnerCreatePost(ctx *context.Context, ownerID, repoID int64, template, successTemplate base.TplName) {
	form := web.GetForm(ctx).(*forms.CreateRunnerForm)

	runner := actions_model.ActionRunner{
		UUID:        gouuid.New().String(),
		Name:        form.RunnerName,
		OwnerID:     ownerID,
		RepoID:      repoID,
		Description: form.RunnerDescription,
		Ephemeral:   false,
	}
	runner.GenerateToken()

	ctx.Data["PageIsSharedSettingsRunners"] = true
	ctx.Data["Title"] = ctx.Tr("actions.runners.runner_setup.page_title", runner.Name)
	ctx.Data["AppURL"] = setting.AppURL
	ctx.Data["Runner"] = runner
	ctx.Data["RunnerOwnerID"] = ownerID
	ctx.Data["RunnerRepoID"] = repoID

	if ctx.HasError() {
		ctx.HTML(http.StatusOK, template)
		return
	}

	err := actions_model.CreateRunner(ctx, &runner)
	if err != nil {
		ctx.ServerError("CreateRunner", err)
		return
	}

	ctx.HTML(http.StatusOK, successTemplate)
}

// RunnerEdit displays a form to modify the given runner.
func RunnerEdit(ctx *context.Context, runnerID, ownerID, repoID int64, template base.TplName) {
	runner, err := actions_model.GetVisibleRunnerByID(ctx, runnerID, ownerID, repoID)
	if errors.Is(err, util.ErrNotExist) {
		ctx.NotFound("GetVisibleRunnerByID", err)
		return
	} else if err != nil {
		ctx.ServerError("GetVisibleRunnerByID", err)
		return
	}
	if err := runner.LoadAttributes(ctx); err != nil {
		ctx.ServerError("LoadAttributes", err)
		return
	}
	if !runner.Editable(ownerID, repoID) {
		err = errors.New("no permission to edit this runner")
		ctx.NotFound("RunnerDetails", err)
		return
	}

	ctx.Data["PageIsSharedSettingsRunners"] = true
	ctx.Data["Title"] = ctx.Tr("actions.runners.edit_runner.page_title", runner.Name)
	ctx.Data["Runner"] = runner
	ctx.Data["RunnerOwnerID"] = ownerID
	ctx.Data["RunnerRepoID"] = repoID
	ctx.HTML(http.StatusOK, template)
}

// RunnerEditPost handles the form submitted by RunnerEdit.
func RunnerEditPost(ctx *context.Context, runnerID, ownerID, repoID int64, template, successTemplate base.TplName, redirectTo string) {
	runner, err := actions_model.GetVisibleRunnerByID(ctx, runnerID, ownerID, repoID)
	if errors.Is(err, util.ErrNotExist) {
		ctx.NotFound("GetVisibleRunnerByID", err)
		return
	} else if err != nil {
		ctx.ServerError("GetVisibleRunnerByID", err)
		return
	}
	if !runner.Editable(ownerID, repoID) {
		ctx.NotFound("RunnerEditPost.Editable", util.NewPermissionDeniedErrorf("no permission to edit this runner"))
		return
	}

	ctx.Data["PageIsSharedSettingsRunners"] = true
	ctx.Data["Title"] = ctx.Tr("actions.runners.runner_setup.page_title", runner.Name)
	ctx.Data["AppURL"] = setting.AppURL
	ctx.Data["Runner"] = runner
	ctx.Data["RunnerOwnerID"] = ownerID
	ctx.Data["RunnerRepoID"] = repoID

	form := web.GetForm(ctx).(*forms.EditRunnerForm)
	runner.Name = form.RunnerName
	runner.Description = form.RunnerDescription

	if ctx.HasError() {
		ctx.HTML(http.StatusOK, template)
		return
	}

	if !form.RegenerateToken {
		err = actions_model.UpdateRunner(ctx, runner, "name", "description")
		if err != nil {
			log.Warn("RunnerEditPost.UpdateRunner failed: %v, url: %s", err, ctx.Req.URL)
			ctx.Flash.Warning(ctx.Tr("actions.runners.update_runner.failed"))
			ctx.Redirect(redirectTo)
			return
		}

		log.Debug("RunnerEditPost success: %s", ctx.Req.URL)

		ctx.Flash.Success(ctx.Tr("actions.runners.update_runner.success"))
		ctx.Redirect(redirectTo)
		return
	}

	runner.GenerateToken()
	err = actions_model.UpdateRunner(ctx, runner, "name", "description", "token_hash", "token_salt")
	if err != nil {
		log.Warn("RunnerEditPost.UpdateRunner failed: %v, url: %s", err, ctx.Req.URL)
		ctx.Flash.Warning(ctx.Tr("actions.runners.update_runner.failed"))
		ctx.Redirect(redirectTo)
		return
	}

	ctx.HTML(http.StatusOK, successTemplate)
}

// RunnerResetRegistrationToken resets the runner registration token.
func RunnerResetRegistrationToken(ctx *context.Context, ownerID, repoID int64, redirectTo string) {
	optOwnerID := optional.None[int64]()
	if ownerID != 0 {
		optOwnerID = optional.Some(ownerID)
	}
	optRepoID := optional.None[int64]()
	if repoID != 0 {
		optRepoID = optional.Some(repoID)
	}

	_, err := actions_model.NewRunnerToken(ctx, optOwnerID, optRepoID)
	if err != nil {
		ctx.ServerError("ResetRunnerRegistrationToken", err)
		return
	}

	ctx.Flash.Success(ctx.Tr("actions.runners.reset_registration_token.success"))
	ctx.Redirect(redirectTo)
}

// RunnerDeletePost handles the request for deleting a particular runner.
func RunnerDeletePost(ctx *context.Context, runnerID, ownerID, repoID int64, successRedirectTo, failedRedirectTo string) {
	runner, err := actions_model.GetRunnerByID(ctx, runnerID)
	if err != nil {
		ctx.ServerError("GetRunnerByID", err)
		return
	}

	if !runner.Editable(ownerID, repoID) {
		ctx.NotFound("Editable", util.NewPermissionDeniedErrorf("no permission to edit this runner"))
		return
	}

	if err := actions_model.DeleteRunner(ctx, runner); err != nil {
		log.Warn("DeleteRunnerPost.UpdateRunner failed: %v, url: %s", err, ctx.Req.URL)

		ctx.Flash.Warning(ctx.Tr("actions.runners.delete_runner.failed"))

		ctx.JSONRedirect(failedRedirectTo)
		return
	}

	log.Info("DeleteRunnerPost success: %s", ctx.Req.URL)

	ctx.Flash.Success(ctx.Tr("actions.runners.delete_runner.success"))

	ctx.JSONRedirect(successRedirectTo)
}
