// Package executionruntime checks actual executor compatibility before live tasks
// and carries resolved demand through ordinary workflow reschedules. It does
// not change JobDB's lease selection or add a separate persistence store.
package executionruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type Runtime struct {
	jobdb.WorkflowRuntime
	// Issuer translates recipe grants into task-scoped delegation requests.
	Issuer     permissions.Issuer
	Allocation execution.Allocation
	mu         sync.Mutex
	leases     map[jobdb.JobKey]*lease
}

func New(runtime jobdb.WorkflowRuntime, allocation execution.Allocation) *Runtime {
	r := &Runtime{WorkflowRuntime: runtime, Allocation: allocation, leases: map[jobdb.JobKey]*lease{}}
	if _, ok := runtime.(jobdb.LeaseDelegator); ok {
		r.Issuer = permissions.JobDBIssuer{}
	}
	return r
}

// CreateDelegation forwards the optional runtime capability hidden by embedding
// WorkflowRuntime. Preserve the exact parent chosen by the engine and unwrap our
// admission wrapper so the remote runtime can export its original capability.
func (r *Runtime) CreateDelegation(ctx context.Context, parent jobdb.ExecutionLease, req jobdb.CreateDelegationRequest) (jobdb.RenewableExecutionLease, error) {
	creator, ok := r.WorkflowRuntime.(jobdb.LeaseDelegator)
	if !ok {
		return nil, jobdb.ErrLeaseDelegationUnsupported
	}
	switch l := parent.(type) {
	case *renewableLease:
		parent = l.ExecutionLease
	case *lease:
		parent = l.ExecutionLease
	}
	return creator.CreateDelegation(ctx, parent, req)
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

func (r *Runtime) accept(_ context.Context, l jobdb.ExecutionLease) (jobdb.ExecutionLease, error) {
	return r.wrapLease(l, &leaseState{}), nil
}

func (r *Runtime) wrapLease(l jobdb.ExecutionLease, state *leaseState) jobdb.ExecutionLease {
	wrapped := &lease{ExecutionLease: l, runtime: r, leaseState: state}
	r.mu.Lock()
	r.leases[l.Job().JobKey] = wrapped
	r.mu.Unlock()
	if _, ok := l.(jobdb.RenewableExecutionLease); ok {
		return &renewableLease{wrapped}
	}
	return wrapped
}

type lease struct {
	jobdb.ExecutionLease
	runtime *Runtime
	*leaseState
}

// All refreshed snapshots share invocation state. Renewal must not lose staged
// execution demand or a failure that has already blocked subsequent tasks.
type leaseState struct {
	demand  *execution.Demand
	blocked error
}

// Preserve signed parent bounds for the engine's task-scoped deadline clamp.
func (l *lease) Delegation() *jobdb.DelegationInfo {
	if restricted, ok := l.ExecutionLease.(interface{ Delegation() *jobdb.DelegationInfo }); ok {
		return restricted.Delegation()
	}
	return nil
}

func (l *lease) LeaseSchemaHash() string {
	if v, ok := l.ExecutionLease.(interface{ LeaseSchemaHash() string }); ok {
		return v.LeaseSchemaHash()
	}
	return ""
}

type renewableLease struct{ *lease }

func (l *renewableLease) LeaseExpiry() time.Time {
	return l.ExecutionLease.(jobdb.RenewableExecutionLease).LeaseExpiry()
}

func (l *renewableLease) Renew(ctx context.Context) (jobdb.RenewableExecutionLease, error) {
	next, err := l.ExecutionLease.(jobdb.RenewableExecutionLease).Renew(ctx)
	if err != nil {
		l.forget()
		return nil, err
	}
	return l.runtime.wrapLease(next, l.leaseState).(jobdb.RenewableExecutionLease), nil
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
	if current := l.runtime.leases[l.Job().JobKey]; current != nil && current.leaseState == l.leaseState {
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
		if current == nil || !reflect.DeepEqual(*current, *d) {
			copy := *d
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
		return w.runWithGitProfile(ctx, input, l)
	}
	mismatches, err := execution.Compare(d.Effective, r.Allocation)
	if err != nil {
		return nil, err
	}
	if len(mismatches) == 0 {
		return w.runWithGitProfile(ctx, input, l)
	}
	// A staged demand belongs to the recipe's current task, which may be later
	// than the task route on the acquired lease. Resume recipe replay, not stale
	// pending-task coordinates from that earlier handoff.
	req := jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: l.Route().JobType}}
	req, publishedDemand, published, err := l.prepareReschedule(req)
	if err != nil {
		return l.blockExecution(err)
	}
	metadata, err := json.Marshal(execution.Handoff{Kind: "environment_required", JobKey: ctx.JobKey, Demand: *publishedDemand, Allocation: r.Allocation, Mismatches: mismatches, Published: published})
	if err != nil {
		return l.blockExecution(err)
	}
	err = ctx.Yield(context.Background(), jobworkflow.YieldRequest{Reschedule: req, Metadata: metadata})
	if jobworkflow.IsExecutionSuspended(err) {
		return nil, err
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
