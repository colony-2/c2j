package starter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/colony-2/c2j/internal/dispatchtest"
	"github.com/colony-2/c2j/internal/scenariotest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executioncatalog"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/jobrestart"
	"github.com/colony-2/c2j/pkg/jobsemantics"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"gopkg.in/yaml.v3"
)

const (
	RecipeJobType        = "recipe"
	RecipeArtifactSuffix = ".recipe.yaml"
)

const (
	JobMetadataVersion = 1

	MetaFieldVersion jobdb.FieldName = "v"
	MetaFieldRecipe  jobdb.FieldName = "recipe"
	MetaFieldPrompt  jobdb.FieldName = "prompt"
	MetaFieldTitle   jobdb.FieldName = "title"
	MetaFieldGitRef  jobdb.FieldName = "git_ref"

	MetaFieldParentTenantID       jobdb.FieldName = "parent_tenant_id"
	MetaFieldParentJobID          jobdb.FieldName = "parent_job_id"
	MetaFieldParentJobType        jobdb.FieldName = "parent_job_type"
	MetaFieldParentOpType         jobdb.FieldName = "parent_op_type"
	MetaFieldParentOpStep         jobdb.FieldName = "parent_op_step"
	MetaFieldParentOpTaskType     jobdb.FieldName = "parent_op_task_type"
	MetaFieldParentRepo           jobdb.FieldName = "parent_repo"
	MetaFieldParentGitRef         jobdb.FieldName = "parent_git_ref"
	MetaFieldParentInvocationPath jobdb.FieldName = "parent_invocation_path"
	MetaFieldParentInvocationSeq  jobdb.FieldName = "parent_invocation_seq"
	MetaFieldParentInvocationHash jobdb.FieldName = "parent_invocation_hash"
)

type JobMetadata struct {
	Execution   *execution.Submission `json:"execution,omitempty"`
	InputHash   string                `json:"input_hash,omitempty"`
	SubmittedAt *time.Time            `json:"submitted_at,omitempty"`
	Version     int                   `json:"v"`
	RecipeName  string                `json:"recipe,omitempty"`
	Prompt      string                `json:"prompt,omitempty"`
	Title       string                `json:"title,omitempty"`
	GitRef      string                `json:"git_ref,omitempty"`

	ParentTenantID           string `json:"parent_tenant_id,omitempty"`
	ParentGoalID             string `json:"parent_goal_id,omitempty"`
	ParentJobID              string `json:"parent_job_id,omitempty"`
	ParentJobType            string `json:"parent_job_type,omitempty"`
	ParentOpType             string `json:"parent_op_type,omitempty"`
	ParentOpStep             string `json:"parent_op_step,omitempty"`
	ParentOpTaskType         string `json:"parent_op_task_type,omitempty"`
	ParentRepositorySource   string `json:"parent_repo,omitempty"`
	ParentGitRef             string `json:"parent_git_ref,omitempty"`
	ParentInvocationPath     string `json:"parent_invocation_path,omitempty"`
	ParentInvocationSequence int64  `json:"parent_invocation_seq,omitempty"`
	ParentInvocationHash     string `json:"parent_invocation_hash,omitempty"`
}

func JobMetadataFromStartJob(startJob workflowctl.StartJob) JobMetadata {
	meta := JobMetadata{
		Execution:   startJob.Execution,
		InputHash:   startJob.InputHash,
		SubmittedAt: startJob.SubmittedAt,
		Version:     JobMetadataVersion,
		RecipeName:  startJob.RecipeName,
		GitRef:      startJob.GitRef,
	}
	// Index only submitted text. Secret references remain in the input payload
	// and are never resolved for metadata. Marked embedded inputs are validated
	// and converted to references before this function is called.
	if prompt, ok := startJob.Inputs["prompt"].(string); ok && strings.TrimSpace(prompt) != "" {
		if _, secret := secrets.Template(prompt); !secret {
			meta.Prompt = prompt
		}
	}
	// Blank explicit titles retain legacy fallback. Unsafe nonblank titles are
	// omitted, never resolved and never replaced by a different title.
	title := startJob.Title
	if strings.TrimSpace(title) == "" {
		title, _ = startJob.Inputs["title"].(string)
	}
	if strings.TrimSpace(title) != "" {
		if _, secret := secrets.Template(title); !secret {
			meta.Title = title
		}
	}
	if startJob.Parent != nil {
		applyParentMetadata(&meta, *startJob.Parent)
	}
	return meta
}

