// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package swagger

import (
	api "github.com/CarriedWorldUniverse/cairn/modules/structs"
)

// ActivityFeedsList
// swagger:response ActivityFeedsList
type swaggerActivityFeedsList struct {
	// in:body
	Body []api.Activity `json:"body"`

	// The total number of activity feeds
	TotalCount int64 `json:"X-Total-Count"`
}
