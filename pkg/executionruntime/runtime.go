// Package executionruntime fences actual executor compatibility at lease entry
// and carries resolved demand through ordinary workflow reschedules. It does
// not change JobDB's lease selection or add a separate persistence store.
package executionruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type Runtime struct {
	jobdb.WorkflowRuntime
	Allocation execution.Allocation
	OnHandoff  func(compiler.ExecutionHandoff)
	mu         sync.Mutex
	leases     map[jobdb.JobKey]*lease
}

func New(runtime jobdb.WorkflowRuntime, allocation execution.Allocation, handoff func(compiler.ExecutionHandoff)) *Runtime {
	return &Runtime{WorkflowRuntime: runtime, Allocation: allocation, OnHandoff: handoff, leases: map[jobdb.JobKey]*lease{}}
}

func (r *Runtime) Stage(key jobdb.JobKey, demand execution.Demand) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if l := r.leases[key]; l != nil {
		copy := demand
		l.demand = &copy
	}
}

func (r *Runtime) GetJobLease(ctx context.Context, req jobdb.GetJobLeaseRequest) (jobdb.ExecutionLease, error) {
	l, err := r.WorkflowRuntime.GetJobLease(ctx, req)
	if err != nil || l == nil {
		return l, err
	}
	return r.accept(ctx, l)
}

func (r *Runtime) PollWork(ctx context.Context, req jobdb.PollWorkRequest) ([]jobdb.ExecutionLease, error) {
	leases, err := r.WorkflowRuntime.PollWork(ctx, req)
	if err != nil {
		return nil, err
	}
	var out []jobdb.ExecutionLease
	for _, l := range leases {
		checked, err := r.accept(ctx, l)
		if err != nil {
			// A failed admission must not leave keepalive goroutines extending
			// leases that will never be handed to an executor.
			for _, acquired := range leases {
				acquired.StopKeepAlive()
			}
			for _, admitted := range out {
				admitted.StopKeepAlive()
			}
			return nil, err
		}
		if checked != nil {
			out = append(out, checked)
		}
	}
	return out, nil
}

func (r *Runtime) accept(ctx context.Context, l jobdb.ExecutionLease) (jobdb.ExecutionLease, error) {
	admitted := false
	defer func() {
		if !admitted {
			l.StopKeepAlive()
		}
	}()
	d, err := execution.PayloadDemand(l.ClientPayload())
	if err != nil {
		return nil, fmt.Errorf("job %s execution state: %w", l.Job().JobKey, err)
	}
	if d != nil && d.NodeRequirements == nil {
		m, err := execution.Compare(d.Effective, r.Allocation)
		if err != nil {
			return nil, err
		}
		if len(m) > 0 {
			// Retain the exact pending task coordinates and route. No payload
			// rewrite/revision bump is needed for an already-published demand.
			if err := l.Reschedule(ctx, jobdb.RescheduleExecutionRequest{NextRoute: l.Route(), TaskWait: l.ExecutionState().TaskWait}); err != nil {
				return nil, err
			}
			if r.OnHandoff != nil {
				r.OnHandoff(compiler.ExecutionHandoff{Kind: "environment_required", JobKey: l.Job().JobKey, Demand: *d, Allocation: r.Allocation, Mismatches: m})
			}
			return nil, nil
		}
	}
	wrapped := &lease{ExecutionLease: l, runtime: r}
	r.mu.Lock()
	r.leases[l.Job().JobKey] = wrapped
	r.mu.Unlock()
	admitted = true
	return wrapped, nil
}

type lease struct {
	jobdb.ExecutionLease
	runtime *Runtime
	demand  *execution.Demand
	blocked error
}

func (l *lease) LeaseToken() string {
	if v, ok := l.ExecutionLease.(interface{ LeaseToken() string }); ok {
		return v.LeaseToken()
	}
	return ""
}
func (l *lease) LeaseWorkerID() string {
	if v, ok := l.ExecutionLease.(interface{ LeaseWorkerID() string }); ok {
		return v.LeaseWorkerID()
	}
	return ""
}

func (l *lease) forget() {
	l.runtime.mu.Lock()
	defer l.runtime.mu.Unlock()
	if l.runtime.leases[l.Job().JobKey] == l {
		delete(l.runtime.leases, l.Job().JobKey)
	}
}

func (l *lease) StopKeepAlive() { l.forget(); l.ExecutionLease.StopKeepAlive() }

