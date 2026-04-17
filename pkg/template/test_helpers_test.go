package template

import (
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/stretchr/testify/require"
)

func newRecipeCtx(t *testing.T, inputs map[string]interface{}) *ResolutionContext {
	t.Helper()
	commitCtx := &contextual.GitCommitContext{}
	ctx, err := NewRecipeResolutionContext(commitCtx, inputs, contextual.JobContext{})
	require.NoError(t, err)
	return ctx
}

func newSequenceCtx(t *testing.T, parent *ResolutionContext, id string, inputs map[string]interface{}) *ResolutionContext {
	t.Helper()
	seqCtx, err := parent.NewChildContext(ScopeSequence, recipe.NodeMetadata{ID: id}, "", inputs)
	require.NoError(t, err)
	return seqCtx
}

func newStateMachineCtx(t *testing.T, parent *ResolutionContext, id string, inputs map[string]interface{}) *ResolutionContext {
	t.Helper()
	smCtx, err := parent.NewChildContext(ScopeStateMachine, recipe.NodeMetadata{ID: id}, "", inputs)
	require.NoError(t, err)
	return smCtx
}

func newStateCtx(t *testing.T, parent *ResolutionContext, id string) *ResolutionContext {
	t.Helper()
	stateCtx, err := parent.NewChildContext(ScopeState, recipe.NodeMetadata{ID: id}, id, nil)
	require.NoError(t, err)
	return stateCtx
}

func addOpOutput(t *testing.T, container *ResolutionContext, id string, outputs map[string]interface{}) {
	t.Helper()
	opCtx, err := container.NewChildContext(ScopeOp, recipe.NodeMetadata{ID: id}, id, nil)
	require.NoError(t, err)
	opCtx.AddExecution(outputs)
}

func addStateOutput(t *testing.T, smCtx *ResolutionContext, id string, outputs map[string]interface{}) {
	t.Helper()
	stateCtx := newStateCtx(t, smCtx, id)
	stateCtx.AddExecution(outputs)
}
