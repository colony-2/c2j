package executionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/extensionwait"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type extensionWaitTask struct {
	waitCalls  map[int32]bool
	runs       atomic.Int32
	remaining  int
	request    extensionwait.Request
	identities []string
	run        func(context.Context) error
	waitError  error
}

func (*extensionWaitTask) Name() string { return "wait-task" }
func (w *extensionWaitTask) Run(tc wf.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(tc.Context(), tc, in)
}
func (w *extensionWaitTask) RunTask(ctx context.Context, tc wf.TaskContext, _ jobdb.TaskData) (jobdb.TaskData, error) {
	n := w.runs.Add(1)
	w.identities = append(w.identities, fmt.Sprintf("%s:%d", tc.InvocationID, tc.AttemptNumber))
	shouldWait := int(n) <= w.remaining
	if w.waitCalls != nil {
		shouldWait = w.waitCalls[n]
	}
	if shouldWait {
		if w.waitError != nil {
			return nil, w.waitError
		}
		return nil, extensionwait.Cleaned(extensionwait.New(w.request), nil)
	}
	if w.run != nil {
		if e := w.run(ctx); e != nil {
			return nil, e
		}
	}
	return jobdb.NewTaskDataOrPanic(map[string]any{"done": true}), nil
}

type extensionWaitFixture struct {
	rt     jobdb.ConsumerRuntime
	key    jobdb.JobKey
	worker wf.ExecutionWorker
	task   *extensionWaitTask
	reopen func()
}

