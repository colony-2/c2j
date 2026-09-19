package ops

import (
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSuspendExecutionRecordsValidatedPartialDirective(t *testing.T) {
	deps := NewOpDependenciesBuilder().Build()
	memory, cpu := "16Gi", "2"
	require.NoError(t, SuspendExecution(deps, execution.Requirements{Resources: execution.Resources{Memory: &memory}}))
	require.NoError(t, SuspendExecution(deps, execution.Requirements{Resources: execution.Resources{CPU: &cpu}}))
	got := deps.(interface {
		ExecutionRequirements() *execution.Requirements
	}).ExecutionRequirements()
	require.Equal(t, "16Gi", *got.Resources.Memory)
	require.Equal(t, "2", *got.Resources.CPU)
	bad := "0Gi"
	require.Error(t, SuspendExecution(deps, execution.Requirements{Resources: execution.Resources{Memory: &bad}}))
	require.Error(t, SuspendExecution(deps, execution.Requirements{}))
}
