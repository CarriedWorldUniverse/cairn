// Copyright 2023 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package forgejo_migrations_legacy

import (
	"testing"

	migration_tests "github.com/CarriedWorldUniverse/cairn/models/gitea_migrations/test"
)

func TestMain(m *testing.M) {
	migration_tests.MainTest(m)
}
