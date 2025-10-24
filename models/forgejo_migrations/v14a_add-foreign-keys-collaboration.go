// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package forgejo_migrations

import (
	"xorm.io/xorm"
)

func init() {
	registerMigration(&Migration{
		Description: "add foreign keys to collaboration, repo_id & user_id",
		Upgrade:     addForeignKeysCollaboration,
	})
}

func addForeignKeysCollaboration(x *xorm.Engine) error {
	type Collaboration struct {
		RepoID int64 `xorm:"UNIQUE(s) INDEX NOT NULL REFERENCES(repository, id)"`
		UserID int64 `xorm:"UNIQUE(s) INDEX NOT NULL REFERENCES(user, id)"`
	}
	return syncDoctorForeignKey(x, []any{
		new(Collaboration),
	})
}
