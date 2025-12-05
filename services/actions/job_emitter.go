// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT AND GPL-3.0-or-later

package actions

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	actions_model "forgejo.org/models/actions"
	"forgejo.org/models/db"
	"forgejo.org/modules/graceful"
	"forgejo.org/modules/log"
	"forgejo.org/modules/queue"

	"code.forgejo.org/forgejo/runner/v12/act/jobparser"
	"xorm.io/builder"
)

var (
	logger          = log.GetManager().GetLogger(log.DEFAULT)
	jobEmitterQueue *queue.WorkerPoolQueue[*jobUpdate]
)

type jobUpdate struct {
	RunID int64
}

func EmitJobsIfReady(runID int64) error {
	err := jobEmitterQueue.Push(&jobUpdate{
		RunID: runID,
	})
	if errors.Is(err, queue.ErrAlreadyInQueue) {
		return nil
	}
	return err
}

func jobEmitterQueueHandler(items ...*jobUpdate) []*jobUpdate {
	ctx := graceful.GetManager().ShutdownContext()
	var ret []*jobUpdate
	for _, update := range items {
		if err := checkJobsOfRun(ctx, update.RunID); err != nil {
			logger.Error("checkJobsOfRun failed for RunID = %d: %v", update.RunID, err)
			ret = append(ret, update)
		}
	}
	return ret
}

func checkJobsOfRun(ctx context.Context, runID int64) error {
	jobs, err := db.Find[actions_model.ActionRunJob](ctx, actions_model.FindRunJobOptions{RunID: runID})
	if err != nil {
		return err
	}
	if err := db.WithTx(ctx, func(ctx context.Context) error {
		idToJobs := make(map[string][]*actions_model.ActionRunJob, len(jobs))
		for _, job := range jobs {
			idToJobs[job.JobID] = append(idToJobs[job.JobID], job)
		}

		updates := newJobStatusResolver(jobs).Resolve()
		for _, job := range jobs {
			if status, ok := updates[job.ID]; ok {
				job.Status = status

				if status == actions_model.StatusWaiting {
					ignore, err := tryHandleIncompleteMatrix(ctx, job, jobs)
					if err != nil {
						return fmt.Errorf("error in tryHandleIncompleteMatrix: %w", err)
					} else if ignore {
						continue
					}
				}

				if n, err := UpdateRunJob(ctx, job, builder.Eq{"status": actions_model.StatusBlocked}, "status"); err != nil {
					return err
				} else if n != 1 {
					return fmt.Errorf("no affected for updating blocked job %v", job.ID)
				}
			}
		}
		return nil
	}); err != nil {
		return err
	}
	CreateCommitStatus(ctx, jobs...)
	return nil
}

type jobStatusResolver struct {
	statuses map[int64]actions_model.Status
	needs    map[int64][]int64
	jobMap   map[int64]*actions_model.ActionRunJob
}

func newJobStatusResolver(jobs actions_model.ActionJobList) *jobStatusResolver {
	idToJobs := make(map[string][]*actions_model.ActionRunJob, len(jobs))
	jobMap := make(map[int64]*actions_model.ActionRunJob)
	for _, job := range jobs {
		idToJobs[job.JobID] = append(idToJobs[job.JobID], job)
		jobMap[job.ID] = job
	}

	statuses := make(map[int64]actions_model.Status, len(jobs))
	needs := make(map[int64][]int64, len(jobs))
	for _, job := range jobs {
		statuses[job.ID] = job.Status
		for _, need := range job.Needs {
			for _, v := range idToJobs[need] {
				needs[job.ID] = append(needs[job.ID], v.ID)
			}
		}
	}
	return &jobStatusResolver{
		statuses: statuses,
		needs:    needs,
		jobMap:   jobMap,
	}
}

func (r *jobStatusResolver) Resolve() map[int64]actions_model.Status {
	ret := map[int64]actions_model.Status{}
	for i := 0; i < len(r.statuses); i++ {
		updated := r.resolve()
		if len(updated) == 0 {
			return ret
		}
		for k, v := range updated {
			ret[k] = v
			r.statuses[k] = v
		}
	}
	return ret
}

