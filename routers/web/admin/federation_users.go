// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package admin

import (
	"net/http"

	"github.com/CarriedWorldUniverse/cairn/models/db"
	user_model "github.com/CarriedWorldUniverse/cairn/models/user"
	"github.com/CarriedWorldUniverse/cairn/modules/base"
	"github.com/CarriedWorldUniverse/cairn/modules/setting"
	"github.com/CarriedWorldUniverse/cairn/services/context"
)

const (
	tplFederationUsers base.TplName = "admin/federation/users"
)

func FederationUsers(ctx *context.Context) {
	page := max(ctx.FormInt("page"), 1)

	users, err := user_model.FindFederatedUsers(ctx, db.ListOptions{
		Page:     page,
		PageSize: setting.UI.Admin.FederationUserPagingNum,
	})
	if err != nil {
		ctx.ServerError("FindFederatedUsers", err)
		return
	}

	total, err := user_model.CountFederatedUsers(ctx)
	if err != nil {
		ctx.ServerError("CountFederatedUsers", err)
		return
	}

	ctx.Data["Users"] = users
	ctx.Data["TotalCount"] = int(total)
	ctx.Data["Title"] = ctx.Tr("admin.federation.users.title")
	ctx.Data["PageIsAdminFederationUsers"] = true

	numPages := 0
	if total > 0 {
		numPages = (int(total) - 1/setting.UI.Admin.FederationUserPagingNum)
	}

	pager := context.NewPagination(int(total), setting.UI.Admin.FederationUserPagingNum, page, numPages)
	pager.SetDefaultParams(ctx)
	ctx.Data["Page"] = pager

	ctx.HTML(http.StatusOK, tplFederationUsers)
}
