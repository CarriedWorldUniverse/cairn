// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package stats

// Queue a recalculation of the stats on a `Label` for a given label by its ID
func QueueRecalcLabelByID(labelID int64) error {
	return safePush(recalcRequest{
		RecalcType: LabelByLabelID,
		ObjectID:   labelID,
	})
}

// Queue a recalculation of the stats on all `Label` in a given repository
func QueueRecalcLabelByRepoID(repoID int64) error {
	return safePush(recalcRequest{
		RecalcType: LabelByRepoID,
		ObjectID:   repoID,
	})
}
