// Copyright 2020 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo_test

import (
	"testing"

	"github.com/CarriedWorldUniverse/cairn/models/unittest"

	_ "github.com/CarriedWorldUniverse/cairn/modules/testimport"
)

func TestMain(m *testing.M) {
	unittest.MainTest(m)
}
