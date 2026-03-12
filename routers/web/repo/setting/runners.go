// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"errors"
	"fmt"
	"net/url"

	actions_model "forgejo.org/models/actions"
	"forgejo.org/models/db"
	"forgejo.org/modules/base"
	"forgejo.org/modules/setting"
	actions_shared "forgejo.org/routers/web/shared/actions"
	shared_user "forgejo.org/routers/web/shared/user"
	"forgejo.org/services/context"
)

const (
	tplAdminRunnerCreate  base.TplName = "admin/runners/create"
	tplAdminRunnerDetails base.TplName = "admin/runners/details"
	tplAdminRunnerEdit    base.TplName = "admin/runners/edit"
	tplAdminRunnerSetup   base.TplName = "admin/runners/setup"
	tplAdminRunners       base.TplName = "admin/actions"
	tplOrgRunnerCreate    base.TplName = "org/settings/runners_create"
	tplOrgRunnerDetails   base.TplName = "org/settings/runners_details"
	tplOrgRunnerEdit      base.TplName = "org/settings/runners_edit"
	tplOrgRunnerSetup     base.TplName = "org/settings/runners_setup"
	tplOrgRunners         base.TplName = "org/settings/actions"
	tplRepoRunnerCreate   base.TplName = "repo/settings/runner_create"
	tplRepoRunnerDetails  base.TplName = "repo/settings/runner_details"
	tplRepoRunnerEdit     base.TplName = "repo/settings/runner_edit"
	tplRepoRunnerSetup    base.TplName = "repo/settings/runner_setup"
	tplRepoRunners        base.TplName = "repo/settings/actions"
	tplUserRunnerCreate   base.TplName = "user/settings/runner_create"
	tplUserRunnerDetails  base.TplName = "user/settings/runner_details"
	tplUserRunnerEdit     base.TplName = "user/settings/runner_edit"
	tplUserRunnerSetup    base.TplName = "user/settings/runner_setup"
	tplUserRunners        base.TplName = "user/settings/actions"
)

type runnersCtx struct {
	OwnerID               int64
	RepoID                int64
	IsRepo                bool
	IsOrg                 bool
	IsAdmin               bool
	IsUser                bool
	RunnerCreateTemplate  base.TplName
	RunnerDetailsTemplate base.TplName
	RunnerEditTemplate    base.TplName
	RunnerSetupTemplate   base.TplName
	RunnersTemplate       base.TplName
	RedirectLink          string
}

func getRunnersCtx(ctx *context.Context) (*runnersCtx, error) {
	if ctx.Data["PageIsRepoSettings"] == true {
		return &runnersCtx{
			RepoID:                ctx.Repo.Repository.ID,
			OwnerID:               0,
			IsRepo:                true,
			RunnerCreateTemplate:  tplRepoRunnerCreate,
			RunnerDetailsTemplate: tplRepoRunnerDetails,
			RunnerEditTemplate:    tplRepoRunnerEdit,
			RunnerSetupTemplate:   tplRepoRunnerSetup,
			RunnersTemplate:       tplRepoRunners,
			RedirectLink:          ctx.Repo.RepoLink + "/settings/actions/runners/",
		}, nil
	}

	if ctx.Data["PageIsOrgSettings"] == true {
		err := shared_user.LoadHeaderCount(ctx)
		if err != nil {
			return nil, fmt.Errorf("could not load project and package counts: %w", err)
		}
		return &runnersCtx{
			RepoID:                0,
			OwnerID:               ctx.Org.Organization.ID,
			IsOrg:                 true,
			RunnerCreateTemplate:  tplOrgRunnerCreate,
			RunnerDetailsTemplate: tplOrgRunnerDetails,
			RunnerEditTemplate:    tplOrgRunnerEdit,
			RunnerSetupTemplate:   tplOrgRunnerSetup,
			RunnersTemplate:       tplOrgRunners,
			RedirectLink:          ctx.Org.OrgLink + "/settings/actions/runners/",
		}, nil
	}

	if ctx.Data["PageIsAdmin"] == true {
		return &runnersCtx{
			RepoID:                0,
			OwnerID:               0,
			IsAdmin:               true,
			RunnerCreateTemplate:  tplAdminRunnerCreate,
			RunnerDetailsTemplate: tplAdminRunnerDetails,
			RunnerEditTemplate:    tplAdminRunnerEdit,
			RunnerSetupTemplate:   tplAdminRunnerSetup,
			RunnersTemplate:       tplAdminRunners,
			RedirectLink:          setting.AppSubURL + "/admin/actions/runners/",
		}, nil
	}

	if ctx.Data["PageIsUserSettings"] == true {
		return &runnersCtx{
			OwnerID:               ctx.Doer.ID,
			RepoID:                0,
			IsUser:                true,
			RunnerCreateTemplate:  tplUserRunnerCreate,
			RunnerDetailsTemplate: tplUserRunnerDetails,
			RunnerEditTemplate:    tplUserRunnerEdit,
			RunnerSetupTemplate:   tplUserRunnerSetup,
			RunnersTemplate:       tplUserRunners,
			RedirectLink:          setting.AppSubURL + "/user/settings/actions/runners/",
		}, nil
	}

	return nil, errors.New("unable to set Runners context")
}

