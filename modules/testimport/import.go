// Copyright 2026 The Forgejo Authors
// SPDX-License-Identifier: MIT

package testimport

// ensure the init() function of those modules are called in a test
// environment that may not include them. It matters when the engine
// is trying to figure out the ordering of foreign keys, for instance

import ( //revive:disable:blank-imports
	_ "github.com/CarriedWorldUniverse/cairn/models/actions"
	_ "github.com/CarriedWorldUniverse/cairn/models/activities"
	_ "github.com/CarriedWorldUniverse/cairn/models/auth"
	_ "github.com/CarriedWorldUniverse/cairn/models/forgefed"
	_ "github.com/CarriedWorldUniverse/cairn/models/perm/access"
	_ "github.com/CarriedWorldUniverse/cairn/models/repo"
)