func (l *lease) prepareReschedule(req jobdb.RescheduleExecutionRequest) (jobdb.RescheduleExecutionRequest, *execution.Demand, bool, error) {
	l.runtime.mu.Lock()
	d := l.demand
	l.runtime.mu.Unlock()
	if req.ClientPayloadUpdate == nil && d != nil {
		current, err := execution.PayloadDemand(l.ClientPayload())
		if err != nil {
			return req, nil, false, err
		}
		if current == nil || (d.NodeRequirements != nil && !sameDemand(*current, *d)) {
			copy := *d
			if copy.NodeRequirements != nil {
				if current != nil && copy.Revision < current.Revision {
					copy.Revision = current.Revision
				}
				copy.Revision++
			}
			copy.LastAllocation = &l.runtime.Allocation
			raw, err := execution.PayloadWithDemand(l.ClientPayload(), copy)
			if err != nil {
				return req, nil, false, err
			}
			revision := l.ClientPayloadRevision()
			req.ClientPayloadUpdate = &jobdb.ClientPayloadUpdate{Mode: "reset", Value: json.RawMessage(raw), ExpectedRevision: &revision}
			return req, &copy, true, nil
		}
		return req, current, false, nil
	}
	return req, d, false, nil
}

func sameDemand(a, b execution.Demand) bool {
	a.Revision, b.Revision = 0, 0
	a.LastAllocation, b.LastAllocation = nil, nil
	return reflect.DeepEqual(a, b)
}

func (l *lease) Reschedule(ctx context.Context, req jobdb.RescheduleExecutionRequest) error {
	req, _, _, err := l.prepareReschedule(req)
	if err != nil {
		return err
	}
	err = l.ExecutionLease.Reschedule(ctx, req)
	l.forget()
	return err
}

func (l *lease) Complete(ctx context.Context, req jobdb.CompleteExecutionRequest) error {
	err := l.ExecutionLease.Complete(ctx, req)
	l.forget()
	return err
}

// WrapTaskWorker checks needs only when JobDB actually invokes a task worker,
// after looking up completed results. Use it for every worker in the workset.
func (r *Runtime) WrapTaskWorker(worker jobworkflow.TaskWorker) jobworkflow.TaskWorker {
	return &guardedTask{TaskWorker: worker, runtime: r}
}

type guardedTask struct {
	jobworkflow.TaskWorker
	runtime *Runtime
}

func (w *guardedTask) Run(ctx jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	r := w.runtime
	r.mu.Lock()
	l := r.leases[ctx.JobKey]
	var d *execution.Demand
	var blocked error
	if l != nil {
		d = l.demand
		blocked = l.blocked
	}
	r.mu.Unlock()
	if l == nil {
		return nil, fmt.Errorf("execution lease is no longer available")
	}
	if blocked != nil {
		return nil, blocked
	}
	// Recipe-resolution tasks run before a scope is staged. Scoped recipe work
	// always replays first, including when claimed through a pending-task route.
	if d == nil {
		return w.TaskWorker.Run(ctx, input)
	}
	mismatches, err := execution.Compare(d.Effective, r.Allocation)
	if err != nil {
		return nil, err
	}
	if len(mismatches) == 0 {
		return w.TaskWorker.Run(ctx, input)
	}
	// A staged demand belongs to the recipe's current task, which may be later
	// than the task route on the acquired lease. Resume recipe replay, not stale
	// pending-task coordinates from that earlier handoff.
	req := jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: l.Route().JobType}}
	req, publishedDemand, published, err := l.prepareReschedule(req)
	if err != nil {
		return l.blockExecution(err)
	}
	accepted := true
	defer func() {
		if p := recover(); p != nil {
			panic(p)
		}
		if accepted && r.OnHandoff != nil {
			r.OnHandoff(compiler.ExecutionHandoff{Kind: "environment_required", JobKey: ctx.JobKey, Demand: *publishedDemand, Allocation: r.Allocation, Mismatches: mismatches, Published: published})
		}
	}()
	err = ctx.Yield(context.Background(), req)
	accepted = false
	if err == nil {
		err = fmt.Errorf("execution yield returned without stopping invocation")
	}
	return l.blockExecution(err)
}

// A rejected/uncertain handoff cannot be recovered by a recipe catch that
// dispatches another task under the same execution invocation.
func (l *lease) blockExecution(err error) (jobdb.TaskData, error) {
	l.runtime.mu.Lock()
	l.blocked = err
	l.runtime.mu.Unlock()
	return nil, err
}
