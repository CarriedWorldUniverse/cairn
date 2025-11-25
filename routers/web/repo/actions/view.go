// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2024 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	actions_model "forgejo.org/models/actions"
	"forgejo.org/models/db"
	git_model "forgejo.org/models/git"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	"forgejo.org/modules/actions"
	"forgejo.org/modules/base"
	"forgejo.org/modules/git"
	"forgejo.org/modules/json"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/storage"
	"forgejo.org/modules/templates"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/util"
	"forgejo.org/modules/web"
	"forgejo.org/routers/common"
	actions_service "forgejo.org/services/actions"
	context_module "forgejo.org/services/context"

	"xorm.io/builder"
)

func RedirectToLatestAttempt(ctx *context_module.Context) {
	runIndex := ctx.ParamsInt64("run")
	jobIndex := ctx.ParamsInt64("job")

	job, _ := getRunJobs(ctx, runIndex, jobIndex)
	if ctx.Written() {
		return
	}

	jobURL, err := job.HTMLURL(ctx)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, err.Error())
		return
	}

	ctx.Redirect(jobURL, http.StatusTemporaryRedirect)
}

func View(ctx *context_module.Context) {
	ctx.Data["PageIsActions"] = true
	runIndex := ctx.ParamsInt64("run")
	jobIndex := ctx.ParamsInt64("job")
	// note: this is `attemptNumber` not `attemptIndex` since this value has to matches the ActionTask's Attempt field
	// which uses 1-based numbering... would be confusing as "Index" if it later can't be used to index an slice/array.
	attemptNumber := ctx.ParamsInt64("attempt")

	job, _ := getRunJobs(ctx, runIndex, jobIndex)
	if ctx.Written() {
		return
	}

	workflowName := job.Run.WorkflowID

	ctx.Data["RunIndex"] = runIndex
	ctx.Data["RunID"] = job.Run.ID
	ctx.Data["JobIndex"] = jobIndex
	ctx.Data["ActionsURL"] = ctx.Repo.RepoLink + "/actions"
	ctx.Data["AttemptNumber"] = attemptNumber
	ctx.Data["WorkflowName"] = workflowName
	ctx.Data["WorkflowURL"] = ctx.Repo.RepoLink + "/actions?workflow=" + workflowName

	viewResponse := getViewResponse(ctx, &ViewRequest{}, runIndex, jobIndex, attemptNumber)
	if ctx.Written() {
		return
	}
	artifactsViewResponse := getArtifactsViewResponse(ctx, runIndex)
	if ctx.Written() {
		return
	}

	var buf1, buf2 strings.Builder
	if err := json.NewEncoder(&buf1).Encode(viewResponse); err != nil {
		ctx.ServerError("EncodingError", err)
		return
	}
	ctx.Data["InitialData"] = buf1.String()

	if err := json.NewEncoder(&buf2).Encode(artifactsViewResponse); err != nil {
		ctx.ServerError("EncodingError", err)
		return
	}
	ctx.Data["InitialArtifactsData"] = buf2.String()

	ctx.HTML(http.StatusOK, tplViewActions)
}

func ViewLatest(ctx *context_module.Context) {
	run, err := actions_model.GetLatestRun(ctx, ctx.Repo.Repository.ID)
	if err != nil {
		ctx.NotFound("GetLatestRun", err)
		return
	}
	err = run.LoadAttributes(ctx)
	if err != nil {
		ctx.ServerError("LoadAttributes", err)
		return
	}
	ctx.Redirect(run.HTMLURL(), http.StatusTemporaryRedirect)
}

func ViewLatestWorkflowRun(ctx *context_module.Context) {
	branch := ctx.FormString("branch")
	if branch == "" {
		branch = ctx.Repo.Repository.DefaultBranch
	}
	branch = fmt.Sprintf("refs/heads/%s", branch)
	event := ctx.FormString("event")

	workflowFile := ctx.Params("workflow_name")
	run, err := actions_model.GetLatestRunForBranchAndWorkflow(ctx, ctx.Repo.Repository.ID, branch, workflowFile, event)
	if err != nil {
		if errors.Is(err, util.ErrNotExist) {
			ctx.NotFound("GetLatestRunForBranchAndWorkflow", err)
		} else {
			ctx.ServerError("GetLatestRunForBranchAndWorkflow", err)
		}
		return
	}

	err = run.LoadAttributes(ctx)
	if err != nil {
		ctx.ServerError("LoadAttributes", err)
		return
	}
	ctx.Redirect(run.HTMLURL(), http.StatusTemporaryRedirect)
}

