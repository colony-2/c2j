// Package executionruntime checks actual executor compatibility before live tasks
// and carries resolved demand through ordinary workflow reschedules. It does
// not change JobDB's lease selection or add a separate persistence store.
package executionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/colony-2/c2j/internal/extensioneagain"
	"github.com/colony-2/c2j/internal/resourceretry"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executioncatalog"
	"github.com/colony-2/c2j/pkg/extensionwait"
	"github.com/colony-2/c2j/pkg/jobinputs"
	"github.com/colony-2/c2j/pkg/jobrestart"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/clientpayload"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type Runtime struct {
	// BaseCatalog is driver-owned policy; zero value preserves compatibility.
	BaseCatalog executioncatalog.Policy
	// RestartAPI is a driver-provided, authorized restart service.
	RestartAPI jobrestart.API
	// InputHistory constructs a native history adapter for each helper authority.
	InputHistory jobinputs.Factory
	jobdb.ConsumerRuntime
	// Issuer translates recipe grants into task-scoped delegation requests.
	Issuer     permissions.Issuer
	Allocation execution.Allocation
	mu         sync.Mutex
	leases     map[jobdb.JobKey]*lease
	activity   *runActivity
}

func New(runtime jobdb.ConsumerRuntime, allocation execution.Allocation) *Runtime {
	r := &Runtime{ConsumerRuntime: runtime, Allocation: allocation, leases: map[jobdb.JobKey]*lease{}}
	if _, ok := runtime.(jobdb.LeaseDelegator); ok {
		r.Issuer = permissions.JobDBIssuer{}
	}
	return r
}

// ReadExecutionProvenance forwards the optional reader hidden by embedding.
func (r *Runtime) ReadExecutionProvenance(ctx context.Context, req jobdb.ReadExecutionProvenanceRequest) (jobdb.ExecutionProvenance, error) {
	return jobdb.ReadExecutionProvenance(ctx, r.ConsumerRuntime, req)
}

func (r *Runtime) ListLeaseEvents(ctx context.Context, req jobdb.ListLeaseEventsRequest) (jobdb.ListLeaseEventsResponse, error) {
	return jobdb.ListLeaseEvents(ctx, r.ConsumerRuntime, req)
}

// CreateDelegation forwards the optional runtime capability hidden by embedding
// ConsumerRuntime. Preserve the exact parent chosen by the engine and unwrap our
// admission wrapper so the remote runtime can export its original capability.
func (r *Runtime) CreateDelegation(ctx context.Context, parent jobdb.ExecutionLease, req jobdb.CreateDelegationRequest) (jobdb.RenewableExecutionLease, error) {
	creator, ok := r.ConsumerRuntime.(jobdb.LeaseDelegator)
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
	l, err := r.ConsumerRuntime.GetJobLease(ctx, req)
	if err != nil || l == nil {
		return l, err
	}
	return r.accept(ctx, l)
}

func (r *Runtime) PollWork(ctx context.Context, req jobdb.PollWorkRequest) ([]jobdb.ExecutionLease, error) {
	leases, err := r.ConsumerRuntime.PollWork(ctx, req)
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
	if old := r.leases[l.Job().JobKey]; old != nil {
		old.notifyHistoryLocked()
	}
	if previous := r.leases[l.Job().JobKey]; previous != nil && previous.leaseState != state {
		r.clearActivityLocked()
	}
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
	historyChanged  chan struct{} // guarded by runtime.mu; wakes expiry/renewal watchers
	metricsSnapshot *jobdb.ChapterMetadataValue
	demand          *execution.Demand
	pendingWait     *waitGate
	retireWait      bool
	blocked         error
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
	r := l.runtime
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.leases[l.Job().JobKey]
	if current == nil || current.leaseState != l.leaseState {
		return nil, jobdb.ErrExecutionLeaseLost
	}
	wrapped := &lease{ExecutionLease: next, runtime: r, leaseState: l.leaseState}
	r.leases[l.Job().JobKey] = wrapped
	return &renewableLease{wrapped}, nil
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
		current.notifyHistoryLocked()
		delete(l.runtime.leases, l.Job().JobKey)
		l.runtime.clearActivityLocked()
	}
}

func (l *lease) StopKeepAlive() { l.forget(); l.ExecutionLease.StopKeepAlive() }

