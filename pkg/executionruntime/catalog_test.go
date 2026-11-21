package executionruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executioncatalog"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/clientpayload"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func installCatalog(t *testing.T, f *fixture, names ...string) executioncatalog.Policy {
	t.Helper()
	now := time.Now().UTC()
	if names == nil {
		names = []string{}
	}
	c := execution.BaseCatalog{Version: 1, Scope: execution.CatalogScope{Runtime: "https://test-jobdb", Tenant: "tenant"}, Complete: true, NamedBases: names, Dynamic: []execution.DynamicBaseRule{}, Generation: "generation", PublishedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	_, err = f.runtime.(jobdb.ConfigurationAdmin).SetTenantConfigurationValue(celltest.Context(context.Background()), jobdb.SetTenantConfigurationValueRequest{TenantID: "tenant", Name: execution.BaseCatalogKey, Value: raw})
	require.NoError(t, err)
	return executioncatalog.Policy{Required: true, Scope: c.Scope}
}
func runCatalogNode(t *testing.T, f *fixture, p executioncatalog.Policy, alloc execution.Allocation, tasks []jobworkflow.TaskWorker) jobworkflow.JobRunOutcome {
	t.Helper()
	rt := New(f.runtime, alloc)
	rt.BaseCatalog = p
	worker := compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{Allocation: alloc, StageExecution: rt.Stage, WrapTaskWorker: rt.WrapTaskWorker})
	runnable, err := jobworkflow.GetJobForRun(celltest.Context(context.Background()), rt, jobworkflow.GetJobForRunRequest{JobKey: f.key, JobWorker: worker, TaskWorkers: append(guardTasks(rt, tasks), compiler.NewCellContextTaskWorker()), WorkerID: "catalog", LeaseDuration: time.Minute})
	require.NoError(t, err)
	out, err := runnable.Run(nil)
	require.NoError(t, err)
	return out
}
func TestExecutionBaseCatalogDurableRefusalCatchAndReplay(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f, tasks := newNodeFixture(t, backend, `id: refusal
sequence:
 - id: bad
   op: needs_a
   execution_needs: {base: macos-sequoia-15.7.7-arm64}
   catch:
    - when: "failure.code == 'UNSUPPORTED_EXECUTION_BASE' && failure.retryable == false"
      continue:
       outputs:
        code: "${{ failure.code }}"
        choices: "${{ failure.attrs.named_bases }}"
        source: "${{ failure.attrs.source }}"
 - id: recovery
   op: needs_b
outputs:
 code: "${{ sequence.bad.outputs.code }}"
 choices: "${{ sequence.bad.outputs.choices }}"
 source: "${{ sequence.bad.outputs.source }}"
`)
			p := installCatalog(t, f, "macos-15-arm64")
			out := runCatalogNode(t, f, p, baseAllocation("macos-15-arm64"), f.tasks)
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
			require.Zero(t, tasks[0].runs.Load())
			require.EqualValues(t, 1, tasks[1].runs.Load())
			raw, err := out.Output.GetData()
			require.NoError(t, err)
			require.Contains(t, string(raw), executioncatalog.Unsupported)
			require.Contains(t, string(raw), "macos-15-arm64")
			require.Contains(t, string(raw), "execution.base")
			info, err := f.runtime.GetJob(celltest.Context(context.Background()), f.key)
			require.NoError(t, err)
			d, err := execution.PayloadDemand(info.ClientPayload)
			require.NoError(t, err)
			require.Nil(t, d, "refusal must not publish impossible demand")
			// Replay the durable failed task/catch after the offering has changed.
			installCatalog(t, f)
			engine, err := jobworkflow.NewEngineBuilder().WithRuntime(f.runtime).WithWorkerTenantId("tenant").PlusWorkers(compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{ReadOnlyReplay: true})).BuildEngine()
			require.NoError(t, err)
			_, err = engine.ReplayJobRun(celltest.Context(context.Background()), jobworkflow.ReplayRunRequest{JobKey: f.key, JobWorker: compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{ReadOnlyReplay: true})})
			require.NoError(t, err)
			require.EqualValues(t, 1, tasks[1].runs.Load())
		})
	}
}
func TestExecutionBaseCatalogAllowedHandoffAndWithdrawal(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f, tasks := newNodeFixture(t, backend, `id: handoff
sequence:
 - id: mac
   op: needs_a
   execution_needs: {base: macos-15-arm64}
`)
			p := installCatalog(t, f, "macos-15-arm64")
			out := runCatalogNode(t, f, p, baseAllocation("linux"), f.tasks)
			require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
			event, err := compiler.ExecutionHandoffFromOutcome(out)
			require.NoError(t, err)
			require.NotNil(t, event)
			require.True(t, event.Published)
			require.Zero(t, tasks[0].runs.Load())
			before, err := f.runtime.GetJob(celltest.Context(context.Background()), f.key)
			require.NoError(t, err)
			installCatalog(t, f) // Publication alone must not mutate the stranded job.
			after, err := f.runtime.GetJob(celltest.Context(context.Background()), f.key)
			require.NoError(t, err)
			require.JSONEq(t, string(before.ClientPayload), string(after.ClientPayload))
			lease, err := f.runtime.GetJobLease(celltest.Context(context.Background()), jobdb.GetJobLeaseRequest{JobKey: f.key, WorkerID: "supplied", Routes: []jobdb.Route{*out.NextRoute}, LeaseDuration: time.Minute})
			require.NoError(t, err)
			require.NotNil(t, lease)
			rt := New(f.runtime, baseAllocation("macos-15-arm64"))
			rt.BaseCatalog = p
			out, err = runFixtureLease(t, f, rt, lease, f.tasks)
			require.NoError(t, err)
			require.Equal(t, jobworkflow.JobRunFailed, out.Status)
			require.Zero(t, tasks[0].runs.Load())
			require.Contains(t, out.JobError.Error(), executioncatalog.Unsupported)
		})
	}
}
func TestExecutionBaseCatalogOrdinaryRescheduleAndNativeCallbacks(t *testing.T) {
	f, _ := newNodeFixture(t, "sqlite", `id: probe
op: needs_a
`)
	p := installCatalog(t, f, "linux")
	rt := New(f.runtime, baseAllocation("linux"))
	rt.BaseCatalog = p
	raw, err := f.runtime.GetJobLease(celltest.Context(context.Background()), jobdb.GetJobLeaseRequest{JobKey: f.key, WorkerID: "owner", Routes: []jobdb.Route{{JobType: "recipe"}}, LeaseDuration: time.Minute})
	require.NoError(t, err)
	wrapped, err := rt.accept(context.Background(), raw)
	require.NoError(t, err)
	defer wrapped.StopKeepAlive()
	d := execution.Demand{SchemaVersion: execution.DemandSchemaVersion, RecipeResolution: "resolved", Effective: execution.Requirements{Base: str("bad")}}
	rt.Stage(f.key, d)
	err = wrapped.Reschedule(context.Background(), jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "recipe"}})
	require.ErrorContains(t, err, executioncatalog.Unsupported)
	require.NoError(t, raw.KeepAlive(context.Background()))
	payload, err := execution.PayloadWithDemand(json.RawMessage(`{"external":{"large":9007199254740993}}`), d)
	require.NoError(t, err)
	revision := raw.ClientPayloadRevision()
	err = wrapped.Reschedule(context.Background(), jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "recipe"}, ClientPayloadUpdate: &jobdb.ClientPayloadUpdate{Mode: "reset", Value: payload, ExpectedRevision: &revision}})
	require.ErrorContains(t, err, executioncatalog.Unsupported)
	valid := d
	valid.Effective.Base = str("linux")
	validPayload, err := execution.PayloadWithDemand(json.RawMessage(`{"external":{"large":9007199254740993}}`), valid)
	require.NoError(t, err)
	staleRevision := revision + 1
	err = wrapped.Reschedule(context.Background(), jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "recipe"}, ClientPayloadUpdate: &jobdb.ClientPayloadUpdate{Mode: "reset", Value: validPayload, ExpectedRevision: &staleRevision}})
	require.ErrorIs(t, err, clientpayload.ErrConflict)
	require.NoError(t, raw.KeepAlive(context.Background()), "a failed candidate must not surrender the owning lease")
	initial, err := execution.Initial(nil, true, d.Effective)
	require.NoError(t, err)
	metadata, _ := json.Marshal(map[string]any{"execution": initial})
	job := jobdb.SubmitJob{TenantId: "tenant", Cell: "test", CellRepo: celltest.Repo(""), JobType: "recipe", Data: jobdb.NewTaskDataOrPanic(nil), Metadata: metadata}
	_, err = wrapped.SubmitJob(context.Background(), jobdb.SubmitJobRequest{Job: job})
	require.ErrorContains(t, err, executioncatalog.Unsupported)
	_, err = wrapped.StartOrAppendJob(context.Background(), jobdb.StartOrAppendJobRequest{Job: jobdb.StartOrAppendJob{Job: job, AppendKey: "test", MaxInputs: 2}})
	require.ErrorContains(t, err, executioncatalog.Unsupported)
	_, err = wrapped.SubmitRestartJob(context.Background(), jobdb.SubmitRestartJobRequest{Job: jobdb.SubmitRestartJob{PriorJobKey: f.key, ClientPayloadUpdate: &jobdb.ClientPayloadUpdate{Mode: "reset", Value: payload}}})
	require.ErrorContains(t, err, executioncatalog.Unsupported)
	require.Empty(t, raw.ClientPayload())
}

