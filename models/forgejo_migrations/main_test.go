// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package forgejo_migrations

import (
	"testing"

	migration_tests "github.com/CarriedWorldUniverse/cairn/models/gitea_migrations/test"
)

func TestMain(m *testing.M) {
	migration_tests.MainTest(m)
}