func newExtensionWaitFixture(t *testing.T, backend string, request extensionwait.Request, policy jobdb.RunPolicy, jobPolicy ...jobdb.RunPolicy) *extensionWaitFixture {
	t.Helper()
	ctx := waitTestContext(context.Background())
	var rt jobdb.ConsumerRuntime = toy.New()
	var reopen func()
	if backend != "toy" {
		path := filepath.Join(t.TempDir(), "wait.db")
		db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: path})
		require.NoError(t, err)
		rt = db
		t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
		reopen = func() {
			require.NoError(t, db.Close(ctx))
			db, err = sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: path})
			require.NoError(t, err)
			rt = db
		}
	}
	f := &extensionWaitFixture{task: &extensionWaitTask{remaining: 1, request: request}}
	if backend == "remote" {
		handler := remote.NewConsumerServer(rt)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handler.ServeHTTP(w, r.WithContext(waitTestContext(r.Context())))
		}))
		t.Cleanup(server.Close)
		client, err := remote.New(server.URL, server.Client())
		require.NoError(t, err)
		rt = client
		reopen = nil
	}
	f.rt = rt
	if reopen != nil {
		f.reopen = func() { reopen(); f.rt = rt }
	}
	celltest.Create(t, rt, "wait-tenant", "test", "example.test/test")
	f.worker = wf.ExecutionWorker{JobType: "wait-test", Execute: func(c wf.ExecutionContext, in jobdb.JobData) (jobdb.JobData, error) {
		return c.DoTask(wf.TaskCall{Site: "extension", TaskType: f.task.Name(), ContractVersion: 1, Command: json.RawMessage(`{}`), Policy: policy}, func(context.Context) (jobdb.TaskData, error) { return in, nil })
	}}
	jobRunPolicy := jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 1}}
	if len(jobPolicy) > 0 {
		jobRunPolicy = jobPolicy[0]
	}
	job, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "wait-tenant", JobType: f.worker.Name(), Cell: "test", CellRepo: "example.test/test", RunPolicy: jobRunPolicy, Data: jobdb.NewTaskDataOrPanic(map[string]any{})}})
	require.NoError(t, err)
	f.key = job.JobKey
	return f
}
func (f *extensionWaitFixture) run(t *testing.T) (wf.JobRunOutcome, error) {
	t.Helper()
	rt := New(f.rt, execution.Allocation{SchemaVersion: 2})
	req := wf.GetJobForRunRequest{JobKey: f.key, JobWorker: f.worker, TaskWorkers: []wf.TaskWorker{rt.WrapTaskWorker(f.task)}, WorkerID: "wait-check", LeaseDuration: time.Minute}
	run, err := wf.GetJobForRun(waitTestContext(context.Background()), rt, req)
	if err != nil {
		return wf.JobRunOutcome{}, err
	}
	return run.Run(nil)
}
func (f *extensionWaitFixture) gate(t *testing.T) *waitGate {
	t.Helper()
	info, err := f.rt.GetJob(waitTestContext(context.Background()), f.key)
	require.NoError(t, err)
	g, err := payloadGate(info.ClientPayload)
	require.NoError(t, err)
	return g
}
func (f *extensionWaitFixture) target(t *testing.T, id, goal string) jobdb.JobHandle {
	t.Helper()
	job, err := f.rt.SubmitJob(waitTestContext(context.Background()), jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: f.key.TenantId, JobID: id, GoalID: goal, JobType: "target", Cell: "test", CellRepo: "example.test/test", Data: jobdb.NewTaskDataOrPanic(nil)}})
	require.NoError(t, err)
	return job
}
func (f *extensionWaitFixture) finish(t *testing.T, target jobdb.JobHandle, success bool) {
	t.Helper()
	ctx := waitTestContext(context.Background())
	l, err := f.rt.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: target.JobKey, Routes: []jobdb.Route{{JobType: "target"}}, WorkerID: "target", LeaseDuration: time.Minute})
	require.NoError(t, err)
	require.NotNil(t, l)
	var outcome jobdb.ChapterOutcome = jobdb.ApplicationOutputOutcome{Output: jobdb.ApplicationOutputBytes{Data: []byte(`{}`)}}
	status := "success"
	if !success {
		status = "failed"
		outcome = jobdb.AppErrorOutcome{Error: jobdb.AppErrorPayload{Message: "target failed"}}
	}
	require.NoError(t, l.Complete(ctx, jobdb.CompleteExecutionRequest{Status: status, Chapter: &jobdb.Chapter{Ordinal: 1, TaskType: "target", CreatedAt: time.Now().UTC(), Body: jobdb.JobAttemptOutcomeChapter{Outcome: outcome}}}))
}
func waitReady(t *testing.T, g *waitGate) {
	t.Helper()
	at := g.NotBefore
	if g.Request.Until != nil && g.Request.Until.After(at) {
		at = *g.Request.Until
	}
	if d := time.Until(at); d > 0 {
		time.Sleep(d + 20*time.Millisecond)
	}
}
func TestExtensionWaitScheduling(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			t.Run("W25_W35_time_recovery", func(t *testing.T) {
				until := time.Now().UTC().Add(1400 * time.Millisecond)
				f := newExtensionWaitFixture(t, backend, extensionwait.Request{Version: 1, Until: &until}, jobdb.RunPolicy{})
				out, e := f.run(t)
				require.NoError(t, e)
				require.Equal(t, wf.JobRunSuspended, out.Status, "%v", out.JobError)
				g := f.gate(t)
				require.NotNil(t, g)
				if f.reopen != nil {
					f.reopen()
				}
				_, e = f.run(t)
				require.True(t, e != nil || out.Status == wf.JobRunSuspended, "early activation must remain suspended")
				require.EqualValues(t, 1, f.task.runs.Load())
				waitReady(t, g)
				out, e = f.run(t)
				require.NoError(t, e)
				require.Equal(t, wf.JobRunCompleted, out.Status, "%v", out.JobError)
				require.Equal(t, f.task.identities[0], f.task.identities[1])
				require.Nil(t, f.gate(t))
			})
			for _, condition := range []string{"complete", "success"} {
				for _, terminal := range []string{"success", "failed", "cancelled"} {
					t.Run("W26_W27_"+condition+"_"+terminal, func(t *testing.T) {
						f := newExtensionWaitFixture(t, backend, extensionwait.Request{Version: 1, Dependencies: []extensionwait.Dependency{{Job: "target", Condition: condition}}}, jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 1}})
						target := f.target(t, "target", "")
						out, e := f.run(t)
						require.NoError(t, e)
						require.Equal(t, wf.JobRunSuspended, out.Status, "%v", out.JobError)
						g := f.gate(t)
						require.NotNil(t, g)
						if terminal == "cancelled" {
							require.NoError(t, f.rt.CancelJob(waitTestContext(context.Background()), jobdb.CancelJobRequest{JobKey: target.JobKey}))
						} else {
							f.finish(t, target, terminal == "success")
						}
						waitReady(t, g)
						out, e = f.run(t)
						require.NoError(t, e)
						if condition == "success" && terminal != "success" {
							require.Equal(t, wf.JobRunFailed, out.Status)
							require.EqualValues(t, 1, f.task.runs.Load())
						} else {
							require.Equal(t, wf.JobRunCompleted, out.Status, "%v", out.JobError)
							require.EqualValues(t, 2, f.task.runs.Load())
						}
					})
				}
			}
			t.Run("W28_goal_replacement", func(t *testing.T) {
				f := newExtensionWaitFixture(t, backend, extensionwait.Request{Version: 1}, jobdb.RunPolicy{})
				first := f.target(t, "first", "")
				f.task.request.Dependencies = []extensionwait.Dependency{{Goal: first.GoalID, Condition: "success"}}
				out, e := f.run(t)
				require.NoError(t, e)
				require.Equal(t, wf.JobRunSuspended, out.Status, "%v", out.JobError)
				f.finish(t, first, false)
				_, e = f.run(t)
				require.True(t, e != nil || out.Status == wf.JobRunSuspended)
				second := f.target(t, "replacement", first.GoalID)
				f.finish(t, second, true)
				waitReady(t, f.gate(t))
				out, e = f.run(t)
				require.NoError(t, e)
				require.Equal(t, wf.JobRunCompleted, out.Status, "%v", out.JobError)
			})
			t.Run("W28_goal_failure", func(t *testing.T) {
				f := newExtensionWaitFixture(t, backend, extensionwait.Request{Version: 1}, jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 1}})
				target := f.target(t, "first", "")
				f.task.request.Dependencies = []extensionwait.Dependency{{Goal: target.GoalID, Condition: "success"}}
				out, e := f.run(t)
				require.NoError(t, e)
				require.Equal(t, wf.JobRunSuspended, out.Status)
				f.finish(t, target, false)
				_, e = f.rt.FailGoal(waitTestContext(context.Background()), jobdb.FailGoalRequest{GoalKey: jobdb.GoalKey{TenantId: f.key.TenantId, GoalID: target.GoalID}, Message: "stop"})
				require.NoError(t, e)
				waitReady(t, f.gate(t))
				out, e = f.run(t)
				require.NoError(t, e)
				require.Equal(t, wf.JobRunFailed, out.Status)
				require.EqualValues(t, 1, f.task.runs.Load())
			})
			t.Run("W29_dependencies_first", func(t *testing.T) {
				until := time.Now().UTC().Add(1600 * time.Millisecond)
				f := newExtensionWaitFixture(t, backend, extensionwait.Request{Version: 1, Until: &until, Dependencies: []extensionwait.Dependency{{Job: "target", Condition: "success"}}}, jobdb.RunPolicy{})
				target := f.target(t, "target", "")
				out, e := f.run(t)
				require.NoError(t, e)
				require.Equal(t, wf.JobRunSuspended, out.Status)
				f.finish(t, target, true)
				out, e = f.run(t)
				require.NoError(t, e)
				require.Equal(t, wf.JobRunSuspended, out.Status)
				require.EqualValues(t, 1, f.task.runs.Load())
				waitReady(t, f.gate(t))
				out, e = f.run(t)
				require.NoError(t, e)
				require.Equal(t, wf.JobRunCompleted, out.Status, "%v", out.JobError)
			})
			t.Run("W29_time_first_W30_all", func(t *testing.T) {
				past := time.Now().UTC().Add(-time.Hour)
				f := newExtensionWaitFixture(t, backend, extensionwait.Request{Version: 1, Until: &past, Dependencies: []extensionwait.Dependency{{Job: "first", Condition: "success"}, {Job: "second", Condition: "success"}}}, jobdb.RunPolicy{})
				first, second := f.target(t, "first", ""), f.target(t, "second", "")
				out, e := f.run(t)
				require.NoError(t, e)
				require.Equal(t, wf.JobRunSuspended, out.Status)
				f.finish(t, first, true)
				waitReady(t, f.gate(t))
				_, e = f.run(t)
				require.True(t, e != nil || out.Status == wf.JobRunSuspended)
				require.EqualValues(t, 1, f.task.runs.Load())
				f.finish(t, second, true)
				out, e = f.run(t)
				require.NoError(t, e)
				require.Equal(t, wf.JobRunCompleted, out.Status, "%v", out.JobError)
			})
			t.Run("W46_cancel_consumer", func(t *testing.T) {
				f := newExtensionWaitFixture(t, backend, extensionwait.Request{Version: 1, Dependencies: []extensionwait.Dependency{{Job: "target", Condition: "success"}}}, jobdb.RunPolicy{})
				target := f.target(t, "target", "")
				out, e := f.run(t)
				require.NoError(t, e)
				require.Equal(t, wf.JobRunSuspended, out.Status)
				require.NoError(t, f.rt.CancelJob(waitTestContext(context.Background()), jobdb.CancelJobRequest{JobKey: f.key}))
				out, e = f.run(t)
				require.True(t, e != nil || out.Status != wf.JobRunCompleted)
				info, e := f.rt.GetJob(waitTestContext(context.Background()), target.JobKey)
				require.NoError(t, e)
				require.Empty(t, info.CompletionStatus)
				require.EqualValues(t, 1, f.task.runs.Load())
			})
		})
	}
}