type ViewRequest struct {
	LogCursors []struct {
		Step     int   `json:"step"`
		Cursor   int64 `json:"cursor"`
		Expanded bool  `json:"expanded"`
	} `json:"logCursors"`
}

type ViewResponse struct {
	State ViewState `json:"state"`
	Logs  ViewLogs  `json:"logs"`
}

type ViewState struct {
	Run        ViewRunInfo    `json:"run"`
	CurrentJob ViewCurrentJob `json:"currentJob"`
}

type ViewRunInfo struct {
	Link              string        `json:"link"`
	Title             string        `json:"title"`
	TitleHTML         template.HTML `json:"titleHTML"`
	Status            string        `json:"status"`
	CanCancel         bool          `json:"canCancel"`
	CanApprove        bool          `json:"canApprove"` // the run needs an approval and the doer has permission to approve
	CanRerun          bool          `json:"canRerun"`
	CanDeleteArtifact bool          `json:"canDeleteArtifact"`
	Done              bool          `json:"done"`
	Jobs              []*ViewJob    `json:"jobs"`
	Commit            ViewCommit    `json:"commit"`
	PreExecutionError string        `json:"preExecutionError"`
}

type ViewCurrentJob struct {
	Title       string         `json:"title"`
	Detail      string         `json:"detail"`
	Steps       []*ViewJobStep `json:"steps"`
	AllAttempts []*TaskAttempt `json:"allAttempts"`
}

type ViewLogs struct {
	StepsLog []*ViewStepLog `json:"stepsLog"`
}

type ViewJob struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	CanRerun bool   `json:"canRerun"`
	Duration string `json:"duration"`
}

type ViewCommit struct {
	LocaleCommit   string     `json:"localeCommit"`
	LocalePushedBy string     `json:"localePushedBy"`
	LocaleWorkflow string     `json:"localeWorkflow"`
	ShortSha       string     `json:"shortSHA"`
	Link           string     `json:"link"`
	Pusher         ViewUser   `json:"pusher"`
	Branch         ViewBranch `json:"branch"`
}

type ViewUser struct {
	DisplayName string `json:"displayName"`
	Link        string `json:"link"`
}

type ViewBranch struct {
	Name      string `json:"name"`
	Link      string `json:"link"`
	IsDeleted bool   `json:"isDeleted"`
}

type ViewJobStep struct {
	Summary  string `json:"summary"`
	Duration string `json:"duration"`
	Status   string `json:"status"`
}

type ViewStepLog struct {
	Step    int                `json:"step"`
	Cursor  int64              `json:"cursor"`
	Lines   []*ViewStepLogLine `json:"lines"`
	Started int64              `json:"started"`
}

type ViewStepLogLine struct {
	Index     int64   `json:"index"`
	Message   string  `json:"message"`
	Timestamp float64 `json:"timestamp"`
}

type TaskAttempt struct {
	Number  int64         `json:"number"`
	Started template.HTML `json:"time_since_started_html"`
	Status  string        `json:"status"`
}

func ViewPost(ctx *context_module.Context) {
	req := web.GetForm(ctx).(*ViewRequest)
	runIndex := ctx.ParamsInt64("run")
	jobIndex := ctx.ParamsInt64("job")
	// note: this is `attemptNumber` not `attemptIndex` since this value has to matches the ActionTask's Attempt field
	// which uses 1-based numbering... would be confusing as "Index" if it later can't be used to index an slice/array.
	attemptNumber := ctx.ParamsInt64("attempt")

	resp := getViewResponse(ctx, req, runIndex, jobIndex, attemptNumber)
	if ctx.Written() {
		return
	}

	ctx.JSON(http.StatusOK, resp)
}

