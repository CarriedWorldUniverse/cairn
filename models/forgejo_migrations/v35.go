// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package forgejo_migrations //nolint:revive

import (
	"forgejo.org/modules/timeutil"

	"xorm.io/xorm"
)

func AddIndexToActionRunStopped(x *xorm.Engine) error {
	type ActionRun struct {
		ID      int64
		Stopped timeutil.TimeStamp `xorm:"index"`
	}

	return x.Sync(&ActionRun{})
}
