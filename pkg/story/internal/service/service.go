package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/starter"
	workflowapi "github.com/colony-2/c2j/pkg/story/api"
	"github.com/colony-2/c2j/pkg/story/internal/model"
	jobstory "github.com/colony-2/c2j/pkg/story/internal/story"
	storylive "github.com/colony-2/c2j/pkg/story/live"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/colony-2/strata-go/pkg/client"
	"github.com/colony-2/strata-go/pkg/client/pagination"
	"github.com/colony-2/strata-go/pkg/client/story"
)

var (
	ErrNotFound             = errors.New("workflow: not found")
	ErrWorkflowNotInProject = errors.New("workflow: not in project")
	ErrInvalidProject       = errors.New("workflow: invalid project")
	ErrInvalidCell          = errors.New("workflow: invalid cell")
	ErrRecipeNotFound       = errors.New("workflow: recipe not found")
	ErrEngineUnavailable    = errors.New("workflow: engine unavailable")
	ErrOutcomePending       = errors.New("workflow: outcome not available yet")
	ErrJobRunStoryMismatch  = errors.New("workflow: job run story replay mismatch")
)

type Config struct {
	Engine             jobworkflow.Engine
	SchemaRegistry     jobdb.JobSchemaRegistry
	Strata             *client.Client
	Cells              workflowapi.CellService
	Projects           workflowapi.ProjectService
	Recipes            RecipeProvider
	RootSourceResolver compiler.RecipeSourceResolver
	CELOptionsProvider template.CELOptionsProvider
	Logger             *slog.Logger
}

type Service struct {
	engine       jobworkflow.Engine
	registry     jobdb.JobSchemaRegistry
	strata       *client.Client
	cells        workflowapi.CellService
	projects     workflowapi.ProjectService
	recipes      RecipeProvider
	rootResolver compiler.RecipeSourceResolver
	celProvider  template.CELOptionsProvider
	logger       *slog.Logger
}

type RecipeProvider func(projectID string, recipeRef string) (*recipe.Recipe, error)

type recipeRestartSubmitter interface {
	SubmitRestartJob(context.Context, jobdb.SubmitRestartJob) (jobdb.JobKey, error)
	GetJob(context.Context, jobdb.JobKey) (jobdb.JobInfo, error)
}

func New(cfg Config) (*Service, error) {
	if cfg.Engine == nil {
		return nil, errors.New("workflow service: engine is required")
	}
	registry := cfg.SchemaRegistry
	if registry == nil {
		if engineRegistry, ok := cfg.Engine.(jobdb.JobSchemaRegistry); ok {
			registry = engineRegistry
		}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		engine:       cfg.Engine,
		registry:     registry,
		strata:       cfg.Strata,
		cells:        cfg.Cells,
		projects:     cfg.Projects,
		recipes:      cfg.Recipes,
		rootResolver: cfg.RootSourceResolver,
		celProvider:  cfg.CELOptionsProvider,
		logger:       secrets.MaskLogger(logger),
	}, nil
}

type chapterEnvelope struct {
	Meta        chapterMeta     `json:"meta"`
	PayloadKind string          `json:"payload_kind"`
	Payload     json.RawMessage `json:"payload"`
}

type chapterMeta struct {
	Version       int                   `json:"version"`
	Ordinal       int64                 `json:"ordinal"`
	TaskType      string                `json:"task_type"`
	WorkerID      string                `json:"worker_id"`
	CreatedAt     time.Time             `json:"created_at"`
	InputHash     string                `json:"input_hash"`
	Input         json.RawMessage       `json:"input,omitempty"`
	Attempt       int                   `json:"attempt,omitempty"`
	MaxAttempts   int                   `json:"max_attempts,omitempty"`
	NextAttemptAt *time.Time            `json:"next_attempt_at,omitempty"`
	BackoffMillis int64                 `json:"backoff_ms,omitempty"`
	Retryable     *bool                 `json:"retryable,omitempty"`
	InputRef      *jobdb.InputReference `json:"input_ref,omitempty"`
	RunPolicy     *jobdb.RunPolicy      `json:"run_policy,omitempty"`
}

