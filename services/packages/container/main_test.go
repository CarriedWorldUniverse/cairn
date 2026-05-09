// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package container

import (
	"testing"

	"github.com/CarriedWorldUniverse/cairn/models/unittest"

	_ "github.com/CarriedWorldUniverse/cairn/models"
	_ "github.com/CarriedWorldUniverse/cairn/models/actions"
	_ "github.com/CarriedWorldUniverse/cairn/models/activities"
	_ "github.com/CarriedWorldUniverse/cairn/models/forgefed"
	_ "github.com/CarriedWorldUniverse/cairn/models/packages"
)

func TestMain(m *testing.M) {
	unittest.MainTest(m)
}
