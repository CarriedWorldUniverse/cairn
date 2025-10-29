// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package forgejo_migrations

import (
	"xorm.io/xorm"
)

func init() {
	registerMigration(&Migration{
		Description: "add foreign keys to table forgejo_auth_token",
		Upgrade:     addForeignKeysForgejoAuthToken,
	})
}

func addForeignKeysForgejoAuthToken(x *xorm.Engine) error {
	type ForgejoAuthToken struct {
		UID int64 `xorm:"INDEX REFERENCES(user, id)"`
	}
	return syncDoctorForeignKey(x, []any{
		new(ForgejoAuthToken),
	})
}