func applyParentMetadata(meta *JobMetadata, parent jobcontext.Parent) {
	meta.ParentTenantID = parent.TenantID
	meta.ParentJobID = parent.JobID
	meta.ParentJobType = parent.JobType
	meta.ParentOpType = parent.OpType
	meta.ParentOpStep = parent.OpStep
	meta.ParentOpTaskType = parent.OpTaskType
	meta.ParentRepositorySource = parent.RepositorySource
	meta.ParentGitRef = parent.GitRef
	meta.ParentInvocationPath = parent.InvocationPath
	meta.ParentInvocationSequence = parent.InvocationSequence
	meta.ParentInvocationHash = parent.InvocationHash
}

func StartRecipeJob(ctx context.Context, startJob workflowctl.StartJob, engine any, recipes ...recipe.Recipe) (jobref.Ref, error) {
	return StartRecipeJobWithOptions(ctx, startJob, engine, StartRecipeJobOptions{}, recipes...)
}

type StartRecipeJobOptions struct {
	SubmitterDependency   *bool
	BaseCatalog           *executioncatalog.Policy
	ExecutionRequirements execution.Requirements
	GoalID                string
	JobID                 string
	Dependencies          []jobdb.Dependency
}

func StartRecipeJobWithOptions(ctx context.Context, startJob workflowctl.StartJob, engine any, opts StartRecipeJobOptions, recipes ...recipe.Recipe) (jobref.Ref, error) {
	submitter, ok := engine.(jobref.Submitter)
	if !ok {
		return jobref.Ref{}, fmt.Errorf("submitter must preserve the complete job reference")
	}
	job, err := PrepareRecipeJob(ctx, startJob, opts, recipes...)
	if err != nil {
		return jobref.Ref{}, err
	}
	if opts.BaseCatalog != nil {
		ctx = executioncatalog.WithPolicy(ctx, *opts.BaseCatalog)
	}
	if err := executioncatalog.CheckSubmission(ctx, job); err != nil {
		return jobref.Ref{}, err
	}
	finish, err := beginTestSubmission(ctx, startJob, "submit")
	if err != nil {
		return jobref.Ref{}, err
	}
	ref, err := submitter.SubmitJobRef(ctx, job)
	if err == nil {
		assertions := job
		if assertions.CellRepo == "" {
			assertions.Cell = ""
		} // Backend resolves aliases; receipt captures the canonical name.
		err = ref.ValidateSubmission(assertions)
	}
	return ref, finish(ref, err)
}

// PrepareRecipeJob resolves the complete semantic request without publishing it.
// Retain this value through Descriptor before attempting publication.
func PrepareRecipeJob(ctx context.Context, startJob workflowctl.StartJob, opts StartRecipeJobOptions, recipes ...recipe.Recipe) (jobdb.SubmitJob, error) {
	startJob.StatusHistoryVersion = workflowctl.StatusHistoryVersion
	return prepareRecipeJob(ctx, startJob, opts, recipes...)
}

func PrepareParallelBranchJob(ctx context.Context, startJob workflowctl.StartJob, branch recipe.Recipe) (jobdb.SubmitJob, error) {
	switch startJob.StatusHistoryVersion {
	case 0:
		job, err := prepareRecipeJob(ctx, startJob, StartRecipeJobOptions{}, branch)
		if err != nil {
			return jobdb.SubmitJob{}, err
		}
		job.Schema = &jobdb.JobSchemaSelector{Hash: jobdbschema.PreStatusHistorySchemaHash}
		return job, nil
	case workflowctl.StatusHistoryVersion:
		return PrepareRecipeJob(ctx, startJob, StartRecipeJobOptions{}, branch)
	default:
		return jobdb.SubmitJob{}, fmt.Errorf("unsupported parallel branch status history version %d", startJob.StatusHistoryVersion)
	}
}