func (l *lease) prepareReschedule(ctx context.Context, req jobdb.RescheduleExecutionRequest) (jobdb.RescheduleExecutionRequest, *execution.Demand, bool, error) {
	current, err := l.current()
	if err != nil {
		return req, nil, false, err
	}
	l.runtime.mu.Lock()
	d, pending, retire := l.demand, l.pendingWait, l.retireWait
	l.runtime.mu.Unlock()
	candidate := d
	if req.ClientPayloadUpdate != nil {
		raw, _, err := clientpayload.Apply(current.ClientPayload(), current.ClientPayloadRevision(), req.ClientPayloadUpdate)
		if err != nil {
			return req, nil, false, err
		}
		candidate, err = execution.PayloadDemand(raw)
		if err != nil {
			return req, nil, false, err
		}
	}
	if candidate != nil {
		if err := l.checkBase(ctx, candidate.Effective, "reschedule.execution.base"); err != nil {
			return req, nil, false, err
		}
	} else if l.runtime.BaseCatalog.Required {
		if err := l.checkBase(ctx, execution.Requirements{}, "reschedule.execution.base"); err != nil {
			return req, nil, false, err
		}
	}
	revision := current.ClientPayloadRevision()
	raw, _, err := clientpayload.Apply(current.ClientPayload(), revision, req.ClientPayloadUpdate)
	if err != nil {
		return req, nil, false, err
	}
	changed := req.ClientPayloadUpdate != nil
	gate, err := payloadGate(current.ClientPayload())
	if err != nil {
		return req, nil, false, err
	}
	if pending != nil {
		records, err := current.waitHistory(context.Background())
		if err != nil {
			return req, nil, false, err
		}
		if err := bindDeadline(records, pending); err != nil {
			return req, nil, false, err
		}
		gate = pending
		if req.DependencyWait != nil {
			req.DependencyWait.Deadline = gate.Deadline
		} else if req.WaitUntil != nil && gate.Deadline != nil && gate.Deadline.Before(*req.WaitUntil) {
			at := *gate.Deadline
			req.WaitUntil = &at
		}
	}
	if gate != nil || retire {
		if retire && pending == nil {
			gate = nil
		}
		raw, err = payloadWithGate(raw, gate)
		if err != nil {
			return req, nil, false, err
		}
		changed = true
	}
	published := false
	if d != nil {
		previous, err := execution.PayloadDemand(raw)
		if err != nil {
			return req, nil, false, err
		}
		comparable := *d
		comparable.Overrides = execution.Requirements{}
		comparable.Effective.CapabilitiesDeclared = false
		if previous == nil || !reflect.DeepEqual(*previous, comparable) {
			raw, err = execution.PayloadWithDemand(raw, *d)
			if err != nil {
				return req, nil, false, err
			}
			changed, published = true, true
		}
	}
	if changed {
		req.ClientPayloadUpdate = &jobdb.ClientPayloadUpdate{Mode: "reset", Value: raw, ExpectedRevision: &revision}
	}
	return req, d, published, nil
}

func (l *lease) Reschedule(ctx context.Context, req jobdb.RescheduleExecutionRequest) error {
	req, _, _, err := l.prepareReschedule(ctx, req)
	if err != nil {
		return err
	}
	current, e := l.current()
	if e != nil {
		return e
	}
	err = current.ExecutionLease.Reschedule(ctx, req)
	l.forget()
	return err
}

// Forward the optional port explicitly; embedding ExecutionLease hides it.
// Demand publication uses the same payload CAS as ordinary rescheduling.
func (l *lease) RescheduleResourceBackoff(ctx context.Context, req jobdb.ResourceBackoffRequest) (jobdb.ResourceBackoffReceipt, error) {
	prepared, _, _, err := l.prepareReschedule(ctx, jobdb.RescheduleExecutionRequest{ClientPayloadUpdate: req.ClientPayloadUpdate})
	if err != nil {
		return jobdb.ResourceBackoffReceipt{}, err
	}
	current, err := l.current()
	if err != nil {
		return jobdb.ResourceBackoffReceipt{}, err
	}
	port, ok := current.ExecutionLease.(jobdb.ResourceBackoffLease)
	if !ok {
		return jobdb.ResourceBackoffReceipt{}, jobdb.ErrResourceBackoffUnsupported
	}
	req.ClientPayloadUpdate = prepared.ClientPayloadUpdate
	receipt, err := port.RescheduleResourceBackoff(ctx, req)
	l.forget()
	return receipt, err
}

