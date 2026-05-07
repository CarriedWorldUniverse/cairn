// Copyright 2014 The Gogs Authors. All rights reserved.
// Copyright 2020 The Gitea Authors.
// SPDX-License-Identifier: MIT

package admin

import (
	"github.com/CarriedWorldUniverse/cairn/models/db"
	user_model "github.com/CarriedWorldUniverse/cairn/models/user"
	"github.com/CarriedWorldUniverse/cairn/modules/base"
	"github.com/CarriedWorldUniverse/cairn/modules/setting"
	"github.com/CarriedWorldUniverse/cairn/modules/structs"
	"github.com/CarriedWorldUniverse/cairn/routers/web/explore"
	"github.com/CarriedWorldUniverse/cairn/services/context"
)

const (
	tplOrgs base.TplName = "admin/org/list"
)

// Organizations show all the organizations
func Organizations(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("admin.organizations")
	ctx.Data["PageIsAdminOrganizations"] = true

	if ctx.FormString("sort") == "" {
		ctx.SetFormString("sort", UserSearchDefaultAdminSort)
	}

	explore.RenderUserSearch(ctx, &user_model.SearchUserOptions{
		Actor:           ctx.Doer,
		Type:            user_model.UserTypeOrganization,
		IncludeReserved: true, // administrator needs to list all accounts include reserved
		ListOptions: db.ListOptions{
			PageSize: setting.UI.Admin.OrgPagingNum,
		},
		Visible: []structs.VisibleType{structs.VisibleTypePublic, structs.VisibleTypeLimited, structs.VisibleTypePrivate},
	}, tplOrgs)
}