func prepareRecipeJob(ctx context.Context, startJob workflowctl.StartJob, opts StartRecipeJobOptions, recipes ...recipe.Recipe) (jobdb.SubmitJob, error) {
	if startJob.JobSemanticsVersion != 0 && startJob.JobSemanticsVersion != jobsemantics.Current {
		return jobdb.SubmitJob{}, fmt.Errorf("fresh preparation requires job_semantics_version 2; replay retained descriptors unchanged")
	}
	if startJob.JobContext.JobSemanticsVersion != 0 && startJob.JobContext.JobSemanticsVersion != jobsemantics.Current {
		return jobdb.SubmitJob{}, fmt.Errorf("contradictory job_semantics_version in context")
	}
	startJob.JobSemanticsVersion = jobsemantics.Current
	startJob.JobContext.JobSemanticsVersion = jobsemantics.Current
	for _, r := range recipes {
		if err := recipe.ValidateJobSemantics(r, jobsemantics.Current); err != nil {
			return jobdb.SubmitJob{}, err
		}
	}
	// Keep caller mutations from changing the prepared request or its input.
	if startJob.AvailableAt != nil {
		instant := startJob.AvailableAt.UTC()
		startJob.AvailableAt = &instant
	}
	if err := jobdb.ValidateCellName(startJob.Cell); err != nil {
		return jobdb.SubmitJob{}, fmt.Errorf("cell name is required: %w", err)
	}
	if opts.JobID != "" && startJob.JobID != "" && opts.JobID != startJob.JobID {
		return jobdb.SubmitJob{}, fmt.Errorf("conflicting destination job IDs")
	}
	if opts.GoalID != "" || startJob.GoalID != "" {
		return jobdb.SubmitJob{}, fmt.Errorf("goal selection is unsupported; submit an independent job")
	}
	startJob.HookPolicyVersion = 0
	inherited := startJob.JobContext.InheritedPermissions
	if inherited != nil && inherited.ChildHookLineage != nil {
		return jobdb.SubmitJob{}, fmt.Errorf("hook child lineage must be assigned by the controller")
	}
	if (inherited == nil || !inherited.HasHookLineage()) && os.Getenv("C2J_WRITE_EXECUTION_HOOK_POLICY") == "true" {
		startJob.HookPolicyVersion = workflowctl.ExecutionHookPolicyVersion
	}
	if opts.JobID != "" {
		startJob.JobID = opts.JobID
	}
	for _, dep := range append(append([]jobdb.Dependency(nil), startJob.Dependencies...), opts.Dependencies...) {
		if dep.Target.GoalID != "" {
			return jobdb.SubmitJob{}, fmt.Errorf("goal dependencies are unsupported; select an exact job")
		}
	}

	initial, err := execution.Initial(nil, false, opts.ExecutionRequirements)
	if err != nil {
		return jobdb.SubmitJob{}, err
	}
	if startJob.Execution != nil {
		if err := startJob.Execution.Validate(); err != nil {
			return jobdb.SubmitJob{}, err
		}
		initial = *startJob.Execution
	}
	initial, err = execution.Initial(nil, false, execution.Overlay(initial.JobRequirements, opts.ExecutionRequirements))
	if err != nil {
		return jobdb.SubmitJob{}, err
	}
	for _, r := range recipes {
		if r.GetMetdata().ID == startJob.RecipeName {
			// Validate nested marked fields before persisting embedded inputs.
			contract, err := recipe.CompileInputSchema(r.GetMetdata().SchemaVersion, r.GetMetdata().InputSchema)
			if err != nil {
				return jobdb.SubmitJob{}, err
			}
			inputs, err := contract.PrepareSubmittedSecrets(startJob.Inputs)
			if err != nil {
				return jobdb.SubmitJob{}, err
			}
			startJob.Inputs = inputs

			initial, err = execution.Initial(r.GetMetdata().Execution, true, initial.JobRequirements)
			if recipe.HasExecutionNeeds(r) {
				// The next task's scope can override the root and depend on prior
				// outputs. Do not advertise a guessed frontier at submission.
				initial, err = execution.Initial(nil, false, initial.JobRequirements)
			}
			if err != nil {
				return jobdb.SubmitJob{}, err
			}
		}
	}
	startJob.Execution = &initial
	recipeCount := len(recipes)
	artifacts := make([]jobdb.Artifact, recipeCount+len(startJob.Artifacts))
	for i, r := range recipes {
		recipeYaml, err := yaml.Marshal(&r)
		if err != nil {
			return jobdb.SubmitJob{}, err
		}
		name := r.GetMetadata().ID + RecipeArtifactSuffix
		artifacts[i] = jobdb.NewArtifactFromBytes(name, recipeYaml)
	}

	for i, a := range startJob.Artifacts {
		artifacts[recipeCount+i] = a
	}

	// Goal selection belongs to JobDB, not the persisted recipe input. Keep
	// StartJob.GoalID serializable for broker transport and historical decoding.
	payload := startJob
	payload.GoalID = ""
	payload.Artifacts = nil
	inputData, err := jobdb.NewTaskData(payload, artifacts...)

	if err != nil {
		return jobdb.SubmitJob{}, err
	}

	meta := struct {
		JobMetadata
		SubmitterDependency *bool `json:"submitter_dependency,omitempty"`
	}{JobMetadata: JobMetadataFromStartJob(startJob), SubmitterDependency: opts.SubmitterDependency}
	metaRaw, err := json.Marshal(meta)
	if err != nil {
		return jobdb.SubmitJob{}, err
	}

	runPolicy := recipeRunPolicy(startJob, recipes)
	jobID := opts.JobID
	if jobID == "" {
		jobID = startJob.JobID
	}

	job := jobdb.SubmitJob{
		AvailableAt:  startJob.AvailableAt,
		Cell:         startJob.Cell,
		CellRepo:     startJob.CellRepo,
		TenantId:     startJob.TenantId,
		JobType:      RecipeJobType,
		JobID:        jobID,
		GoalID:       startJob.GoalID,
		Data:         inputData,
		RunPolicy:    runPolicy,
		Metadata:     metaRaw,
		Dependencies: append(append([]jobdb.Dependency(nil), startJob.Dependencies...), opts.Dependencies...),
	}
	if err := jobdbschema.SetSubmitJobSchema(&job); err != nil {
		return jobdb.SubmitJob{}, err
	}
	return job, nil
}