func (r *jobStatusResolver) resolve() map[int64]actions_model.Status {
	ret := map[int64]actions_model.Status{}
	for id, status := range r.statuses {
		if status != actions_model.StatusBlocked {
			continue
		}
		allDone, allSucceed := true, true
		for _, need := range r.needs[id] {
			needStatus := r.statuses[need]
			if !needStatus.IsDone() {
				allDone = false
			}
			if needStatus.In(actions_model.StatusFailure, actions_model.StatusCancelled, actions_model.StatusSkipped) {
				allSucceed = false
			}
		}
		if allDone {
			if allSucceed {
				ret[id] = actions_model.StatusWaiting
			} else {
				// Check if the job has an "if" condition
				hasIf := false
				if wfJobs, _ := jobparser.Parse(r.jobMap[id].WorkflowPayload, false); len(wfJobs) == 1 {
					_, wfJob := wfJobs[0].Job()
					hasIf = len(wfJob.If.Value) > 0
				}

				if hasIf {
					// act_runner will check the "if" condition
					ret[id] = actions_model.StatusWaiting
				} else {
					// If the "if" condition is empty and not all dependent jobs completed successfully,
					// the job should be skipped.
					ret[id] = actions_model.StatusSkipped
				}
			}
		}
	}
	return ret
}

