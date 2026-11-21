package executionruntime

import (
	"context"
	"fmt"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executioncatalog"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// catalogPolicy always reads through the renewed owning lease. Delegated child
// credentials are deliberately not granted configuration access.
func (l *lease) catalogPolicy() executioncatalog.Policy {
	p := l.runtime.BaseCatalog
	p.Scope.Tenant = l.Job().JobKey.TenantId
	p.Read = func(ctx context.Context) (jobdb.ConfigurationSnapshot, error) {
		r := l.runtime
		r.mu.Lock()
		current := r.leases[l.Job().JobKey]
		r.mu.Unlock()
		if current == nil || current.leaseState != l.leaseState {
			return jobdb.ConfigurationSnapshot{}, jobdb.ErrExecutionLeaseLost
		}
		result, err := current.ExecutionLease.GetConfiguration(ctx)
		r.mu.Lock()
		after := r.leases[l.Job().JobKey]
		r.mu.Unlock()
		if after == nil || after.leaseState != l.leaseState {
			return jobdb.ConfigurationSnapshot{}, jobdb.ErrExecutionLeaseLost
		}
		if ctx.Err() != nil {
			return jobdb.ConfigurationSnapshot{}, ctx.Err()
		}
		return result, err
	}
	return p
}
func (l *lease) checkBase(ctx context.Context, needs execution.Requirements, source string) error {
	return l.catalogPolicy().Check(ctx, needs, source)
}
func (l *lease) submissionContext(ctx context.Context) context.Context {
	return executioncatalog.WithPolicy(ctx, l.catalogPolicy())
}
func taskBaseSource(invocation string, step int64) string {
	return fmt.Sprintf("task %s (result %d).execution.base", invocation, step)
}

// Guard native TaskContext callbacks too, rather than relying on a CLI preflight.
func (l *lease) SubmitJob(ctx context.Context, req jobdb.SubmitJobRequest) (jobdb.JobHandle, error) {
	if err := executioncatalog.CheckSubmission(l.submissionContext(ctx), req.Job); err != nil {
		return jobdb.JobHandle{}, err
	}
	return l.ExecutionLease.SubmitJob(ctx, req)
}
func (l *lease) StartOrAppendJob(ctx context.Context, req jobdb.StartOrAppendJobRequest) (jobdb.JobHandle, error) {
	if err := executioncatalog.CheckSubmission(l.submissionContext(ctx), req.Job.Job); err != nil {
		return jobdb.JobHandle{}, err
	}
	return l.ExecutionLease.StartOrAppendJob(ctx, req)
}
func (l *lease) SubmitRestartJob(ctx context.Context, req jobdb.SubmitRestartJobRequest) (jobdb.JobHandle, error) {
	if err := executioncatalog.CheckRestart(l.submissionContext(ctx), req); err != nil {
		return jobdb.JobHandle{}, err
	}
	return l.ExecutionLease.SubmitRestartJob(ctx, req)
}

// Undo a candidate when final admission failed before a handoff was accepted.
// Renewal shares leaseState; a different invocation must never be modified.
func (l *lease) restoreDemand(previous *execution.Demand) {
	r := l.runtime
	r.mu.Lock()
	defer r.mu.Unlock()
	if current := r.leases[l.Job().JobKey]; current != nil && current.leaseState == l.leaseState {
		current.demand = previous
	}
}