func TestExecutionBaseCatalogUncertainHandoffRecoversOnce(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f, tasks := newNodeFixture(t, backend, `id: uncertain
op: needs_a
execution_needs: {base: macos-15-arm64}
`)
			policy := installCatalog(t, f, "macos-15-arm64")
			rt := New(lostResponseRuntime{f.runtime}, baseAllocation("linux"))
			rt.BaseCatalog = policy
			worker := compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{Allocation: rt.Allocation, StageExecution: rt.Stage, WrapTaskWorker: rt.WrapTaskWorker})
			runnable, err := jobworkflow.GetJobForRun(celltest.Context(context.Background()), rt, jobworkflow.GetJobForRunRequest{JobKey: f.key, JobWorker: worker, TaskWorkers: append(guardTasks(rt, f.tasks), compiler.NewCellContextTaskWorker()), WorkerID: "lost-catalog-response", LeaseDuration: time.Minute})
			require.NoError(t, err)
			uncertain, runErr := runnable.Run(nil) // The committed reschedule also prevents completion on this lease.
			require.Nil(t, uncertain.Handoff, "a lost response is not an acknowledged handoff")
			require.Zero(t, tasks[0].runs.Load())
			info, err := f.runtime.GetJob(celltest.Context(context.Background()), f.key)
			require.NoError(t, err)
			demand, err := execution.PayloadDemand(info.ClientPayload)
			require.NoError(t, err)
			require.NotNil(t, demand, "outcome=%s error=%v run error=%v", uncertain.Status, uncertain.JobError, runErr)
			require.Equal(t, "macos-15-arm64", *demand.Effective.Base)
			out := runCatalogNode(t, f, policy, baseAllocation("macos-15-arm64"), f.tasks)
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
			require.EqualValues(t, 1, tasks[0].runs.Load())
		})
	}
}

