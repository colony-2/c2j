package template

import (
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/recipe"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceSharesGitStateWithinBoundaryAndIsolatesPatches(t *testing.T) {
	root, err := NewRecipeResolutionContext(&contextual.GitCommitContext{PersistHash: "A-commit"}, nil, contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: "A", BaseRef: "main"}})
	require.NoError(t, err)
	scoped, err := root.WithWorkspace(contextual.WorkspaceContext{Cell: "B", ScopeID: "B-scope", InitialHash: "base-B"}, contextual.GitBaseContext{BaseRepo: "B", BaseRef: "main", ResolvedBaseHash: "base-B"})
	require.NoError(t, err)
	child, err := scoped.NewChildContext(ScopeOp, recipe.NodeMetadata{ID: "op"}, "", nil)
	require.NoError(t, err)
	sibling, err := scoped.NewChildContext(ScopeOp, recipe.NodeMetadata{ID: "sibling"}, "", nil)
	require.NoError(t, err)
	child.UpdateGitState(contextual.GitCommitContext{PersistHash: "B-commit", ParentHash: "base-B"})
	require.Equal(t, "B-commit", sibling.TaskExecutionContext().GitTask.PersistHash)
	require.Equal(t, "A-commit", root.GetGitCommitContext().PersistHash)
	require.NoError(t, child.ApplyContextPatch(coretask.ContextPatch{Job: map[string]any{"git": map[string]any{"author": "B author", "ref": "new-base"}, "environment": map[string]any{"inbox": "patched"}}}))
	require.Equal(t, "A", root.TaskExecutionContext().GitTask.BaseRepo)
	require.Equal(t, "main", root.TaskExecutionContext().GitTask.BaseRef)
	require.Equal(t, "new-base", sibling.TaskExecutionContext().GitTask.BaseRef)
	require.Equal(t, "patched", root.TaskExecutionContext().Environment.ArtifactInbox)
	for _, expr := range []string{"{{ context.workspace.cell }}", "${{ context.workspace.cell }}"} {
		value, err := child.ResolveValueWithLocals(expr, nil)
		require.NoError(t, err)
		require.Equal(t, "B", value)
	}
	caller, err := scoped.ResolveCompositeInputs(map[string]any{"repo": "{{ context.git.repo }}"})
	require.NoError(t, err)
	require.Equal(t, "A", caller["repo"])
	require.Error(t, child.ApplyContextPatch(coretask.ContextPatch{Job: map[string]any{"workspace": map[string]any{"scope_id": "A"}}}))
}