func (s *Service) ListWorkflows(ctx context.Context, req model.ListWorkflowsRequest) ([]model.WorkflowSummary, error) {
	s.logger.Debug("ListWorkflows: request received", "component", "story",
		"project_id", req.ProjectID,
		"limit", req.Limit,
		"offset", req.Offset,
		"statuses", req.Statuses,
		"cell_id", req.CellID)

	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := req.Offset
	if offset < 0 {
		offset = 0
	}

	target := limit + offset
	pageSize := target
	if pageSize <= 0 {
		pageSize = 1
	}
	if pageSize > jobdb.MaxListJobsPageSize {
		pageSize = jobdb.MaxListJobsPageSize
	}

	summaries := make([]model.WorkflowSummary, 0, target)
	pageToken := ""
	jobStatuses := workflowStatusesToJobStatuses(req.Statuses)

	metaFilter := jobdb.Metadata()
	metaFilterActive := false
	if req.CellID != nil && strings.TrimSpace(*req.CellID) != "" {
		cellID := strings.TrimSpace(*req.CellID)
		field := starter.MetaFieldCellID
		value := any(cellID)
		if s.cells != nil {
			cellRecord, err := s.cells.GetCell(ctx, cellID)
			if err != nil || cellRecord == nil || cellRecord.ProjectID != req.ProjectID {
				return []model.WorkflowSummary{}, nil
			}
			field = starter.MetaFieldCellName
			value = cellRecord.Name
		}
		var err error
		metaFilter, err = metaFilter.EqualFilter(field, value)
		if err != nil {
			return nil, err
		}
		metaFilterActive = true
	}

	for {
		listReq := jobdb.ListJobsRequest{
			TenantIds:     []string{req.ProjectID},
			Statuses:      jobStatuses,
			Stores:        []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived},
			JobTypes:      []string{starter.RecipeJobType},
			CreatedAfter:  req.Since,
			CreatedBefore: req.Until,
			PageSize:      pageSize,
			PageToken:     pageToken,
		}
		if metaFilterActive {
			listReq.MetadataFilter = metaFilter
		}

		resp, err := s.engine.ListJobs(ctx, listReq)
		if err != nil {
			return nil, err
		}
		s.logger.Debug("ListWorkflows: engine returned jobs", "component", "story",
			"job_count", len(resp.Jobs),
			"project_id", req.ProjectID,
			"statuses", jobStatuses)
		if len(resp.Jobs) == 0 {
			break
		}
		for i, job := range resp.Jobs {
			s.logger.Debug("ListWorkflows: processing job", "component", "story",
				"index", i,
				"job_id", job.JobKey.JobId,
				"tenant_id", job.JobKey.TenantId,
				"status", job.Status,
				"created_at", job.CreatedAt,
				"archived_at", job.ArchivedAt)
			summary, ok, err := s.buildSummary(ctx, req.ProjectID, job)
			if err != nil {
				return nil, err
			}
			if !ok {
				s.logger.Debug("ListWorkflows: job skipped by buildSummary", "component", "story",
					"job_id", job.JobKey.JobId)
				continue
			}
			s.logger.Debug("ListWorkflows: job included", "component", "story",
				"job_id", job.JobKey.JobId,
				"status", summary.Status)
			if req.CellID != nil && strings.TrimSpace(*req.CellID) != "" {
				expected := strings.TrimSpace(*req.CellID)
				if summary.CellID == nil || strings.TrimSpace(*summary.CellID) != expected {
					continue
				}
			}
			summaries = append(summaries, summary)
			if len(summaries) >= target {
				break
			}
		}
		if len(summaries) >= target || resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}

	if offset >= len(summaries) {
		s.logger.Debug("ListWorkflows: offset beyond results", "component", "story",
			"project_id", req.ProjectID,
			"offset", offset,
			"total_summaries", len(summaries))
		return []model.WorkflowSummary{}, nil
	}
	end := offset + limit
	if end > len(summaries) {
		end = len(summaries)
	}
	result := summaries[offset:end]
	s.logger.Debug("ListWorkflows: returning results", "component", "story",
		"project_id", req.ProjectID,
		"result_count", len(result),
		"total_summaries", len(summaries))
	return result, nil
}

func (s *Service) StartWorkflow(ctx context.Context, req model.StartWorkflowRequest) (*model.WorkflowSummary, error) {
	if s.engine == nil {
		return nil, ErrEngineUnavailable
	}

	projectID := strings.TrimSpace(req.ProjectID)
	if projectID == "" {
		return nil, ErrInvalidProject
	}
	recipeName := strings.TrimSpace(req.RecipeName)
	if recipeName == "" {
		return nil, ErrRecipeNotFound
	}
	cellID := strings.TrimSpace(req.CellID)
	if cellID == "" {
		return nil, ErrInvalidCell
	}

	if s.projects == nil {
		return nil, ErrInvalidProject
	}
	projectRecord, err := s.projects.GetProject(ctx, projectID)
	if err != nil {
		if errors.Is(err, workflowapi.ErrProjectNotFound) {
			return nil, ErrInvalidProject
		}
		return nil, err
	}

	if s.cells == nil {
		return nil, ErrInvalidCell
	}
	cellRecord, err := s.cells.GetCell(ctx, cellID)
	if err != nil {
		if errors.Is(err, workflowapi.ErrCellNotFound) {
			return nil, ErrInvalidCell
		}
		return nil, err
	}
	if cellRecord.ProjectID != projectID {
		return nil, ErrInvalidCell
	}

	repo := strings.TrimSpace(projectRecord.GitRepoPath)
	if repo == "" {
		return nil, ErrInvalidProject
	}
	if cellRecord.GitRepoName != nil && strings.TrimSpace(*cellRecord.GitRepoName) != "" {
		repo = strings.TrimSpace(*cellRecord.GitRepoName)
	}

	gitRef := resolveGitRef(req.GitRef, cellRecord, projectRecord)
	submittedAt := time.Now().UTC()
	inputHash := hashInputs(req.Inputs)

	start := workflowctl.StartJob{
		TenantId:   projectID,
		RecipeName: recipeName,
		Inputs:     req.Inputs,
		JobContext: contextual.JobContext{
			Workflow: contextual.WorkflowContext{
				CellID:    cellID,
				CellName:  cellRecord.Name,
				ProjectId: projectID,
			},
			GitBase: contextual.GitBaseContext{
				BaseRepo: repo,
				BaseRef:  gitRef,
			},
		},
		GitRef:      gitRef,
		SubmittedAt: &submittedAt,
		InputHash:   inputHash,
	}

	var submitter any = jobdbschema.WorkflowEngine{Engine: s.engine, Registry: s.registry}
	if direct, ok := s.engine.(jobref.Submitter); ok {
		submitter = direct
	}
	jobKey, err := starter.StartRecipeJobWithOptions(ctx, start, submitter, starter.StartRecipeJobOptions{
		Dependencies: req.Dependencies,
	})
	if err != nil {
		return nil, err
	}

	cellIDCopy := string(cellRecord.ID)
	cellName := cellRecord.Name
	summary := &model.WorkflowSummary{
		WorkflowID:  jobKey.JobID,
		RunID:       jobKey.JobID,
		Status:      model.WorkflowStatusRunning,
		RecipeName:  recipeName,
		InputHash:   stringPtr(inputHash),
		SubmittedAt: &submittedAt,
		CellID:      &cellIDCopy,
		CellName:    &cellName,
		StartTime:   &submittedAt,
		CreatedAt:   submittedAt,
		Actor:       model.Actor{Type: model.ActorTypeUser},
	}

	return summary, nil
}

