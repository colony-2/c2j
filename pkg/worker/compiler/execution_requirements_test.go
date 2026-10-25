package compiler

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type failingExecutionContext struct {
	jobworkflow.ExecutionContext
	err    error
	yields int
}

func (*failingExecutionContext) ClientPayload() json.RawMessage { return nil }
func (*failingExecutionContext) ClientPayloadRevision() int64   { return 0 }
func (c *failingExecutionContext) Yield(context.Context, jobworkflow.YieldRequest) error {
	c.yields++
	return c.err
}

func TestExecutionChangesReconstructWithoutYielding(t *testing.T) {
	ctx := &failingExecutionContext{err: errors.New("must not yield")}
	var staged execution.Demand
	s := executionSession{ctx: ctx, guarded: true, stage: func(_ jobdb.JobKey, d execution.Demand) { staged = d }}
	for _, memory := range []string{"16Gi", "4Gi", "16Gi"} {
		require.NoError(t, s.apply(execution.Requirements{Resources: execution.Resources{Memory: &memory}}))
		require.Equal(t, memory, *staged.Effective.Resources.Memory)
	}
	require.Zero(t, ctx.yields)
}

func TestExecutionChangesRequireGuardedTasks(t *testing.T) {
	s := executionSession{}
	memory := "16Gi"
	err := s.apply(execution.Requirements{Resources: execution.Resources{Memory: &memory}})
	require.ErrorContains(t, err, "guarded task workers")
	require.True(t, isExecutionControlError(err))
}

func (*failingExecutionContext) ResolveSecrets(context.Context, []string) (jobdb.SecretResolveResult, error) {
	return jobdb.SecretResolveResult{}, jobdb.ErrNoSecretProvider
}

func (*failingExecutionContext) GetConfiguration(context.Context) (jobdb.ConfigurationSnapshot, error) {
	return jobdb.ConfigurationSnapshot{}, jobdb.ErrConfigurationUnavailable
}

func (*failingExecutionContext) GetJobKey() jobdb.JobKey {
	return jobdb.JobKey{TenantId: "test", JobId: "job"}
}

func (c *failingExecutionContext) ResolveSecretsFresh(ctx context.Context, names []string) (jobdb.SecretResolveResult, error) {
	return c.ResolveSecrets(ctx, names)
}

func (c *failingExecutionContext) Job() jobdb.JobHandle {
	return jobdb.JobHandle{JobKey: c.GetJobKey()}
}
