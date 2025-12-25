// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package admin

import (
	"forgejo.org/routers/api/v1/shared"
	"forgejo.org/services/context"
)

// GetRunnerRegistrationToken returns a token to register global runners
func GetRunnerRegistrationToken(ctx *context.APIContext) {
	// swagger:operation GET /admin/actions/runners/registration-token admin adminGetRunnerRegistrationToken
	// ---
	// summary: Get a runner registration token for registering global runners
	// produces:
	// - application/json
	// parameters:
	// responses:
	//   "200":
	//     "$ref": "#/responses/RegistrationToken"

	shared.GetRegistrationToken(ctx, 0, 0)
}

// GetRegistrationToken returns the token to register global runners
//
// Deprecated: This operation has been deprecated in Forgejo 15. Use GetRunnerRegistrationToken instead.
func GetRegistrationToken(ctx *context.APIContext) {
	// swagger:operation GET /admin/runners/registration-token admin adminGetRegistrationToken
	// ---
	// summary: Get a runner registration token for registering global runners
	// description: >
	//   This operation has been deprecated in Forgejo 15.
	//   Use [`/admin/actions/runners/registration-token`](#/admin/adminGetRunnerRegistrationToken) instead.
	// deprecated: true
	// produces:
	// - application/json
	// parameters:
	// responses:
	//   "200":
	//     "$ref": "#/responses/RegistrationToken"

	shared.GetRegistrationToken(ctx, 0, 0)
}

// GetActionRunJobs returns a list of action run jobs
func GetActionRunJobs(ctx *context.APIContext) {
	// swagger:operation GET /admin/actions/runners/jobs admin adminGetActionRunJobs
	// ---
	// summary: Get action run jobs
	// produces:
	// - application/json
	// parameters:
	// - name: labels
	//   in: query
	//   description: a comma separated list of labels to search for
	//   type: string
	// responses:
	//   "200":
	//     "$ref": "#/responses/RunJobList"
	//   "403":
	//     "$ref": "#/responses/forbidden"
	shared.GetActionRunJobs(ctx, 0, 0)
}

// SearchActionRunJobs returns a list of actions jobs filtered by the provided parameters
//
// Deprecated: This operation has been deprecated in Forgejo 15. Use GetActionRunJobs instead.
func SearchActionRunJobs(ctx *context.APIContext) {
	// swagger:operation GET /admin/runners/jobs admin adminSearchRunJobs
	// ---
	// summary: Search action jobs according to filter conditions
	// description: >
	//   This operation has been deprecated in Forgejo 15.
	//   Use [`/admin/actions/runners/jobs`](#/admin/adminGetActionRunJobs) instead.
	// deprecated: true
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
	//   "403":
	//     "$ref": "#/responses/forbidden"
	shared.GetActionRunJobs(ctx, 0, 0)
}

// ListRunners returns all runners, no matter whether they are global runners or scoped to an organization, user, or repository
func ListRunners(ctx *context.APIContext) {
	// swagger:operation GET /admin/actions/runners admin getAdminRunners
	// ---
	// summary: Get all runners, no matter whether they are global runners or scoped to an organization, user, or repository
	// produces:
	// - application/json
	// parameters:
	// - name: page
	//   in: query
	//   description: page number of results to return (1-based)
	//   type: integer
	// - name: limit
	//   in: query
	//   description: page size of results
	//   type: integer
	// responses:
	//   "200":
	//     "$ref": "#/responses/ActionRunnerList"
	//   "400":
	//     "$ref": "#/responses/error"
	//   "404":
	//     "$ref": "#/responses/notFound"
	shared.ListRunners(ctx, 0, 0)
}

// GetRunner returns a particular runner, no matter whether it is a global runner or scoped to an organization, user, or repository
func GetRunner(ctx *context.APIContext) {
	// swagger:operation GET /admin/actions/runners/{runner_id} admin getAdminRunner
	// ---
	// summary: Get a particular runner, no matter whether it is a global runner or scoped to an organization, user, or repository
	// produces:
	// - application/json
	// parameters:
	// - name: runner_id
	//   in: path
	//   description: ID of the runner
	//   type: string
	//   required: true
	// responses:
	//   "200":
	//     "$ref": "#/responses/ActionRunner"
	//   "400":
	//     "$ref": "#/responses/error"
	//   "404":
	//     "$ref": "#/responses/notFound"
	shared.GetRunner(ctx, 0, 0, ctx.ParamsInt64("runner_id"))
}

// DeleteRunner removes a particular runner, no matter whether it is a global runner or scoped to an organization, user, or repository
func DeleteRunner(ctx *context.APIContext) {
	// swagger:operation DELETE /admin/actions/runners/{runner_id} admin deleteAdminRunner
	// ---
	// summary: Delete a particular runner, no matter whether it is a global runner or scoped to an organization, user, or repository
	// produces:
	// - application/json
	// parameters:
	// - name: runner_id
	//   in: path
	//   description: ID of the runner
	//   type: string
	//   required: true
	// responses:
	//   "204":
	//     description: runner has been deleted
	//   "400":
	//     "$ref": "#/responses/error"
	//   "404":
	//     "$ref": "#/responses/notFound"
	shared.DeleteRunner(ctx, 0, 0, ctx.ParamsInt64("runner_id"))
}