func TestExecutionBaseCatalogRepositoryRefusalDoesNotContaminateRecovery(t *testing.T) {
	f, tasks := newNodeFixture(t, "sqlite", `id: defaults
sequence:
 - id: bad_defaults
   op: needs_a
   catch:
    - when: "failure.code == 'UNSUPPORTED_EXECUTION_BASE'"
      continue: {outputs: {caught: true}}
 - id: recovery
   op: needs_b
   execution_needs: {base: linux}
`)
	p := installCatalog(t, f, "linux")
	a := &repositoryTask{nodeTask: tasks[0], defaults: execution.Requirements{Base: str("bad")}}
	out := runCatalogNode(t, f, p, baseAllocation("linux"), []jobworkflow.TaskWorker{a, tasks[1]})
	require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
	require.Zero(t, tasks[0].runs.Load())
	require.EqualValues(t, 1, tasks[1].runs.Load())
}

type delayedCatalogLease struct {
	jobdb.ExecutionLease
	entered chan struct{}
	release chan struct{}
}

func (l delayedCatalogLease) GetConfiguration(ctx context.Context) (jobdb.ConfigurationSnapshot, error) {
	snapshot, err := l.ExecutionLease.GetConfiguration(ctx)
	close(l.entered)
	<-l.release
	return snapshot, err
}
func TestExecutionBaseCatalogFencesReadAgainstLeaseLoss(t *testing.T) {
	f, _ := newNodeFixture(t, "sqlite", "id: fence\nop: needs_a\n")
	p := installCatalog(t, f)
	raw, err := f.runtime.GetJobLease(celltest.Context(context.Background()), jobdb.GetJobLeaseRequest{JobKey: f.key, WorkerID: "owner", Routes: []jobdb.Route{{JobType: "recipe"}}, LeaseDuration: time.Minute})
	require.NoError(t, err)
	defer raw.StopKeepAlive()
	entered, release := make(chan struct{}), make(chan struct{})
	rt := New(f.runtime, baseAllocation("linux"))
	rt.BaseCatalog = p
	wrapped := rt.wrapLease(delayedCatalogLease{raw, entered, release}, &leaseState{}).(*lease)
	result := make(chan error, 1)
	go func() {
		result <- wrapped.checkBase(context.Background(), execution.Requirements{Base: str("bad")}, "task")
	}()
	<-entered
	rt.forgetLeaseForCatalogTest(f.key)
	close(release)
	require.ErrorIs(t, <-result, jobdb.ErrExecutionLeaseLost, "stale read must not become a requester refusal")
}
func (r *Runtime) forgetLeaseForCatalogTest(key jobdb.JobKey) {
	r.mu.Lock()
	delete(r.leases, key)
	r.mu.Unlock()
}

