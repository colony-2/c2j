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

	MetaFieldVersion  jobdb.FieldName = "v"
	MetaFieldRecipe   jobdb.FieldName = "recipe"
	MetaFieldPrompt   jobdb.FieldName = "prompt"
	MetaFieldCellID   jobdb.FieldName = "cell_id"
	MetaFieldCellName jobdb.FieldName = "cell_name"
	MetaFieldRepo     jobdb.FieldName = "repo"
	MetaFieldGitRef   jobdb.FieldName = "git_ref"

	MetaFieldParentTenantID       jobdb.FieldName = "parent_tenant_id"
	MetaFieldParentJobID          jobdb.FieldName = "parent_job_id"
	MetaFieldParentJobType        jobdb.FieldName = "parent_job_type"
	MetaFieldParentOpType         jobdb.FieldName = "parent_op_type"
	MetaFieldParentOpStep         jobdb.FieldName = "parent_op_step"
	MetaFieldParentOpTaskType     jobdb.FieldName = "parent_op_task_type"
	MetaFieldParentCellName       jobdb.FieldName = "parent_cell_name"
	MetaFieldParentRepo           jobdb.FieldName = "parent_repo"
	MetaFieldParentGitRef         jobdb.FieldName = "parent_git_ref"
	MetaFieldParentInvocationPath jobdb.FieldName = "parent_invocation_path"
	MetaFieldParentInvocationSeq  jobdb.FieldName = "parent_invocation_seq"
	MetaFieldParentInvocationHash jobdb.FieldName = "parent_invocation_hash"
)

type JobMetadata struct {
	Execution        *execution.Submission `json:"execution,omitempty"`
	InputHash        string                `json:"input_hash,omitempty"`
	SubmittedAt      *time.Time            `json:"submitted_at,omitempty"`
	Version          int                   `json:"v"`
	RecipeName       string                `json:"recipe,omitempty"`
	Prompt           string                `json:"prompt,omitempty"`
	CellID           string                `json:"cell_id,omitempty"`
	CellName         string                `json:"cell_name,omitempty"`
	RepositorySource string                `json:"repo,omitempty"`
	GitRef           string                `json:"git_ref,omitempty"`

	ParentTenantID           string `json:"parent_tenant_id,omitempty"`
	ParentGoalID             string `json:"parent_goal_id,omitempty"`
	ParentJobID              string `json:"parent_job_id,omitempty"`
	ParentJobType            string `json:"parent_job_type,omitempty"`
	ParentOpType             string `json:"parent_op_type,omitempty"`
	ParentOpStep             string `json:"parent_op_step,omitempty"`
	ParentOpTaskType         string `json:"parent_op_task_type,omitempty"`
	ParentCellName           string `json:"parent_cell_name,omitempty"`
	ParentRepositorySource   string `json:"parent_repo,omitempty"`
	ParentGitRef             string `json:"parent_git_ref,omitempty"`
	ParentInvocationPath     string `json:"parent_invocation_path,omitempty"`
	ParentInvocationSequence int64  `json:"parent_invocation_seq,omitempty"`
	ParentInvocationHash     string `json:"parent_invocation_hash,omitempty"`
}

