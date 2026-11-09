package executionruntime

import (
	"context"
	"fmt"
	"time"

	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/jobrestart"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

func (r *Runtime) inspection(access *permissions.Access, tc jobworkflow.TaskContext, owner *lease) jobinspect.API {
	return &jobinspect.Service{Runtime: r.WorkflowRuntime, Tenant: tc.JobKey.TenantId, Authorize: func(call context.Context, _ string) error {
		if err := call.Err(); err != nil {
			return err
		}
		if err := access.Context().Err(); err != nil {
			return err
		}
		if err := tc.Context().Err(); err != nil {
			return err
		}
		r.mu.Lock()
		live := r.leases[tc.JobKey]
		r.mu.Unlock()
		if live == nil || live.leaseState != owner.leaseState {
			return jobdb.ErrExecutionLeaseLost
		}
		if expiry, ok := live.ExecutionLease.(interface{ LeaseExpiry() time.Time }); ok && !expiry.LeaseExpiry().After(time.Now()) {
			return jobdb.ErrExecutionLeaseLost
		}
		return nil
	}}
}

// Restart helpers share the history reader's lease-lifetime guard. The driver
// supplies replay support, without adding a compiler dependency to this runtime.
func (r *Runtime) restartInspection(access *permissions.Access, tc jobworkflow.TaskContext, owner *lease) jobrestart.API {
	return &guardedRestart{api: r.RestartAPI, authorize: r.inspection(access, tc, owner).(*jobinspect.Service).Authorize}
}

type guardedRestart struct {
	api       jobrestart.API
	authorize func(context.Context, string) error
}

func (g *guardedRestart) Restart(ctx context.Context, q jobrestart.Request) (*jobrestart.Response, error) {
	if err := g.authorize(ctx, q.JobID); err != nil {
		return nil, err
	}
	if g.api == nil {
		return nil, fmt.Errorf("restart history/replay capability unavailable in this worker")
	}
	return g.api.Restart(ctx, q)
}