// A native versioned task, not a mocked compiler result, waits more than the
// compiler's chain limit while keeping one unfinished attempt and one identity.
func TestExtensionWaitRepeatedBeyond64(t *testing.T) {
	if testing.Short() {
		t.Skip("65 elapsed scheduler activations")
	}
	until := time.Now().UTC().Add(-time.Hour)
	f := newExtensionWaitFixture(t, "toy", extensionwait.Request{Version: 1, Until: &until}, jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 1}})
	f.task.remaining = 65
	seen := map[string]bool{}
	for i := 0; i < 65; i++ {
		out, e := f.run(t)
		require.NoError(t, e)
		require.Equal(t, wf.JobRunSuspended, out.Status, "activation %d: %v", i, out.JobError)
		g := f.gate(t)
		require.False(t, seen[g.ID])
		seen[g.ID] = true
		waitReady(t, g)
	}
	out, e := f.run(t)
	require.NoError(t, e)
	require.Equal(t, wf.JobRunCompleted, out.Status, "%v", out.JobError)
	require.EqualValues(t, 66, f.task.runs.Load())
	for _, id := range f.task.identities {
		require.Equal(t, f.task.identities[0], id)
	}
}

func TestExtensionWaitRetainedDeadline(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		for _, kind := range []string{"invocation", "total"} {
			t.Run(backend+"/"+kind, func(t *testing.T) {
				until := time.Now().UTC().Add(time.Hour)
				policy := jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 1}}
				budget := jobdb.Duration(250 * time.Millisecond)
				if kind == "invocation" {
					policy.InvocationTimeout = &budget
				} else {
					policy.TotalTimeout = &budget
				}
				f := newExtensionWaitFixture(t, backend, extensionwait.Request{Version: 1, Until: &until}, policy)
				out, e := f.run(t)
				require.NoError(t, e)
				require.Equal(t, wf.JobRunSuspended, out.Status, "%v", out.JobError)
				g := f.gate(t)
				require.NotNil(t, g.Deadline)
				require.Equal(t, kind, g.TimeoutScope)
				if f.reopen != nil {
					f.reopen()
				}
				time.Sleep(time.Until(*g.Deadline) + 30*time.Millisecond)
				out, e = f.run(t)
				require.NoError(t, e)
				require.Equal(t, wf.JobRunFailed, out.Status, "%v", out.JobError)
				require.EqualValues(t, 1, f.task.runs.Load())
				var timeout jobdb.TimeoutError
				require.True(t, errors.As(out.JobError, &timeout), "%T: %v", out.JobError, out.JobError)
			})
		}
	}
}

func waitTestContext(ctx context.Context) context.Context {
	return jobdb.WithPrimaryGoalSelectionAuthorizer(celltest.Context(ctx), func(context.Context, jobdb.GoalKey, jobdb.ResolvedCell) error { return nil })
}