func recipeRunPolicy(startJob workflowctl.StartJob, recipes []recipe.Recipe) jobdb.RunPolicy {
	// Recipe retries belong to the root execution scope, including recipes
	// resolved by the worker. An outer job retry would multiply that budget.
	policy := jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 1}}
	for _, r := range recipes {
		if r.GetMetdata().ID != startJob.RecipeName {
			continue
		}
		if timeout := time.Duration(r.GetMetdata().Timeout); timeout > 0 {
			totalTimeout := jobdb.Duration(timeout)
			policy.TotalTimeout = &totalTimeout
		}
		return policy
	}
	return policy
}

// SubmissionRestartScopeID selects the original submission, before any execution.
// It is a c2j boundary, not a JobDB execution scope ID.
const SubmissionRestartScopeID = "c2j:submission"

type RestartOptions struct {
	Goal      string
	RequestID string
}

// PrepareRestartJob freezes an independent continuation at a recipe scope or submission.
func PrepareRestartJob(ctx context.Context, engine any, prior jobdb.JobKey, scopeID string, patch *task.ContextPatch, opts RestartOptions) (jobdb.SubmitRestartJobRequest, error) {
	if opts.Goal != "" && opts.Goal != "new" {
		return jobdb.SubmitRestartJobRequest{}, fmt.Errorf("goal sharing is unsupported; restart creates an independent job")
	}
	if err := jobrestart.ValidatePatch(patch); err != nil {
		return jobdb.SubmitRestartJobRequest{}, err
	}
	if strings.TrimSpace(scopeID) == "" {
		return jobdb.SubmitRestartJobRequest{}, fmt.Errorf("scope ID is required")
	}
	req := jobdb.SubmitRestartJob{ShareGoal: false, PriorJobKey: prior, ExecutionRestart: &jobdb.ExecutionRestart{ScopeID: scopeID}}
	if scopeID == SubmissionRestartScopeID {
		if patch != nil && len(*patch) > 0 {
			return jobdb.SubmitRestartJobRequest{}, fmt.Errorf("submission restart does not support context patches; select a recipe scope to patch execution context")
		}
		// Keep the original input and its artifacts, but no execution header,
		// setup task results, or prior scope continuations.
		req.ExecutionRestart = nil
		req.LastStepToKeep = 0
	}
	reader, ok := engine.(interface {
		ListJobs(context.Context, jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error)
	})
	if !ok {
		return jobdb.SubmitRestartJobRequest{}, fmt.Errorf("restart metadata reader is unavailable")
	}
	jobs, err := reader.ListJobs(ctx, jobdb.ListJobsRequest{
		TenantIds: []string{prior.TenantId}, JobKeys: []jobdb.JobKey{prior},
		// An explicitly selected source remains restartable when hidden from
		// discovery, just as its retained history remains directly readable.
		IncludeHidden: true,
		Stores:        []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived}, PageSize: 1,
	})
	if err != nil {
		return jobdb.SubmitRestartJobRequest{}, err
	}
	if len(jobs.Jobs) != 1 {
		return jobdb.SubmitRestartJobRequest{}, fmt.Errorf("restart source job: %w", jobdb.ErrJobNotFound)
	}
	req.Cell = jobs.Jobs[0].Cell
	// Restart from the original scheduling hint, never the prior job's latest
	// demand: the selected scope may precede any of its runtime changes.
	initial := execution.Inspect(jobs.Jobs[0].Metadata, nil)
	if initial.Diagnostic != "" {
		return jobdb.SubmitRestartJobRequest{}, fmt.Errorf("initial execution requirements: %s", initial.Diagnostic)
	}
	if initial.Initial == nil && executioncatalog.FromContext(ctx).Required {
		raw, err := execution.PayloadWithDemand(nil, execution.Demand{SchemaVersion: execution.DemandSchemaVersion, RecipeResolution: "unresolved"})
		if err != nil {
			return jobdb.SubmitRestartJobRequest{}, err
		}
		req.ClientPayloadUpdate = &jobdb.ClientPayloadUpdate{Mode: "reset", Value: raw}
	}
	if initial.Initial != nil {
		raw, err := execution.PayloadWithDemand(nil, initial.Initial.Demand)
		if err != nil {
			return jobdb.SubmitRestartJobRequest{}, err
		}
		req.ClientPayloadUpdate = &jobdb.ClientPayloadUpdate{Mode: "reset", Value: raw}
	}

	if err := jobdbschema.SetSubmitRestartJobSchema(&req); err != nil {
		return jobdb.SubmitRestartJobRequest{}, err
	}

	if patch != nil && req.ExecutionRestart != nil {
		raw, err := json.Marshal(patch)
		if err != nil {
			return jobdb.SubmitRestartJobRequest{}, err
		}
		req.ExecutionRestart.Payload = raw
	}

	if strings.TrimSpace(opts.RequestID) == "" {
		return jobdb.SubmitRestartJobRequest{}, fmt.Errorf("restart request ID is required")
	}
	return jobdb.SubmitRestartJobRequest{Job: req, RequestID: opts.RequestID}, nil
}

