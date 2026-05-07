// Copyright 2024 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package alt

import (
	"context"

	packages_model "github.com/CarriedWorldUniverse/cairn/models/packages"
	rpm_module "github.com/CarriedWorldUniverse/cairn/modules/packages/rpm"
)

type PackageSearchOptions struct {
	OwnerID      int64
	GroupID      int64
	Architecture string
}

// GetGroups gets all available groups
func GetGroups(ctx context.Context, ownerID int64) ([]string, error) {
	return packages_model.GetDistinctPropertyValues(
		ctx,
		packages_model.TypeAlt,
		ownerID,
		packages_model.PropertyTypeFile,
		rpm_module.PropertyGroup,
		nil,
	)
}
