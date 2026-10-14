package executionruntime

import (
	"context"

	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

// RunWithLease executes exactly one supplied lease using JobDB's shared runner.
// It validates and renews before application work, and
// never acquires replacement work. Configure the recipe worker with this
// runtime's Stage and WrapTaskWorker, as for ordinary claim-and-run execution.
// Allocation checks use context Yield so acceptance is reported in the outcome.
// WorkerID should normally be left empty to retain the supplied lease owner.
func (r *Runtime) RunWithLease(ctx context.Context, supplied jobdb.ExecutionLease, req jobworkflow.GetJobForRunRequest, listener jobworkflow.JobRunListener) (jobworkflow.JobRunOutcome, error) {
	if _, ok := supplied.(jobdb.RenewableExecutionLease); !ok {
		return jobworkflow.JobRunOutcome{}, jobdb.ErrLeaseRenewalUnsupported
	}
	wrapped := r.wrapLease(supplied, &leaseState{}).(*renewableLease)
	defer wrapped.forget()
	runnable, err := jobworkflow.GetJobForRunWithLease(ctx, r, wrapped, req)
	if err != nil {
		return jobworkflow.JobRunOutcome{}, err
	}
	return runnable.Run(listener)
}