func getViewResponse(ctx *context_module.Context, req *ViewRequest, runIndex, jobIndex, attemptNumber int64) *ViewResponse {
	current, jobs := getRunJobs(ctx, runIndex, jobIndex)
	if ctx.Written() {
		return nil
	}
	run := current.Run
	if err := run.LoadAttributes(ctx); err != nil {
		ctx.Error(http.StatusInternalServerError, err.Error())
		return nil
	}

	resp := &ViewResponse{}

	metas := ctx.Repo.Repository.ComposeMetas(ctx)

	resp.State.Run.Title = run.Title
	resp.State.Run.TitleHTML = templates.RenderCommitMessage(ctx, run.Title, metas)
	resp.State.Run.Link = run.Link()
	resp.State.Run.CanCancel = !run.Status.IsDone() && ctx.Repo.CanWrite(unit.TypeActions)
	resp.State.Run.CanApprove = run.NeedApproval && ctx.Repo.CanWrite(unit.TypeActions)
	resp.State.Run.CanRerun = run.Status.IsDone() && ctx.Repo.CanWrite(unit.TypeActions)
	resp.State.Run.CanDeleteArtifact = run.Status.IsDone() && ctx.Repo.CanWrite(unit.TypeActions)
	resp.State.Run.Jobs = make([]*ViewJob, 0, len(jobs)) // marshal to '[]' instead of 'null' in json
	resp.State.Run.Status = run.Status.String()
	resp.State.Run.PreExecutionError = run.PreExecutionError

	// It's possible for the run to be marked with a finalized status (eg. failure) because of a  single job within the
	// run; eg. one job fails, the run fails. But other jobs can still be running. The frontend RepoActionView uses the
	// `done` flag to indicate whether to stop querying the run's status -- so even though the run has reached a final
	// state, it may not be time to stop polling for updates.
	done := run.Status.IsDone()

	for _, v := range jobs {
		if !v.Status.IsDone() {
			// Ah, another job is still running. Keep the frontend polling enabled then.
			done = false
		}
		resp.State.Run.Jobs = append(resp.State.Run.Jobs, &ViewJob{
			ID:       v.ID,
			Name:     v.Name,
			Status:   v.Status.String(),
			CanRerun: v.Status.IsDone() && ctx.Repo.CanWrite(unit.TypeActions),
			Duration: v.Duration().String(),
		})
	}
	resp.State.Run.Done = done

	pusher := ViewUser{
		DisplayName: run.TriggerUser.GetDisplayName(),
		Link:        run.TriggerUser.HomeLink(),
	}
	branch := ViewBranch{
		Name: run.PrettyRef(),
		Link: run.RefLink(),
	}
	refName := git.RefName(run.Ref)
	if refName.IsBranch() {
		b, err := git_model.GetBranch(ctx, ctx.Repo.Repository.ID, refName.ShortName())
		if err != nil && !git_model.IsErrBranchNotExist(err) {
			log.Error("GetBranch: %v", err)
		} else if git_model.IsErrBranchNotExist(err) || (b != nil && b.IsDeleted) {
			branch.IsDeleted = true
		}
	}

	resp.State.Run.Commit = ViewCommit{
		LocaleCommit:   ctx.Locale.TrString("actions.runs.commit"),
		LocalePushedBy: ctx.Locale.TrString("actions.runs.pushed_by"),
		LocaleWorkflow: ctx.Locale.TrString("actions.runs.workflow"),
		ShortSha:       base.ShortSha(run.CommitSHA),
		Link:           fmt.Sprintf("%s/commit/%s", run.Repo.Link(), run.CommitSHA),
		Pusher:         pusher,
		Branch:         branch,
	}

	var task *actions_model.ActionTask
	// TaskID will be set only when the ActionRunJob has been picked by a runner, resulting in an ActionTask being
	// created representing the specific task.  If current.TaskID is not set, then the user is attempting to view a job
	// that hasn't been picked up by a runner... in this case we're not going to try to fetch the specific attempt.
	// This helps to support the UI displaying a useful and error-free page when viewing a job that is queued but not
	// picked, or an attempt that is queued for rerun but not yet picked.
	if current.TaskID > 0 {
		var err error
		task, err = actions_model.GetTaskByJobAttempt(ctx, current.ID, attemptNumber)
		if err != nil {
			ctx.Error(http.StatusInternalServerError, err.Error())
			return nil
		}
		task.Job = current
		if err := task.LoadAttributes(ctx); err != nil {
			ctx.Error(http.StatusInternalServerError, err.Error())
			return nil
		}
	}

	resp.State.CurrentJob.Title = current.Name
	resp.State.CurrentJob.Detail = current.Status.LocaleString(ctx.Locale)
	if run.NeedApproval {
		resp.State.CurrentJob.Detail = ctx.Locale.TrString("actions.need_approval_desc")
	}
	resp.State.CurrentJob.Steps = make([]*ViewJobStep, 0) // marshal to '[]' instead of 'null' in json
	resp.Logs.StepsLog = make([]*ViewStepLog, 0)          // marshal to '[]' instead of 'null' in json
	// As noted above with TaskID; task will be nil when the job hasn't be picked yet...
	if task != nil {
		taskAttempts, err := task.GetAllAttempts(ctx)
		if err != nil {
			ctx.Error(http.StatusInternalServerError, err.Error())
			return nil
		}
		allAttempts := make([]*TaskAttempt, len(taskAttempts))
		for i, actionTask := range taskAttempts {
			allAttempts[i] = &TaskAttempt{
				Number:  actionTask.Attempt,
				Started: templates.TimeSince(actionTask.Started),
				Status:  actionTask.Status.String(),
			}
		}
		resp.State.CurrentJob.AllAttempts = allAttempts

		steps := actions.FullSteps(task)
		for _, v := range steps {
			resp.State.CurrentJob.Steps = append(resp.State.CurrentJob.Steps, &ViewJobStep{
				Summary:  v.Name,
				Duration: v.Duration().String(),
				Status:   v.Status.String(),
			})
		}

		for _, cursor := range req.LogCursors {
			if !cursor.Expanded {
				continue
			}

			step := steps[cursor.Step]

			// if task log is expired, return a consistent log line
			if task.LogExpired {
				if cursor.Cursor == 0 {
					resp.Logs.StepsLog = append(resp.Logs.StepsLog, &ViewStepLog{
						Step:   cursor.Step,
						Cursor: 1,
						Lines: []*ViewStepLogLine{
							{
								Index:   1,
								Message: ctx.Locale.TrString("actions.runs.expire_log_message"),
								// Timestamp doesn't mean anything when the log is expired.
								// Set it to the task's updated time since it's probably the time when the log has expired.
								Timestamp: float64(task.Updated.AsTime().UnixNano()) / float64(time.Second),
							},
						},
						Started: int64(step.Started),
					})
				}
				continue
			}

			logLines := make([]*ViewStepLogLine, 0) // marshal to '[]' instead of 'null' in json

			index := step.LogIndex + cursor.Cursor
			validCursor := cursor.Cursor >= 0 &&
				// !(cursor.Cursor < step.LogLength) when the frontend tries to fetch next line before it's ready.
				// So return the same cursor and empty lines to let the frontend retry.
				cursor.Cursor < step.LogLength &&
				// !(index < task.LogIndexes[index]) when task data is older than step data.
				// It can be fixed by making sure write/read tasks and steps in the same transaction,
				// but it's easier to just treat it as fetching the next line before it's ready.
				index < int64(len(task.LogIndexes))

			if validCursor {
				length := step.LogLength - cursor.Cursor
				offset := task.LogIndexes[index]
				logRows, err := actions.ReadLogs(ctx, task.LogInStorage, task.LogFilename, offset, length)
				if err != nil {
					ctx.Error(http.StatusInternalServerError, err.Error())
					return nil
				}

				for i, row := range logRows {
					logLines = append(logLines, &ViewStepLogLine{
						Index:     cursor.Cursor + int64(i) + 1, // start at 1
						Message:   row.Content,
						Timestamp: float64(row.Time.AsTime().UnixNano()) / float64(time.Second),
					})
				}
			}

			resp.Logs.StepsLog = append(resp.Logs.StepsLog, &ViewStepLog{
				Step:    cursor.Step,
				Cursor:  cursor.Cursor + int64(len(logLines)),
				Lines:   logLines,
				Started: int64(step.Started),
			})
		}
	}

	return resp
}

