// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package forgejo_migrations_legacy

import (
	"errors"
	"fmt"

	"forgejo.org/modules/log"

	"xorm.io/builder"
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

func AddForeignKeysStopwatchTrackedTime(x *xorm.Engine) error {
	type Stopwatch struct {
		IssueID int64 `xorm:"INDEX REFERENCES(issue, id)"`
		UserID  int64 `xorm:"INDEX REFERENCES(user, id)"`
	}
	type TrackedTime struct {
		ID      int64 `xorm:"pk autoincr"`
		IssueID int64 `xorm:"INDEX REFERENCES(issue, id)"`
		UserID  int64 `xorm:"INDEX REFERENCES(user, id)"`
	}

	// TrackedTime.UserID used to be an intentionally dangling reference if a user was deleted, in order to maintain the
	// time that was tracked against an issue.  With the addition of a foreign key, we set UserID to NULL where the user
	// doesn't exist instead of leaving it pointing to an invalid record:
	var trackedTime []TrackedTime
	err := x.Table("tracked_time").
		Join("LEFT", "`user`", "`tracked_time`.user_id = `user`.id").
		Where(builder.IsNull{"`user`.id"}).
		Find(&trackedTime)
	if err != nil {
		return err
	}
	for _, tt := range trackedTime {
		affected, err := x.Table(&TrackedTime{}).Where("id = ?", tt.ID).Update(map[string]any{"user_id": nil})
		if err != nil {
			return err
		} else if affected != 1 {
			return fmt.Errorf("expected to update 1 tracked_time record with ID %d, but actually affected %d records", tt.ID, affected)
		}
	}

	return syncDoctorForeignKey(x, []any{
		new(Stopwatch),
		new(TrackedTime),
	})
}
