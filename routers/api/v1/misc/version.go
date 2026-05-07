// Copyright 2017 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package misc

import (
	"net/http"

	"github.com/CarriedWorldUniverse/cairn/modules/setting"
	"github.com/CarriedWorldUniverse/cairn/modules/structs"
	"github.com/CarriedWorldUniverse/cairn/services/context"
)

// Version shows the version of the Gitea server
func Version(ctx *context.APIContext) {
	// swagger:operation GET /version miscellaneous getVersion
	// ---
	// summary: Returns the version of the running application
	// produces:
	// - application/json
	// responses:
	//   "200":
	//     "$ref": "#/responses/ServerVersion"
	ctx.JSON(http.StatusOK, &structs.ServerVersion{Version: setting.AppVer})
}