// When used with the JS `linkAction` handler (typically a <button> with class="link-action" and a data-url), will cause
// the browser to redirect to the target page.
type redirectObject struct {
	Redirect string `json:"redirect"`
}

// Rerun will rerun jobs in the given run
// If jobIndexStr is a blank string, it means rerun all jobs
func Rerun(ctx *context_module.Context) {
	runIndex := ctx.ParamsInt64("run")
	jobIndexStr := ctx.Params("job")
	var jobIndex int64
	if jobIndexStr != "" {
		jobIndex, _ = strconv.ParseInt(jobIndexStr, 10, 64)
	}

	run, err := actions_model.GetRunByIndex(ctx, ctx.Repo.Repository.ID, runIndex)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, err.Error())
		return
	}

	// can not rerun job when workflow is disabled
	cfgUnit := ctx.Repo.Repository.MustGetUnit(ctx, unit.TypeActions)
	cfg := cfgUnit.ActionsConfig()
	if cfg.IsWorkflowDisabled(run.WorkflowID) {
		ctx.JSONError(ctx.Locale.Tr("actions.workflow.disabled"))
		return
	}

	// reset run's start and stop time when it is done
	if run.Status.IsDone() {
		run.PreviousDuration = run.Duration()
		run.Started = 0
		run.Stopped = 0
		if err := actions_service.UpdateRun(ctx, run, "started", "stopped", "previous_duration"); err != nil {
			ctx.Error(http.StatusInternalServerError, err.Error())
			return
		}
	}

	job, jobs := getRunJobs(ctx, runIndex, jobIndex)
	if ctx.Written() {
		return
	}

	if jobIndexStr == "" { // rerun all jobs
		var redirectURL string
		for _, j := range jobs {
			// if the job has needs, it should be set to "blocked" status to wait for other jobs
			shouldBlock := len(j.Needs) > 0
			if err := rerunJob(ctx, j, shouldBlock); err != nil {
				ctx.Error(http.StatusInternalServerError, err.Error())
				return
			}
			if redirectURL == "" {
				// ActionRunJob's `Attempt` field won't be updated to reflect the rerun until the job is picked by a
				// runner. But we need to redirect the user somewhere; if they stay on the current attempt then the
				// rerun's logs won't appear. So, we redirect to the upcoming new attempt and then we'll handle the
				// weirdness in the UI if the attempt doesn't exist yet.
				j.Attempt++ // note: this is intentionally not persisted
				redirectURL, err = j.HTMLURL(ctx)
				if err != nil {
					ctx.Error(http.StatusInternalServerError, err.Error())
					return
				}
			}
		}

		if redirectURL != "" {
			ctx.JSON(http.StatusOK, &redirectObject{Redirect: redirectURL})
		} else {
			ctx.Error(http.StatusInternalServerError, "unable to determine redirectURL for job rerun")
		}
		return
	}

	rerunJobs := actions_service.GetAllRerunJobs(job, jobs)

	var redirectURL string
	for _, j := range rerunJobs {
		// jobs other than the specified one should be set to "blocked" status
		shouldBlock := j.JobID != job.JobID
		if err := rerunJob(ctx, j, shouldBlock); err != nil {
			ctx.Error(http.StatusInternalServerError, err.Error())
			return
		}
		if j.JobID == job.JobID {
			// see earlier comment about redirectURL, applicable here as well
			j.Attempt++ // note: this is intentionally not persisted
			redirectURL, err = j.HTMLURL(ctx)
			if err != nil {
				ctx.Error(http.StatusInternalServerError, err.Error())
				return
			}
		}
	}

	if redirectURL != "" {
		ctx.JSON(http.StatusOK, &redirectObject{Redirect: redirectURL})
	} else {
		ctx.Error(http.StatusInternalServerError, "unable to determine redirectURL for job rerun")
	}
}