// RestartRecipeJob prepares and submits a new independent restart. Hosts that
// need durable recovery should persist PrepareRestartJob's complete request
// before publication, then call ReplayRestartJob with that unchanged request.
func RestartRecipeJob(ctx context.Context, engine any, prior jobdb.JobKey, scopeID string, patch *task.ContextPatch, opts RestartOptions) (jobref.Ref, error) {
	request, err := PrepareRestartJob(ctx, engine, prior, scopeID, patch, opts)
	if err != nil {
		return jobref.Ref{}, err
	}
	submitter, ok := engine.(jobref.RestartSubmitter)
	if !ok {
		return jobref.Ref{}, fmt.Errorf("restart submitter must preserve the complete job reference")
	}
	return ReplayRestartJob(ctx, submitter, request)
}

// ReplayRestartJob resends the frozen original operation using its original
// request ID. It never reselects a boundary, rebuilds a patch, strips sharing
// mode, changes routes, or falls back to a fresh operation. Legacy ShareGoal
// remains semantic input to the backend's original idempotent receipt.
func ReplayRestartJob(ctx context.Context, submitter jobref.RestartSubmitter, request jobdb.SubmitRestartJobRequest) (jobref.Ref, error) {
	if strings.TrimSpace(request.RequestID) == "" {
		return jobref.Ref{}, fmt.Errorf("restart request ID is required")
	}
	if err := executioncatalog.CheckRestart(ctx, request); err != nil {
		return jobref.Ref{}, err
	}
	var ref jobref.Ref
	var err error
	if recovery, ok := submitter.(jobref.HistoricalRecovery); ok && request.Job.ShareGoal {
		ref, err = recovery.ReplayRestartJobRef(ctx, request)
	} else {
		ref, err = submitter.SubmitRestartJobRef(ctx, request)
	}
	if err == nil {
		err = ref.ValidateSubmission(jobdb.SubmitJob{TenantId: request.Job.PriorJobKey.TenantId, JobID: request.Job.JobID, Cell: request.Job.Cell, CellRepo: request.Job.CellRepo})
	}
	return ref, err
}

func beginTestSubmission(ctx context.Context, start workflowctl.StartJob, kind string) (func(jobref.Ref, error) error, error) {
	a := dispatchtest.Identity(ctx)
	s := scenariotest.Submission{Caller: a.JobID, InvocationID: a.InvocationID, NodePath: a.NodePath, Attempt: a.Attempt, Kind: kind}
	if start.Parent != nil && s.Caller == "" {
		s.Caller = start.Parent.JobID
		s.InvocationID = start.Parent.InvocationHash
		s.NodePath = start.Parent.InvocationPath
	}
	return scenariotest.Begin(ctx, s)
}
