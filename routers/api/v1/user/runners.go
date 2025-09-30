// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package user

import (
	"forgejo.org/routers/api/v1/shared"
	"forgejo.org/services/context"
)

// https://docs.github.com/en/rest/actions/self-hosted-runners?apiVersion=2022-11-28#create-a-registration-token-for-an-organization

// GetRegistrationToken returns the token to register user runners
func GetRegistrationToken(ctx *context.APIContext) {
	// swagger:operation GET /user/actions/runners/registration-token user userGetRunnerRegistrationToken
	// ---
	// summary: Get an user's actions runner registration token
	// produces:
	// - application/json
	// parameters:
	// responses:
	//   "200":
	//     "$ref": "#/responses/RegistrationToken"
	//   "401":
	//     "$ref": "#/responses/unauthorized"
	//   "403":
	//     "$ref": "#/responses/forbidden"

	shared.GetRegistrationToken(ctx, ctx.Doer.ID, 0)
}

// SearchActionRunJobs return a list of actions jobs filtered by the provided parameters
func SearchActionRunJobs(ctx *context.APIContext) {
	// swagger:operation GET /user/actions/runners/jobs user userSearchRunJobs
	// ---
	// summary: Search for user's action jobs according filter conditions
	// produces:
	// - application/json
	// parameters:
	// - name: labels
	//   in: query
	//   description: a comma separated list of run job labels to search for
	//   type: string
	// responses:
	//   "200":
	//     "$ref": "#/responses/RunJobList"
	//   "401":
	//     "$ref": "#/responses/unauthorized"
	//   "403":
	//     "$ref": "#/responses/forbidden"
	shared.GetActionRunJobs(ctx, ctx.Doer.ID, 0)
}

// CreateRegistrationToken returns the token to register user runners
func CreateRegistrationToken(ctx *context.APIContext) {
	// swagger:operation POST /user/actions/runners/registration-token user userCreateRunnerRegistrationToken
	// ---
	// summary: Get an user's actions runner registration token
	// produces:
	// - application/json
	// parameters:
	// responses:
	//   "200":
	//     "$ref": "#/responses/RegistrationToken"
	//   "401":
	//     "$ref": "#/responses/unauthorized"

	shared.GetRegistrationToken(ctx, ctx.Doer.ID, 0)
}

// ListRunners get user-level runners
func ListRunners(ctx *context.APIContext) {
	// swagger:operation GET /user/actions/runners user getUserRunners
	// ---
	// summary: Get user-level runners
	// produces:
	// - application/json
	// responses:
	//   "200":
	//     "$ref": "#/responses/ActionRunnersResponse"
	//   "400":
	//     "$ref": "#/responses/error"
	//   "401":
	//     "$ref": "#/responses/unauthorized"
	//   "404":
	//     "$ref": "#/responses/notFound"
	shared.ListRunners(ctx, ctx.Doer.ID, 0)
}

// GetRunner get an user-level runner
func GetRunner(ctx *context.APIContext) {
	// swagger:operation GET /user/actions/runners/{runner_id} user getUserRunner
	// ---
	// summary: Get an user-level runner
	// produces:
	// - application/json
	// parameters:
	// - name: runner_id
	//   in: path
	//   description: id of the runner
	//   type: string
	//   required: true
	// responses:
	//   "200":
	//     "$ref": "#/responses/ActionRunner"
	//   "400":
	//     "$ref": "#/responses/error"
	//   "401":
	//     "$ref": "#/responses/unauthorized"
	//   "404":
	//     "$ref": "#/responses/notFound"
	shared.GetRunner(ctx, ctx.Doer.ID, 0, ctx.ParamsInt64("runner_id"))
}

// DeleteRunner delete an user-level runner
func DeleteRunner(ctx *context.APIContext) {
	// swagger:operation DELETE /user/actions/runners/{runner_id} user deleteUserRunner
	// ---
	// summary: Delete an user-level runner
	// produces:
	// - application/json
	// parameters:
	// - name: runner_id
	//   in: path
	//   description: id of the runner
	//   type: string
	//   required: true
	// responses:
	//   "204":
	//     description: runner has been deleted
	//   "400":
	//     "$ref": "#/responses/error"
	//   "401":
	//     "$ref": "#/responses/unauthorized"
	//   "404":
	//     "$ref": "#/responses/notFound"
	shared.DeleteRunner(ctx, ctx.Doer.ID, 0, ctx.ParamsInt64("runner_id"))
}
