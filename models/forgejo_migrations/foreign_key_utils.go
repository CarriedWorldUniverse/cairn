// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package forgejo_migrations

import (
	"errors"

	"forgejo.org/modules/log"

	"xorm.io/xorm"
)

func syncDoctorForeignKey(x *xorm.Engine, beans []any) error {
	for _, bean := range beans {
		// Sync() drops indexes by default, which will cause unnecessary rebuilding of indexes when syncDoctorForeignKey
		// is used with partial bean definitions; so we disable that option
		_, err := x.SyncWithOptions(xorm.SyncOptions{IgnoreDropIndices: true}, bean)
		if err != nil {
			if errors.Is(err, xorm.ErrForeignKeyViolation) {
				tableName := x.TableName(bean)
				log.Error(
					"Foreign key creation on table %s failed. Run `forgejo doctor check --all` to identify the orphaned records preventing this foreign key from being created. Error was: %v",
					tableName, err)
				return err
			}
			return err
		}
	}
	return nil
}
