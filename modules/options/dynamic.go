// Copyright 2016 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build !bindata

package options

import (
	"github.com/CarriedWorldUniverse/cairn/modules/assetfs"
	"github.com/CarriedWorldUniverse/cairn/modules/setting"
)

func BuiltinAssets() *assetfs.Layer {
	return assetfs.Local("builtin(static)", setting.StaticRootPath, "options")
}