func TestExtensionWaitRunningDeadline(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			past := time.Now().UTC().Add(-time.Hour)
			budget := jobdb.Duration(1500 * time.Millisecond)
			f := newExtensionWaitFixture(t, backend, extensionwait.Request{Version: 1, Until: &past}, jobdb.RunPolicy{InvocationTimeout: &budget, Retry: jobdb.RetryPolicy{MaximumAttempts: 1}})
			out, e := f.run(t)
			require.NoError(t, e)
			require.Equal(t, wf.JobRunSuspended, out.Status)
			g := f.gate(t)
			waitReady(t, g)
			f.task.run = func(ctx context.Context) error {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(2 * time.Second):
					return nil
				}
			}
			start := time.Now()
			out, e = f.run(t)
			require.NoError(t, e)
			require.Equal(t, wf.JobRunFailed, out.Status, "%v", out.JobError)
			require.Less(t, time.Since(start), time.Second)
			var timeout jobdb.TimeoutError
			require.True(t, errors.As(out.JobError, &timeout), "%T: %v", out.JobError, out.JobError)
			require.Equal(t, jobdb.TimeoutScopeInvocation, timeout.Payload.Scope)
		})
	}
}

type waitFaultRuntime struct {
	jobdb.ConsumerRuntime
	mode     string
	acquired jobdb.ExecutionLease
	calls    atomic.Int32
}
type waitFaultLease struct {
	jobdb.ExecutionLease
	owner *waitFaultRuntime
}

func (r *waitFaultRuntime) GetJobLease(ctx context.Context, req jobdb.GetJobLeaseRequest) (jobdb.ExecutionLease, error) {
	l, e := r.ConsumerRuntime.GetJobLease(ctx, req)
	if e != nil || l == nil {
		return l, e
	}
	return r.wrap(l), nil
}
func (r *waitFaultRuntime) wrap(l jobdb.ExecutionLease) jobdb.ExecutionLease {
	r.acquired = l
	wrapped := &waitFaultLease{l, r}
	if _, ok := l.(jobdb.RenewableExecutionLease); ok {
		return &renewableWaitFaultLease{wrapped}
	}
	return wrapped
}

type renewableWaitFaultLease struct{ *waitFaultLease }

func (l *renewableWaitFaultLease) LeaseExpiry() time.Time {
	return l.ExecutionLease.(jobdb.RenewableExecutionLease).LeaseExpiry()
}
func (l *renewableWaitFaultLease) Renew(ctx context.Context) (jobdb.RenewableExecutionLease, error) {
	next, e := l.ExecutionLease.(jobdb.RenewableExecutionLease).Renew(ctx)
	if e != nil {
		return nil, e
	}
	return l.owner.wrap(next).(jobdb.RenewableExecutionLease), nil
}
func (l *waitFaultLease) LeaseWorkerID() string {
	if v, ok := l.ExecutionLease.(interface{ LeaseWorkerID() string }); ok {
		return v.LeaseWorkerID()
	}
	return ""
}
func (l *waitFaultLease) LeaseToken() string {
	if v, ok := l.ExecutionLease.(interface{ LeaseToken() string }); ok {
		return v.LeaseToken()
	}
	return ""
}
func (l *waitFaultLease) Reschedule(ctx context.Context, req jobdb.RescheduleExecutionRequest) error {
	l.owner.calls.Add(1)
	if l.owner.mode == "pass" {
		return l.ExecutionLease.Reschedule(ctx, req)
	}
	if l.owner.mode == "before" {
		return errors.New("injected pre-publication failure")
	}
	if l.owner.mode == "conflict" {
		return jobdb.ErrConflict
	}
	if e := l.ExecutionLease.Reschedule(ctx, req); e != nil {
		return e
	}
	return errors.New("injected lost acknowledgment")
}
func TestExtensionWaitHandoffFaults(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		for _, mode := range []string{"before", "after", "conflict"} {
			t.Run(backend+"/"+mode, func(t *testing.T) {
				past := time.Now().UTC().Add(-time.Hour)
				f := newExtensionWaitFixture(t, backend, extensionwait.Request{Version: 1, Until: &past}, jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 1}})
				base := f.rt
				fault := &waitFaultRuntime{ConsumerRuntime: base, mode: mode}
				f.rt = fault
				if _, ok := base.(jobdb.LeaseDelegator); ok {
					f.rt = &waitFaultDelegatingRuntime{fault}
				}
				out, e := f.run(t)
				require.True(t, e != nil || out.Status != wf.JobRunCompleted)
				require.EqualValues(t, 1, fault.calls.Load(), "run error=%v job error=%v status=%s", e, out.JobError, out.Status)
				require.EqualValues(t, 1, f.task.runs.Load())
				chapters, e := base.ListChapters(waitTestContext(context.Background()), jobdb.ListChaptersRequest{JobKey: f.key})
				require.NoError(t, e)
				for _, ch := range chapters {
					if b, ok := ch.Body.(jobdb.ExecutionChapter); ok {
						r, e := jobdb.DecodeExecutionRecord(b.Record)
						require.NoError(t, e)
						require.NotEqual(t, "result", r.Kind, "uncertain handoff must not become a task outcome")
					}
				}
				f.rt = base
				if mode == "after" {
					g := f.gate(t)
					require.NotNil(t, g)
					waitReady(t, g)
				} else {
					require.Nil(t, f.gate(t))
					require.NoError(t, fault.acquired.Reschedule(waitTestContext(context.Background()), jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "wait-test"}}))
				}
				out, e = f.run(t)
				require.NoError(t, e)
				require.Equal(t, wf.JobRunCompleted, out.Status, "%v", out.JobError)
				require.EqualValues(t, 2, f.task.runs.Load())
			})
		}
	}
}

type waitFaultDelegatingRuntime struct{ *waitFaultRuntime }

func (r *waitFaultDelegatingRuntime) CreateDelegation(ctx context.Context, parent jobdb.ExecutionLease, req jobdb.CreateDelegationRequest) (jobdb.RenewableExecutionLease, error) {
	switch l := parent.(type) {
	case *waitFaultLease:
		parent = l.ExecutionLease
	case *renewableWaitFaultLease:
		parent = l.ExecutionLease
	}
	return r.ConsumerRuntime.(jobdb.LeaseDelegator).CreateDelegation(ctx, parent, req)
}

