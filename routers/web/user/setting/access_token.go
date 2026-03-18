// Copyright 2014 The Gogs Authors. All rights reserved.
// Copyright 2018 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"net/http"
	"slices"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/modules/base"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/web"
	"forgejo.org/services/context"
	"forgejo.org/services/forms"
)

const (
	tplAccessTokenEdit base.TplName = "user/settings/access_token_edit"
)

func loadAccessTokenCreateData(ctx *context.Context) {
	ctx.Data["AccessTokenScopePublicOnly"] = string(auth_model.AccessTokenScopePublicOnly) // note: SliceUtils.Contains won't work in the template if this is a `auth_model.AccessTokenScope`, so it's cast to a string here

	categories := []string{
		"activitypub",
		"issue",
		"misc",
		"notification",
		"organization",
		"package",
		"repository",
		"user",
	}
	if ctx.Doer.IsAdmin {
		categories = append(categories, "admin")
	}
	slices.Sort(categories)
	ctx.Data["Categories"] = categories
}

// Applications render manage access token page
func AccessTokenCreate(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("settings.applications")
	ctx.Data["PageIsSettingsApplications"] = true

	loadAccessTokenCreateData(ctx)

	ctx.HTML(http.StatusOK, tplAccessTokenEdit)
}

// ApplicationsPost response for add user's access token
func AccessTokenCreatePost(ctx *context.Context) {
	form := web.GetForm(ctx).(*forms.NewAccessTokenForm)
	ctx.Data["Title"] = ctx.Tr("settings")
	ctx.Data["PageIsSettingsApplications"] = true

	if ctx.HasError() {
		loadAccessTokenCreateData(ctx)
		ctx.HTML(http.StatusOK, tplAccessTokenEdit)
		return
	}

	scope, err := form.GetScope()
	if err != nil {
		ctx.ServerError("GetScope", err)
		return
	}
	if !scope.HasPermissionScope() {
		loadAccessTokenCreateData(ctx)
		ctx.RenderWithErr(ctx.Tr("settings.at_least_one_permission"), tplAccessTokenEdit, form)
		return
	}
	t := &auth_model.AccessToken{
		UID:   ctx.Doer.ID,
		Name:  form.Name,
		Scope: scope,

		// maintain legacy behaviour until new UI options are added -- token has access to all resources, is not
		// fine-grained
		ResourceAllRepos: true,
	}

	exist, err := auth_model.AccessTokenByNameExists(ctx, t)
	if err != nil {
		ctx.ServerError("AccessTokenByNameExists", err)
		return
	}
	if exist {
		loadAccessTokenCreateData(ctx)
		ctx.RenderWithErr(ctx.Tr("settings.generate_token_name_duplicate", t.Name), tplAccessTokenEdit, form)
		return
	}

	if err := auth_model.NewAccessToken(ctx, t); err != nil {
		ctx.ServerError("NewAccessToken", err)
		return
	}

	ctx.Flash.Success(ctx.Tr("settings.generate_token_success"))
	ctx.Flash.Info(t.Token)

	ctx.Redirect(setting.AppSubURL + "/user/settings/applications")
}

// DeleteAccessToken response for delete user access token
func DeleteAccessToken(ctx *context.Context) {
	if err := auth_model.DeleteAccessTokenByID(ctx, ctx.FormInt64("id"), ctx.Doer.ID); err != nil {
		ctx.Flash.Error("DeleteAccessTokenByID: " + err.Error())
	} else {
		ctx.Flash.Success(ctx.Tr("settings.delete_token_success"))
	}

	ctx.JSONRedirect(setting.AppSubURL + "/user/settings/applications")
}

// RegenerateAccessToken response for regenerating user access token
func RegenerateAccessToken(ctx *context.Context) {
	if t, err := auth_model.RegenerateAccessTokenByID(ctx, ctx.FormInt64("id"), ctx.Doer.ID); err != nil {
		if auth_model.IsErrAccessTokenNotExist(err) {
			ctx.Flash.Error(ctx.Tr("error.not_found"))
		} else {
			ctx.Flash.Error(ctx.Tr("error.server_internal"))
			log.Error("DeleteAccessTokenByID", err)
		}
	} else {
		ctx.Flash.Success(ctx.Tr("settings.regenerate_token_success"))
		ctx.Flash.Info(t.Token)
	}

	ctx.JSONRedirect(setting.AppSubURL + "/user/settings/applications")
}