func (s *Service) GetWorkflow(ctx context.Context, req model.GetWorkflowRequest) (*model.WorkflowDetail, error) {
	if req.WorkflowID == "" {
		return nil, ErrNotFound
	}
	jobKey := jobdb.JobKey{TenantId: req.ProjectID, JobId: req.WorkflowID}

	s.logger.Debug("GetWorkflow: querying engine for job", "component", "story",
		"project_id", req.ProjectID,
		"workflow_id", req.WorkflowID)
	resp, err := s.engine.ListJobs(ctx, jobdb.ListJobsRequest{
		TenantIds: []string{req.ProjectID},
		JobKeys:   []jobdb.JobKey{jobKey},
		Stores:    []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived},
		PageSize:  1,
	})
	if err != nil {
		s.logger.Debug("GetWorkflow: engine query failed", "component", "story",
			"project_id", req.ProjectID,
			"workflow_id", req.WorkflowID,
			"error", err)
		return nil, err
	}
	if len(resp.Jobs) == 0 {
		s.logger.Debug("GetWorkflow: workflow not found in engine", "component", "story",
			"project_id", req.ProjectID,
			"workflow_id", req.WorkflowID)
		return nil, ErrNotFound
	}
	s.logger.Debug("GetWorkflow: found workflow in engine", "component", "story",
		"project_id", req.ProjectID,
		"workflow_id", req.WorkflowID,
		"status", resp.Jobs[0].Status)

	job := resp.Jobs[0]
	if job.JobKey.TenantId != req.ProjectID {
		return nil, ErrWorkflowNotInProject
	}

	meta, metaErr := jobMetadataFromRaw(job.Metadata)
	if metaErr != nil {
		s.logger.Debug("GetWorkflow: failed to parse job metadata", "component", "story",
			"workflow_id", req.WorkflowID,
			"error", metaErr)
	}

	recipeName := ""
	if meta != nil {
		recipeName = meta.RecipeName
	}

	status := mapWorkflowStatus(job.Status)
	createdAt := job.CreatedAt
	startTime := &createdAt
	closeTime := job.ArchivedAt

	detail := model.WorkflowDetail{
		WorkflowID: job.JobKey.JobId,
		RunID:      job.JobKey.JobId,
		Status:     status,
		RecipeName: recipeName,
		StartTime:  startTime,
		CloseTime:  closeTime,
		Actor:      actorFromJobMetadata(meta),
		CreatedAt:  createdAt,
	}

	cellID := ""
	cellName := ""
	if meta != nil {
		cellID = meta.CellID
		cellName = meta.CellName
	}
	if strings.TrimSpace(cellName) != "" {
		cellName = strings.TrimSpace(cellName)
		detail.CellName = &cellName
	}
	if strings.TrimSpace(cellID) != "" {
		cellID = strings.TrimSpace(cellID)
		detail.CellID = &cellID
	} else if detail.CellName != nil && s.cells != nil {
		if resolved, err := s.findCellID(ctx, req.ProjectID, *detail.CellName); err == nil {
			detail.CellID = resolved
		}
	}

	gitRef := ""
	if meta != nil {
		gitRef = meta.GitRef
	}
	if strings.TrimSpace(gitRef) != "" {
		gitRef = strings.TrimSpace(gitRef)
		detail.GitRef = &gitRef
	}

	if req.IncludeRawJobData {
		// Job input is distinct from mutable client payload and framework state.
		run, err := s.engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: job.JobKey, IncludeInputs: true})
		if err != nil {
			return nil, fmt.Errorf("load job input: %w", err)
		}
		if run.Start.Input != nil {
			detail.RawJobData = mapFromRaw(run.Start.Input.Data)
		}
	}

	chapters, err := s.loadChapters(ctx, job.JobKey)
	if err != nil {
		return nil, err
	}
	detail.Chapters = chapters

	// Populate artifact URLs
	for chapterIdx := range detail.Chapters {
		for artifactIdx := range detail.Chapters[chapterIdx].Artifacts {
			artifact := &detail.Chapters[chapterIdx].Artifacts[artifactIdx]

			// Build URL for this artifact using the artifact name
			url := fmt.Sprintf("/api/projects/%s/workflows/%s/chapters/%d/artifacts/%s",
				req.ProjectID,
				req.WorkflowID,
				chapterIdx,
				artifact.Name,
			)
			artifact.URL = &url
		}
	}

	return &detail, nil
}

