// Copyright 2014 The Gogs Authors. All rights reserved.
// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package misc

import (
	api "github.com/CarriedWorldUniverse/cairn/modules/structs"
	"github.com/CarriedWorldUniverse/cairn/modules/web"
	"github.com/CarriedWorldUniverse/cairn/routers/common"
	"github.com/CarriedWorldUniverse/cairn/services/context"
)

// Markup render markup document to HTML
func Markup(ctx *context.Context) {
	form := web.GetForm(ctx).(*api.MarkupOption)

	re := common.Renderer{
		Mode:       form.Mode,
		Text:       form.Text,
		URLPrefix:  form.Context,
		FilePath:   form.FilePath,
		BranchPath: form.BranchPath,
		IsWiki:     form.Wiki,
	}

	re.RenderMarkup(ctx.Base, ctx.Repo)
}