func rerunJob(ctx *context_module.Context, job *actions_model.ActionRunJob, shouldBlock bool) error {
	status := job.Status
	if !status.IsDone() {
		return nil
	}

	job.TaskID = 0
	job.Status = actions_model.StatusWaiting
	if shouldBlock {
		job.Status = actions_model.StatusBlocked
	}
	job.Started = 0
	job.Stopped = 0

	if err := db.WithTx(ctx, func(ctx context.Context) error {
		_, err := actions_service.UpdateRunJob(ctx, job, builder.Eq{"status": status}, "task_id", "status", "started", "stopped")
		return err
	}); err != nil {
		return err
	}

	actions_service.CreateCommitStatus(ctx, job)
	return nil
}

func Logs(ctx *context_module.Context) {
	runIndex := ctx.ParamsInt64("run")
	jobIndex := ctx.ParamsInt64("job")
	attemptNumber := ctx.ParamsInt64("attempt")

	job, _ := getRunJobs(ctx, runIndex, jobIndex)
	if ctx.Written() {
		return
	}
	if job.TaskID == 0 {
		ctx.Error(http.StatusNotFound, "job is not started")
		return
	}

	err := job.LoadRun(ctx)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, err.Error())
		return
	}

	task, err := actions_model.GetTaskByJobAttempt(ctx, job.ID, attemptNumber)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, err.Error())
		return
	}
	if task.LogExpired {
		ctx.Error(http.StatusNotFound, "logs have been cleaned up")
		return
	}

	reader, err := actions.OpenLogs(ctx, task.LogInStorage, task.LogFilename)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, err.Error())
		return
	}
	defer reader.Close()

	workflowName := job.Run.WorkflowID
	if p := strings.Index(workflowName, "."); p > 0 {
		workflowName = workflowName[0:p]
	}
	ctx.ServeContent(reader, &context_module.ServeHeaderOptions{
		Filename:           fmt.Sprintf("%v-%v-%v.log", workflowName, job.Name, task.ID),
		ContentLength:      &task.LogSize,
		ContentType:        "text/plain",
		ContentTypeCharset: "utf-8",
		Disposition:        "attachment",
	})
}

