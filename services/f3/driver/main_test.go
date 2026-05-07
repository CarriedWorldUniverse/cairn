// Copyright Earl Warren <contact@earl-warren.org>
// Copyright Loïc Dachary <loic@dachary.org>
// SPDX-License-Identifier: MIT

package driver

import (
	"testing"

	"github.com/CarriedWorldUniverse/cairn/models/unittest"
	"github.com/CarriedWorldUniverse/cairn/modules/log"
	"github.com/CarriedWorldUniverse/cairn/modules/setting"
	"github.com/CarriedWorldUniverse/cairn/modules/test"
	driver_options "github.com/CarriedWorldUniverse/cairn/services/f3/driver/options"

	_ "github.com/CarriedWorldUniverse/cairn/models"
	_ "github.com/CarriedWorldUniverse/cairn/models/actions"
	_ "github.com/CarriedWorldUniverse/cairn/models/activities"
	_ "github.com/CarriedWorldUniverse/cairn/models/perm/access"
	_ "github.com/CarriedWorldUniverse/cairn/services/f3/driver/tests"

	tests_f3 "code.forgejo.org/f3/gof3/v3/tree/tests/f3"
	"github.com/stretchr/testify/require"
)

func TestF3(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	defer test.MockVariableValue(&setting.SSH.RootPath, t.TempDir())()
	log.SetConsoleLogger(log.DEFAULT, "console", log.TRACE)
	defer func() {
		log.SetConsoleLogger(log.DEFAULT, "console", log.INFO)
	}()
	tests_f3.ForgeCompliance(t, driver_options.Name)
}

func TestMain(m *testing.M) {
	unittest.MainTest(m)
}