// Invoked once a job has all its `needs` parameters met and is ready to transition to waiting, this may expand the
// job's `strategy.matrix` into multiple new jobs.
func tryHandleIncompleteMatrix(ctx context.Context, blockedJob *actions_model.ActionRunJob, jobsInRun []*actions_model.ActionRunJob) (bool, error) {
	incompleteMatrix, _, err := blockedJob.IsIncompleteMatrix()
	if err != nil {
		return false, fmt.Errorf("job IsIncompleteMatrix: %w", err)
	}

	incompleteRunsOn, _, _, err := blockedJob.IsIncompleteRunsOn()
	if err != nil {
		return false, fmt.Errorf("job IsIncompleteRunsOn: %w", err)
	}

	if !incompleteMatrix && !incompleteRunsOn {
		// Not relevant to attempt re-parsing the job if it wasn't marked as Incomplete[...] previously.
		return false, nil
	}

	if err := blockedJob.LoadRun(ctx); err != nil {
		return false, fmt.Errorf("failure LoadRun in tryHandleIncompleteMatrix: %w", err)
	}

	// Compute jobOutputs for all the other jobs required as needed by this job:
	jobOutputs := make(map[string]map[string]string, len(jobsInRun))
	for _, job := range jobsInRun {
		if !slices.Contains(blockedJob.Needs, job.JobID) {
			// Only include jobs that are in the `needs` of the blocked job.
			continue
		} else if !job.Status.IsDone() {
			// Unexpected: `job` is needed by `blockedJob` but it isn't done; `jobStatusResolver` shouldn't be calling
			// `tryHandleIncompleteMatrix` in this case.
			return false, fmt.Errorf(
				"jobStatusResolver attempted to tryHandleIncompleteMatrix for a job (id=%d) with an incomplete 'needs' job (id=%d)", blockedJob.ID, job.ID)
		}

		outputs, err := actions_model.FindTaskOutputByTaskID(ctx, job.TaskID)
		if err != nil {
			return false, fmt.Errorf("failed loading task outputs: %w", err)
		}

		outputsMap := make(map[string]string, len(outputs))
		for _, v := range outputs {
			outputsMap[v.OutputKey] = v.OutputValue
		}
		jobOutputs[job.JobID] = outputsMap
	}

	// Re-parse the blocked job, providing all the other completed jobs' outputs, to turn this incomplete job into
	// one-or-more new jobs:
	newJobWorkflows, err := jobparser.Parse(blockedJob.WorkflowPayload, false,
		jobparser.WithJobOutputs(jobOutputs),
		jobparser.WithWorkflowNeeds(blockedJob.Needs),
		jobparser.SupportIncompleteRunsOn(),
	)
	if err != nil {
		return false, fmt.Errorf("failure re-parsing SingleWorkflow: %w", err)
	}

	// Even though every job in the `needs` list is done, perform a consistency check if the job was still unable to be
	// evaluated into a fully complete job with the correct matrix and runs-on values. Evaluation errors here need to be
	// reported back to the user for them to correct their workflow, so we slip this notification into
	// PreExecutionError.
	for _, swf := range newJobWorkflows {
		if swf.IncompleteMatrix {
			errorCode, errorDetails := persistentIncompleteMatrixError(blockedJob, swf.IncompleteMatrixNeeds)
			if err := FailRunPreExecutionError(ctx, blockedJob.Run, errorCode, errorDetails); err != nil {
				return false, fmt.Errorf("failure when marking run with error: %w", err)
			}
			// Return `true` to skip running this job in this invalid state
			return true, nil
		} else if swf.IncompleteRunsOn {
			errorCode, errorDetails := persistentIncompleteRunsOnError(blockedJob, swf.IncompleteRunsOnNeeds, swf.IncompleteRunsOnMatrix)
			if err := FailRunPreExecutionError(ctx, blockedJob.Run, errorCode, errorDetails); err != nil {
				return false, fmt.Errorf("failure when marking run with error: %w", err)
			}
			// Return `true` to skip running this job in this invalid state
			return true, nil
		}
	}

	err = db.WithTx(ctx, func(ctx context.Context) error {
		err := actions_model.InsertRunJobs(ctx, blockedJob.Run, newJobWorkflows)
		if err != nil {
			return fmt.Errorf("failure in InsertRunJobs: %w", err)
		}

		// Delete the blocked job which has been expanded into `newJobWorkflows`.
		count, err := db.DeleteByID[actions_model.ActionRunJob](ctx, blockedJob.ID)
		if err != nil {
			return err
		} else if count != 1 {
			return fmt.Errorf("unexpected record count in delete incomplete_matrix=true job with ID %d; count = %d", blockedJob.ID, count)
		}

		return nil
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

func persistentIncompleteMatrixError(job *actions_model.ActionRunJob, incompleteNeeds *jobparser.IncompleteNeeds) (actions_model.PreExecutionError, []any) {
	var errorCode actions_model.PreExecutionError
	var errorDetails []any

	// `incompleteNeeds` tells us what part of a `${{ needs... }}` expression was missing
	if incompleteNeeds != nil {
		jobRef := incompleteNeeds.Job       // always provided
		outputRef := incompleteNeeds.Output // missing if the entire job wasn't present
		if outputRef != "" {
			errorCode = actions_model.ErrorCodeIncompleteMatrixMissingOutput
			errorDetails = []any{
				job.JobID,
				jobRef,
				outputRef,
			}
		} else {
			errorCode = actions_model.ErrorCodeIncompleteMatrixMissingJob
			errorDetails = []any{
				job.JobID,
				jobRef,
				strings.Join(job.Needs, ", "),
			}
		}
		return errorCode, errorDetails
	}

	// Not sure why we ended up in `IncompleteMatrix` when nothing was marked as incomplete
	errorCode = actions_model.ErrorCodeIncompleteMatrixUnknownCause
	errorDetails = []any{job.JobID}
	return errorCode, errorDetails
}

func persistentIncompleteRunsOnError(job *actions_model.ActionRunJob, incompleteNeeds *jobparser.IncompleteNeeds, incompleteMatrix *jobparser.IncompleteMatrix) (actions_model.PreExecutionError, []any) {
	var errorCode actions_model.PreExecutionError
	var errorDetails []any

	// `incompleteMatrix` tells us which dimension of a matrix was accessed that was missing
	if incompleteMatrix != nil {
		dimension := incompleteMatrix.Dimension
		errorCode = actions_model.ErrorCodeIncompleteRunsOnMissingMatrixDimension
		errorDetails = []any{
			job.JobID,
			dimension,
		}
		return errorCode, errorDetails
	}

	// `incompleteNeeds` tells us what part of a `${{ needs... }}` expression was missing
	if incompleteNeeds != nil {
		jobRef := incompleteNeeds.Job       // always provided
		outputRef := incompleteNeeds.Output // missing if the entire job wasn't present
		if outputRef != "" {
			errorCode = actions_model.ErrorCodeIncompleteRunsOnMissingOutput
			errorDetails = []any{
				job.JobID,
				jobRef,
				outputRef,
			}
		} else {
			errorCode = actions_model.ErrorCodeIncompleteRunsOnMissingJob
			errorDetails = []any{
				job.JobID,
				jobRef,
				strings.Join(job.Needs, ", "),
			}
		}
		return errorCode, errorDetails
	}

	// Not sure why we ended up in `IncompleteRunsOn` when nothing was marked as incomplete
	errorCode = actions_model.ErrorCodeIncompleteRunsOnUnknownCause
	errorDetails = []any{job.JobID}
	return errorCode, errorDetails
}
