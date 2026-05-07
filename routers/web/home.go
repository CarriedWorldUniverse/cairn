// Copyright 2014 The Gogs Authors. All rights reserved.
// Copyright 2019 The Gitea Authors. All rights reserved.
// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package web

import (
	"net/http"
	"strconv"

	auth_model "github.com/CarriedWorldUniverse/cairn/models/auth"
	"github.com/CarriedWorldUniverse/cairn/models/db"
	repo_model "github.com/CarriedWorldUniverse/cairn/models/repo"
	user_model "github.com/CarriedWorldUniverse/cairn/models/user"
	"github.com/CarriedWorldUniverse/cairn/modules/base"
	"github.com/CarriedWorldUniverse/cairn/modules/log"
	"github.com/CarriedWorldUniverse/cairn/modules/optional"
	"github.com/CarriedWorldUniverse/cairn/modules/setting"
	"github.com/CarriedWorldUniverse/cairn/modules/sitemap"
	"github.com/CarriedWorldUniverse/cairn/modules/structs"
	"github.com/CarriedWorldUniverse/cairn/modules/web/middleware"
	"github.com/CarriedWorldUniverse/cairn/routers/web/auth"
	"github.com/CarriedWorldUniverse/cairn/routers/web/user"
	"github.com/CarriedWorldUniverse/cairn/services/context"
)

const (
	// tplHome home page template
	tplHome base.TplName = "home"
)

// Home render home page
func Home(ctx *context.Context) {
	if ctx.IsSigned {
		if !ctx.Doer.IsActive && setting.Service.RegisterEmailConfirm {
			ctx.Data["Title"] = ctx.Tr("auth.active_your_account")
			ctx.HTML(http.StatusOK, auth.TplActivate)
			return
		}
		if !ctx.Doer.IsActive || ctx.Doer.ProhibitLogin {
			log.Info("Failed authentication attempt for %s from %s", ctx.Doer.Name, ctx.RemoteAddr())
			ctx.Data["Title"] = ctx.Tr("auth.prohibit_login")
			ctx.HTML(http.StatusOK, "user/auth/prohibit_login")
			return
		}
		if ctx.Doer.MustChangePassword {
			ctx.Data["Title"] = ctx.Tr("auth.must_change_password")
			ctx.Data["ChangePasscodeLink"] = setting.AppSubURL + "/user/change_password"
			middleware.SetRedirectToCookie(ctx.Resp, setting.AppSubURL+ctx.Req.URL.RequestURI())
			ctx.Redirect(setting.AppSubURL + "/user/settings/change_password")
			return
		}
		if ctx.Doer.MustHaveTwoFactor() {
			hasTwoFactor, err := auth_model.HasTwoFactorByUID(ctx, ctx.Doer.ID)
			if err != nil {
				ctx.Data["Title"] = ctx.Tr("auth.prohibit_login")
				log.Error("Error getting 2fa: %s", err)
				ctx.Error(http.StatusInternalServerError, "HasTwoFactorByUID", err.Error())
				return
			}
			if !hasTwoFactor {
				ctx.Data["Title"] = ctx.Tr("auth.prohibit_login")
				ctx.Redirect(setting.AppSubURL + "/user/settings/security")
				return
			}
		}

		user.Dashboard(ctx)
		return
		// Check non-logged users landing page.
	} else if setting.LandingPageURL != setting.LandingPageHome {
		ctx.Redirect(setting.AppSubURL + string(setting.LandingPageURL))
		return
	}

	// Check auto-login.
	if ctx.GetSiteCookie(setting.CookieRememberName) != "" {
		ctx.Redirect(setting.AppSubURL + "/user/login")
		return
	}

	ctx.Data["PageIsHome"] = true
	ctx.Data["IsRepoIndexerEnabled"] = setting.Indexer.RepoIndexerEnabled

	ctx.Data["OpenGraphDescription"] = setting.UI.Meta.Description

	ctx.HTML(http.StatusOK, tplHome)
}

// HomeSitemap renders the main sitemap
func HomeSitemap(ctx *context.Context) {
	m := sitemap.NewSitemapIndex()
	if !setting.Service.Explore.DisableUsersPage {
		_, cnt, err := user_model.SearchUsers(ctx, &user_model.SearchUserOptions{
			Type:        user_model.UserTypeIndividual,
			ListOptions: db.ListOptions{PageSize: 1},
			IsActive:    optional.Some(true),
			Visible:     []structs.VisibleType{structs.VisibleTypePublic},
		})
		if err != nil {
			ctx.ServerError("SearchUsers", err)
			return
		}
		count := int(cnt)
		idx := 1
		for i := 0; i < count; i += setting.UI.SitemapPagingNum {
			m.Add(sitemap.URL{URL: setting.AppURL + "explore/users/sitemap-" + strconv.Itoa(idx) + ".xml"})
			idx++
		}
	}

	_, cnt, err := repo_model.SearchRepository(ctx, &repo_model.SearchRepoOptions{
		ListOptions: db.ListOptions{
			PageSize: 1,
		},
		Actor:     ctx.Doer,
		AllPublic: true,
	})
	if err != nil {
		ctx.ServerError("SearchRepository", err)
		return
	}
	count := int(cnt)
	idx := 1
	for i := 0; i < count; i += setting.UI.SitemapPagingNum {
		m.Add(sitemap.URL{URL: setting.AppURL + "explore/repos/sitemap-" + strconv.Itoa(idx) + ".xml"})
		idx++
	}

	ctx.Resp.Header().Set("Content-Type", "text/xml")
	if _, err := m.WriteTo(ctx.Resp); err != nil {
		log.Error("Failed writing sitemap: %v", err)
	}
}
