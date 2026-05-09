// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package asymkey

import (
	"testing"

	"github.com/CarriedWorldUniverse/cairn/models/unittest"

	_ "github.com/CarriedWorldUniverse/cairn/models/actions"
	_ "github.com/CarriedWorldUniverse/cairn/models/activities"
	_ "github.com/CarriedWorldUniverse/cairn/models/forgefed"
)

func TestMain(m *testing.M) {
	unittest.MainTest(m)
}
