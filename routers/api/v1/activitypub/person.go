// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package activitypub

import (
	"net/http"

	"forgejo.org/modules/activitypub"
	"forgejo.org/modules/log"
	"forgejo.org/modules/web"
	"forgejo.org/services/context"
	"forgejo.org/services/convert"
	"forgejo.org/services/federation"

	ap "github.com/go-ap/activitypub"
	"github.com/go-ap/jsonld"
)

// Person function returns the Person actor for a user
func Person(ctx *context.APIContext) {
	// swagger:operation GET /activitypub/user-id/{user-id} activitypub activitypubPerson
	// ---
	// summary: Returns the Person actor for a user
	// produces:
	// - application/json
	// parameters:
	// - name: user-id
	//   in: path
	//   description: user ID of the user
	//   type: integer
	//   required: true
	// responses:
	//   "200":
	//     "$ref": "#/responses/ActivityPub"

	person, err := convert.ToActivityPubPerson(ctx, ctx.ContextUser)
	if err != nil {
		ctx.ServerError("convert.ToActivityPubPerson", err)
		return
	}

	binary, err := jsonld.WithContext(jsonld.IRI(ap.ActivityBaseURI), jsonld.IRI(ap.SecurityContextURI)).Marshal(person)
	if err != nil {
		ctx.ServerError("MarshalJSON", err)
		return
	}
	ctx.Resp.Header().Add("Content-Type", activitypub.ActivityStreamsContentType)
	ctx.Resp.WriteHeader(http.StatusOK)
	if _, err = ctx.Resp.Write(binary); err != nil {
		log.Error("write to resp err: %v", err)
	}
}

// PersonInbox function handles the incoming data for a user inbox
func PersonInbox(ctx *context.APIContext) {
	// swagger:operation POST /activitypub/user-id/{user-id}/inbox activitypub activitypubPersonInbox
	// ---
	// summary: Send to the inbox
	// produces:
	// - application/json
	// parameters:
	// - name: user-id
	//   in: path
	//   description: user ID of the user
	//   type: integer
	//   required: true
	// responses:
	//   "202":
	//     "$ref": "#/responses/empty"

	form := web.GetForm(ctx)
	activity := form.(*ap.Activity)
	result, err := federation.ProcessPersonInbox(ctx, ctx.ContextUser, activity)
	if err != nil {
		ctx.Error(federation.HTTPStatus(err), "PersonInbox", err)
		return
	}
	responseServiceResult(ctx, result)
}
