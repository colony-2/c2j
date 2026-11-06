package executionruntime

import (
	"context"
	"time"

	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

func (r *Runtime) inspection(access *permissions.Access, tc jobworkflow.TaskContext, owner *lease) jobinspect.API {
	return &jobinspect.Service{Runtime: r.WorkflowRuntime, Tenant: tc.JobKey.TenantId, Parent: tc.JobKey.JobId, Authorize: func(call context.Context, id string) error {
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
		if id == tc.JobKey.JobId {
			return nil
		}
		page, err := r.WorkflowRuntime.ListJobs(call, jobdb.ListJobsRequest{TenantIds: []string{tc.JobKey.TenantId}, JobKeys: []jobdb.JobKey{{TenantId: tc.JobKey.TenantId, JobId: id}}, IncludeHidden: true, PageSize: 1})
		if err != nil {
			return err
		}
		for _, job := range page.Jobs {
			// Use JobDB's recorded lineage, never caller-authored metadata.
			if job.ParentJobID == tc.JobKey.JobId {
				return nil
			}
		}
		return jobdb.ErrLeasePermissionDenied
	}}
}
