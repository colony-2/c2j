package compiler

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type failingExecutionContext struct {
	jobworkflow.JobContext
	err    error
	yields int
}

func (*failingExecutionContext) ClientPayload() json.RawMessage { return nil }
func (*failingExecutionContext) ClientPayloadRevision() int64   { return 0 }
func (c *failingExecutionContext) Yield(context.Context, jobdb.RescheduleExecutionRequest) error {
	c.yields++
	return c.err
}

func TestExecutionSuspensionFailureIsControlError(t *testing.T) {
	for _, cause := range []error{context.Canceled, errors.New("stale lease"), errors.New("revision conflict")} {
		t.Run(cause.Error(), func(t *testing.T) {
			ctx := &failingExecutionContext{err: cause}
			d, err := execution.Initial(nil, "pinned", execution.Requirements{})
			require.NoError(t, err)
			s := executionSession{ctx: ctx, demand: d, allocation: execution.Allocation{SchemaVersion: 1}, onHandoff: func(ExecutionHandoff) { t.Fatal("failed publication reported success") }}
			memory := "16Gi"
			err = s.suspend(ctx, "test", execution.Requirements{Resources: execution.Resources{Memory: &memory}})
			require.True(t, isExecutionControlError(err))
			require.ErrorIs(t, err, cause)
			require.Equal(t, 1, ctx.yields)
		})
	}
}

func TestExecutionSuspensionUsesActiveTimeoutScope(t *testing.T) {
	ctx := &failingExecutionContext{err: errors.New("must not yield")}
	d, err := execution.Initial(nil, "pinned", execution.Requirements{})
	require.NoError(t, err)
	s := executionSession{ctx: ctx, demand: d, allocation: execution.Allocation{SchemaVersion: 1}}
	expired := &timeoutJobContext{inner: ctx, deadline: time.Now().Add(-time.Second), label: "test"}
	memory := "16Gi"
	err = s.suspend(expired, "test", execution.Requirements{Resources: execution.Resources{Memory: &memory}})
	require.Error(t, err)
	require.True(t, isExecutionControlError(err))
	require.Zero(t, ctx.yields)
}
