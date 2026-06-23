package workflow

import (
	"context"
	"fmt"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type RecipeProjectProvider func(projectId string, recipeRef string) (*recipe.Recipe, error)

type SWFWorkflowControl struct {
	Engine                        jobworkflow.Engine
	Registry                      RecipeProjectProvider
	PreferRuntimeRecipeResolution bool
}

func (s *SWFWorkflowControl) GetWaitingTask(ctx context.Context, jobKey jobdb.JobKey) (workflowctl.TaskHandle, error) {
	e, err := s.Engine.GetWaitingTask(ctx, jobKey)
	if err != nil {
		return nil, err
	}
	return e, nil
}

func (s *SWFWorkflowControl) ListJobs(ctx context.Context, request jobdb.ListJobsRequest) (jobs []workflowctl.JobItem, nextPage string, err error) {
	resp, err := s.Engine.ListJobs(ctx, request)
	if err != nil {
		return nil, "", err
	}

	jobs = make([]workflowctl.JobItem, len(resp.Jobs))
	for i, j := range resp.Jobs {
		jobs[i] = workflowctl.JobItem{
			JobSummary: j,
			TaskData: &taskDataGetter{
				engine:  s.Engine,
				jobKey:  j.JobKey,
				ordinal: j.TaskWaitInput,
			},
		}
	}

	return jobs, resp.NextPageToken, nil
}

func (s *SWFWorkflowControl) JobResult(ctx context.Context, key jobdb.JobKey) (jobdb.JobData, error) {
	// Use the SWF helper to return standardized errors (not finished / cancelled / failed)
	// and to construct a JobData with lazy artifacts.
	run, err := s.Engine.GetJobRun(ctx, jobdb.GetJobRunRequest{
		JobKey:           key,
		IncludeOutputs:   true,
		IncludeArtifacts: true,
	})
	if err != nil {
		return nil, err
	}
	return run.GetOutput(s.Engine, key.TenantId)
}

func (s *SWFWorkflowControl) InspectJob(ctx context.Context, key jobdb.JobKey) (workflowctl.JobInspection, error) {
	run, err := s.Engine.GetJobRun(ctx, jobdb.GetJobRunRequest{
		JobKey:           key,
		IncludeOutputs:   true,
		IncludeArtifacts: true,
	})
	if err != nil {
		return workflowctl.JobInspection{}, err
	}

	inspection := workflowctl.JobInspection{
		JobKey:    key,
		Status:    normalizeInspectionStatus(run.Job.Status),
		Terminal:  isTerminalInspectionStatus(run.Job.Status),
		StartedAt: run.Job.CreatedAt,
	}
	if !run.Start.CreatedAt.IsZero() {
		inspection.StartedAt = run.Start.CreatedAt
	}
	if run.Job.ArchivedAt != nil {
		inspection.FinishedAt = *run.Job.ArchivedAt
	}

	if len(run.Attempts) == 0 {
		return inspection, nil
	}

	latest := latestInspectionAttempt(run.Attempts)
	if !latest.CreatedAt.IsZero() {
		inspection.StartedAt = latest.CreatedAt
	}
	if latest.Output != nil {
		output, err := taskIOToJobData(latest.Output, s.Engine, key.TenantId, key, latest.Ordinal)
		if err != nil {
			return workflowctl.JobInspection{}, err
		}
		inspection.Output = output
	}

	if latest.Outcome.Status == jobdb.TaskOutcomeStatusFailed || latest.Outcome.Error != nil {
		inspection.Terminal = true
		inspection.Status = "failed"
		inspection.FailureKind = failureKindFromTaskError(latest.Outcome.Error)
		inspection.FailureMessage = failureMessageFromTaskError(latest.Outcome.Error)
		if inspection.FailureKind == "timeout" {
			inspection.Status = "failed"
		}
		return inspection, nil
	}

	if run.Job.Status == jobdb.JobStatusCompleted {
		inspection.Terminal = true
		inspection.Status = "completed"
		inspection.FailureKind = "none"
	}
	return inspection, nil
}

func (s *SWFWorkflowControl) CompleteTask(ctx context.Context, jobKey jobdb.JobKey, taskOrdinal int64, hash string, outType any) error {
	handle, err := s.Engine.GetWaitingTask(ctx, jobKey)
	if err != nil {
		return err
	}
	if handle.TaskOrdinalToComplete() != taskOrdinal {
		return fmt.Errorf("unexpected task ordinal: %d (actual pending: %d)", taskOrdinal, handle.TaskOrdinalToComplete())
	}

	out := ops.ActivityInvocationOutputRaw{
		GitResult: contextual.GitCommitContext{
			PersistHash: hash,
			ParentHash:  hash,
		},
		Output: outType,
	}

	env, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, out)
	if err != nil {
		return err
	}
	outData, err := jobdb.NewTaskData(env)
	if err != nil {
		return err
	}
	return handle.Finish(ctx, outData)
}

