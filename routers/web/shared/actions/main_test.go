// Copyright 2025 The Forgejo Authors.
// SPDX-License-Identifier: GPL-3.0-or-later

package actions

import (
	"testing"

	"github.com/CarriedWorldUniverse/cairn/models/unittest"

	_ "github.com/CarriedWorldUniverse/cairn/models"
	_ "github.com/CarriedWorldUniverse/cairn/models/forgefed"
)

func TestMain(m *testing.M) {
	unittest.MainTest(m)
}
