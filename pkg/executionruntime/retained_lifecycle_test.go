package executionruntime

import (
	"context"
	"fmt"
	"testing"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type lifecycleCapture struct {
	jobdb.ConsumerRuntime
	ctx    context.Context
	cancel jobdb.CancelJobRequest
	clear  jobdb.ClearCrashConcernRequest
	err    error
}

func (r *lifecycleCapture) CancelJob(ctx context.Context, q jobdb.CancelJobRequest) error {
	r.ctx, r.cancel = ctx, q
	return r.err
}
func (r *lifecycleCapture) ClearCrashConcern(ctx context.Context, q jobdb.ClearCrashConcernRequest) error {
	r.ctx, r.clear = ctx, q
	return r.err
}

func TestC05RetainedLifecycleForwarding(t *testing.T) {
	for _, cause := range []error{nil, jobdb.ErrConflict, context.Canceled, jobdb.ErrLeasePermissionDenied} {
		base := &lifecycleCapture{}
		if cause != nil {
			base.err = fmt.Errorf("provider: %w", cause)
		}
		runtime := New(base, execution.Allocation{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		stop := jobdb.CancelJobRequest{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "job"}, WorkerID: "caller", Reason: "exact reason\n"}
		err := runtime.CancelJob(ctx, stop)
		require.ErrorIs(t, err, cause)
		require.Same(t, ctx, base.ctx)
		require.Equal(t, stop, base.cancel)
		clear := jobdb.ClearCrashConcernRequest{JobKey: stop.JobKey, Reason: "reviewed\n"}
		err = runtime.ClearCrashConcern(ctx, clear)
		require.ErrorIs(t, err, cause)
		require.Same(t, ctx, base.ctx)
		require.Equal(t, clear, base.clear)
	}
}
