package template

import (
	"context"
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/secrets"
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
	require.NoError(t, child.ApplyContextPatch(parseTestPatch(t, `[{"op":"add","path":"/context/git/author","value":"B author"},{"op":"add","path":"/context/git/ref","value":"new-base"},{"op":"add","path":"/context/environment/inbox","value":"patched"}]`)))
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
	require.Error(t, child.ApplyRestartPayload([]byte(`[{"op":"add","path":"/context/workspace/scope_id","value":"A"}]`)))
}

func TestCompositeInputLifetimePreservesCallerWorkspaceAndAuthority(t *testing.T) {
	root, err := NewRecipeResolutionContext(&contextual.GitCommitContext{}, map[string]any{"token": secrets.Reference{Name: "forwarded"}}, contextual.JobContext{GitBase: contextual.GitBaseContext{BaseRepo: "caller", BaseRef: "main"}})
	require.NoError(t, err)
	root.Permissions = permissions.State{Version: permissions.Version, Inputs: &permissions.InputPolicy{Foreign: true}}
	scoped, err := root.WithWorkspace(contextual.WorkspaceContext{Cell: "selected", ScopeID: "selected-scope"}, contextual.GitBaseContext{BaseRepo: "selected", BaseRef: "main"})
	require.NoError(t, err)
	bindings := map[string]any{"repo": "${{ context.git.repo }}", "token": "${{ inputs.token }}"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bound, err := scoped.ResolveCompositeInputsWithContext(ctx, bindings)
	require.NoError(t, err)
	require.Equal(t, "caller", bound["repo"])
	require.Equal(t, secrets.Reference{Name: "forwarded"}, bound["token"])
	_, err = scoped.ResolveCompositeInputsWithContext(ctx, map[string]any{"token": "${{ context.secrets.forwarded }}"})
	require.ErrorContains(t, err, "lease permission denied")
	cancel()
	_, err = scoped.ResolveCompositeInputsWithContext(ctx, bindings)
	require.ErrorIs(t, err, context.Canceled)
	// The temporary lifetime and secret-enabled view must not escape this call.
	_, err = root.ResolveMap(map[string]any{"token": "${{ inputs.token }}"})
	require.Error(t, err)
	again, err := scoped.ResolveCompositeInputs(bindings)
	require.NoError(t, err)
	require.Equal(t, bound, again)
}

func TestExecutionLifetimeInheritanceAndRebinding(t *testing.T) {
	root, err := NewRecipeResolutionContext(&contextual.GitCommitContext{}, nil, contextual.JobContext{GitBase: contextual.GitBaseContext{BaseRepo: "caller", BaseRef: "main"}})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempt, err := root.WithEvaluationContext(ctx)
	require.NoError(t, err)
	child, err := attempt.NewChildContext(ScopeSequence, recipe.NodeMetadata{ID: "child"}, "", map[string]any{})
	require.NoError(t, err)
	workspace, err := child.WithWorkspace(contextual.WorkspaceContext{Cell: "selected", ScopeID: "selected-scope"}, contextual.GitBaseContext{BaseRepo: "selected", BaseRef: "main"})
	require.NoError(t, err)
	cancel()
	_, err = child.ResolveValueWithMode("${{ 1 + 1 }}", ModeInterpolation)
	require.ErrorIs(t, err, context.Canceled)
	_, err = workspace.ResolveCompositeInputs(map[string]any{"repo": "${{ context.git.repo }}"})
	require.ErrorIs(t, err, context.Canceled)
	// Catch/retry entry rebinds the view, including caller-workspace inputs.
	fresh, err := workspace.WithEvaluationContext(context.Background())
	require.NoError(t, err)
	bindings, err := fresh.ResolveCompositeInputs(map[string]any{"repo": "${{ context.git.repo }}"})
	require.NoError(t, err)
	require.Equal(t, "caller", bindings["repo"])
	root.AdoptExecution(attempt)
	value, err := root.ResolveValueWithMode("${{ 1 + 1 }}", ModeInterpolation)
	require.NoError(t, err, "publishing an attempt must not adopt its expired lifetime")
	require.EqualValues(t, 2, value)
}