// Runners renders the list of all available runners.
func Runners(ctx *context.Context) {
	rCtx, err := getRunnersCtx(ctx)
	if err != nil {
		ctx.ServerError("getRunnersCtx", err)
		return
	}

	page := ctx.FormInt("page")
	if page <= 1 {
		page = 1
	}

	opts := actions_model.FindRunnerOptions{
		ListOptions: db.ListOptions{
			Page:     page,
			PageSize: 100,
		},
		Sort:   ctx.Req.URL.Query().Get("sort"),
		Filter: ctx.Req.URL.Query().Get("q"),
	}
	if rCtx.IsRepo {
		opts.RepoID = rCtx.RepoID
		opts.WithAvailable = true
	} else if rCtx.IsOrg || rCtx.IsUser {
		opts.OwnerID = rCtx.OwnerID
		opts.WithAvailable = true
	}

	ctx.Data["RunnersListLink"] = rCtx.RedirectLink

	actions_shared.RunnersList(ctx, rCtx.RunnersTemplate, opts)
}

// RunnersDetails renders a read-only view of the most important properties of a runner. It is accessible to every user
// that can use that particular runner.
func RunnersDetails(ctx *context.Context) {
	rCtx, err := getRunnersCtx(ctx)
	if err != nil {
		ctx.ServerError("getRunnersCtx", err)
		return
	}

	runnerID := ctx.ParamsInt64(":runnerid")
	page := ctx.FormInt("page")
	if page <= 1 {
		page = 1
	}

	ctx.Data["RunnersListLink"] = rCtx.RedirectLink

	actions_shared.RunnerDetails(ctx, runnerID, rCtx.OwnerID, rCtx.RepoID, rCtx.RunnerDetailsTemplate, page)
}

// RunnersCreate renders the form for creating a new runner.
func RunnersCreate(ctx *context.Context) {
	rCtx, err := getRunnersCtx(ctx)
	if err != nil {
		ctx.ServerError("getRunnersCtx", err)
		return
	}

	ctx.Data["RunnersListLink"] = rCtx.RedirectLink

	actions_shared.RunnerCreate(ctx, rCtx.RunnerCreateTemplate)
}

// RunnersCreatePost handles the form submitted by RunnersCreate.
func RunnersCreatePost(ctx *context.Context) {
	rCtx, err := getRunnersCtx(ctx)
	if err != nil {
		ctx.ServerError("getRunnersCtx", err)
		return
	}

	ctx.Data["RunnersListLink"] = rCtx.RedirectLink

	actions_shared.RunnerCreatePost(ctx, rCtx.OwnerID, rCtx.RepoID, rCtx.RunnerCreateTemplate, rCtx.RunnerSetupTemplate)
}

// RunnersEdit renders the form for changing an existing runner.
func RunnersEdit(ctx *context.Context) {
	rCtx, err := getRunnersCtx(ctx)
	if err != nil {
		ctx.ServerError("getRunnersCtx", err)
		return
	}

	ctx.Data["RunnersListLink"] = rCtx.RedirectLink

	actions_shared.RunnerEdit(ctx, ctx.ParamsInt64(":runnerid"), rCtx.OwnerID, rCtx.RepoID, rCtx.RunnerEditTemplate)
}

// RunnersEditPost handles the form submitted by RunnersEdit.
func RunnersEditPost(ctx *context.Context) {
	rCtx, err := getRunnersCtx(ctx)
	if err != nil {
		ctx.ServerError("getRunnersCtx", err)
		return
	}

	ctx.Data["RunnersListLink"] = rCtx.RedirectLink

	runnerID := ctx.ParamsInt64(":runnerid")
	redirectURL := rCtx.RedirectLink + url.PathEscape(ctx.Params(":runnerid"))
	actions_shared.RunnerEditPost(ctx, runnerID, rCtx.OwnerID, rCtx.RepoID, rCtx.RunnerEditTemplate,
		rCtx.RunnerSetupTemplate, redirectURL)
}

// ResetRunnerRegistrationToken handles the request to reset the runner registration token.
func ResetRunnerRegistrationToken(ctx *context.Context) {
	rCtx, err := getRunnersCtx(ctx)
	if err != nil {
		ctx.ServerError("getRunnersCtx", err)
		return
	}

	ctx.Data["RunnersListLink"] = rCtx.RedirectLink

	actions_shared.RunnerResetRegistrationToken(ctx, rCtx.OwnerID, rCtx.RepoID, rCtx.RedirectLink)
}

// RunnerDeletePost handles the request to delete a runner.
func RunnerDeletePost(ctx *context.Context) {
	rCtx, err := getRunnersCtx(ctx)
	if err != nil {
		ctx.ServerError("getRunnersCtx", err)
		return
	}

	ctx.Data["RunnersListLink"] = rCtx.RedirectLink

	runnerID := ctx.ParamsInt64(":runnerid")
	successRedirectURL := rCtx.RedirectLink
	failureRedirectURL := rCtx.RedirectLink
	actions_shared.RunnerDeletePost(ctx, runnerID, rCtx.OwnerID, rCtx.RepoID, successRedirectURL, failureRedirectURL)
}

func RedirectToDefaultSetting(ctx *context.Context) {
	ctx.Redirect(ctx.Repo.RepoLink + "/settings/actions/runners")
}