func TestExecutionBaseCatalogControllerAndDelegatedSubmissions(t *testing.T) {
	for _, delegated := range []bool{false, true} {
		t.Run(fmt.Sprint(delegated), func(t *testing.T) {
			backend := "sqlite"
			if delegated {
				backend = "remote"
			}
			f, _ := newNodeFixture(t, backend, "id: parent\nop: needs_a\n")
			p := installCatalog(t, f, "linux")
			rt := New(f.runtime, baseAllocation("linux"))
			rt.BaseCatalog = p
			if !delegated {
				rt.Issuer = nil
			}
			baseline, err := f.runtime.ListJobs(celltest.Context(context.Background()), jobdb.ListJobsRequest{TenantIds: []string{"tenant"}})
			require.NoError(t, err)
			probe := delegationTask(func(local context.Context, tc jobworkflow.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
				rt.mu.Lock()
				owner := rt.leases[tc.JobKey]
				rt.mu.Unlock()
				grant := permissions.JobDB{Jobs: permissions.JobPermissions{Submit: true}}
				access, _, cancel, err := rt.restrictedAccess(local, tc, owner, grant, nil)
				require.NoError(t, err)
				defer cancel()
				initial, err := execution.Initial(nil, true, execution.Requirements{Base: str("bad")})
				require.NoError(t, err)
				metadata, _ := json.Marshal(map[string]any{"execution": initial})
				job := jobdb.SubmitJob{TenantId: "tenant", Cell: "test", CellRepo: tc.Job().CellRepo, JobType: "child", Data: jobdb.NewTaskDataOrPanic(nil), Metadata: metadata}
				_, err = access.Submit(local, job)
				require.ErrorContains(t, err, executioncatalog.Unsupported)
				_, err = access.StartOrAppend(local, jobdb.StartOrAppendJob{Job: job, AppendKey: "bad", MaxInputs: 2})
				require.ErrorContains(t, err, executioncatalog.Unsupported)
				if delegated {
					child, err := (permissions.JobDBIssuer{}).IssueRestrictedLease(local, tc, grant, time.Time{})
					require.NoError(t, err)
					_, err = child.GetConfiguration(local)
					require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied, "admission must not expand delegated configuration grants")
				}
				job.Metadata = nil
				ref, err := access.Submit(local, job)
				require.NoError(t, err)
				require.NotEmpty(t, ref.JobID)
				return jobdb.NewTaskDataOrPanic(map[string]any{"ok": true}), nil
			})
			worker := jobworkflow.ExecutionWorker{JobType: "recipe", Execute: func(c jobworkflow.ExecutionContext, in jobdb.JobData) (jobdb.JobData, error) {
				return c.DoTask(jobworkflow.TaskCall{Site: "delegate", TaskType: probe.Name(), ContractVersion: 1, Command: json.RawMessage(`{}`)}, func(context.Context) (jobdb.TaskData, error) { return jobdb.NewTaskDataOrPanic(nil), nil })
			}}
			run, err := jobworkflow.GetJobForRun(celltest.Context(context.Background()), rt, jobworkflow.GetJobForRunRequest{JobKey: f.key, JobWorker: worker, TaskWorkers: []jobworkflow.TaskWorker{probe}, WorkerID: "owner", LeaseDuration: time.Minute})
			require.NoError(t, err)
			out, err := run.Run(nil)
			require.NoError(t, err)
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
			listed, err := f.runtime.ListJobs(celltest.Context(context.Background()), jobdb.ListJobsRequest{TenantIds: []string{"tenant"}})
			require.NoError(t, err)
			require.Len(t, listed.Jobs, len(baseline.Jobs)+1, "only the valid child should have been created")
		})
	}
}

