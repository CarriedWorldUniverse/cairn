// Copyright Earl Warren <contact@earl-warren.org>
// Copyright Loïc Dachary <loic@dachary.org>
// SPDX-License-Identifier: MIT

package tests

import (
	driver_options "github.com/CarriedWorldUniverse/cairn/services/f3/driver/options"

	tests_forge "code.forgejo.org/f3/gof3/v3/tree/tests/f3/forge"
)

func init() {
	tests_forge.RegisterFactory(driver_options.Name, newForgeTest)
}
