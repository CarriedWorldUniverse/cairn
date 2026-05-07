// Copyright Earl Warren <contact@earl-warren.org>
// Copyright Loïc Dachary <loic@dachary.org>
// SPDX-License-Identifier: MIT

package driver

import (
	driver_options "github.com/CarriedWorldUniverse/cairn/services/f3/driver/options"

	"code.forgejo.org/f3/gof3/v3/options"
	f3_tree "code.forgejo.org/f3/gof3/v3/tree/f3"
)

func init() {
	f3_tree.RegisterForgeFactory(driver_options.Name, newTreeDriver)
	options.RegisterFactory(driver_options.Name, newOptions)
}
