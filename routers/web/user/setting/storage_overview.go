// Copyright 2024 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package setting

import (
	"github.com/CarriedWorldUniverse/cairn/modules/base"
	"github.com/CarriedWorldUniverse/cairn/routers/web/shared"
	"github.com/CarriedWorldUniverse/cairn/services/context"
)

const (
	tplSettingsStorageOverview base.TplName = "user/settings/storage_overview"
)

// StorageOverview render a size overview of the user, as well as relevant
// quota limits of the instance.
func StorageOverview(ctx *context.Context) {
	shared.StorageOverview(ctx, ctx.Doer.ID, tplSettingsStorageOverview)
}