func Cancel(ctx *context_module.Context) {
	runIndex := ctx.ParamsInt64("run")

	_, jobs := getRunJobs(ctx, runIndex, -1)
	if ctx.Written() {
		return
	}

	if err := db.WithTx(ctx, func(ctx context.Context) error {
		for _, job := range jobs {
			status := job.Status
			if status.IsDone() {
				continue
			}
			if job.TaskID == 0 {
				job.Status = actions_model.StatusCancelled
				job.Stopped = timeutil.TimeStampNow()
				n, err := actions_service.UpdateRunJob(ctx, job, builder.Eq{"task_id": 0}, "status", "stopped")
				if err != nil {
					return err
				}
				if n == 0 {
					return errors.New("job has changed, try again")
				}
				continue
			}
			if err := actions_service.StopTask(ctx, job.TaskID, actions_model.StatusCancelled); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		ctx.Error(http.StatusInternalServerError, err.Error())
		return
	}

	actions_service.CreateCommitStatus(ctx, jobs...)

	ctx.JSON(http.StatusOK, struct{}{})
}

func Approve(ctx *context_module.Context) {
	runIndex := ctx.ParamsInt64("run")

	current, jobs := getRunJobs(ctx, runIndex, -1)
	if ctx.Written() {
		return
	}
	run := current.Run
	doer := ctx.Doer

	if err := db.WithTx(ctx, func(ctx context.Context) error {
		run.NeedApproval = false
		run.ApprovedBy = doer.ID
		if err := actions_service.UpdateRun(ctx, run, "need_approval", "approved_by"); err != nil {
			return err
		}
		for _, job := range jobs {
			if len(job.Needs) == 0 && job.Status.IsBlocked() {
				job.Status = actions_model.StatusWaiting
				_, err := actions_service.UpdateRunJob(ctx, job, nil, "status")
				if err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		ctx.Error(http.StatusInternalServerError, err.Error())
		return
	}

	actions_service.CreateCommitStatus(ctx, jobs...)

	ctx.JSON(http.StatusOK, struct{}{})
}

// getRunJobs gets the jobs of runIndex, and returns jobs[jobIndex], jobs.
// Any error will be written to the ctx.
// It never returns a nil job of an empty jobs, if the jobIndex is out of range, it will be treated as 0.
func getRunJobs(ctx *context_module.Context, runIndex, jobIndex int64) (*actions_model.ActionRunJob, []*actions_model.ActionRunJob) {
	run, err := actions_model.GetRunByIndex(ctx, ctx.Repo.Repository.ID, runIndex)
	if err != nil {
		if errors.Is(err, util.ErrNotExist) {
			ctx.Error(http.StatusNotFound, err.Error())
			return nil, nil
		}
		ctx.Error(http.StatusInternalServerError, err.Error())
		return nil, nil
	}
	run.Repo = ctx.Repo.Repository

	jobs, err := actions_model.GetRunJobsByRunID(ctx, run.ID)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, err.Error())
		return nil, nil
	}
	if len(jobs) == 0 {
		ctx.Error(http.StatusNotFound)
		return nil, nil
	}

	for _, v := range jobs {
		v.Run = run
	}

	if jobIndex >= 0 && jobIndex < int64(len(jobs)) {
		return jobs[jobIndex], jobs
	}
	return jobs[0], jobs
}

type ArtifactsViewResponse struct {
	Artifacts []*ArtifactsViewItem `json:"artifacts"`
}

type ArtifactsViewItem struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Status string `json:"status"`
}

func ArtifactsView(ctx *context_module.Context) {
	runIndex := ctx.ParamsInt64("run")
	artifactsResponse := getArtifactsViewResponse(ctx, runIndex)
	if ctx.Written() {
		return
	}
	ctx.JSON(http.StatusOK, artifactsResponse)
}

func getArtifactsViewResponse(ctx *context_module.Context, runIndex int64) *ArtifactsViewResponse {
	run, err := actions_model.GetRunByIndex(ctx, ctx.Repo.Repository.ID, runIndex)
	if err != nil {
		if errors.Is(err, util.ErrNotExist) {
			ctx.Error(http.StatusNotFound, err.Error())
			return nil
		}
		ctx.Error(http.StatusInternalServerError, err.Error())
		return nil
	}
	artifacts, err := actions_model.ListUploadedArtifactsMeta(ctx, run.ID)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, err.Error())
		return nil
	}
	artifactsResponse := ArtifactsViewResponse{
		Artifacts: make([]*ArtifactsViewItem, 0, len(artifacts)),
	}
	for _, art := range artifacts {
		status := "completed"
		if art.Status == actions_model.ArtifactStatusExpired {
			status = "expired"
		}
		artifactsResponse.Artifacts = append(artifactsResponse.Artifacts, &ArtifactsViewItem{
			Name:   art.ArtifactName,
			Size:   art.FileSize,
			Status: status,
		})
	}
	return &artifactsResponse
}

