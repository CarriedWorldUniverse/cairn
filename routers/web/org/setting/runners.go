// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"github.com/CarriedWorldUniverse/cairn/services/context"
)

func RedirectToDefaultSetting(ctx *context.Context) {
	ctx.Redirect(ctx.Org.OrgLink + "/settings/actions/runners")
}
