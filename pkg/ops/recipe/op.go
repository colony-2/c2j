package recipe

import (
	"context"
	"fmt"
	"strings"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// RecipeInput defines the input for recipe activities
type SingleRecipe struct {
	Wait      WaitPolicy             `json:"wait,omitempty"`
	Name      string                 `json:"name" validate:"required"`
	CellName  string                 `json:"cell_name,omitempty" default:"{{ context.workspace.cell }}"`
	Inputs    map[string]interface{} `json:"inputs" secret:"forward"`
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
	Wait    WaitPolicy     `json:"wait,omitempty"`
	GitRef  string         `json:"git_ref" default:"${{ has(context.git.hash) && context.git.hash != \"\" ? context.git.hash : context.git.ref }}" validate:"required"`
	Recipes []SingleRecipe `json:"recipes"`
}

type ResultInput struct {
	Target WaitTarget `json:"target"`
	Wait   WaitPolicy `json:"wait,omitempty"`
}

func (in ResultInput) started() (StartedJob, error) {
	if (in.Target.Job == "") == (in.Target.Goal == "") {
		return StartedJob{}, fmt.Errorf("target requires exactly one job or goal")
	}
	return StartedJob{Target: in.Target, Wait: in.Wait}, nil
}

type StartedJob struct {
	Result   *ResultSource `json:"result,omitempty"`
	JobID    string        `json:"job_id"`
	GoalID   string        `json:"goal_id"`
	TenantID string        `json:"tenant_id"`
	Target   WaitTarget    `json:"target,omitempty"`
	Wait     WaitPolicy    `json:"wait,omitempty"`
}

type SingleRecipeOutput struct {
	Result    ResultSource                   `json:"result"`
	Outputs   map[string]interface{}         `json:"outputs"`
	Artifacts map[string]recipeartifacts.Ref `json:"artifacts,omitempty"`
	GitResult contextual.GitCommitContext    `json:"git,omitempty"`
}

type MultipleRecipeOutput struct {
	Outputs []SingleRecipeOutput `json:"outputs"`
}

type StartedJobs struct {
	GoalIDs []string     `json:"goal_ids"`
	Items   []StartedJob `json:"items"`
	Wait    WaitPolicy   `json:"wait,omitempty"`
	JobIDs  []string     `json:"job_ids"`
}

func GetOps() []ops.RegisterableOp {
	list := []ops.RegisterableOp{}

	list = append(list, ops.NewOp().
		WithType("recipe.await_result").
		AddStep("wait_and_get", ops.NewStepWithDeps[ResultInput, SingleRecipeOutput](func(d ops.OpDependencies, c context.Context, in ResultInput) (SingleRecipeOutput, error) {
			s, e := in.started()
			if e != nil {
				return SingleRecipeOutput{}, e
			}
			return waitAndGetRecipeOutput(d, c, s)
		})).
		BuildOrPanic(),
	)
	list = append(list, ops.NewOp().
		WithType("recipe.await_result_soft").
		AddStep("inspect", ops.NewStepWithDeps[AwaitResultSoftInput, ChildStatusOutput](awaitResultSoft)).
		BuildOrPanic(),
	)
	list = append(list, ops.NewOp().
		WithType("recipe.get_result").
		AddStep("get", ops.NewStepWithDeps[ResultInput, SingleRecipeOutput](func(d ops.OpDependencies, c context.Context, in ResultInput) (SingleRecipeOutput, error) {
			s, e := in.started()
			if e != nil {
				return SingleRecipeOutput{}, e
			}
			return getRecipeOutput(d, c, s)
		})).
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

	list = append(list, getChildGroupOps()...)

	return list
}

func waitAndNoResult(deps ops.OpDependencies, ctx context.Context, input StartedJobs) (StartedJobs, error) {
	for i, item := range input.Items {
		binding, err := awaitChild(deps, ctx, item, false)
		if err != nil {
			return input, err
		}
		input.Items[i].Result = &ResultSource{Goal: binding.Target.GoalID, Job: binding.SatisfiedByJobID}
	}
	return input, nil
}

func waitAndGetRecipeOutput(deps ops.OpDependencies, ctx context.Context, input StartedJob) (SingleRecipeOutput, error) {
	binding, err := awaitChild(deps, ctx, input, true)
	if err != nil {
		return SingleRecipeOutput{}, err
	}
	result, err := getRecipeOutput(deps, ctx, StartedJob{JobID: binding.SatisfiedByJobID})
	result.Result = ResultSource{Goal: binding.Target.GoalID, Job: binding.SatisfiedByJobID}
	return result, err
}

func getRecipeOutput(deps ops.OpDependencies, ctx context.Context, input StartedJob) (SingleRecipeOutput, error) {
	var zero SingleRecipeOutput
	if deps == nil || deps.JobTool() == nil || deps.WorkflowControl() == nil {
		return zero, fmt.Errorf("result collection requires job tool and workflow control")
	}
	if input.Target.Job != "" && input.Target.Goal != "" {
		return zero, fmt.Errorf("target requires exactly one job or goal")
	}
	if input.Target.Goal != "" || input.Target.Job != "" {
		if input.Target.Goal != "" {
			inspector, ok := deps.WorkflowControl().(goalInspector)
			if !ok {
				return zero, fmt.Errorf("goal inspection unavailable")
			}
			goal, err := inspector.GetGoal(ctx, jobdb.GoalKey{TenantId: deps.JobTool().GetJobKey().TenantId, GoalID: input.Target.Goal})
			if err != nil {
				return zero, err
			}
			if goal.State == jobdb.GoalFailed && goal.Failure != nil {
				return zero, &jobdb.GoalFailedError{Failure: *goal.Failure}
			}
			if goal.State != jobdb.GoalSucceeded {
				return zero, fmt.Errorf("goal %s is still open", input.Target.Goal)
			}
			input.JobID = goal.SuccessfulJobID
			input.GoalID = goal.GoalKey.GoalID
		} else {
			input.JobID = input.Target.Job
		}
	}
	if input.JobID == "" {
		return zero, fmt.Errorf("result job is required")
	}
	data, err := deps.WorkflowControl().JobResult(ctx, jobdb.JobKey{TenantId: deps.JobTool().GetJobKey().TenantId, JobId: input.JobID})
	if err != nil {
		return zero, err
	}

	decoded, err := decodeRecipeJobOutput(deps, data)
	if err != nil {
		return zero, err
	}

	return SingleRecipeOutput{Outputs: decoded.Outputs, Artifacts: decoded.Artifacts, GitResult: decoded.GitResult, Result: ResultSource{Job: input.JobID, Goal: input.GoalID}}, nil
}

func startSingleJob(deps ops.OpDependencies, ctx context.Context, input SingleRecipeWithRef) (StartedJob, error) {
	wait, err := input.Wait.normalized(true)
	if err != nil {
		return StartedJob{}, err
	}
	input.Wait = wait
	parentJobKey := deps.JobTool().GetJobKey()
	recipeSourceRepo, recipeSourceRef := currentRecipeSource(deps)
	keys, err := startJobs(ctx, deps, parentJobKey, currentInvocation(deps), deps.WorkflowControl(), recipeSourceRepo, recipeSourceRef, []SingleRecipe{input.SingleRecipe}, input.GitRef, parentContextFromDeps(deps))
	if err != nil {
		return StartedJob{}, err
	}
	return StartedJob{JobID: keys[0].JobID, GoalID: keys[0].GoalID, TenantID: keys[0].TenantID, Wait: input.Wait}, nil
}

func startMultipleJobs(deps ops.OpDependencies, ctx context.Context, input MultipleRecipes) (StartedJobs, error) {
	wait, err := input.Wait.normalized(false)
	if err != nil {
		return StartedJobs{}, err
	}
	input.Wait = wait
	resolved := make([]SingleRecipe, len(input.Recipes))
	for i, recipe := range input.Recipes {
		resolved[i] = recipe
		childWait := input.Wait
		if recipe.Wait.On != "" {
			childWait.On = recipe.Wait.On
			childWait.Condition = recipe.Wait.Condition
		}
		if recipe.Wait.Condition != "" {
			childWait.Condition = recipe.Wait.Condition
		}
		childWait, err = childWait.normalized(false)
		if err != nil {
			return StartedJobs{}, err
		}
		resolved[i].Wait = childWait
	}
	recipeSourceRepo, recipeSourceRef := currentRecipeSource(deps)
	keys, err := startJobs(ctx, deps, deps.JobTool().GetJobKey(), currentInvocation(deps), deps.WorkflowControl(), recipeSourceRepo, recipeSourceRef, resolved, input.GitRef, parentContextFromDeps(deps))
	if err != nil {
		return StartedJobs{}, err
	}

	ids := make([]string, len(keys))
	for i, k := range keys {
		ids[i] = k.JobID
	}
	out := StartedJobs{JobIDs: ids, GoalIDs: make([]string, len(keys)), Items: make([]StartedJob, len(keys)), Wait: input.Wait}
	for i, k := range keys {
		out.GoalIDs[i] = k.GoalID
		wait := resolved[i].Wait
		out.Items[i] = StartedJob{JobID: k.JobID, GoalID: k.GoalID, TenantID: k.TenantID, Wait: wait}
	}
	return out, nil
}

func currentInvocation(deps ops.OpDependencies) string {
	return deps.JobTool().InvocationID()
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

func parentContextFromDeps(deps ops.OpDependencies) jobcontext.Parent {
	if deps == nil {
		return jobcontext.Parent{}
	}
	if current := deps.CurrentJobContext(); current.HasJob() {
		return jobcontext.ParentFromCurrent(current)
	}
	parent := jobcontext.Parent{}
	if jobTool := deps.JobTool(); jobTool != nil {
		key := jobTool.GetJobKey()
		parent.TenantID = strings.TrimSpace(key.TenantId)
		parent.JobID = strings.TrimSpace(key.JobId)
		parent.JobType = starter.RecipeJobType
		if tool, ok := jobTool.(interface{ GetJobRef() jobref.Ref }); ok {
			parent.GoalID = tool.GetJobRef().GoalID
		}
	}
	gitContext := deps.GitContext()
	parent.CellName = strings.TrimSpace(gitContext.CellName)
	parent.RepositorySource, parent.GitRef = currentRecipeSource(deps)
	parent.InvocationPath = strings.TrimSpace(gitContext.NodePath)
	parent.InvocationSequence = gitContext.InvokeSeq
	parent.InvocationHash = strings.TrimSpace(gitContext.InvokeHash)
	if parent.HasJob() {
		return parent
	}
	return jobcontext.Parent{}
}