type catalogLeaseSubmitter struct{ l *lease }

func (s catalogLeaseSubmitter) SubmitJobRef(ctx context.Context, job jobdb.SubmitJob) (jobref.Ref, error) {
	handle, err := s.l.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: job})
	if err != nil {
		return jobref.Ref{}, err
	}
	return jobref.FromHandle(handle)
}

type catalogSubmissionTask struct {
	*nodeTask
	rt      *Runtime
	submits int
	restart bool
}

func (w *catalogSubmissionTask) RunTask(ctx context.Context, tc jobworkflow.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	w.rt.mu.Lock()
	owner := w.rt.leases[tc.JobKey]
	w.rt.mu.Unlock()
	base := "macos-15-arm64"
	var err error
	if w.restart {
		var payload json.RawMessage
		payload, err = execution.PayloadWithDemand(nil, execution.Demand{SchemaVersion: execution.DemandSchemaVersion, RecipeResolution: "resolved", Effective: execution.Requirements{Base: &base}})
		if err == nil {
			req := jobdb.SubmitRestartJobRequest{RequestID: "catalog-restart", Job: jobdb.SubmitRestartJob{PriorJobKey: tc.JobKey, Cell: "test", ClientPayloadUpdate: &jobdb.ClientPayloadUpdate{Mode: "reset", Value: payload}}}
			err = jobdbschema.SetSubmitRestartJobSchema(&req.Job)
			if err == nil {
				_, err = owner.SubmitRestartJob(ctx, req)
			}
		}
	} else {
		_, err = starter.StartRecipeJobWithOptions(ctx, workflowctl.StartJob{TenantId: tc.JobKey.TenantId, Cell: "test", CellRepo: tc.Job().CellRepo, RecipeName: "child"}, catalogLeaseSubmitter{owner}, starter.StartRecipeJobOptions{ExecutionRequirements: execution.Requirements{Base: &base}})
	}
	if err != nil {
		return nil, err
	}
	w.submits++
	return w.nodeTask.Run(tc, in)
}
func (w *catalogSubmissionTask) Run(tc jobworkflow.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(tc.Context(), tc, in)
}
func TestExecutionBaseCatalogAcknowledgedSubmissionReplay(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprintf("restart=%t", restart), func(t *testing.T) {
			f, tasks := newNodeFixture(t, "sqlite", `id: receipts
sequence:
 - id: submit
   op: needs_a
 - id: finish
   op: needs_b
`)
			p := installCatalog(t, f, "linux", "macos-15-arm64")
			first := &catalogSubmissionTask{nodeTask: tasks[0], restart: restart}
			run := func(workers []jobworkflow.TaskWorker) jobworkflow.JobRunOutcome {
				rt := New(f.runtime, baseAllocation("linux"))
				rt.BaseCatalog = p
				first.rt = rt
				worker := compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{Allocation: rt.Allocation, StageExecution: rt.Stage, WrapTaskWorker: rt.WrapTaskWorker})
				runnable, err := jobworkflow.GetJobForRun(celltest.Context(context.Background()), rt, jobworkflow.GetJobForRunRequest{JobKey: f.key, JobWorker: worker, TaskWorkers: append(guardTasks(rt, workers), compiler.NewCellContextTaskWorker()), WorkerID: "receipt", LeaseDuration: time.Minute})
				require.NoError(t, err)
				out, err := runnable.Run(nil)
				require.NoError(t, err)
				return out
			}
			out := run([]jobworkflow.TaskWorker{first})
			require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
			require.Equal(t, 1, first.submits)
			installCatalog(t, f, "linux")
			out = run([]jobworkflow.TaskWorker{first, tasks[1]})
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
			require.Equal(t, 1, first.submits, "acknowledged task receipt must replay after child-base withdrawal")
		})
	}
}

