package template

import (
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/stretchr/testify/require"
)

func TestAttemptIsolatesStateAndPublishesSharedWorkspaceCounters(t *testing.T) {
	root, err := NewRecipeResolutionContext(&contextual.GitCommitContext{PersistHash: "initial"}, nil, contextual.JobContext{})
	require.NoError(t, err)
	root.TemplateData.Sequence["previous"] = StepOutput{Outputs: map[string]any{"value": "initial"}}
	op, err := root.NewChildContext(ScopeOp, recipe.NodeMetadata{ID: "op"}, "", nil)
	require.NoError(t, err)
	attempt, err := op.CloneAttempt()
	require.NoError(t, err)
	abandoned, err := op.CloneAttempt()
	require.NoError(t, err)
	attempt.TemplateData.Sequence["previous"].Outputs["value"] = "committed"
	attempt.UpdateGitState(contextual.GitCommitContext{PersistHash: "committed"})
	firstWorkspace := attempt.NextWorkspaceID("workspace")
	require.Equal(t, "initial", root.TemplateData.Sequence["previous"].Outputs["value"])
	require.Equal(t, "initial", root.GetGitCommitContext().PersistHash)
	require.Equal(t, "committed", attempt.Parent.TemplateData.Sequence["previous"].Outputs["value"])
	op.AdoptAttempt(attempt)
	require.Equal(t, "committed", root.TemplateData.Sequence["previous"].Outputs["value"])
	require.Equal(t, "committed", root.GetGitCommitContext().PersistHash)
	repeated, err := root.NewChildContext(ScopeOp, recipe.NodeMetadata{ID: "op"}, "", nil)
	require.NoError(t, err)
	require.NotEqual(t, firstWorkspace, repeated.NextWorkspaceID("workspace"), "completed attempts publish counters to enclosing scopes")
	// A canceled callback may finish late; its private graph cannot overwrite the winner.
	abandoned.UpdateGitState(contextual.GitCommitContext{PersistHash: "late"})
	abandoned.TemplateData.Sequence["previous"].Outputs["value"] = "late"
	require.Equal(t, "committed", root.GetGitCommitContext().PersistHash)
	require.Equal(t, "committed", root.TemplateData.Sequence["previous"].Outputs["value"])
}