func JobMetadataFromStartJob(startJob workflowctl.StartJob) JobMetadata {
	repo := startJob.JobContext.GitBase.BaseRepo
	if repo == "" {
		repo = startJob.JobContext.RecipeSource.Repo
	}
	meta := JobMetadata{
		Execution:        startJob.Execution,
		InputHash:        startJob.InputHash,
		SubmittedAt:      startJob.SubmittedAt,
		Version:          JobMetadataVersion,
		RecipeName:       startJob.RecipeName,
		CellID:           startJob.JobContext.Workflow.CellID,
		CellName:         startJob.JobContext.Workflow.CellName,
		RepositorySource: repo,
		GitRef:           startJob.GitRef,
	}
	// Index only submitted text. Secret references remain in the input payload
	// and are never resolved for metadata. Marked embedded inputs are validated
	// and converted to references before this function is called.
	if prompt, ok := startJob.Inputs["prompt"].(string); ok && strings.TrimSpace(prompt) != "" {
		if _, secret := secrets.Template(prompt); !secret {
			meta.Prompt = prompt
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
	meta.ParentCellName = parent.CellName
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
	JobID                 string
	Dependencies          []jobdb.Dependency
}

func StartRecipeJobWithOptions(ctx context.Context, startJob workflowctl.StartJob, engine any, opts StartRecipeJobOptions, recipes ...recipe.Recipe) (jobref.Ref, error) {
	submitter, ok := engine.(jobref.Submitter)
	if !ok {
		return jobref.Ref{}, fmt.Errorf("submitter must preserve the complete job reference")
	}

	initial, err := execution.Initial(nil, false, opts.ExecutionRequirements)
	if err != nil {
		return jobref.Ref{}, err
	}
	if startJob.Execution != nil {
		if err := startJob.Execution.Validate(); err != nil {
			return jobref.Ref{}, err
		}
		initial = *startJob.Execution
	}
	initial, err = execution.Initial(nil, false, execution.Overlay(initial.JobRequirements, opts.ExecutionRequirements))
	if err != nil {
		return jobref.Ref{}, err
	}
	for _, r := range recipes {
		if r.GetMetdata().ID == startJob.RecipeName {
			// Validate marked fields before persisting embedded recipe inputs.
			var inputs map[string]interface{}
			if startJob.Inputs != nil {
				inputs = make(map[string]interface{}, len(startJob.Inputs))
				for k, v := range startJob.Inputs {
					inputs[k] = v
				}
			}
			for k, def := range r.GetMetdata().InputSchema {
				if !def.Secret {
					continue
				}
				if err := def.ValidateSecretDeclaration(); err != nil {
					return jobref.Ref{}, fmt.Errorf("secret input %q: %w", k, err)
				}
				v, exists := inputs[k]
				if !exists {
					continue
				}
				if text, ok := v.(string); ok {
					if ref, ok := secrets.Template(text); ok {
						v = ref
						inputs[k] = v
					}
				}
				if _, ok, err := secrets.Parse(v); err != nil || !ok {
					return jobref.Ref{}, fmt.Errorf("secret input %q requires a reference, not plaintext", k)
				}
			}
			startJob.Inputs = inputs

			initial, err = execution.Initial(r.GetMetdata().Execution, true, initial.JobRequirements)
			if recipe.HasExecutionNeeds(r) {
				// The next task's scope can override the root and depend on prior
				// outputs. Do not advertise a guessed frontier at submission.
				initial, err = execution.Initial(nil, false, initial.JobRequirements)
			}
			if err != nil {
				return jobref.Ref{}, err
			}
		}
	}
	startJob.Execution = &initial
	recipeCount := len(recipes)
	artifacts := make([]jobdb.Artifact, recipeCount+len(startJob.Artifacts))
	for i, r := range recipes {
		recipeYaml, err := yaml.Marshal(&r)
		if err != nil {
			return jobref.Ref{}, err
		}
		name := r.GetMetadata().ID + RecipeArtifactSuffix
		artifacts[i] = jobdb.NewArtifactFromBytes(name, recipeYaml)
	}

	for i, a := range startJob.Artifacts {
		artifacts[recipeCount+i] = a
	}

	payload := startJob
	payload.Artifacts = nil
	inputData, err := jobdb.NewTaskData(payload, artifacts...)

	if err != nil {
		return jobref.Ref{}, err
	}

	meta := JobMetadataFromStartJob(startJob)
	metaRaw, err := json.Marshal(meta)
	if err != nil {
		return jobref.Ref{}, err
	}

	runPolicy := recipeRunPolicy(startJob, recipes)
	jobID := opts.JobID
	if jobID == "" {
		jobID = startJob.JobID
	}

	job := jobdb.SubmitJob{
		TenantId:     startJob.TenantId,
		JobType:      RecipeJobType,
		JobID:        jobID,
		Data:         inputData,
		RunPolicy:    runPolicy,
		Metadata:     metaRaw,
		Dependencies: append(append([]jobdb.Dependency(nil), startJob.Dependencies...), opts.Dependencies...),
	}
	if err := jobdbschema.SetSubmitJobSchema(&job); err != nil {
		return jobref.Ref{}, err
	}
	return submitter.SubmitJobRef(ctx, job)
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

// RestartRecipeJob starts a fresh continuation at a recorded, patchable scope.
type RestartOptions struct {
	Goal      string
	RequestID string
}

func RestartRecipeJob(ctx context.Context, engine any, prior jobdb.JobKey, scopeID string, patch *task.ContextPatch, opts RestartOptions) (jobref.Ref, error) {
	if strings.TrimSpace(scopeID) == "" {
		return jobref.Ref{}, fmt.Errorf("scope ID is required")
	}
	req := jobdb.SubmitRestartJob{PriorJobKey: prior, ExecutionRestart: &jobdb.ExecutionRestart{ScopeID: scopeID}}
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

	if patch != nil {
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