func (s *Service) GetWorkflowOutcome(ctx context.Context, req model.GetWorkflowOutcomeRequest) (*model.WorkflowOutcome, error) {
	if s.engine == nil {
		return nil, ErrEngineUnavailable
	}

	projectID := strings.TrimSpace(req.ProjectID)
	jobID := strings.TrimSpace(req.JobID)
	if projectID == "" {
		return nil, ErrInvalidProject
	}
	if jobID == "" {
		return nil, ErrNotFound
	}

	jobKey := jobdb.JobKey{TenantId: projectID, JobId: jobID}
	run, err := s.engine.GetJobRun(ctx, jobdb.GetJobRunRequest{
		JobKey:           jobKey,
		IncludeOutputs:   true,
		IncludeArtifacts: true,
	})
	if err != nil {
		if errors.Is(err, jobdb.ErrJobNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	status := mapWorkflowStatus(run.Job.Status)
	// An unsuccessful attempt is not a terminal outcome while the job is
	// active, waiting, or scheduled to retry.
	if status == model.WorkflowStatusRunning {
		return nil, ErrOutcomePending
	}

	var attemptOrdinal *int64
	var output map[string]interface{}
	var errMsg *string

	latest := (*jobdb.JobAttempt)(nil)
	if len(run.Attempts) > 0 {
		latest = &run.Attempts[len(run.Attempts)-1]
	}

	// With the new SWF shape, job-level output/error is carried directly on the latest attempt.
	if latest != nil {
		hasOutput := latest.Output != nil && len(latest.Output.Data) > 0
		hasError := latest.Outcome.Error != nil || latest.Outcome.Status == jobdb.TaskOutcomeStatusFailed

		attemptOrdinal = &latest.Ordinal
		if hasOutput {
			if m := mapFromRaw(latest.Output.Data); m != nil {
				output = *m
			}
		}
		if latest.Outcome.Error != nil && latest.Outcome.Error.Message != "" {
			msg := latest.Outcome.Error.Message
			errMsg = &msg
		} else if latest.Outcome.Status == jobdb.TaskOutcomeStatusFailed {
			msg := "task failed"
			errMsg = &msg
		}
		// COMPLETED is JobDB's terminal lifecycle state for both success and
		// failure. Preserve cancellation/expiry statuses while reconciling it.
		if run.Job.Status == jobdb.JobStatusCompleted && hasError {
			status = model.WorkflowStatusFailed
		}
	}

	tasks, err := swfutil.ExecutionTasks(run)
	if err != nil {
		return nil, err
	}
	attempts := append([]jobdb.JobAttempt(nil), run.Attempts...)
	activityArtifacts := jobdb.JobAttempt{}
	for _, task := range tasks {
		activityArtifacts.Tasks = append(activityArtifacts.Tasks, task.TaskRun)
	}
	attempts = append(attempts, activityArtifacts)
	artifacts := aggregateArtifacts(attempts, projectID, jobID)

	return &model.WorkflowOutcome{
		JobID:          jobID,
		Status:         status,
		AttemptOrdinal: attemptOrdinal,
		Output:         output,
		Error:          errMsg,
		Artifacts:      artifacts,
	}, nil
}

func (s *Service) GetJobRunStory(ctx context.Context, req model.GetJobRunStoryRequest) (*model.JobRunStory, error) {
	if s.engine == nil {
		return nil, ErrEngineUnavailable
	}

	projectID := strings.TrimSpace(req.ProjectID)
	jobID := strings.TrimSpace(req.JobID)
	if projectID == "" {
		return nil, ErrInvalidProject
	}
	if jobID == "" {
		return nil, ErrNotFound
	}

	jobKey := jobdb.JobKey{TenantId: projectID, JobId: jobID}
	st, err := jobstory.BuildJobRunStory(ctx, s.engine, jobKey, s.celProvider, s.logger, s.rootResolver)
	if err != nil {
		if errors.Is(err, jobdb.ErrJobNotFound) {
			return nil, ErrNotFound
		}
		if errors.Is(err, jobdb.ErrWorkflowNotDeterministic) {
			return st, fmt.Errorf("%w: %w", ErrJobRunStoryMismatch, err)
		}
		return st, err
	}
	info, err := s.engine.GetJob(ctx, jobKey)
	if err != nil {
		return nil, err
	}
	st.GoalID = info.GoalID
	st.OtherGoalIDs = append([]string{}, info.OtherGoalIDs...)
	return st, nil
}

func latestAttemptTerminalError(run jobdb.GetJobRunResponse) (msg string, code string, ok bool) {
	if len(run.Attempts) == 0 {
		return "", "", false
	}
	latest := run.Attempts[len(run.Attempts)-1]
	if latest.Outcome.Error == nil {
		return "", "", false
	}
	msg = strings.TrimSpace(latest.Outcome.Error.Message)
	code = strings.TrimSpace(latest.Outcome.Error.Code)
	if msg == "" {
		return "", "", false
	}
	return msg, code, true
}

func latestAttemptOutcome(run jobdb.GetJobRunResponse) (jobdb.TaskOutcome, bool) {
	if len(run.Attempts) == 0 {
		return jobdb.TaskOutcome{}, false
	}
	return run.Attempts[len(run.Attempts)-1].Outcome, true
}

func isTimeoutOutcome(out jobdb.TaskOutcome) bool {
	if strings.EqualFold(strings.TrimSpace(out.PayloadKind), "Timeout") {
		return true
	}
	if out.Error == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(out.Error.Kind), "TIMEOUT") {
		return true
	}
	code := strings.ToLower(strings.TrimSpace(out.Error.Code))
	return strings.HasPrefix(code, "timeout")
}

func isTerminalJobStatus(st jobdb.JobStatus) bool {
	switch st {
	case jobdb.JobStatusCompleted, jobdb.JobStatusCancelled, jobdb.JobStatusExpired, jobdb.JobStatusCrashConcern:
		return true
	default:
		return false
	}
}

func shouldDebugDumpJobRunStory() bool {
	v := strings.TrimSpace(os.Getenv("C2_DEBUG_JOB_RUN_STORY_DUMPS"))
	if v == "" {
		return false
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func dumpSWFJobRunForLog(run jobdb.GetJobRunResponse) map[string]any {
	latest := (*jobdb.JobAttempt)(nil)
	if len(run.Attempts) > 0 {
		latest = &run.Attempts[len(run.Attempts)-1]
	}
	out := map[string]any{
		"job": map[string]any{
			"job_key":     run.Job.JobKey,
			"job_type":    run.Job.JobType,
			"status":      run.Job.Status,
			"created_at":  run.Job.CreatedAt,
			"archived_at": run.Job.ArchivedAt,
			"metadata":    string(run.Job.Metadata),
		},
		"start": map[string]any{
			"ordinal":    run.Start.Ordinal,
			"worker_id":  run.Start.WorkerID,
			"created_at": run.Start.CreatedAt,
			"input":      truncateTaskIOForLog(run.Start.Input),
		},
		"attempts": dumpJobAttemptsForLog(run.Attempts),
	}
	if latest != nil {
		out["result"] = map[string]any{
			"ordinal":    latest.Ordinal,
			"attempt":    latest.Attempt,
			"worker_id":  latest.WorkerID,
			"created_at": latest.CreatedAt,
			"outcome":    latest.Outcome,
			"output":     truncateTaskIOForLog(latest.Output),
		}
	}
	return out
}

func dumpTaskRunsForLog(tasks []jobdb.TaskRun) []any {
	out := make([]any, 0, len(tasks))
	for _, tr := range tasks {
		row := map[string]any{
			"task_run_id": tr.TaskRunID,
			"task_type":   tr.TaskType,
			"attempts":    dumpTaskAttemptsForLog(tr.Attempts),
		}
		out = append(out, row)
	}
	return out
}

func dumpTaskAttemptsForLog(attempts []jobdb.TaskAttempt) []any {
	out := make([]any, 0, len(attempts))
	for _, a := range attempts {
		row := map[string]any{
			"ordinal":         a.Ordinal,
			"attempt":         a.Attempt,
			"worker_id":       a.WorkerID,
			"created_at":      a.CreatedAt,
			"state":           a.State,
			"outcome":         a.Outcome,
			"input_hash":      a.InputHash,
			"input_ref":       a.InputRef,
			"run_policy":      a.RunPolicy,
			"retryable":       a.Retryable,
			"max_attempts":    a.MaxAttempts,
			"next_attempt_at": a.NextAttemptAt,
			"backoff_ms":      a.BackoffMillis,
			"input":           truncateTaskIOForLog(a.Input),
			"output":          truncateTaskIOForLog(a.Output),
		}
		out = append(out, row)
	}
	return out
}

func dumpJobAttemptsForLog(attempts []jobdb.JobAttempt) []any {
	out := make([]any, 0, len(attempts))
	for _, a := range attempts {
		row := map[string]any{
			"ordinal":    a.Ordinal,
			"attempt":    a.Attempt,
			"worker_id":  a.WorkerID,
			"created_at": a.CreatedAt,
			"input_ref":  a.InputRef,
			"outcome":    a.Outcome,
			"output":     truncateTaskIOForLog(a.Output),
			"task_count": len(a.Tasks),
			"tasks":      dumpTaskRunsForLog(a.Tasks),
		}
		out = append(out, row)
	}
	return out
}

func truncateTaskIOForLog(io *jobdb.TaskIO) map[string]any {
	if io == nil {
		return nil
	}
	arts := make([]map[string]any, 0, len(io.Artifacts))
	for _, a := range io.Artifacts {
		arts = append(arts, map[string]any{
			"name":       a.Name,
			"size_bytes": a.SizeBytes,
			"sha256":     a.Sha256,
			"key":        a.Key,
		})
	}
	return map[string]any{
		"data":            string(io.Data),
		"data_len_bytes":  len(io.Data),
		"artifact_count":  len(io.Artifacts),
		"artifacts_brief": arts,
	}
}

func strataStoryKey(jobKey jobdb.JobKey) story.Key {
	return story.Key{
		AnthologyID: jobKey.TenantId,
		StoryID:     jobKey.JobId,
	}
}

func (s *Service) dumpStrataChaptersForLog(ctx context.Context, jobKey jobdb.JobKey) ([]any, error) {
	if s.strata == nil {
		return nil, errors.New("strata unavailable")
	}
	st, err := s.strata.Story(ctx, strataStoryKey(jobKey))
	if err != nil {
		return nil, err
	}
	iter, err := st.Chapters(ctx, story.ChaptersOptions{PageSize: 500, Direction: story.DirectionForward})
	if err != nil {
		return nil, err
	}

	out := make([]any, 0, 128)
	for iter.HasNext() {
		chap, err := iter.Next(ctx)
		if errors.Is(err, pagination.ErrNoMoreItems) {
			break
		}
		if err != nil {
			return nil, err
		}
		body := chap.Body()
		entry := map[string]any{
			"ordinal":        chap.Ordinal(),
			"body_len":       len(body),
			"body":           string(body),
			"artifact_count": len(chap.Artifacts()),
		}
		var env chapterEnvelope
		if json.Unmarshal(body, &env) == nil {
			entry["meta_task_type"] = env.Meta.TaskType
			entry["meta_worker_id"] = env.Meta.WorkerID
			entry["meta_created_at"] = env.Meta.CreatedAt
			entry["meta_ordinal"] = env.Meta.Ordinal
			entry["meta_attempt"] = env.Meta.Attempt
			entry["payload_kind"] = env.PayloadKind
			entry["payload_len"] = len(env.Payload)
			entry["payload"] = string(env.Payload)
		}
		out = append(out, entry)
	}
	return out, nil
}

func storyStatusForLog(st *model.JobRunStory) string {
	if st == nil {
		return ""
	}
	return string(st.Status)
}

func storyRecipeIDForLog(st *model.JobRunStory) string {
	if st == nil {
		return ""
	}
	return st.Recipe.ID
}

func errorChainForLog(err error) []string {
	if err == nil {
		return nil
	}
	out := make([]string, 0, 6)
	seen := make(map[error]struct{}, 6)
	for err != nil {
		if _, ok := seen[err]; ok {
			break
		}
		seen[err] = struct{}{}
		out = append(out, err.Error())
		err = errors.Unwrap(err)
		if len(out) >= 6 {
			break
		}
	}
	return out
}

func (s *Service) RestartRecipeJob(ctx context.Context, req model.RestartRecipeJobRequest) (*model.RestartRecipeJobResponse, error) {
	if s.engine == nil {
		return nil, ErrEngineUnavailable
	}

	projectID := strings.TrimSpace(req.ProjectID)
	jobID := strings.TrimSpace(req.JobID)
	if projectID == "" {
		return nil, ErrInvalidProject
	}
	if jobID == "" {
		return nil, ErrNotFound
	}
	if req.ChapterOrdinal != nil {
		if strings.TrimSpace(req.ScopeID) != "" {
			return nil, fmt.Errorf("%w: specify chapter ordinal or scope ID, not both", storylive.ErrInvalidRestartBoundary)
		}
		run, err := s.engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: jobdb.JobKey{TenantId: projectID, JobId: jobID}})
		if err != nil {
			if errors.Is(err, jobdb.ErrJobNotFound) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		boundary, err := storylive.RestartBoundary(run.Execution, *req.ChapterOrdinal)
		if err != nil {
			return nil, err
		}
		req.ScopeID = boundary.Record.ID
	}

	var restartSubmitter any = jobdbschema.WorkflowEngine{Engine: s.engine, Registry: s.registry}
	if _, ok := s.engine.(jobref.RestartSubmitter); ok {
		restartSubmitter = s.engine
	}
	newKey, err := starter.RestartRecipeJob(ctx, restartSubmitter, jobdb.JobKey{TenantId: projectID, JobId: jobID}, req.ScopeID, req.Patch, starter.RestartOptions{Goal: req.Goal.Mode, RequestID: req.RequestID})
	if err != nil {
		if errors.Is(err, jobdb.ErrJobNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	return &model.RestartRecipeJobResponse{JobID: newKey.JobID, GoalID: newKey.GoalID, TenantID: newKey.TenantID}, nil
}

func (s *Service) GetArtifactByOrdinal(ctx context.Context, req model.GetArtifactByOrdinalRequest) (*model.ArtifactData, error) {
	if s.engine == nil {
		return nil, ErrEngineUnavailable
	}

	projectID := strings.TrimSpace(req.ProjectID)
	jobID := strings.TrimSpace(req.JobID)
	if projectID == "" || jobID == "" {
		return nil, ErrInvalidProject
	}
	if req.ArtifactName == "" || req.TaskOrdinal < 0 {
		return nil, fmt.Errorf("invalid artifact request")
	}

	key := jobdb.ArtifactKey{
		JobId:       strings.TrimSpace(req.JobID),
		TaskOrdinal: req.TaskOrdinal,
		Name:        req.ArtifactName,
		SizeBytes:   -1,
	}

	artifact, err := s.engine.GetArtifact(projectID, key)
	if err != nil {
		return nil, err
	}

	bytes, err := artifact.Bytes(ctx)
	if err != nil {
		return nil, err
	}

	return &model.ArtifactData{
		Content:   bytes,
		Filename:  artifact.Name(),
		SizeBytes: artifact.Size(),
		Metadata: map[string]string{
			"taskOrdinal": fmt.Sprintf("%d", req.TaskOrdinal),
		},
	}, nil
}

func (s *Service) buildSummary(ctx context.Context, projectID string, job jobdb.JobSummary) (model.WorkflowSummary, bool, error) {
	meta, metaErr := jobMetadataFromRaw(job.Metadata)
	if metaErr != nil {
		s.logger.Debug("buildSummary: failed to parse job metadata", "component", "story",
			"job_id", job.JobKey.JobId,
			"error", metaErr)
	}
	if meta == nil {
		s.logger.Debug("buildSummary: no job metadata", "component", "story",
			"job_id", job.JobKey.JobId,
			"strata_available", s.strata != nil)
		return model.WorkflowSummary{}, false, nil
	}

	status := mapWorkflowStatus(job.Status)
	createdAt := job.CreatedAt
	startTime := &createdAt
	closeTime := job.ArchivedAt

	summary := model.WorkflowSummary{
		WorkflowID: job.JobKey.JobId,
		RunID:      job.JobKey.JobId,
		Status:     status,
		RecipeName: meta.RecipeName,
		StartTime:  startTime,
		CloseTime:  closeTime,
		Actor:      actorFromJobMetadata(meta),
		CreatedAt:  createdAt,
	}
	summary.SubmittedAt = &createdAt
	if meta.CellName != "" {
		cellName := meta.CellName
		summary.CellName = &cellName
	}
	if meta.CellID != "" {
		cellID := meta.CellID
		summary.CellID = &cellID
	}

	if summary.CellID == nil && summary.CellName != nil && s.cells != nil {
		if cellID, err := s.findCellID(ctx, projectID, *summary.CellName); err == nil {
			summary.CellID = cellID
		}
	}

	return summary, true, nil
}

func (s *Service) loadChapters(ctx context.Context, jobKey jobdb.JobKey) ([]model.ChapterDetail, error) {
	if s.strata == nil {
		return []model.ChapterDetail{}, nil
	}
	s.logger.Debug("loadChapters: loading story from strata", "component", "story",
		"tenant_id", jobKey.TenantId,
		"job_id", jobKey.JobId)
	storyHandle, err := s.strata.Story(ctx, strataStoryKey(jobKey))
	if err != nil {
		s.logger.Debug("loadChapters: failed to load story from strata", "component", "story",
			"tenant_id", jobKey.TenantId,
			"job_id", jobKey.JobId,
			"error", err)
		return nil, err
	}
	s.logger.Debug("loadChapters: creating chapters iterator", "component", "story",
		"tenant_id", jobKey.TenantId,
		"job_id", jobKey.JobId,
		"page_size", 100)
	iter, err := storyHandle.Chapters(ctx, story.ChaptersOptions{PageSize: 100, Direction: story.DirectionForward})
	if err != nil {
		s.logger.Debug("loadChapters: failed to create chapters iterator", "component", "story",
			"tenant_id", jobKey.TenantId,
			"job_id", jobKey.JobId,
			"error", err)
		return nil, err
	}

	chapters := []model.ChapterDetail{}
	for iter.HasNext() {
		chap, err := iter.Next(ctx)
		if errors.Is(err, pagination.ErrNoMoreItems) {
			break
		}
		if err != nil {
			s.logger.Debug("loadChapters: failed to get next chapter", "component", "story",
				"tenant_id", jobKey.TenantId,
				"job_id", jobKey.JobId,
				"error", err)
			return nil, err
		}
		detail, err := chapterToDetail(chap)
		if err != nil {
			s.logger.Debug("loadChapters: failed to convert chapter to detail", "component", "story",
				"tenant_id", jobKey.TenantId,
				"job_id", jobKey.JobId,
				"chapter_ordinal", chap.Ordinal(),
				"error", err)
			return nil, err
		}
		chapters = append(chapters, detail)
	}
	s.logger.Debug("loadChapters: successfully loaded all chapters", "component", "story",
		"tenant_id", jobKey.TenantId,
		"job_id", jobKey.JobId,
		"chapter_count", len(chapters))
	return chapters, nil
}

func chapterToDetail(chap story.Chapter) (model.ChapterDetail, error) {
	var env chapterEnvelope
	if err := json.Unmarshal(chap.Body(), &env); err != nil {
		return model.ChapterDetail{}, err
	}
	status := mapChapterStatus(env.PayloadKind)
	startTime := env.Meta.CreatedAt

	input := map[string]interface{}{}
	if env.Meta.Input != nil {
		inputM := mapFromRaw(env.Meta.Input)
		if inputM != nil {
			input = *inputM
		}
	}

	var output *map[string]interface{}
	var errMsg *string

	payloadMap := map[string]interface{}{}
	if len(env.Payload) > 0 && json.Unmarshal(env.Payload, &payloadMap) == nil {
		if status == model.ChapterStatusFailed {
			if msg := extractErrorMessage(payloadMap); msg != "" {
				errMsg = &msg
			}
		} else if chap.Ordinal() == 0 {
			input = payloadMap
		} else {
			output = &payloadMap
		}
	}

	artifacts := make([]model.ArtifactReference, 0)
	for _, art := range chap.Artifacts() {
		size := art.SizeBytes()
		artID := art.ID()
		artType := art.ContentType()
		name := art.Name()
		createdAt := env.Meta.CreatedAt
		artifacts = append(artifacts, model.ArtifactReference{
			ArtifactID:   artID,
			ArtifactType: artType,
			Name:         name,
			SizeBytes:    &size,
			CreatedAt:    createdAt,
		})
	}

	chapterType := env.Meta.TaskType
	if chapterType == "" {
		chapterType = "workflow"
	}

	return model.ChapterDetail{
		ChapterNumber: int(chap.Ordinal()),
		ChapterType:   chapterType,
		Status:        status,
		StartTime:     &startTime,
		Input:         input,
		Output:        output,
		Error:         errMsg,
		Artifacts:     artifacts,
	}, nil
}

func mapWorkflowStatus(status jobdb.JobStatus) model.WorkflowStatus {
	switch status {
	case jobdb.JobStatusActive, jobdb.JobStatusPendingJobs, jobdb.JobStatusAwaitingFuture, jobdb.JobStatusReady:
		return model.WorkflowStatusRunning
	case jobdb.JobStatusCompleted:
		return model.WorkflowStatusCompleted
	case jobdb.JobStatusCancelled:
		return model.WorkflowStatusCanceled
	case jobdb.JobStatusExpired:
		return model.WorkflowStatusTimedOut
	case jobdb.JobStatusCrashConcern:
		return model.WorkflowStatusFailed
	default:
		return model.WorkflowStatusUnknown
	}
}

func workflowStatusesToJobStatuses(statuses []model.WorkflowStatus) []jobdb.JobStatus {
	if len(statuses) == 0 {
		// No status filter - return nil to query all statuses
		// swf-go correctly handles nil as "all statuses"
		return nil
	}
	jobStatuses := make([]jobdb.JobStatus, 0, len(statuses)*4)
	for _, status := range statuses {
		switch status {
		case model.WorkflowStatusRunning:
			jobStatuses = append(jobStatuses, jobdb.JobStatusActive, jobdb.JobStatusPendingJobs, jobdb.JobStatusAwaitingFuture, jobdb.JobStatusReady)
		case model.WorkflowStatusCompleted:
			jobStatuses = append(jobStatuses, jobdb.JobStatusCompleted)
		case model.WorkflowStatusCanceled:
			jobStatuses = append(jobStatuses, jobdb.JobStatusCancelled)
		case model.WorkflowStatusTimedOut:
			jobStatuses = append(jobStatuses, jobdb.JobStatusExpired)
		case model.WorkflowStatusFailed:
			jobStatuses = append(jobStatuses, jobdb.JobStatusCrashConcern)
		}
	}
	return jobStatuses
}

func mapChapterStatus(payloadKind string) model.ChapterStatus {
	switch payloadKind {
	case "App", "AppChildJob":
		return model.ChapterStatusCompleted
	case "AppError", "SystemError", "Timeout":
		return model.ChapterStatusFailed
	case "":
		return model.ChapterStatusPending
	default:
		return model.ChapterStatusCompleted
	}
}

func extractErrorMessage(payload map[string]interface{}) string {
	if msg, ok := payload["message"].(string); ok {
		return msg
	}
	return ""
}

func mapFromRaw(payload json.RawMessage) *map[string]interface{} {
	if len(payload) == 0 {
		return nil
	}
	m := map[string]interface{}{}
	if err := json.Unmarshal(payload, &m); err != nil {
		return nil
	}
	return &m
}

func resolveGitRef(requested *string, cellRecord *workflowapi.Cell, projectRecord *workflowapi.Project) string {
	if requested != nil && strings.TrimSpace(*requested) != "" {
		return strings.TrimSpace(*requested)
	}
	if cellRecord != nil && cellRecord.GitBranch != nil && strings.TrimSpace(*cellRecord.GitBranch) != "" {
		return strings.TrimSpace(*cellRecord.GitBranch)
	}
	if projectRecord != nil && projectRecord.GitRepoBranch != nil && strings.TrimSpace(*projectRecord.GitRepoBranch) != "" {
		return strings.TrimSpace(*projectRecord.GitRepoBranch)
	}
	return "main"
}

func derefString(ptr *string) string {
	if ptr == nil {
		return ""
	}
	return *ptr
}

func stringPtr(val string) *string {
	if val == "" {
		return nil
	}
	return &val
}

func aggregateArtifacts(jobAttempts []jobdb.JobAttempt, projectID, jobID string) []model.ArtifactReference {
	type artifactKey struct {
		id   string
		name string
	}
	seen := map[artifactKey]bool{}
	refs := make([]model.ArtifactReference, 0)

	addArtifacts := func(arts []jobdb.ArtifactInfo, createdAt time.Time, ordinal int64) {
		for _, art := range arts {
			key := artifactKey{id: art.ID, name: art.Name}
			if seen[key] {
				continue
			}
			seen[key] = true
			size := art.SizeBytes
			artifactJob, artifactOrdinal, artifactName := jobID, ordinal, art.Name
			if art.Key != nil {
				artifactJob, artifactOrdinal, artifactName = art.Key.JobId, art.Key.TaskOrdinal, art.Key.Name
			}
			url := fmt.Sprintf("/api/projects/%s/jobs/%s/tasks/%d/artifacts/%s", projectID, artifactJob, artifactOrdinal, artifactName)
			refs = append(refs, model.ArtifactReference{
				ArtifactID:   art.ID,
				ArtifactType: art.ContentType,
				Name:         art.Name,
				SizeBytes:    &size,
				URL:          &url,
				CreatedAt:    createdAt,
			})
		}
	}

	for _, attempt := range jobAttempts {
		if attempt.Output != nil {
			addArtifacts(attempt.Output.Artifacts, attempt.CreatedAt, attempt.Ordinal)
		}
		for _, task := range attempt.Tasks {
			for _, att := range task.Attempts {
				if att.Output != nil {
					addArtifacts(att.Output.Artifacts, att.CreatedAt, att.Ordinal)
				}
			}
		}
	}

	return refs
}

func hashInputs(inputs map[string]interface{}) string {
	if len(inputs) == 0 {
		return ""
	}
	raw, err := json.Marshal(inputs)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func jobMetadataFromRaw(raw json.RawMessage) (*starter.JobMetadata, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var meta starter.JobMetadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func actorFromJobMetadata(meta *starter.JobMetadata) model.Actor {
	return model.Actor{Type: model.ActorTypeUser}
}

func (s *Service) findCellID(ctx context.Context, projectID string, cellName string) (*string, error) {
	if s.cells == nil {
		return nil, fmt.Errorf("cells service unavailable")
	}
	iter, err := s.cells.ListCells(ctx, workflowapi.SearchFilter{
		ProjectIDs: []string{projectID},
		Names:      []string{cellName},
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close(ctx)
	c, err := iter.Next(ctx)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, fmt.Errorf("cell not found")
	}
	id := string(c.ID)
	return &id, nil
}

func (s *Service) GetWorkflowArtifact(
	ctx context.Context,
	req model.GetWorkflowArtifactRequest,
) (*model.ArtifactData, error) {
	// 1. Validate request
	if req.ProjectID == "" || req.WorkflowID == "" || req.ArtifactName == "" {
		return nil, fmt.Errorf("missing required parameters")
	}
	if req.ChapterNumber < 0 {
		return nil, fmt.Errorf("invalid chapter number: %d", req.ChapterNumber)
	}

	// 2. Get workflow to verify existence and get run ID
	workflowDetail, err := s.GetWorkflow(ctx, model.GetWorkflowRequest{
		ProjectID:  req.ProjectID,
		WorkflowID: req.WorkflowID,
	})
	if err != nil {
		return nil, fmt.Errorf("workflow not found: %w", err)
	}

	// 3. Verify chapter exists
	if req.ChapterNumber >= len(workflowDetail.Chapters) {
		return nil, fmt.Errorf("chapter %d not found (workflow has %d chapters)",
			req.ChapterNumber, len(workflowDetail.Chapters))
	}

	// 4. Verify artifact exists in chapter
	chapter := workflowDetail.Chapters[req.ChapterNumber]
	var artifactRef *model.ArtifactReference
	for i := range chapter.Artifacts {
		if chapter.Artifacts[i].Name == req.ArtifactName {
			artifactRef = &chapter.Artifacts[i]
			break
		}
	}
	if artifactRef == nil {
		return nil, fmt.Errorf("artifact %s not found in chapter %d",
			req.ArtifactName, req.ChapterNumber)
	}

	// 5. Get the chapter from strata to access its artifacts
	jobKey := jobdb.JobKey{
		TenantId: req.ProjectID,
		JobId:    req.WorkflowID,
	}

	s.logger.Debug("GetWorkflowArtifact: loading chapter from strata", "component", "story",
		"tenant_id", jobKey.TenantId,
		"job_id", jobKey.JobId,
		"chapter_number", req.ChapterNumber)
	chap, err := s.strata.Chapter(ctx, strataStoryKey(jobKey), int64(req.ChapterNumber))
	if err != nil {
		s.logger.Debug("GetWorkflowArtifact: failed to retrieve chapter from strata", "component", "story",
			"tenant_id", jobKey.TenantId,
			"job_id", jobKey.JobId,
			"chapter_number", req.ChapterNumber,
			"error", err)
		return nil, fmt.Errorf("failed to retrieve chapter: %w", err)
	}

	// 6. Find the artifact in the chapter's artifact list by name and retrieve its content
	var content []byte
	found := false
	for _, art := range chap.Artifacts() {
		if art.Name() == req.ArtifactName {
			// 7. Retrieve artifact bytes
			s.logger.Debug("GetWorkflowArtifact: loading artifact bytes from strata", "component", "story",
				"tenant_id", jobKey.TenantId,
				"job_id", jobKey.JobId,
				"chapter_number", req.ChapterNumber,
				"artifact_name", req.ArtifactName,
				"artifact_id", art.ID())
			content, err = art.Bytes(ctx)
			if err != nil {
				s.logger.Debug("GetWorkflowArtifact: failed to read artifact content from strata", "component", "story",
					"tenant_id", jobKey.TenantId,
					"job_id", jobKey.JobId,
					"chapter_number", req.ChapterNumber,
					"artifact_name", req.ArtifactName,
					"artifact_id", art.ID(),
					"error", err)
				return nil, fmt.Errorf("failed to read artifact content: %w", err)
			}
			found = true
			break
		}
	}
	if !found {
		s.logger.Debug("GetWorkflowArtifact: artifact not found in strata chapter", "component", "story",
			"tenant_id", jobKey.TenantId,
			"job_id", jobKey.JobId,
			"chapter_number", req.ChapterNumber,
			"artifact_name", req.ArtifactName)
		return nil, fmt.Errorf("artifact %s not found in chapter %d",
			req.ArtifactName, req.ChapterNumber)
	}
	s.logger.Debug("GetWorkflowArtifact: successfully loaded artifact", "component", "story",
		"tenant_id", jobKey.TenantId,
		"job_id", jobKey.JobId,
		"chapter_number", req.ChapterNumber,
		"artifact_name", req.ArtifactName,
		"size_bytes", len(content))

	// 8. Return artifact data
	return &model.ArtifactData{
		Content:   content,
		Filename:  artifactRef.Name,
		SizeBytes: int64(len(content)),
		Metadata: map[string]string{
			"artifactType":  artifactRef.ArtifactType,
			"chapterNumber": fmt.Sprintf("%d", req.ChapterNumber),
		},
	}, nil
}
