package starter

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"gopkg.in/yaml.v3"
)

type recipeJobSubmitter interface {
	SubmitJob(ctx context.Context, job jobdb.SubmitJob) (jobdb.JobKey, error)
}

type recipeJobRestartSubmitter interface {
	SubmitRestartJob(ctx context.Context, req jobdb.SubmitRestartJob) (jobdb.JobKey, error)
}

const (
	RecipeJobType        = "recipe"
	RecipeArtifactSuffix = ".recipe.yaml"
)

const (
	JobMetadataVersion = 1

	MetaFieldVersion  jobdb.FieldName = "v"
	MetaFieldRecipe   jobdb.FieldName = "recipe"
	MetaFieldCellID   jobdb.FieldName = "cell_id"
	MetaFieldCellName jobdb.FieldName = "cell_name"
	MetaFieldRepo     jobdb.FieldName = "repo"
	MetaFieldGitRef   jobdb.FieldName = "git_ref"
)

type JobMetadata struct {
	Version          int    `json:"v"`
	RecipeName       string `json:"recipe,omitempty"`
	CellID           string `json:"cell_id,omitempty"`
	CellName         string `json:"cell_name,omitempty"`
	RepositorySource string `json:"repo,omitempty"`
	GitRef           string `json:"git_ref,omitempty"`
}

func JobMetadataFromStartJob(startJob workflowctl.StartJob) JobMetadata {
	repo := startJob.JobContext.GitBase.BaseRepo
	if repo == "" {
		repo = startJob.JobContext.RecipeSource.Repo
	}
	return JobMetadata{
		Version:          JobMetadataVersion,
		RecipeName:       startJob.RecipeName,
		CellID:           startJob.JobContext.Workflow.CellID,
		CellName:         startJob.JobContext.Workflow.CellName,
		RepositorySource: repo,
		GitRef:           startJob.GitRef,
	}
}

func StartRecipeJob(ctx context.Context, startJob workflowctl.StartJob, engine recipeJobSubmitter, recipes ...recipe.Recipe) (jobdb.JobKey, error) {
	return StartRecipeJobWithOptions(ctx, startJob, engine, StartRecipeJobOptions{}, recipes...)
}

type StartRecipeJobOptions struct {
	JobID         string
	Prerequisites []jobdb.JobPrerequisite
}

func StartRecipeJobWithOptions(ctx context.Context, startJob workflowctl.StartJob, engine recipeJobSubmitter, opts StartRecipeJobOptions, recipes ...recipe.Recipe) (jobdb.JobKey, error) {
	recipeCount := len(recipes)
	artifacts := make([]jobdb.Artifact, recipeCount+len(startJob.Artifacts))
	for i, r := range recipes {
		recipeYaml, err := yaml.Marshal(&r)
		if err != nil {
			return jobdb.JobKey{}, err
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
		return jobdb.JobKey{}, err
	}

	meta := JobMetadataFromStartJob(startJob)
	metaRaw, err := json.Marshal(meta)
	if err != nil {
		return jobdb.JobKey{}, err
	}

	runPolicy := recipeRunPolicy(startJob, recipes)
	jobID := opts.JobID
	if jobID == "" {
		jobID = startJob.JobID
	}

	job := jobdb.SubmitJob{
		TenantId:      startJob.TenantId,
		JobType:       RecipeJobType,
		JobID:         jobID,
		Data:          inputData,
		RunPolicy:     runPolicy,
		Metadata:      metaRaw,
		Prerequisites: opts.Prerequisites,
	}
	return engine.SubmitJob(ctx, job)
}

func recipeRunPolicy(startJob workflowctl.StartJob, recipes []recipe.Recipe) jobdb.RunPolicy {
	policy := jobdb.DefaultRunPolicy()
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

// RestartRecipeJob restarts an existing recipe job from the provided step offset.
//
// stepOffset is the next chapter ordinal to execute (0-based). Internally SWF uses LastStepToKeep,
// so we keep chapters up to stepOffset-1.
//
// If patch is non-nil, we inject a context patch envelope as the next chapter output to be replayed.
// This intentionally causes a swf.TaskInputMismatchError at replay time, allowing the recipe worker
// to detect and apply the patch before re-executing the task.
func RestartRecipeJob(ctx context.Context, engine recipeJobRestartSubmitter, prior jobdb.JobKey, stepOffset int64, patch *task.ContextPatch) (jobdb.JobKey, error) {
	if stepOffset < 0 {
		return jobdb.JobKey{}, fmt.Errorf("stepOffset must be >= 0, got %d", stepOffset)
	}
	lastToKeep := stepOffset - 1

	req := jobdb.SubmitRestartJob{
		PriorJobKey:    prior,
		LastStepToKeep: lastToKeep,
	}

	if patch != nil {
		env, err := task.NewOutputEnvelope(task.OutputKindContextPatch, patch)
		if err != nil {
			return jobdb.JobKey{}, err
		}
		out, err := jobdb.NewTaskData(env)
		if err != nil {
			return jobdb.JobKey{}, err
		}

		// Use an input that will not match normal ActivityInvocationRequest inputs, so the worker
		// receives TaskInputMismatchError and can inspect the cached patch output.
		in, err := jobdb.NewTaskData(map[string]any{"kind": string(task.OutputKindContextPatch)})
		if err != nil {
			return jobdb.JobKey{}, err
		}

		req.ExtraTaskInput = in
		req.ExtraTaskOutput = out
	}

	return engine.SubmitRestartJob(ctx, req)
}