func TestExtensionWaitInvalidTargets(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		for _, kind := range []string{"missing", "foreign", "self", "cycle"} {
			t.Run(backend+"/"+kind, func(t *testing.T) {
				f := newExtensionWaitFixture(t, backend, extensionwait.Request{Version: 1}, jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 1}})
				id := "missing"
				switch kind {
				case "self":
					id = f.key.JobId
				case "foreign":
					celltest.Create(t, f.rt, "foreign", "test", "example.test/test")
					_, e := f.rt.SubmitJob(waitTestContext(context.Background()), jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "foreign", JobID: "foreign-target", JobType: "target", Cell: "test", CellRepo: "example.test/test", Data: jobdb.NewTaskDataOrPanic(nil)}})
					require.NoError(t, e)
					id = "foreign-target"
				case "cycle":
					_, e := f.rt.SubmitJob(waitTestContext(context.Background()), jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: f.key.TenantId, JobID: "cycle-target", JobType: "target", Cell: "test", CellRepo: "example.test/test", Data: jobdb.NewTaskDataOrPanic(nil), Dependencies: []jobdb.Dependency{{Target: jobdb.DependencyTarget{JobID: f.key.JobId}, Condition: jobdb.JobPrereqSuccess}}}})
					require.NoError(t, e)
					id = "cycle-target"
				}
				f.task.request.Dependencies = []extensionwait.Dependency{{Job: id, Condition: "success"}}
				out, e := f.run(t)
				require.True(t, e != nil || out.Status != wf.JobRunCompleted)
				require.EqualValues(t, 1, f.task.runs.Load())
				require.Nil(t, f.gate(t), "rejected reschedule must not publish gate independently")
			})
		}
	}
}

func TestExtensionWaitReleasedGateRecovery(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			past := time.Now().UTC().Add(-time.Hour)
			budget := jobdb.Duration(3 * time.Second)
			f := newExtensionWaitFixture(t, backend, extensionwait.Request{Version: 1, Until: &past}, jobdb.RunPolicy{InvocationTimeout: &budget, Retry: jobdb.RetryPolicy{MaximumAttempts: 1}})
			out, e := f.run(t)
			require.NoError(t, e)
			require.Equal(t, wf.JobRunSuspended, out.Status)
			g := f.gate(t)
			waitReady(t, g)
			base := f.rt
			recorder := &waitFaultRuntime{ConsumerRuntime: base, mode: "pass"}
			f.rt = recorder
			if _, ok := base.(jobdb.LeaseDelegator); ok {
				f.rt = &waitFaultDelegatingRuntime{recorder}
			}
			f.task.run = func(context.Context) error { return jobdb.ErrExecutionLeaseLost }
			_, _ = f.run(t)
			after := f.gate(t)
			require.Equal(t, g, after, "released gate remains until an outcome commits")
			require.NotNil(t, recorder.acquired)
			require.NoError(t, recorder.acquired.Reschedule(waitTestContext(context.Background()), jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "wait-test"}}))
			f.rt = base
			f.task.run = nil
			if f.reopen != nil {
				f.reopen()
			}
			out, e = f.run(t)
			require.NoError(t, e)
			require.Equal(t, wf.JobRunCompleted, out.Status, "%v", out.JobError)
			require.EqualValues(t, 3, f.task.runs.Load())
			require.Nil(t, f.gate(t))
			for _, id := range f.task.identities {
				require.Equal(t, f.task.identities[0], id)
			}

		})
	}
}

func TestExtensionWaitTaskRouteResume(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			past := time.Now().UTC().Add(-time.Hour)
			f := newExtensionWaitFixture(t, backend, extensionwait.Request{Version: 1, Until: &past}, jobdb.RunPolicy{})
			ctx := waitTestContext(context.Background())
			rt := New(f.rt, execution.Allocation{SchemaVersion: 2})
			runner, e := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: f.key, JobWorker: f.worker, WorkerID: "orchestrator", LeaseDuration: time.Minute})
			require.NoError(t, e)
			out, e := runner.Run(nil)
			require.NoError(t, e)
			require.Equal(t, wf.JobRunSuspended, out.Status)
			require.Zero(t, f.task.runs.Load())
			info, e := f.rt.GetJob(ctx, f.key)
			require.NoError(t, e)
			require.NotNil(t, info.ExecutionState.TaskWait)
			out, e = f.run(t)
			require.NoError(t, e)
			require.Equal(t, wf.JobRunSuspended, out.Status, "%v", out.JobError)
			require.EqualValues(t, 1, f.task.runs.Load())
			info, e = f.rt.GetJob(ctx, f.key)
			require.NoError(t, e)
			require.Nil(t, info.ExecutionState.TaskWait)
			require.Empty(t, info.NextRoute.TaskType)
			g := f.gate(t)
			waitReady(t, g)
			out, e = f.run(t)
			require.NoError(t, e)
			require.Equal(t, wf.JobRunCompleted, out.Status, "%v", out.JobError)
			require.EqualValues(t, 2, f.task.runs.Load())
			require.Equal(t, f.task.identities[0], f.task.identities[1])
		})
	}
}