func (s *SWFWorkflowControl) StartJob(ctx context.Context, req workflowctl.StartJob) (jobdb.JobKey, error) {
	if s.PreferRuntimeRecipeResolution || s.Registry == nil {
		return starter.StartRecipeJobWithOptions(ctx, req, s.Engine, starter.StartRecipeJobOptions{
			JobID: req.JobID,
		})
	}

	r, err := s.Registry(req.TenantId, req.RecipeName)
	if err != nil {
		return jobdb.JobKey{}, err
	}

	return starter.StartRecipeJobWithOptions(ctx, req, s.Engine, starter.StartRecipeJobOptions{
		JobID: req.JobID,
	}, *r)
}

func (s *SWFWorkflowControl) Cancel(ctx context.Context, jobKey jobdb.JobKey) error {
	return s.Engine.CancelJob(ctx, jobdb.CancelJob{JobKey: jobKey})
}

func (s *SWFWorkflowControl) GetArtifactLazy(ctx context.Context, tenantId string, key jobdb.ArtifactKey) jobdb.Artifact {
	return key.ToLazyArtifact(s.Engine, tenantId)
}

type taskDataGetter struct {
	loaded  bool
	engine  jobworkflow.Engine
	jobKey  jobdb.JobKey
	ordinal *int64
	data    jobdb.TaskData
}

func (t *taskDataGetter) checkLoad() error {
	if t.loaded {
		return nil
	}
	if t.ordinal == nil {
		return fmt.Errorf("ordinal is required")
	}
	handle, err := t.engine.GetWaitingTask(context.Background(), t.jobKey)
	if err != nil {
		return err
	}

	targetCompletion := *t.ordinal + 1
	if handle.TaskOrdinalToComplete() != targetCompletion {
		return fmt.Errorf("unexpected task ordinal: %d (actual pending: %d)", targetCompletion, handle.TaskOrdinalToComplete())
	}

	data, err := handle.Data()
	if err != nil {
		return err
	}
	t.data = data
	t.loaded = true
	return nil
}

func (t *taskDataGetter) GetData() (jobdb.Data, error) {
	if err := t.checkLoad(); err != nil {
		return nil, err
	}
	return t.data.GetData()
}

func (t *taskDataGetter) GetDataOrPanic() jobdb.Data {
	d, err := t.GetData()
	if err != nil {
		panic(err)
	}
	return d
}

func (t *taskDataGetter) GetArtifacts() ([]jobdb.Artifact, error) {
	if err := t.checkLoad(); err != nil {
		return nil, err
	}
	return t.data.GetArtifacts()
}

var _ jobdb.TaskData = &taskDataGetter{}

var _ workflowctl.WorkflowControl = &SWFWorkflowControl{}
var _ workflowctl.JobInspector = &SWFWorkflowControl{}

func normalizeInspectionStatus(status jobdb.JobStatus) string {
	switch status {
	case jobdb.JobStatusCompleted:
		return "completed"
	case jobdb.JobStatusCancelled:
		return "cancelled"
	case jobdb.JobStatusExpired:
		return "timed_out"
	case jobdb.JobStatusReady, jobdb.JobStatusPendingJobs, jobdb.JobStatusAwaitingFuture, jobdb.JobStatusActive, jobdb.JobStatusCrashConcern:
		return "running"
	default:
		return "unknown"
	}
}

func isTerminalInspectionStatus(status jobdb.JobStatus) bool {
	switch status {
	case jobdb.JobStatusCompleted, jobdb.JobStatusCancelled, jobdb.JobStatusExpired:
		return true
	default:
		return false
	}
}

func latestInspectionAttempt(attempts []jobdb.JobAttempt) jobdb.JobAttempt {
	best := attempts[0]
	for i := 1; i < len(attempts); i++ {
		attempt := attempts[i]
		if attempt.Attempt > best.Attempt || (attempt.Attempt == best.Attempt && attempt.Ordinal > best.Ordinal) {
			best = attempt
		}
	}
	return best
}

func taskIOToJobData(io *jobdb.TaskIO, engine jobworkflow.Engine, tenantID string, jobKey jobdb.JobKey, ordinal int64) (jobdb.JobData, error) {
	if io == nil {
		return nil, nil
	}
	artifacts := make([]jobdb.Artifact, 0, len(io.Artifacts))
	for _, info := range io.Artifacts {
		key := jobdb.ArtifactKey{
			JobId:       jobKey.JobId,
			TaskOrdinal: ordinal,
			Name:        info.Name,
			SizeBytes:   info.SizeBytes,
		}
		if info.Key != nil {
			key = *info.Key
		}
		if err := key.Validate(); err != nil {
			return nil, fmt.Errorf("invalid inspected artifact key: %w", err)
		}
		artifacts = append(artifacts, key.ToLazyArtifact(engine, tenantID))
	}
	return &jobdb.SimpleTaskData{
		Data:      append([]byte(nil), io.Data...),
		Artifacts: artifacts,
	}, nil
}

func failureKindFromTaskError(taskErr *jobdb.TaskError) string {
	if taskErr == nil {
		return "unknown"
	}
	switch taskErr.Kind {
	case jobdb.TaskErrorKindApp:
		return "task_error"
	case jobdb.TaskErrorKindSystem:
		return "system_error"
	case jobdb.TaskErrorKindTimeout:
		return "timeout"
	default:
		return "unknown"
	}
}

func failureMessageFromTaskError(taskErr *jobdb.TaskError) string {
	if taskErr == nil {
		return ""
	}
	return taskErr.Message
}
