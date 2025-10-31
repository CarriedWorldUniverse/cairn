// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package stats

import (
	"forgejo.org/modules/optional"
	"forgejo.org/modules/timeutil"
)

// Queue a recalculation of the stats on a `Milestone` for a given milestone by its ID
func QueueRecalcMilestoneByID(labelID int64) error {
	return safePush(recalcRequest{
		RecalcType: MilestoneByMilestoneID,
		ObjectID:   labelID,
	})
}

func QueueRecalcMilestoneByIDWithDate(labelID int64, updateTimestamp timeutil.TimeStamp) error {
	return safePush(recalcRequest{
		RecalcType:      MilestoneByMilestoneID,
		ObjectID:        labelID,
		UpdateTimestamp: optional.Some(updateTimestamp),
	})
}
