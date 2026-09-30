package executionruntime

import (
	"context"

	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

// RunWithLease executes exactly one supplied lease using JobDB's shared runner.
// It validates and renews before allocation admission or application work, and
// never acquires replacement work. Configure the recipe worker with this
// runtime's Stage and WrapTaskWorker, as for ordinary claim-and-run execution.
// BeforeRun, when provided, runs after successful allocation admission under
// the same renewing lease. WorkerID should normally be left empty to retain the
// supplied lease's owner.
func (r *Runtime) RunWithLease(ctx context.Context, supplied jobdb.ExecutionLease, req jobworkflow.GetJobForRunRequest, listener jobworkflow.JobRunListener) (jobworkflow.JobRunOutcome, error) {
	if _, ok := supplied.(jobdb.RenewableExecutionLease); !ok {
		return jobworkflow.JobRunOutcome{}, jobdb.ErrLeaseRenewalUnsupported
	}
	wrapped := r.wrapLease(supplied, &leaseState{}).(*renewableLease)
	defer wrapped.forget()
	before := req.BeforeRun
	req.BeforeRun = func(ctx context.Context, current jobdb.ExecutionLease) error {
		accepted, err := r.checkAdmission(ctx, current)
		if err != nil || !accepted {
			return err
		}
		if before != nil {
			return before(ctx, current)
		}
		return nil
	}
	runnable, err := jobworkflow.GetJobForRunWithLease(ctx, r, wrapped, req)
	if err != nil {
		return jobworkflow.JobRunOutcome{}, err
	}
	return runnable.Run(listener)
}
