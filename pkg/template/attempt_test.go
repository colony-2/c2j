package template

import (
	"encoding/json"
	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/cellref"
	"github.com/colony-2/c2j/pkg/toolenv"
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/stretchr/testify/require"
)

func TestRestartCloneIsolatesStateAndPublishesSharedWorkspaceCounters(t *testing.T) {
	root, err := NewRecipeResolutionContext(&contextual.GitCommitContext{PersistHash: "initial"}, nil, contextual.JobContext{})
	require.NoError(t, err)
	root.TemplateData.Sequence["previous"] = StepOutput{Outputs: map[string]any{"value": "initial"}}
	op, err := root.NewChildContext(ScopeOp, recipe.NodeMetadata{ID: "op"}, "", nil)
	require.NoError(t, err)
	attempt, err := op.CloneForRestart()
	require.NoError(t, err)
	abandoned, err := op.CloneForRestart()
	require.NoError(t, err)
	attempt.TemplateData.Sequence["previous"].Outputs["value"] = "committed"
	attempt.UpdateGitState(contextual.GitCommitContext{PersistHash: "committed"})
	require.Equal(t, "committed", attempt.TaskExecutionContext().GitTask.PersistHash)
	require.Equal(t, "initial", abandoned.TaskExecutionContext().GitTask.PersistHash)
	firstWorkspace := attempt.NextWorkspaceID("workspace")
	require.Equal(t, "initial", root.TemplateData.Sequence["previous"].Outputs["value"])
	require.Equal(t, "initial", root.GetGitCommitContext().PersistHash)
	require.Equal(t, "committed", attempt.Parent.TemplateData.Sequence["previous"].Outputs["value"])
	op.AdoptRestart(attempt)
	require.Equal(t, "committed", root.TemplateData.Sequence["previous"].Outputs["value"])
	require.Equal(t, "committed", root.GetGitCommitContext().PersistHash)
	repeated, err := root.NewChildContext(ScopeOp, recipe.NodeMetadata{ID: "op"}, "", nil)
	require.NoError(t, err)
	require.NotEqual(t, firstWorkspace, repeated.NextWorkspaceID("workspace"), "completed attempts publish counters to enclosing scopes")
	// A discarded restart patch remains isolated from the installed graph.
	abandoned.UpdateGitState(contextual.GitCommitContext{PersistHash: "late"})
	abandoned.TemplateData.Sequence["previous"].Outputs["value"] = "late"
	require.Equal(t, "committed", root.GetGitCommitContext().PersistHash)
	require.Equal(t, "committed", root.TemplateData.Sequence["previous"].Outputs["value"])
}

func TestWorkspaceIdentitySurvivesRestartWithNewJobAndGoal(t *testing.T) {
	original, err := NewRecipeResolutionContext(&contextual.GitCommitContext{}, nil, contextual.JobContext{
		Workflow: contextual.WorkflowContext{JobID: "original", GoalID: "original-goal"},
	})
	require.NoError(t, err)
	restarted, err := NewRecipeResolutionContext(&contextual.GitCommitContext{}, nil, contextual.JobContext{
		Workflow: contextual.WorkflowContext{JobID: "replacement", GoalID: "new-goal"},
	})
	require.NoError(t, err)
	first := original.NextWorkspaceID("workspace")
	require.Equal(t, first, restarted.NextWorkspaceID("workspace"))
	next := original.NextWorkspaceID("workspace")
	require.NotEqual(t, first, next, "repeated visits still have distinct identities")
	require.Equal(t, next, restarted.NextWorkspaceID("workspace"))
}

func TestRestartNumericResolver(t *testing.T) {
	root, err := NewRecipeResolutionContext(&contextual.GitCommitContext{}, nil, contextual.JobContext{})
	require.NoError(t, err)
	defaults, err := root.ResolveCompositeInputs(map[string]any{"prior_rebase_repairs": "${{ inputs.?prior_rebase_repairs.orValue(0) }}"})
	require.NoError(t, err)
	require.IsType(t, int64(0), defaults["prior_rebase_repairs"])
	sm, err := root.NewChildContext(ScopeStateMachine, recipe.NodeMetadata{ID: "develop"}, "", defaults)
	require.NoError(t, err)
	check := func(rc *ResolutionContext) {
		t.Helper()
		out, err := rc.ResolveMap(map[string]any{"counter": "${{ inputs.prior_rebase_repairs + (state_exists('rebase_repair') ? size(states.rebase_repair.runs) + 1 : 0) }}"})
		require.NoError(t, err)
		require.Equal(t, int64(0), out["counter"])
	}
	check(sm)
	check(sm)
	op, err := sm.NewChildContext(ScopeOp, recipe.NodeMetadata{ID: "target"}, "", nil)
	require.NoError(t, err)
	attempt, err := op.CloneForRestart()
	require.NoError(t, err)
	check(attempt.Parent)
	op.AdoptRestart(attempt)
	check(sm)
}