func (l *lease) Complete(ctx context.Context, req jobdb.CompleteExecutionRequest) error {
	current, err := l.current()
	if err != nil {
		return err
	}
	gate, err := payloadGate(current.ClientPayload())
	if err != nil {
		return err
	}
	if gate != nil {
		revision := current.ClientPayloadRevision()
		raw, _, err := clientpayload.Apply(current.ClientPayload(), revision, req.ClientPayloadUpdate)
		if err != nil {
			return err
		}
		raw, err = payloadWithGate(raw, nil)
		if err != nil {
			return err
		}
		req.ClientPayloadUpdate = &jobdb.ClientPayloadUpdate{Mode: "reset", Value: raw, ExpectedRevision: &revision}
	}
	req = l.finalizeMetrics(ctx, req)
	err = current.ExecutionLease.Complete(ctx, req)
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

func (w *guardedTask) MetricAdmission() {}
func (w *guardedTask) Run(ctx jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(ctx.Context(), ctx, input)
}
func (w *guardedTask) RunTask(local context.Context, ctx jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
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
	gate, parked, err := l.waitAdmission(local, ctx)
	if err != nil || parked {
		return nil, err
	}
	if gate != nil && gate.Deadline != nil {
		var cancel context.CancelFunc
		local, cancel = context.WithDeadlineCause(local, *gate.Deadline, gate.timeout())
		defer cancel()
	}
	// Recipe-resolution tasks run before a scope is staged. Scoped recipe work
	// always replays first, including when claimed through a pending-task route.
	needs := execution.Requirements{}
	if d != nil {
		needs = d.Effective
	}
	if err := l.checkBase(ctx.Context(), needs, taskBaseSource(ctx.InvocationID, ctx.Step)); err != nil {
		return nil, err
	}
	if d != nil {
		mismatches, err := execution.Compare(d.Effective, r.Allocation)
		if err != nil {
			return nil, err
		}
		if len(mismatches) > 0 {
			return l.yieldEnvironment(ctx, mismatches)
		}
	}
	local = r.activityContext(local, ctx.JobKey, l.leaseState)
	_, port := l.ExecutionLease.(jobdb.ResourceBackoffLease)
	local = resourceretry.WithSupport(local, port && l.ExecutionState().ResourceBackoff.Validate(true) == nil)
	local = context.WithValue(local, continuationLeaseKey{}, continuationLease{l.Route(), l.ExecutionState()})
	local = extensioneagain.WithWait(local, WaitForContinuation)
	out, err := w.runWithGitProfile(local, ctx, input, l)
	if extensionwait.IsDiscard(err) {
		if _, eligible := extensionwait.Eligible(err); !eligible {
			return l.handleWait(ctx, err)
		}
	}
	if gate != nil && gate.Deadline != nil && !time.Now().Before(*gate.Deadline) {
		return nil, gate.timeout()
	}
	if extensionwait.IsDiscard(err) {
		return l.handleWait(ctx, err)
	}
	if resourceretry.Is(err) {
		if stop := context.Cause(ctx.Context()); stop != nil {
			return nil, stop
		}
		if stop := context.Cause(local); stop != nil {
			return nil, stop
		}
		if !resourceretry.Supported(local) {
			return nil, jobdb.ClassifyExecutionInfrastructure(jobdb.ErrResourceBackoffUnsupported, jobdb.ExecutionInfrastructureRuntimeIO, "resource_backoff", nil, nil)
		}
		if ctx.Logger != nil {
			ctx.Logger.Info("Waiting for temporary resource shortage", "reason", jobdb.ResourceBackoffReason)
		}
		return nil, jobworkflow.EAGAINWait(ctx)
	}
	var required *execution.Required
	if !errors.As(err, &required) {
		return out, err
	}
	if err := ctx.Context().Err(); err != nil {
		return nil, err
	}
	mismatches, err := execution.Compare(required.Needs, r.Allocation)
	if err != nil {
		return nil, err
	}
	if len(mismatches) == 0 {
		return nil, fmt.Errorf("extension execution_required protocol error: requested requirements are already satisfied")
	}
	r.mu.Lock()
	d = l.demand
	r.mu.Unlock()
	if d == nil {
		return nil, fmt.Errorf("extension execution_required requires staged recipe execution requirements")
	}
	negotiated, err := execution.Negotiate(*d, required.Needs)
	if err != nil {
		return nil, err
	}
	if err := l.checkBase(ctx.Context(), negotiated.Effective, taskBaseSource(ctx.InvocationID, ctx.Step)+" (extension execution_required)"); err != nil {
		return nil, err
	}
	r.Stage(ctx.JobKey, negotiated)
	out, err = l.yieldEnvironment(ctx, mismatches)
	if err != nil && !jobworkflow.IsExecutionSuspended(err) {
		l.restoreDemand(d)
	}
	return out, err
}

func (l *lease) yieldEnvironment(ctx jobworkflow.TaskContext, mismatches []execution.Mismatch) (jobdb.TaskData, error) {
	// A staged demand belongs to the recipe's current task, which may be later
	// than the task route on the acquired lease. Resume recipe replay, not stale
	// pending-task coordinates from that earlier handoff.
	req := jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: l.Route().JobType}}
	req, publishedDemand, published, err := l.prepareReschedule(ctx.Context(), req)
	if err != nil {
		return nil, err
	}
	metadata, err := json.Marshal(execution.Handoff{Kind: "environment_required", JobKey: ctx.JobKey, Demand: *publishedDemand, Allocation: l.runtime.Allocation, Mismatches: mismatches, Published: published})
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
