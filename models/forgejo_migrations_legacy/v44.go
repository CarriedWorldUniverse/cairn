// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package forgejo_migrations_legacy

import (
	"xorm.io/xorm"
)

func AddForeignKeysAccess(x *xorm.Engine) error {
	type Access struct {
		UserID int64 `xorm:"UNIQUE(s) REFERENCES(user, id)"`
		RepoID int64 `xorm:"UNIQUE(s) REFERENCES(repository, id)"`
	}
	return syncDoctorForeignKey(x, []any{
		new(Access),
	})
}