func TestRestartPayloadTypesAndIsolation(t *testing.T) {
	root, err := NewRecipeResolutionContext(&contextual.GitCommitContext{}, nil, contextual.JobContext{})
	require.NoError(t, err)
	payload := map[string]any{"numbers": []any{int64(9007199254740993), int64(-9007199254740993), int64(9223372036854775807), int64(-9223372036854775808), float64(1), 1.25}, "typed": map[string][]int64{"x": {3}}, "nil": []any(nil), "empty": []any{}}
	root.TemplateData.ContainerInputs = cloneTemplateVars(payload)
	root.TemplateData.Vars = cloneTemplateVars(payload)
	root.TemplateData.Outputs = cloneTemplateVars(payload)
	root.TemplateData.Locals = cloneTemplateVars(payload)
	root.TemplateData.Transition.Payload = cloneTemplateVars(payload)
	root.TemplateData.Failure = &recipe.RuntimeFailure{Attrs: cloneTemplateVars(payload)}
	root.TemplateData.Transition.Failure = root.TemplateData.Failure
	root.TemplateData.Context.Workspace = &contextual.WorkspaceContext{Cell: "original"}
	root.TemplateData.Context.InlineStack = []contextual.InlineBoundaryFrame{{RecipeID: "original"}}
	root.TemplateData.Sequence["s"] = StepOutput{Outputs: cloneTemplateVars(payload), Runs: []RunOutput{{Outputs: cloneTemplateVars(payload)}}}
	root.TemplateData.States["s"] = root.TemplateData.Sequence["s"]
	root.lastExecution = cloneTemplateVars(payload)
	exact, err := root.CloneForRestart()
	require.NoError(t, err)
	require.Equal(t, root.TemplateData, exact.TemplateData)
	installed, err := root.CloneForRestart()
	require.NoError(t, err)
	installed.AdoptRestart(exact)
	require.Equal(t, root.TemplateData, installed.TemplateData)
	for _, rc := range []*ResolutionContext{root, exact, installed} {
		out, err := rc.ResolveMap(map[string]any{"n": "${{ inputs.numbers[0] }}", "d": "${{ inputs.numbers[4] + inputs.numbers[5] }}"})
		require.NoError(t, err)
		require.Equal(t, int64(9007199254740993), out["n"])
		require.Equal(t, 2.25, out["d"])
		_, err = rc.ResolveMap(map[string]any{"n": "${{ inputs.numbers[4] + 1 }}"})
		require.Error(t, err)
	}
	attempt, err := root.CloneForRestart()
	require.NoError(t, err)
	sibling, err := root.CloneForRestart()
	require.NoError(t, err)
	require.Equal(t, root.TemplateData, attempt.TemplateData)
	mapsToCheck := []map[string]any{attempt.TemplateData.ContainerInputs, attempt.TemplateData.Vars, attempt.TemplateData.Outputs, attempt.TemplateData.Locals, attempt.TemplateData.Transition.Payload, attempt.TemplateData.Failure.Attrs, attempt.TemplateData.Transition.Failure.Attrs, attempt.TemplateData.Sequence["s"].Outputs, attempt.TemplateData.Sequence["s"].Runs[0].Outputs, attempt.TemplateData.States["s"].Outputs, attempt.lastExecution}
	for _, m := range mapsToCheck {
		require.Equal(t, payload, m)
		m["numbers"].([]any)[0] = int64(7)
		m["typed"].(map[string][]int64)["x"][0] = 8
	}
	attempt.TemplateData.Context.Workspace.Cell = "changed"
	attempt.TemplateData.Context.InlineStack[0].RecipeID = "changed"
	require.Equal(t, payload, root.TemplateData.ContainerInputs)
	require.Equal(t, root.TemplateData, sibling.TemplateData)
	root.AdoptRestart(attempt)
	sibling.TemplateData.ContainerInputs["numbers"].([]any)[0] = int64(99)
	require.Equal(t, int64(7), root.TemplateData.ContainerInputs["numbers"].([]any)[0])
	root.TemplateData.ContainerInputs = map[string]any{"double": float64(1), "fraction": 1.25}
	out, err := root.ResolveMap(map[string]any{"v": "${{ inputs.double + inputs.fraction }}"})
	require.NoError(t, err)
	require.Equal(t, 2.25, out["v"])
	_, err = root.ResolveMap(map[string]any{"v": "${{ inputs.double + 1 }}"})
	require.Error(t, err)
}

