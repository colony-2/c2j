package recipe

import (
	"context"
	"encoding/json"
	"strings"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/swf-go/pkg/swf"
)

// RecipeInput defines the input for recipe activities
type SingleRecipe struct {
	Name      string                 `json:"name" validate:"required"`
	CellName  string                 `json:"cell_name,omitempty" default:"{{ context.workflow.cell }}"`
	CellPath  string                 `json:"cell_path,omitempty" default:"{{ context.workflow.cell_path }}"`
	Inputs    map[string]interface{} `json:"inputs"`
	Artifacts []recipeartifacts.Ref  `json:"artifacts"`
	Git       SingleRecipeGit        `json:"git"`
}

type SingleRecipeGit struct {
	BaseRepo string `json:"base_repo,omitempty" default:"{{ context.git.repo }}"`
	BaseRef  string `json:"base_ref,omitempty" default:"{{ context.git.ref }}"`
	BaseHash string `json:"base_hash,omitempty" default:"{{ context.git.resolved_hash }}"`
	Author   string `json:"author,omitempty" default:"{{ context.git.author }}"`
}

type SingleRecipeWithRef struct {
	SingleRecipe `json:",squash"`
	GitRef       string `json:"git_ref" default:"${{ has(context.git.hash) && context.git.hash != \"\" ? context.git.hash : context.git.ref }}" validate:"required"`
}

type MultipleRecipes struct {
	GitRef  string         `json:"git_ref" default:"${{ has(context.git.hash) && context.git.hash != \"\" ? context.git.hash : context.git.ref }}" validate:"required"`
	Recipes []SingleRecipe `json:"recipes"`
}

type StartedJob struct {
	JobId string `json:"job_id"`
}

type SingleRecipeOutput struct {
	Outputs   map[string]interface{}      `json:"outputs"`
	GitResult contextual.GitCommitContext `json:"git,omitempty"`
}

type MultipleRecipeOutput struct {
	Outputs []SingleRecipeOutput `json:"outputs"`
}

type StartedJobs struct {
	JobIDs []string `json:"job_ids"`
}

func GetOps() []ops.RegisterableOp {
	list := []ops.RegisterableOp{}

	list = append(list, ops.NewOp().
		WithType("recipe.await_result").
		AddStep("wait_and_get", ops.NewStepWithDeps[StartedJob, SingleRecipeOutput](waitAndGetRecipeOutput)).
		BuildOrPanic(),
	)
	list = append(list, ops.NewOp().
		WithType("recipe.get_result").
		AddStep("get", ops.NewStepWithDeps[StartedJob, SingleRecipeOutput](getRecipeOutput)).
		BuildOrPanic(),
	)

	// single recipe sync
	list = append(list, ops.NewOp().
		WithType("recipe.run_and_get_result").
		AddStep("start", ops.NewStepWithDeps[SingleRecipeWithRef, StartedJob](startSingleJob)).
		AddStep("finish", ops.NewStepWithDeps[StartedJob, SingleRecipeOutput](waitAndGetRecipeOutput)).
		BuildOrPanic(),
	)

	list = append(list, ops.NewOp().
		WithType("recipes.run_and_wait").
		AddStep("start", ops.NewStepWithDeps[MultipleRecipes, StartedJobs](startMultipleJobs)).
		AddStep("await", ops.NewStepWithDeps[StartedJobs, StartedJobs](waitAndNoResult)).
		BuildOrPanic(),
	)

	// mutliple recipes sync
	list = append(list, ops.NewOp().
		WithType("recipes.run").
		AddStep("start", ops.NewStepWithDeps[MultipleRecipes, StartedJobs](startMultipleJobs)).
		BuildOrPanic(),
	)

	return list
}

func waitAndNoResult(deps ops.OpDependencies, ctx context.Context, input StartedJobs) (StartedJobs, error) {
	err := deps.JobTool().AwaitJobs(input.JobIDs...)
	return input, err
}