func TestExtensionWaitSuppliedLeaseGate(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			until := time.Now().UTC().Add(1600 * time.Millisecond)
			f := newExtensionWaitFixture(t, backend, extensionwait.Request{Version: 1, Until: &until, Dependencies: []extensionwait.Dependency{{Job: "target", Condition: "success"}}}, jobdb.RunPolicy{})
			target := f.target(t, "target", "")
			out, e := f.run(t)
			require.NoError(t, e)
			require.Equal(t, wf.JobRunSuspended, out.Status)
			f.finish(t, target, true)
			ctx := waitTestContext(context.Background())
			supplied, e := f.rt.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: f.key, Routes: []jobdb.Route{{JobType: "wait-test"}}, WorkerID: "dispatcher", LeaseDuration: time.Minute})
			require.NoError(t, e)
			require.NotNil(t, supplied)
			rt := New(f.rt, execution.Allocation{SchemaVersion: 2})
			rt.ConsumerRuntime = noClaimsRuntime{f.rt, t}
			out, e = rt.RunWithLease(ctx, supplied, wf.GetJobForRunRequest{JobKey: f.key, JobWorker: f.worker, TaskWorkers: []wf.TaskWorker{rt.WrapTaskWorker(f.task)}}, nil)
			require.NoError(t, e)
			require.Equal(t, wf.JobRunSuspended, out.Status)
			require.EqualValues(t, 1, f.task.runs.Load())
			waitReady(t, f.gate(t))
			out, e = f.run(t)
			require.NoError(t, e)
			require.Equal(t, wf.JobRunCompleted, out.Status, "%v", out.JobError)
		})
	}
}
func TestExtensionWaitEnclosingDeadlines(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		for _, bound := range []string{"node", "job"} {
			t.Run(backend+"/"+bound, func(t *testing.T) {
				until := time.Now().UTC().Add(time.Hour)
				var jobPolicy []jobdb.RunPolicy
				if bound == "job" {
					budget := jobdb.Duration(250 * time.Millisecond)
					jobPolicy = []jobdb.RunPolicy{{InvocationTimeout: &budget, Retry: jobdb.RetryPolicy{MaximumAttempts: 1}}}
				}
				f := newExtensionWaitFixture(t, backend, extensionwait.Request{Version: 1, Until: &until}, jobdb.RunPolicy{}, jobPolicy...)
				body := f.worker.Execute
				if bound == "node" {
					f.worker.Execute = func(c wf.ExecutionContext, data jobdb.JobData) (jobdb.JobData, error) {
						return c.DoScope(bound, wf.ScopeOptions{AttemptTimeout: 250 * time.Millisecond}, func(inner wf.ExecutionContext) (jobdb.JobData, error) { return body(inner, data) })
					}
				}
				out, e := f.run(t)
				require.NoError(t, e)
				require.Equal(t, wf.JobRunSuspended, out.Status, "%v", out.JobError)
				g := f.gate(t)
				require.NotNil(t, g.Deadline)
				time.Sleep(time.Until(*g.Deadline) + 30*time.Millisecond)
				out, e = f.run(t)
				require.NoError(t, e)
				require.Equal(t, wf.JobRunFailed, out.Status)
				require.EqualValues(t, 1, f.task.runs.Load())
				var timeout jobdb.TimeoutError
				require.True(t, errors.As(out.JobError, &timeout))
			})
		}
	}
}

func TestExtensionWaitCleanupStopsActivation(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			past := time.Now().UTC().Add(-time.Hour)
			r := extensionwait.Request{Version: 1, Until: &past}
			f := newExtensionWaitFixture(t, backend, r, jobdb.RunPolicy{})
			f.task.waitError = extensionwait.Cleaned(extensionwait.New(r), errors.New("injected cleanup failure"))
			out, e := f.run(t)
			require.True(t, e != nil || out.Status != wf.JobRunCompleted)
			require.EqualValues(t, 1, f.task.runs.Load())
			require.Nil(t, f.gate(t))
			chapters, e := f.rt.ListChapters(waitTestContext(context.Background()), jobdb.ListChaptersRequest{JobKey: f.key})
			require.NoError(t, e)
			for _, ch := range chapters {
				if b, ok := ch.Body.(jobdb.ExecutionChapter); ok {
					r, e := jobdb.DecodeExecutionRecord(b.Record)
					require.NoError(t, e)
					require.NotEqual(t, "result", r.Kind)
				}
			}
		})
	}
}

func TestExtensionWaitExpiredCleanupStopsActivation(t *testing.T) {
	past := time.Now().UTC().Add(-time.Hour)
	request := extensionwait.Request{Version: 1, Until: &past}
	budget := jobdb.Duration(1400 * time.Millisecond)
	f := newExtensionWaitFixture(t, "sqlite", request, jobdb.RunPolicy{InvocationTimeout: &budget, Retry: jobdb.RetryPolicy{MaximumAttempts: 1}})
	out, err := f.run(t)
	require.NoError(t, err)
	require.Equal(t, wf.JobRunSuspended, out.Status)
	waitReady(t, f.gate(t))
	f.task.run = func(ctx context.Context) error {
		<-ctx.Done()
		return extensionwait.Cleaned(extensionwait.New(request), errors.New("injected cleanup failure at deadline"))
	}
	out, err = f.run(t)
	require.True(t, err != nil || out.Status != wf.JobRunCompleted)
	chapters, err := f.rt.ListChapters(waitTestContext(context.Background()), jobdb.ListChaptersRequest{JobKey: f.key})
	require.NoError(t, err)
	for _, ch := range chapters {
		if b, ok := ch.Body.(jobdb.ExecutionChapter); ok {
			r, err := jobdb.DecodeExecutionRecord(b.Record)
			require.NoError(t, err)
			require.NotEqual(t, "result", r.Kind, "deadline must not hide a failed discard cleanup")
		}
	}
}