type withdrawingCatalogLease struct {
	jobdb.RenewableExecutionLease
	reads     *atomic.Int32
	allowed   jobdb.ConfigurationSnapshot
	withdrawn jobdb.ConfigurationSnapshot
}

func (l *withdrawingCatalogLease) GetConfiguration(context.Context) (jobdb.ConfigurationSnapshot, error) {
	reads := l.reads.Add(1)
	// Task admission and repository admission succeed; final publication sees withdrawal.
	if reads >= 4 {
		return l.withdrawn, nil
	}
	return l.allowed, nil
}

type catalogRepositoryProbe struct {
	*repositoryTask
	rt    *Runtime
	after *execution.Demand
}

func (w *catalogRepositoryProbe) RunTask(ctx context.Context, tc jobworkflow.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	out, err := w.repositoryTask.RunTask(ctx, tc, in)
	w.rt.mu.Lock()
	if l := w.rt.leases[tc.JobKey]; l != nil && l.demand != nil {
		copy := *l.demand
		w.after = &copy
	}
	w.rt.mu.Unlock()
	return out, err
}
func (w *catalogRepositoryProbe) Run(tc jobworkflow.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(tc.Context(), tc, in)
}
func TestExecutionBaseCatalogFinalPublicationWithdrawalRestoresCandidate(t *testing.T) {
	f, tasks := newNodeFixture(t, "sqlite", `id: withdrawal
op: needs_a
`)
	p := installCatalog(t, f, "macos-15-arm64")
	ctx := celltest.Context(context.Background())
	allowed, err := f.runtime.(jobdb.ConfigurationAdmin).GetTenantConfiguration(ctx, "tenant")
	require.NoError(t, err)
	installCatalog(t, f)
	withdrawn, err := f.runtime.(jobdb.ConfigurationAdmin).GetTenantConfiguration(ctx, "tenant")
	require.NoError(t, err)
	raw, err := f.runtime.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: f.key, WorkerID: "owner", Routes: []jobdb.Route{{JobType: "recipe"}}, LeaseDuration: time.Minute})
	require.NoError(t, err)
	held := &withdrawingCatalogLease{RenewableExecutionLease: raw.(jobdb.RenewableExecutionLease), allowed: allowed, withdrawn: withdrawn, reads: &atomic.Int32{}}
	rt := New(f.runtime, baseAllocation("linux"))
	rt.BaseCatalog = p
	probe := &catalogRepositoryProbe{repositoryTask: &repositoryTask{nodeTask: tasks[0], defaults: execution.Requirements{Base: str("macos-15-arm64")}}, rt: rt}
	out, err := runFixtureLease(t, f, rt, held, []jobworkflow.TaskWorker{probe})
	require.NoError(t, err)
	require.Equal(t, jobworkflow.JobRunFailed, out.Status)
	require.ErrorContains(t, out.JobError, executioncatalog.Unsupported)
	require.GreaterOrEqual(t, held.reads.Load(), int32(4))
	require.Contains(t, out.JobError.Error(), "reschedule.execution.base")
	require.NotNil(t, probe.after)
	require.Nil(t, probe.after.Effective.Base, "rejected final check must restore the prior staged requirements")
	info, err := f.runtime.GetJob(ctx, f.key)
	require.NoError(t, err)
	require.Empty(t, info.ClientPayload)
	require.Zero(t, tasks[0].runs.Load())
}

