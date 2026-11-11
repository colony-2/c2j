package starter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/jobrestart"
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
	if title, ok := startJob.Inputs["title"].(string); ok && strings.TrimSpace(title) != "" {
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
	meta.ParentGoalID = parent.GoalID
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
	ref, err := submitter.SubmitJobRef(ctx, job)
	if err == nil && job.GoalID != "" {
		err = ref.ValidateSelected(job)
	}
	return ref, err
}

// PrepareRecipeJob resolves the complete semantic request without publishing it.
// Retain this value through Descriptor before attempting selected publication.
func PrepareRecipeJob(ctx context.Context, startJob workflowctl.StartJob, opts StartRecipeJobOptions, recipes ...recipe.Recipe) (jobdb.SubmitJob, error) {
	if err := jobdb.ValidateCellName(startJob.Cell); err != nil {
		return jobdb.SubmitJob{}, fmt.Errorf("cell name is required: %w", err)
	}
	if opts.JobID != "" && startJob.JobID != "" && opts.JobID != startJob.JobID {
		return jobdb.SubmitJob{}, fmt.Errorf("conflicting destination job IDs")
	}
	if opts.GoalID != "" && startJob.GoalID != "" && opts.GoalID != startJob.GoalID {
		return jobdb.SubmitJob{}, fmt.Errorf("conflicting primary goal IDs")
	}
	if opts.GoalID != "" {
		startJob.GoalID = opts.GoalID
	}
	if opts.JobID != "" {
		startJob.JobID = opts.JobID
	}
	if err := jobdb.ValidatePrimaryGoalSelection(jobdb.SubmitJob{TenantId: startJob.TenantId, JobID: startJob.JobID, GoalID: startJob.GoalID}); err != nil {
		return jobdb.SubmitJob{}, err
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

	meta := JobMetadataFromStartJob(startJob)
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

// RestartRecipeJob starts a fresh continuation at a recipe scope or submission.
func RestartRecipeJob(ctx context.Context, engine any, prior jobdb.JobKey, scopeID string, patch *task.ContextPatch, opts RestartOptions) (jobref.Ref, error) {
	if err := jobrestart.ValidatePatch(patch); err != nil {
		return jobref.Ref{}, err
	}
	if strings.TrimSpace(scopeID) == "" {
		return jobref.Ref{}, fmt.Errorf("scope ID is required")
	}
	req := jobdb.SubmitRestartJob{PriorJobKey: prior, ExecutionRestart: &jobdb.ExecutionRestart{ScopeID: scopeID}}
	if scopeID == SubmissionRestartScopeID {
		if patch != nil && len(*patch) > 0 {
			return jobref.Ref{}, fmt.Errorf("submission restart does not support context patches; select a recipe scope to patch execution context")
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
		return jobref.Ref{}, fmt.Errorf("restart metadata reader is unavailable")
	}
	jobs, err := reader.ListJobs(ctx, jobdb.ListJobsRequest{
		TenantIds: []string{prior.TenantId}, JobKeys: []jobdb.JobKey{prior},
		Stores: []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived}, PageSize: 1,
	})
	if err != nil {
		return jobref.Ref{}, err
	}
	if len(jobs.Jobs) != 1 {
		return jobref.Ref{}, fmt.Errorf("restart source job: %w", jobdb.ErrJobNotFound)
	}
	req.Cell = jobs.Jobs[0].Cell
	// Restart from the original scheduling hint, never the prior job's latest
	// demand: the selected scope may precede any of its runtime changes.
	initial := execution.Inspect(jobs.Jobs[0].Metadata, nil)
	if initial.Diagnostic != "" {
		return jobref.Ref{}, fmt.Errorf("initial execution requirements: %s", initial.Diagnostic)
	}
	if initial.Initial != nil {
		raw, err := execution.PayloadWithDemand(nil, initial.Initial.Demand)
		if err != nil {
			return jobref.Ref{}, err
		}
		req.ClientPayloadUpdate = &jobdb.ClientPayloadUpdate{Mode: "reset", Value: raw}
	}

	if err := jobdbschema.SetSubmitRestartJobSchema(&req); err != nil {
		return jobref.Ref{}, err
	}

	if patch != nil && req.ExecutionRestart != nil {
		raw, err := json.Marshal(patch)
		if err != nil {
			return jobref.Ref{}, err
		}
		req.ExecutionRestart.Payload = raw
	}

	switch opts.Goal {
	case "", "continue":
		req.ShareGoal = true
	case "new":
	default:
		return jobref.Ref{}, fmt.Errorf("goal mode must be continue or new")
	}
	if strings.TrimSpace(opts.RequestID) == "" {
		return jobref.Ref{}, fmt.Errorf("restart request ID is required")
	}
	submitter, ok := engine.(jobref.RestartSubmitter)
	if !ok {
		return jobref.Ref{}, fmt.Errorf("restart submitter must preserve the complete job reference")
	}
	return submitter.SubmitRestartJobRef(ctx, jobdb.SubmitRestartJobRequest{Job: req, RequestID: opts.RequestID})
}
