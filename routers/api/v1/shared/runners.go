// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package shared

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	actions_model "forgejo.org/models/actions"
	"forgejo.org/models/db"
	"forgejo.org/modules/structs"
	"forgejo.org/modules/util"
	"forgejo.org/routers/api/v1/utils"
	"forgejo.org/services/context"
	"forgejo.org/services/convert"
)

// RegistrationToken is a string used to register a runner with a server
type RegistrationToken struct {
	Token string `json:"token"`
}

func GetRegistrationToken(ctx *context.APIContext, ownerID, repoID int64) {
	token, err := actions_model.GetLatestRunnerToken(ctx, ownerID, repoID)
	if errors.Is(err, util.ErrNotExist) || (token != nil && !token.IsActive) {
		token, err = actions_model.NewRunnerToken(ctx, ownerID, repoID)
	}
	if err != nil {
		ctx.InternalServerError(err)
		return
	}

	ctx.JSON(http.StatusOK, RegistrationToken{Token: token.Token})
}

func GetActionRunJobs(ctx *context.APIContext, ownerID, repoID int64) {
	labels := strings.Split(ctx.FormTrim("labels"), ",")

	total, err := db.Find[actions_model.ActionRunJob](ctx, &actions_model.FindTaskOptions{
		Status:  []actions_model.Status{actions_model.StatusWaiting, actions_model.StatusRunning},
		OwnerID: ownerID,
		RepoID:  repoID,
	})
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "CountWaitingActionRunJobs", err)
		return
	}

	res := fromRunJobModelToResponse(total, labels)

	ctx.JSON(http.StatusOK, res)
}

func fromRunJobModelToResponse(job []*actions_model.ActionRunJob, labels []string) []*structs.ActionRunJob {
	var res []*structs.ActionRunJob
	for i := range job {
		if job[i].ItRunsOn(labels) {
			res = append(res, &structs.ActionRunJob{
				ID:      job[i].ID,
				RepoID:  job[i].RepoID,
				OwnerID: job[i].OwnerID,
				Name:    job[i].Name,
				Needs:   job[i].Needs,
				RunsOn:  job[i].RunsOn,
				TaskID:  job[i].TaskID,
				Status:  job[i].Status.String(),
			})
		}
	}
	return res
}

// ListRunners lists runners for api route validated ownerID and repoID
// ownerID == 0 and repoID == 0 means all runners including global runners, does not appear in sql where clause
// ownerID == 0 and repoID != 0 means all runners for the given repo
// ownerID != 0 and repoID == 0 means all runners for the given user/org
// ownerID != 0 and repoID != 0 undefined behavior
// Access rights are checked at the API route level
func ListRunners(ctx *context.APIContext, ownerID, repoID int64) {
	if ownerID != 0 && repoID != 0 {
		ctx.Error(http.StatusUnprocessableEntity, "", fmt.Errorf("ownerID and repoID should not be both set: %d and %d", ownerID, repoID))
		return
	}
	runners, total, err := db.FindAndCount[actions_model.ActionRunner](ctx, &actions_model.FindRunnerOptions{
		OwnerID:     ownerID,
		RepoID:      repoID,
		ListOptions: utils.GetListOptions(ctx),
	})
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "FindCountRunners", map[string]string{})
		return
	}

	res := new(structs.ActionRunnersResponse)
	res.TotalCount = total

	res.Entries = make([]*structs.ActionRunner, len(runners))
	for i, runner := range runners {
		res.Entries[i] = convert.ToActionRunner(ctx, runner)
	}

	ctx.JSON(http.StatusOK, &res)
}

// GetRunner get the runner for api route validated ownerID and repoID
// ownerID == 0 and repoID == 0 means any runner including global runners
// ownerID == 0 and repoID != 0 means any runner for the given repo
// ownerID != 0 and repoID == 0 means any runner for the given user/org
// ownerID != 0 and repoID != 0 undefined behavior
// Access rights are checked at the API route level
func GetRunner(ctx *context.APIContext, ownerID, repoID, runnerID int64) {
	if ownerID != 0 && repoID != 0 {
		ctx.Error(http.StatusUnprocessableEntity, "", fmt.Errorf("ownerID and repoID should not be both set: %d and %d", ownerID, repoID))
		return
	}
	runner, err := actions_model.GetRunnerByID(ctx, runnerID)
	if err != nil {
		if errors.Is(err, util.ErrNotExist) {
			ctx.Error(http.StatusNotFound, "GetRunnerNotFound", err)
		} else {
			ctx.Error(http.StatusInternalServerError, "GetRunnerFailed", err)
		}
		return
	}
	if !runner.Editable(ownerID, repoID) {
		ctx.Error(http.StatusNotFound, "RunnerEdit", "No permission to get this runner")
		return
	}
	ctx.JSON(http.StatusOK, convert.ToActionRunner(ctx, runner))
}

// DeleteRunner deletes the runner for api route validated ownerID and repoID
// ownerID == 0 and repoID == 0 means any runner including global runners
// ownerID == 0 and repoID != 0 means any runner for the given repo
// ownerID != 0 and repoID == 0 means any runner for the given user/org
// ownerID != 0 and repoID != 0 undefined behavior
// Access rights are checked at the API route level
func DeleteRunner(ctx *context.APIContext, ownerID, repoID, runnerID int64) {
	if ownerID != 0 && repoID != 0 {
		ctx.Error(http.StatusUnprocessableEntity, "", fmt.Errorf("ownerID and repoID should not be both set: %d and %d", ownerID, repoID))
		return
	}
	runner, err := actions_model.GetRunnerByID(ctx, runnerID)
	if err != nil {
		if errors.Is(err, util.ErrNotExist) {
			ctx.Error(http.StatusNotFound, "DeleteRunnerNotFound", err)
		} else {
			ctx.Error(http.StatusInternalServerError, "DeleteRunnerFailed", err)
		}
		return
	}
	if !runner.Editable(ownerID, repoID) {
		ctx.Error(http.StatusNotFound, "EditRunner", "No permission to delete this runner")
		return
	}

	err = actions_model.DeleteRunner(ctx, runner)
	if err != nil {
		ctx.InternalServerError(err)
		return
	}
	ctx.Status(http.StatusNoContent)
}