func (l *withdrawingCatalogLease) LeaseSchemaHash() string {
	return l.RenewableExecutionLease.(interface{ LeaseSchemaHash() string }).LeaseSchemaHash()
}

func (l *withdrawingCatalogLease) Renew(ctx context.Context) (jobdb.RenewableExecutionLease, error) {
	next, err := l.RenewableExecutionLease.Renew(ctx)
	if err != nil {
		return nil, err
	}
	return &withdrawingCatalogLease{RenewableExecutionLease: next, reads: l.reads, allowed: l.allowed, withdrawn: l.withdrawn}, nil
}

func TestExecutionBaseCatalogInvalidAuthorityAndOpenNamespaceDoNotPublish(t *testing.T) {
	for _, kind := range []string{"missing", "malformed", "expired", "open_namespace"} {
		t.Run(kind, func(t *testing.T) {
			f, tasks := newNodeFixture(t, "toy", `id: invalid
op: needs_a
execution_needs: {base: 'o*'}
`)
			p := executioncatalog.Policy{Required: true, Scope: execution.CatalogScope{Runtime: "https://test-jobdb", Tenant: "tenant"}}
			raw := json.RawMessage(`{"version":2}`)
			want := executioncatalog.Unavailable
			if kind == "expired" || kind == "open_namespace" {
				now := time.Now().UTC()
				expiry := now.Add(-time.Second)
				if kind == "open_namespace" {
					expiry = now.Add(time.Hour)
					want = executioncatalog.CannotVerify
				}
				c := execution.BaseCatalog{Version: 1, Scope: p.Scope, Complete: true, NamedBases: []string{}, Dynamic: []execution.DynamicBaseRule{{Kind: "oci"}}, Generation: "one", PublishedAt: now.Add(-time.Hour), ExpiresAt: expiry}
				raw, _ = json.Marshal(c)
			}
			if kind != "missing" {
				_, err := f.runtime.(jobdb.ConfigurationAdmin).SetTenantConfigurationValue(celltest.Context(context.Background()), jobdb.SetTenantConfigurationValueRequest{TenantID: "tenant", Name: execution.BaseCatalogKey, Value: raw})
				require.NoError(t, err)
			}
			out := runCatalogNode(t, f, p, baseAllocation("linux"), f.tasks)
			require.Equal(t, jobworkflow.JobRunFailed, out.Status)
			require.ErrorContains(t, out.JobError, want)
			require.Zero(t, tasks[0].runs.Load())
			require.Nil(t, out.Handoff)
			info, err := f.runtime.GetJob(celltest.Context(context.Background()), f.key)
			require.NoError(t, err)
			require.Empty(t, info.ClientPayload)
		})
	}
}
