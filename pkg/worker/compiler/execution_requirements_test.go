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

func TestExecutionSuspensionFailureIsControlError(t *testing.T) {
	for _, cause := range []error{context.Canceled, errors.New("stale lease"), errors.New("revision conflict")} {
		t.Run(cause.Error(), func(t *testing.T) {
			ctx := &failingExecutionContext{err: cause}
			d, err := execution.Initial(nil, "pinned", execution.Requirements{})
			require.NoError(t, err)
			s := executionSession{ctx: ctx, demand: d, allocation: execution.Allocation{SchemaVersion: 1}}
			memory := "16Gi"
			err = s.suspend(ctx, "test", execution.Requirements{Resources: execution.Resources{Memory: &memory}})
			require.True(t, isExecutionControlError(err))
			require.ErrorIs(t, err, cause)
			require.Equal(t, 1, ctx.yields)
		})
	}
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
