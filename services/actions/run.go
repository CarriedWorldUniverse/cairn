// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package actions

import (
	"context"

	actions_model "forgejo.org/models/actions"
	"forgejo.org/models/db"
	"forgejo.org/modules/timeutil"
)

func CancelRun(ctx context.Context, run *actions_model.ActionRun) error {
	return db.WithTx(ctx, func(ctx context.Context) error {
		jobs, err := actions_model.GetRunJobsByRunID(ctx, run.ID)
		if err != nil {
			return err
		}
		for _, job := range jobs {
			status := job.Status
			if status.IsDone() {
				continue
			}
			if job.TaskID == 0 {
				job.Status = actions_model.StatusCancelled
				job.Stopped = timeutil.TimeStampNow()
				_, err := actions_model.UpdateRunJobWithoutNotification(ctx, job, nil, "status", "stopped")
				if err != nil {
					return err
				}
				continue
			}
			if err := StopTask(ctx, job.TaskID, actions_model.StatusCancelled); err != nil {
				return err
			}
		}

		if run.NeedApproval {
			if err := actions_model.UpdateRunApprovalByID(ctx, run.ID, actions_model.DoesNotNeedApproval, 0); err != nil {
				return err
			}
		}

		CreateCommitStatus(ctx, jobs...)

		return nil
	})
}

func ApproveRun(ctx context.Context, run *actions_model.ActionRun, doerID int64) error {
	return db.WithTx(ctx, func(ctx context.Context) error {
		jobs, err := actions_model.GetRunJobsByRunID(ctx, run.ID)
		if err != nil {
			return err
		}
		for _, job := range jobs {
			if len(job.Needs) == 0 && job.Status.IsBlocked() {
				job.Status = actions_model.StatusWaiting
				_, err := UpdateRunJob(ctx, job, nil, "status")
				if err != nil {
					return err
				}
			}
		}
		CreateCommitStatus(ctx, jobs...)

		return actions_model.UpdateRunApprovalByID(ctx, run.ID, actions_model.DoesNotNeedApproval, doerID)
	})
}
