package template

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompletionPositionIsWriteOnce(t *testing.T) {
	machine := newStateMachineCtx(t, newRecipeCtx(t, nil), "machine", map[string]any{})
	state := newStateCtx(t, machine, "work")
	view := state.WithFailure(nil)
	require.NoError(t, state.AddExecution(map[string]any{"value": "first"}))
	require.ErrorIs(t, view.AddExecution(map[string]any{"value": "duplicate"}), ErrAlreadyWritten)
	require.Equal(t, "first", machine.TemplateData.States["work"].Outputs["value"])
	require.Empty(t, machine.TemplateData.States["work"].Runs)
	// A later invocation is a new position, even though its logical name repeats.
	next := newStateCtx(t, machine, "work")
	require.NoError(t, next.AddExecution(map[string]any{"value": "second"}))
	require.Equal(t, "second", machine.TemplateData.States["work"].Outputs["value"])
	require.Len(t, machine.TemplateData.States["work"].Runs, 1)
	require.Equal(t, "first", machine.TemplateData.States["work"].Runs[0].Outputs["value"])
}
