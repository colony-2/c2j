package executionruntime

import (
	"context"
	"fmt"

	"github.com/colony-2/c2j/internal/resourceretry"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type continuationLeaseKey struct{}

type continuationLease struct {
	route jobdb.Route
	state jobdb.ExecutionState
}

func (r *Runtime) ContinuationRoute(ctx jobworkflow.ExecutionContext, producerOrdinal int64) error {
	if err := context.Cause(ctx.Context()); err != nil {
		return err
	}
	r.mu.Lock()
	current := r.leases[ctx.GetJobKey()]
	r.mu.Unlock()
	if current == nil {
		return continuationError(jobdb.ErrExecutionLeaseLost)
	}
	route := current.Route()
	recipe := jobdb.Route{JobType: route.JobType}
	pending := current.ExecutionState().TaskWait
	if pending == nil {
		if route != recipe {
			return continuationError(fmt.Errorf("extension EAGAIN requires a recipe-route lease"))
		}
		return nil
	}
	if producerOrdinal < pending.OutputOrdinal {
		return nil
	}
	err := ctx.Yield(ctx.Context(), jobworkflow.YieldRequest{Reschedule: jobdb.RescheduleExecutionRequest{NextRoute: recipe}})
	if jobworkflow.IsExecutionSuspended(err) {
		return err
	}
	if err == nil {
		err = fmt.Errorf("extension EAGAIN recipe-route handoff was not acknowledged")
	}
	return continuationError(err)
}

func WaitForContinuation(local context.Context, task jobworkflow.TaskContext) error {
	if err := context.Cause(local); err != nil {
		return err
	}
	if err := context.Cause(task.Context()); err != nil {
		return err
	}
	lease, ok := local.Value(continuationLeaseKey{}).(continuationLease)
	if !ok || !resourceretry.Supported(local) || lease.state.TaskWait != nil || lease.route != (jobdb.Route{JobType: lease.route.JobType}) || task.InvocationID == "" || task.AttemptNumber < 1 {
		return continuationError(fmt.Errorf("extension EAGAIN cannot wait on this lease"))
	}
	selected := lease.state.ResourceResume
	invocation := fmt.Sprintf("%s/attempt:%d", task.InvocationID, task.AttemptNumber)
	if selected == nil || selected.Invocation != invocation {
		return jobworkflow.EAGAINWait(task)
	}
	if selected.Version != 1 || selected.Attempt != task.AttemptNumber || selected.Route != lease.route || selected.TaskWait != nil {
		return continuationError(fmt.Errorf("extension EAGAIN resource wake does not match the wait task"))
	}
	return nil
}

func continuationError(err error) error {
	return jobdb.ClassifyExecutionInfrastructure(err, jobdb.ExecutionInfrastructureRuntimeIO, "c2j.extension_eagain", nil, nil)
}