func ArtifactsDeleteView(ctx *context_module.Context) {
	runIndex := ctx.ParamsInt64("run")
	artifactName := ctx.Params("artifact_name")

	run, err := actions_model.GetRunByIndex(ctx, ctx.Repo.Repository.ID, runIndex)
	if err != nil {
		ctx.NotFoundOrServerError("GetRunByIndex", func(err error) bool {
			return errors.Is(err, util.ErrNotExist)
		}, err)
		return
	}
	if err = actions_model.SetArtifactNeedDelete(ctx, run.ID, artifactName); err != nil {
		ctx.Error(http.StatusInternalServerError, err.Error())
		return
	}
	ctx.JSON(http.StatusOK, struct{}{})
}

func getRunByID(ctx *context_module.Context, runID int64) *actions_model.ActionRun {
	if runID == 0 {
		log.Debug("Requested runID is zero.")
		ctx.Error(http.StatusNotFound, "zero is not a valid run ID")
		return nil
	}

	run, has, err := actions_model.GetRunByIDWithHas(ctx, runID)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, err.Error())
		return nil
	}
	if !has {
		log.Debug("Requested runID[%d] not found.", runID)
		ctx.Error(http.StatusNotFound, fmt.Sprintf("no such run %d", runID))
		return nil
	}
	if run.RepoID != ctx.Repo.Repository.ID {
		log.Debug("Requested runID[%d] does not belong to repo[%-v].", runID, ctx.Repo.Repository)
		ctx.Error(http.StatusNotFound, "no such run")
		return nil
	}
	return run
}

func artifactsFind(ctx *context_module.Context, opts actions_model.FindArtifactsOptions) []*actions_model.ActionArtifact {
	artifacts, err := db.Find[actions_model.ActionArtifact](ctx, opts)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, err.Error())
		return nil
	}
	if len(artifacts) == 0 {
		return nil
	}
	return artifacts
}

func artifactsFindByNameOrID(ctx *context_module.Context, runID int64, nameOrID string) []*actions_model.ActionArtifact {
	artifacts := artifactsFind(ctx, actions_model.FindArtifactsOptions{
		RunID:        runID,
		ArtifactName: nameOrID,
	})
	if ctx.Written() {
		return nil
	}
	// if lookup by name found nothing, maybe it is an ID
	if len(artifacts) == 0 {
		id, err := strconv.ParseInt(nameOrID, 10, 64)
		if err != nil || id == 0 {
			ctx.Error(http.StatusNotFound, fmt.Sprintf("runID %d: artifact name not found: %v", runID, nameOrID))
			return nil
		}
		artifacts = artifactsFind(ctx, actions_model.FindArtifactsOptions{
			RunID: runID,
			ID:    id,
		})
		if ctx.Written() {
			return nil
		}
		if len(artifacts) == 0 {
			ctx.Error(http.StatusNotFound, fmt.Sprintf("runID %d: artifact ID not found: %v", runID, nameOrID))
			return nil
		}
	}
	return artifacts
}