func TestExtensionWaitReplacesDependencyBinding(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f := newExtensionWaitFixture(t, backend, extensionwait.Request{Version: 1, Dependencies: []extensionwait.Dependency{{Job: "first", Condition: "success"}}}, jobdb.RunPolicy{})
			f.task.remaining = 2
			first, second := f.target(t, "first", ""), f.target(t, "second", "")
			out, e := f.run(t)
			require.NoError(t, e)
			require.Equal(t, wf.JobRunSuspended, out.Status)
			g1 := f.gate(t)
			f.finish(t, first, true)
			waitReady(t, g1)
			f.task.request.Dependencies = []extensionwait.Dependency{{Job: "second", Condition: "success"}}
			out, e = f.run(t)
			require.NoError(t, e)
			require.Equal(t, wf.JobRunSuspended, out.Status)
			g2 := f.gate(t)
			require.NotEqual(t, g1.ID, g2.ID)
			require.Equal(t, g1.Invocation, g2.Invocation)
			out, e = f.run(t)
			require.True(t, e != nil || out.Status == wf.JobRunSuspended)
			require.EqualValues(t, 2, f.task.runs.Load())
			f.finish(t, second, true)
			waitReady(t, g2)
			out, e = f.run(t)
			require.NoError(t, e)
			require.Equal(t, wf.JobRunCompleted, out.Status, "%v", out.JobError)
		})
	}
}

type extensionWaitBindingSnapshot struct {
	jobdb.ConsumerRuntime
	binding jobdb.DependencyWaitInfo
}

func (r extensionWaitBindingSnapshot) GetDependencyWait(context.Context, jobdb.DependencyWaitKey) (jobdb.DependencyWaitInfo, error) {
	return r.binding, nil
}

func TestExtensionWaitBindingAdmission(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			budget := jobdb.Duration(time.Hour)
			f := newExtensionWaitFixture(t, backend, extensionwait.Request{Version: 1}, jobdb.RunPolicy{InvocationTimeout: &budget})
			a, goalA := f.target(t, "job-a", ""), f.target(t, "goal-job-a", "")
			f.target(t, "job-b", "")
			goalB := f.target(t, "goal-job-b", "")
			f.task.request.Dependencies = []extensionwait.Dependency{{Job: "job-a", Condition: "success"}, {Goal: goalA.GoalID, Condition: "success"}}
			out, err := f.run(t)
			require.NoError(t, err)
			require.Equal(t, wf.JobRunSuspended, out.Status)
			g := f.gate(t)
			require.NotNil(t, g.Deadline)
			f.finish(t, a, true)
			f.finish(t, goalA, true)
			waitReady(t, g)
			if f.reopen != nil {
				f.reopen()
			}
			ctx := waitTestContext(context.Background())
			binding, err := f.rt.GetDependencyWait(ctx, jobdb.DependencyWaitKey{JobKey: f.key, WaitID: g.ID})
			require.NoError(t, err)
			require.Equal(t, jobdb.DependencySatisfied, binding.State)
			rt := New(f.rt, execution.Allocation{SchemaVersion: 2})
			acquired, err := rt.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: f.key, Routes: []jobdb.Route{{JobType: "wait-test"}}, WorkerID: "binding-check", LeaseDuration: time.Minute})
			require.NoError(t, err)
			require.NotNil(t, acquired)
			l := rt.leases[f.key]
			tc := wf.NewTaskContext(f.key, 123, nil, nil, nil)
			tc.InvocationID, tc.AttemptNumber = g.Invocation, g.Attempt
			type admissionCase struct {
				name    string
				gate    func(*waitGate)
				binding func(*jobdb.DependencyWaitInfo)
				allowed bool
			}
			cases := []admissionCase{
				{name: "matching", allowed: true},
				{name: "equivalent_deadline", allowed: true, binding: func(b *jobdb.DependencyWaitInfo) {
					at := b.Deadline.In(time.FixedZone("offset", 3600))
					b.Deadline = &at
				}},
				{name: "job_target", gate: func(g *waitGate) { g.Request.Dependencies[0].Job = "job-b" }},
				{name: "goal_target", gate: func(g *waitGate) { g.Request.Dependencies[1].Goal = goalB.GoalID }},
				{name: "target_kind", gate: func(g *waitGate) {
					g.Request.Dependencies[0] = extensionwait.Dependency{Goal: goalB.GoalID, Condition: "success"}
				}},
				{name: "condition", gate: func(g *waitGate) { g.Request.Dependencies[0].Condition = "complete" }},
				{name: "missing_dependency", gate: func(g *waitGate) { g.Request.Dependencies = g.Request.Dependencies[:1] }},
				{name: "extra_dependency", gate: func(g *waitGate) {
					g.Request.Dependencies = append(g.Request.Dependencies, extensionwait.Dependency{Job: "job-b", Condition: "success"})
				}},
				{name: "reordered_dependencies", gate: func(g *waitGate) {
					g.Request.Dependencies[0], g.Request.Dependencies[1] = g.Request.Dependencies[1], g.Request.Dependencies[0]
				}},
				{name: "changed_deadline", gate: func(g *waitGate) { at := g.Deadline.Add(time.Minute); g.Deadline = &at }},
				{name: "missing_gate_deadline", gate: func(g *waitGate) { g.Deadline, g.TimeoutScope = nil, "" }},
				{name: "missing_binding_deadline", binding: func(b *jobdb.DependencyWaitInfo) { b.Deadline = nil }},
				{name: "wrong_tenant", binding: func(b *jobdb.DependencyWaitInfo) { b.JobKey.TenantId = "other" }},
				{name: "wrong_job", binding: func(b *jobdb.DependencyWaitInfo) { b.JobKey.JobId = "other" }},
				{name: "wrong_wait", binding: func(b *jobdb.DependencyWaitInfo) { b.WaitID = "other" }},
			}
			for _, state := range []jobdb.DependencyState{jobdb.DependencyPending, jobdb.DependencyFailed, jobdb.DependencyCancelled, jobdb.DependencyTimedOut} {
				cases = append(cases, admissionCase{name: "mismatch_before_" + string(state), binding: func(b *jobdb.DependencyWaitInfo) {
					b.State = state
					b.Dependencies[0].Target.JobID = "job-b"
				}})
			}
			for _, test := range cases {
				t.Run(test.name, func(t *testing.T) {
					copy := *g
					copy.Request.Dependencies = append([]extensionwait.Dependency(nil), g.Request.Dependencies...)
					rt.ConsumerRuntime = f.rt
					if test.gate != nil {
						test.gate(&copy)
					}
					if test.binding != nil {
						b := binding
						b.Dependencies = append([]jobdb.DependencyResolution(nil), binding.Dependencies...)
						test.binding(&b)
						rt.ConsumerRuntime = extensionWaitBindingSnapshot{f.rt, b}
					}
					raw, err := payloadWithGate(l.ClientPayload(), &copy)
					require.NoError(t, err)
					rt.wrapLease(waitLeaseSnapshot{l.ExecutionLease, raw, l.ClientPayloadRevision()}, l.leaseState)
					admitted, parked, err := l.waitAdmission(ctx, tc)
					require.False(t, parked, "a binding disagreement must not reschedule")
					if test.allowed {
						require.NoError(t, err)
						require.NotNil(t, admitted)
					} else {
						require.ErrorContains(t, err, "dependency binding does not match gate")
						require.Nil(t, admitted)
					}
					require.Nil(t, l.pendingWait)
					require.EqualValues(t, 1, f.task.runs.Load(), "admission must not execute the extension")
				})
			}
		})
	}
}

