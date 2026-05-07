// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package forgejo_migrations

import (
	"testing"

	"github.com/CarriedWorldUniverse/cairn/models/db"
	migration_tests "github.com/CarriedWorldUniverse/cairn/models/gitea_migrations/test"
	"github.com/CarriedWorldUniverse/cairn/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_addForeignKeysForgejoAuthToken(t *testing.T) {
	type AuthorizationPurpose string
	type ForgejoAuthToken struct {
		ID              int64  `xorm:"pk autoincr"`
		UID             int64  `xorm:"INDEX"`
		LookupKey       string `xorm:"INDEX UNIQUE"`
		HashedValidator string
		Purpose         AuthorizationPurpose `xorm:"NOT NULL DEFAULT 'long_term_authorization'"`
		Expiry          timeutil.TimeStamp
	}
	type User struct {
		ID int64 `xorm:"pk autoincr"`
	}
	x, deferable := migration_tests.PrepareTestEnv(t, 0, new(User), new(ForgejoAuthToken))
	defer deferable()
	if x == nil || t.Failed() {
		return
	}

	require.NoError(t, addForeignKeysForgejoAuthToken(x))

	var remainingRecords []*ForgejoAuthToken
	require.NoError(t,
		db.GetEngine(t.Context()).
			Table("forgejo_auth_token").
			Select("`id`, `uid`").
			OrderBy("`id`").
			Find(&remainingRecords))
	assert.Equal(t,
		[]*ForgejoAuthToken{
			{ID: 1, UID: 1},
		},
		remainingRecords)
}