func TestRestartMetadataAndGraphIsolation(t *testing.T) {
	root, err := NewRecipeResolutionContext(&contextual.GitCommitContext{}, nil, contextual.JobContext{})
	require.NoError(t, err)
	root.ToolScopes = []toolenv.Scope{{Packages: []string{"original"}}}
	ref := artifacts.Ref{External: &artifacts.ExternalRef{URL: "original"}, Value: json.RawMessage(`{"n":1}`)}
	root.TemplateData.Context.Artifacts = map[string]artifacts.Ref{"a": ref}
	root.TemplateData.Context.CellResolution = &cellref.Context{SelfCell: "original"}
	code := 2
	root.TemplateData.Failure = &recipe.RuntimeFailure{Task: &recipe.FailureTask{ExitCode: &code}, Cause: &recipe.RuntimeFailure{Attrs: map[string]any{"list": []any{int64(3)}}}, Artifacts: map[string]artifacts.Ref{"a": ref}}
	root.lastArtifactRefs = []artifacts.Ref{ref}
	sm, err := root.NewChildContext(ScopeStateMachine, recipe.NodeMetadata{ID: "sm"}, "", map[string]any{})
	require.NoError(t, err)
	state, err := sm.NewChildContext(ScopeState, recipe.NodeMetadata{ID: "state"}, "state", nil)
	require.NoError(t, err)
	op, err := state.NewChildContext(ScopeOp, recipe.NodeMetadata{ID: "op"}, "", nil)
	require.NoError(t, err)
	// A workspace caller may be reachable by both routes; retain node identity.
	op.workspaceCaller = sm
	clone, err := op.CloneForRestart()
	require.NoError(t, err)
	require.Same(t, clone.Parent.Parent, clone.workspaceCaller)
	require.Same(t, clone.commitContext, clone.Parent.commitContext)
	clone.TemplateData.States["new"] = StepOutput{Outputs: map[string]any{"n": int64(4)}}
	require.Contains(t, clone.Parent.Parent.TemplateData.States, "new")
	require.NotContains(t, sm.TemplateData.States, "new")
	clonedRoot := clone.Parent.Parent.Parent
	clonedRoot.ToolScopes[0].Packages[0] = "changed"
	clonedRoot.TemplateData.Context.CellResolution.SelfCell = "changed"
	clonedRoot.TemplateData.Context.Artifacts["a"].External.URL = "changed"
	clonedRoot.TemplateData.Context.Artifacts["a"].Value[0] = ' '
	*clonedRoot.TemplateData.Failure.Task.ExitCode = 7
	clonedRoot.TemplateData.Failure.Cause.Attrs["list"].([]any)[0] = int64(8)
	clonedRoot.lastArtifactRefs[0].External.URL = "last"
	require.Equal(t, "original", root.ToolScopes[0].Packages[0])
	require.Equal(t, "original", root.TemplateData.Context.CellResolution.SelfCell)
	require.Equal(t, "original", root.TemplateData.Context.Artifacts["a"].External.URL)
	require.Equal(t, byte('{'), root.TemplateData.Context.Artifacts["a"].Value[0])
	require.Equal(t, 2, *root.TemplateData.Failure.Task.ExitCode)
	require.Equal(t, int64(3), root.TemplateData.Failure.Cause.Attrs["list"].([]any)[0])
	require.Equal(t, "original", root.lastArtifactRefs[0].External.URL)
	op.AdoptRestart(clone)
	require.Equal(t, int64(4), sm.TemplateData.States["new"].Outputs["n"])
	empty := templateData{ContainerInputs: map[string]any{}, Sequence: map[string]StepOutput{}, Locals: map[string]any{"nilmap": map[string]any(nil), "empty": []any{}}}
	require.Equal(t, empty, cloneRestartData(empty))
	require.Nil(t, cloneRestartData(templateData{}).Transition.Payload)
}
