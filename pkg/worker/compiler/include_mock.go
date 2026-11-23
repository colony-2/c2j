package compiler

import (
	"context"
	"encoding/json"

	"github.com/colony-2/c2j/internal/includetest"
	"github.com/colony-2/c2j/internal/jsonexact"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

func runIncludeMock(ctx workflow.Context, caller, scope *template.ResolutionContext, meta recipe.NodeMetadata, inputs map[string]any) (bool, error) {
	if caller.Options.Mode == template.ModeValidate || meta.Internal == nil || meta.Internal.Inline == nil {
		return false, nil
	}
	control, ok := includetest.FromContext(ctx.JobContext.Context())
	if !ok {
		return false, nil
	}

	path := meta.Internal.Inline.CallsitePath
	callerContext := caller.TaskExecutionContext()
	stack := callerContext.InlineStack
	// Direct state includes add their own frame before entering the wrapper.
	// Remove that frame when checking whether this is a root recipe call site.
	if caller.ScopeType == template.ScopeState && len(stack) > 0 {
		last := stack[len(stack)-1]
		if last.CallsitePath == path && last.BoundaryNodePath == callerContext.Invocation.NodePath {
			stack = stack[:len(stack)-1]
		}
	}
	if len(stack) != 0 || !control.Matches(ctx.GetJobKey(), path) {
		return false, nil
	}

	command, err := json.Marshal(path)
	if err != nil {
		return true, err
	}
	if ctx.StageNodeExecution != nil {
		ctx.StageNodeExecution(scope.ExecutionNeeds)
	}
	result, err := ctx.DoTask(jobworkflow.TaskCall{
		Site: path, TaskType: includetest.TaskType, ContractVersion: 1, Command: command,
		Policy: jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 1}},
	}, func(build context.Context) (jobdb.TaskData, error) {
		view, err := caller.WithEvaluationContext(build)
		if err != nil {
			return nil, err
		}
		refs, err := resolveArtifactBindings(view, meta.Artifacts)
		if err != nil {
			return nil, err
		}
		active := scope.TaskExecutionContext()
		cell := active.Workflow.CellName
		if active.Workspace != nil {
			cell = active.Workspace.Cell
		}
		return jobdb.NewTaskData(includetest.Invocation{NodePath: path, Cell: cell, Inputs: inputs, ArtifactRefs: refs})
	})
	if err != nil {
		return true, err
	}
	data, err := result.GetData()
	if err != nil {
		return true, err
	}
	var response includetest.Result
	if err := jsonexact.Unmarshal(data, &response); err != nil {
		return true, err
	}
	if response.Failure != nil {
		return true, &jobdb.AppError{Payload: *response.Failure}
	}
	if response.Outputs == nil {
		response.Outputs = map[string]any{}
	}
	if err := validateExpectedOutputs(meta.ExpectOutputs, response.Outputs); err != nil {
		return true, control.Reject(path, err)
	}
	files, err := result.GetArtifacts()
	if err != nil {
		return true, err
	}
	return true, completeNodeScope(ctx, scope, template.ExecutionOutput{
		Outputs: response.Outputs, Artifacts: files, ArtifactRefs: artifactsToMap(files),
	})
}