func ArtifactsDownloadView(ctx *context_module.Context) {
	run := getRunByID(ctx, ctx.ParamsInt64("run"))
	if ctx.Written() {
		return
	}
	artifactNameOrID := ctx.Params("artifact_name_or_id")

	artifacts := artifactsFindByNameOrID(ctx, run.ID, artifactNameOrID)
	if ctx.Written() {
		return
	}

	// if artifacts status is not uploaded-confirmed, treat it as not found
	for _, art := range artifacts {
		if art.Status != int64(actions_model.ArtifactStatusUploadConfirmed) {
			ctx.Error(http.StatusNotFound, "artifact not found")
			return
		}
	}

	// Artifacts using the v4 backend are stored as a single combined zip file per artifact on the backend
	// The v4 backend ensures ContentEncoding is set to "application/zip", which is not the case for the old backend
	if len(artifacts) == 1 && artifacts[0].ArtifactName+".zip" == artifacts[0].ArtifactPath && artifacts[0].ContentEncoding == "application/zip" {
		art := artifacts[0]
		if setting.Actions.ArtifactStorage.MinioConfig.ServeDirect {
			u, err := storage.ActionsArtifacts.URL(art.StoragePath, art.ArtifactPath, nil)

			if u != nil && err == nil {
				ctx.Redirect(u.String())
				return
			}
		}
		f, err := storage.ActionsArtifacts.Open(art.StoragePath)
		if err != nil {
			ctx.Error(http.StatusInternalServerError, err.Error())
			return
		}
		common.ServeContentByReadSeeker(ctx.Base, artifacts[0].ArtifactName+".zip", util.ToPointer(art.UpdatedUnix.AsTime()), f)
		return
	}

	// Artifacts using the v1-v3 backend are stored as multiple individual files per artifact on the backend
	// Those need to be zipped for download
	artifactName := artifacts[0].ArtifactName

	ctx.Resp.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s.zip; filename*=UTF-8''%s.zip", url.PathEscape(artifactName), artifactName))
	writer := zip.NewWriter(ctx.Resp)
	defer writer.Close()
	for _, art := range artifacts {
		f, err := storage.ActionsArtifacts.Open(art.StoragePath)
		if err != nil {
			ctx.Error(http.StatusInternalServerError, err.Error())
			return
		}

		var r io.ReadCloser
		if art.ContentEncoding == "gzip" {
			r, err = gzip.NewReader(f)
			if err != nil {
				ctx.Error(http.StatusInternalServerError, err.Error())
				return
			}
		} else {
			r = f
		}
		defer r.Close()

		w, err := writer.Create(art.ArtifactPath)
		if err != nil {
			ctx.Error(http.StatusInternalServerError, err.Error())
			return
		}
		if _, err := io.Copy(w, r); err != nil {
			ctx.Error(http.StatusInternalServerError, err.Error())
			return
		}
	}
}

func DisableWorkflowFile(ctx *context_module.Context) {
	disableOrEnableWorkflowFile(ctx, false)
}

func EnableWorkflowFile(ctx *context_module.Context) {
	disableOrEnableWorkflowFile(ctx, true)
}

func disableOrEnableWorkflowFile(ctx *context_module.Context, isEnable bool) {
	workflow := ctx.FormString("workflow")
	if len(workflow) == 0 {
		ctx.ServerError("workflow", nil)
		return
	}

	cfgUnit := ctx.Repo.Repository.MustGetUnit(ctx, unit.TypeActions)
	cfg := cfgUnit.ActionsConfig()

	if isEnable {
		cfg.EnableWorkflow(workflow)
	} else {
		cfg.DisableWorkflow(workflow)
	}

	if err := repo_model.UpdateRepoUnit(ctx, cfgUnit); err != nil {
		ctx.ServerError("UpdateRepoUnit", err)
		return
	}

	if isEnable {
		ctx.Flash.Success(ctx.Tr("actions.workflow.enable_success", workflow))
	} else {
		ctx.Flash.Success(ctx.Tr("actions.workflow.disable_success", workflow))
	}

	redirectURL := fmt.Sprintf("%s/actions?workflow=%s&actor=%s&status=%s", ctx.Repo.RepoLink, url.QueryEscape(workflow),
		url.QueryEscape(ctx.FormString("actor")), url.QueryEscape(ctx.FormString("status")))
	ctx.JSONRedirect(redirectURL)
}
