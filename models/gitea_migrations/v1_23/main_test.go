// Copyright 2024 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1_23

import (
	"testing"

	migration_tests "github.com/CarriedWorldUniverse/cairn/models/gitea_migrations/test"
)

func TestMain(m *testing.M) {
	migration_tests.MainTest(m)
}
