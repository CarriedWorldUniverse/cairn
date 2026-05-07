// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package swagger

import (
	"github.com/CarriedWorldUniverse/cairn/modules/forgefed"
	api "github.com/CarriedWorldUniverse/cairn/modules/structs"
)

// ActivityPub
// swagger:response ActivityPub
type swaggerResponseActivityPub struct {
	// in:body
	Body api.ActivityPub `json:"body"`
}

// Outbox
// swagger:response Outbox
type swaggerResponseOutbox struct {
	// in:body
	Body forgefed.ForgeOutbox `json:"body"`
}
