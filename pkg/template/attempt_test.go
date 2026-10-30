package template

import (
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