func TestExtensionWaitRepeatedDeadlineCap(t *testing.T) {
	past := time.Now().UTC().Add(-time.Hour)
	budget := jobdb.Duration(1500 * time.Millisecond)
	f := newExtensionWaitFixture(t, "sqlite", extensionwait.Request{Version: 1, Until: &past}, jobdb.RunPolicy{InvocationTimeout: &budget, Retry: jobdb.RetryPolicy{MaximumAttempts: 1}})
	f.task.remaining = 10
	out, e := f.run(t)
	require.NoError(t, e)
	require.Equal(t, wf.JobRunSuspended, out.Status)
	first := f.gate(t)
	waitReady(t, first)
	out, e = f.run(t)
	require.NoError(t, e)
	require.Equal(t, wf.JobRunSuspended, out.Status)
	second := f.gate(t)
	require.Equal(t, first.Deadline, second.Deadline)
	require.Equal(t, first.TimeoutScope, second.TimeoutScope)
	time.Sleep(time.Until(*second.Deadline) + 30*time.Millisecond)
	out, e = f.run(t)
	require.NoError(t, e)
	require.Equal(t, wf.JobRunFailed, out.Status)
	require.EqualValues(t, 2, f.task.runs.Load())
}

func TestExtensionWaitRetiresBeforeNextTask(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			past := time.Now().UTC().Add(-time.Hour)
			f := newExtensionWaitFixture(t, backend, extensionwait.Request{Version: 1, Until: &past}, jobdb.RunPolicy{})
			f.task.waitCalls = map[int32]bool{1: true, 3: true}
			body := f.worker.Execute
			f.worker.Execute = func(c wf.ExecutionContext, data jobdb.JobData) (jobdb.JobData, error) {
				if _, e := body(c, data); e != nil {
					return nil, e
				}
				return c.DoTask(wf.TaskCall{Site: "after", TaskType: f.task.Name(), ContractVersion: 1, Command: json.RawMessage(`{}`)}, func(context.Context) (jobdb.TaskData, error) { return data, nil })
			}
			out, e := f.run(t)
			require.NoError(t, e)
			require.Equal(t, wf.JobRunSuspended, out.Status)
			first := f.gate(t)
			waitReady(t, first)
			out, e = f.run(t)
			require.NoError(t, e)
			require.Equal(t, wf.JobRunSuspended, out.Status, "%v", out.JobError)
			second := f.gate(t)
			require.NotEqual(t, first.Invocation, second.Invocation)
			require.NotEqual(t, first.ID, second.ID)
			require.EqualValues(t, 3, f.task.runs.Load())
			waitReady(t, second)
			out, e = f.run(t)
			require.NoError(t, e)
			require.Equal(t, wf.JobRunCompleted, out.Status)
			require.Nil(t, f.gate(t))
			require.EqualValues(t, 4, f.task.runs.Load())
		})
	}
}
func TestExtensionWaitDependencyDeadline(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			budget := jobdb.Duration(250 * time.Millisecond)
			f := newExtensionWaitFixture(t, backend, extensionwait.Request{Version: 1, Dependencies: []extensionwait.Dependency{{Job: "target", Condition: "success"}}}, jobdb.RunPolicy{InvocationTimeout: &budget, Retry: jobdb.RetryPolicy{MaximumAttempts: 1}})
			target := f.target(t, "target", "")
			out, e := f.run(t)
			require.NoError(t, e)
			require.Equal(t, wf.JobRunSuspended, out.Status)
			g := f.gate(t)
			require.NotNil(t, g.Deadline)
			time.Sleep(time.Until(*g.Deadline) + 30*time.Millisecond)
			out, e = f.run(t)
			require.NoError(t, e)
			require.Equal(t, wf.JobRunFailed, out.Status)
			require.EqualValues(t, 1, f.task.runs.Load())
			var timeout jobdb.TimeoutError
			require.True(t, errors.As(out.JobError, &timeout))
			info, e := f.rt.GetJob(waitTestContext(context.Background()), target.JobKey)
			require.NoError(t, e)
			require.Empty(t, info.CompletionStatus)
		})
	}
}