func waitAndGetRecipeOutput(deps ops.OpDependencies, ctx context.Context, input StartedJob) (SingleRecipeOutput, error) {
	err := deps.JobTool().AwaitJobs(input.JobId)
	if err != nil {
		return SingleRecipeOutput{}, err
	}
	return getRecipeOutput(deps, ctx, input)
}

func getRecipeOutput(deps ops.OpDependencies, ctx context.Context, input StartedJob) (SingleRecipeOutput, error) {
	var zero SingleRecipeOutput
	data, err := deps.WorkflowControl().JobResult(ctx, swf.JobKey{deps.JobTool().GetJobKey().TenantId, input.JobId})
	if err != nil {
		return zero, err
	}

	jd, err := data.GetData()
	if err != nil {
		return zero, err
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(jd, &raw); err != nil {
		return zero, err
	}

	outputs := map[string]interface{}{}
	artifactRefs := map[string]recipeartifacts.Ref{}
	if wrapped, ok := raw["output"]; ok {
		if cast, ok := wrapped.(map[string]interface{}); ok {
			outputs = cast
		}
	} else {
		outputs = raw
	}
	if wrapped, ok := raw["artifact_refs"]; ok {
		buf, err := json.Marshal(wrapped)
		if err != nil {
			return zero, err
		}
		if err := json.Unmarshal(buf, &artifactRefs); err != nil {
			return zero, err
		}
	}

	artifacts, err := data.GetArtifacts()
	if err != nil {
		return zero, err
	}

	for _, a := range artifacts {
		err = deps.AddOutputArtifact(a)
		if err != nil {
			return zero, err
		}
	}
	for name, artifactRef := range artifactRefs {
		if artifactRef.External == nil {
			continue
		}
		if err := deps.AddExternalArtifact(name, artifactRef.External.URL, artifactRef.External.Expand); err != nil {
			return zero, err
		}
	}

	return SingleRecipeOutput{Outputs: outputs}, nil
}

func startSingleJob(deps ops.OpDependencies, ctx context.Context, input SingleRecipeWithRef) (StartedJob, error) {
	parentJobKey := deps.JobTool().GetJobKey()
	recipeSourceRepo, recipeSourceRef := currentRecipeSource(deps)
	keys, err := startJobs(ctx, parentJobKey, currentInvocation(deps), deps.WorkflowControl(), recipeSourceRepo, recipeSourceRef, []SingleRecipe{input.SingleRecipe}, input.GitRef)
	if err != nil {
		return StartedJob{}, err
	}
	return StartedJob{JobId: keys[0].JobId}, nil
}

func startMultipleJobs(deps ops.OpDependencies, ctx context.Context, input MultipleRecipes) (StartedJobs, error) {
	resolved := make([]SingleRecipe, len(input.Recipes))
	for i, recipe := range input.Recipes {
		resolved[i] = recipe
	}
	recipeSourceRepo, recipeSourceRef := currentRecipeSource(deps)
	keys, err := startJobs(ctx, deps.JobTool().GetJobKey(), currentInvocation(deps), deps.WorkflowControl(), recipeSourceRepo, recipeSourceRef, resolved, input.GitRef)
	if err != nil {
		return StartedJobs{}, err
	}

	ids := make([]string, len(keys))
	for i, k := range keys {
		ids[i] = k.JobId
	}
	return StartedJobs{JobIDs: ids}, nil
}

func currentInvocation(deps ops.OpDependencies) contextual.Invocation {
	gitContext := deps.GitContext()
	return contextual.Invocation{
		NodePath:  gitContext.NodePath,
		InvokeSeq: gitContext.InvokeSeq,
	}
}

func currentRecipeSource(deps ops.OpDependencies) (string, string) {
	gitContext := deps.GitContext()

	repo := strings.TrimSpace(gitContext.RecipeSourceRepo)
	if repo == "" {
		repo = strings.TrimSpace(gitContext.BaseRepo)
	}

	ref := strings.TrimSpace(gitContext.RecipeSourceRef)
	if ref == "" {
		ref = strings.TrimSpace(gitContext.BaseRef)
	}

	return repo, ref
}
