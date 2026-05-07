// SPDX-License-Identifier: MIT

package forgejo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/models/db"
	"github.com/CarriedWorldUniverse/cairn/models/unittest"
	"github.com/CarriedWorldUniverse/cairn/modules/setting"

	"github.com/stretchr/testify/require"
)

func TestForgejo_PreMigrationSanityChecks(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := db.DefaultContext
	e := db.GetEngine(ctx)

	require.NoError(t, PreMigrationSanityChecks(e, ForgejoV4DatabaseVersion, configFixture(t, "")))
}

func configFixture(t *testing.T, content string) setting.ConfigProvider {
	config := filepath.Join(t.TempDir(), "app.ini")
	require.NoError(t, os.WriteFile(config, []byte(content), 0o777))
	cfg, err := setting.NewConfigProviderFromFile(config)
	require.NoError(t, err)
	return cfg
}
