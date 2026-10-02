package compiler

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type timeoutReproRuntime struct {
	jobdb.WorkflowRuntime
	mu          sync.Mutex
	handoffs    []timeoutReproHandoff
	completions []jobdb.CompleteExecutionRequest
}
type timeoutReproHandoff struct {
	at  time.Time
	req jobdb.RescheduleExecutionRequest
}
type timeoutReproLease struct {
	jobdb.ExecutionLease
	owner *timeoutReproRuntime
}

// Preserve optional remote transport identity when decorating a lease.
func (l *timeoutReproLease) LeaseToken() string {
	if v, ok := l.ExecutionLease.(interface{ LeaseToken() string }); ok {
		return v.LeaseToken()
	}
	return ""
}
func (l *timeoutReproLease) LeaseWorkerID() string {
	if v, ok := l.ExecutionLease.(interface{ LeaseWorkerID() string }); ok {
		return v.LeaseWorkerID()
	}
	return ""
}

func (r *timeoutReproRuntime) GetJobLease(ctx context.Context, req jobdb.GetJobLeaseRequest) (jobdb.ExecutionLease, error) {
	l, err := r.WorkflowRuntime.GetJobLease(ctx, req)
	if err != nil || l == nil {
		return l, err
	}
	return &timeoutReproLease{l, r}, nil
}
func (l *timeoutReproLease) Reschedule(ctx context.Context, req jobdb.RescheduleExecutionRequest) error {
	if err := l.ExecutionLease.Reschedule(ctx, req); err != nil {
		return err
	}
	l.owner.mu.Lock()
	defer l.owner.mu.Unlock()
	l.owner.handoffs = append(l.owner.handoffs, timeoutReproHandoff{time.Now(), req})
	return nil
}
func (l *timeoutReproLease) Complete(ctx context.Context, req jobdb.CompleteExecutionRequest) error {
	if err := l.ExecutionLease.Complete(ctx, req); err != nil {
		return err
	}
	l.owner.mu.Lock()
	defer l.owner.mu.Unlock()
	l.owner.completions = append(l.owner.completions, req)
	return nil
}
func (r *timeoutReproRuntime) lastHandoff(t *testing.T) timeoutReproHandoff {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.NotEmpty(t, r.handoffs)
	return r.handoffs[len(r.handoffs)-1]
}

func newTimeoutReproRuntime(t *testing.T, backend string) *timeoutReproRuntime {
	t.Helper()
	var rt jobdb.WorkflowRuntime = toy.New()
	if backend != "toy" {
		s, err := sqlite.NewFromConfig(context.Background(), sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })
		rt = s
	}
	if backend == "remote" {
		s := httptest.NewServer(remote.NewServer(rt))
		t.Cleanup(s.Close)
		r, err := remote.New(s.URL, s.Client())
		require.NoError(t, err)
		rt = r
	}
	return &timeoutReproRuntime{WorkflowRuntime: rt}
}

type timeoutReproWorker struct {
	name string
	run  func(jobworkflow.JobContext, jobdb.JobData) (jobdb.JobData, error)
}

func (w timeoutReproWorker) Name() string { return w.name }
func (w timeoutReproWorker) Run(ctx jobworkflow.JobContext, data jobdb.JobData) (jobdb.JobData, error) {
	return w.run(ctx, data)
}

func timeoutReproPolicy(total, invocation time.Duration) jobdb.RunPolicy {
	p := jobdb.DefaultRunPolicy()
	p.TotalTimeout, p.InvocationTimeout = jobdb.AsDuration(total), jobdb.AsDuration(invocation)
	p.Retry.MaximumAttempts = 1
	return p
}
func submitTimeoutRepro(t *testing.T, rt jobdb.WorkflowRuntime, id string, p jobdb.RunPolicy) jobdb.JobKey {
	t.Helper()
	data, err := jobdb.NewTaskData(map[string]any{"id": id})
	require.NoError(t, err)
	h, err := rt.SubmitJob(context.Background(), jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "timeout-repro", JobID: id, JobType: "repro", Data: data, RunPolicy: p}})
	require.NoError(t, err)
	return h.JobKey
}
func runTimeoutRepro(t *testing.T, rt jobdb.WorkflowRuntime, key jobdb.JobKey, worker jobworkflow.JobWorker, tasks ...jobworkflow.TaskWorker) jobworkflow.JobRunOutcome {
	t.Helper()
	// Keep c2j's 250ms task-deadline monitor in-process. An artificially
	// smaller threshold would make that monitor yield during recipe resolution.
	r, err := jobworkflow.GetJobForRun(context.Background(), rt, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: worker, TaskWorkers: tasks, WorkerID: "restarted-worker", LeaseDuration: time.Second, AwaitThreshold: time.Second})
	require.NoError(t, err)
	out, err := r.Run(nil)
	require.NoError(t, err)
	return out
}
func waitTimeoutRepro(t *testing.T, until time.Time) {
	t.Helper()
	if delay := time.Until(until); delay > 0 {
		time.Sleep(delay)
	}
}
func requireTimeoutCompletion(t *testing.T, rt *timeoutReproRuntime, key jobdb.JobKey, out jobworkflow.JobRunOutcome) {
	t.Helper()
	require.Equal(t, jobworkflow.JobRunFailed, out.Status)
	var timeout jobdb.TimeoutError
	require.ErrorAs(t, out.JobError, &timeout)
	info, err := rt.GetJob(context.Background(), key)
	require.NoError(t, err)
	require.Equal(t, jobdb.JobStatusCompleted, info.Status)
	run, err := jobdb.GetJobRun(context.Background(), rt, jobdb.GetJobRunRequest{JobKey: key, IncludeOutputs: true})
	require.NoError(t, err)
	require.NotEmpty(t, run.Attempts)
	last := run.Attempts[len(run.Attempts)-1]
	require.NotNil(t, last.Outcome.Error)
	require.Equal(t, jobdb.TaskErrorKindTimeout, last.Outcome.Error.Kind)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	require.NotEmpty(t, rt.completions)
	require.Equal(t, "failed_timeout", rt.completions[len(rt.completions)-1].Status)
	t.Logf("CONTROL: terminal status=%s, completion=failed_timeout, durable outcome=%s scope=%s", info.Status, last.Outcome.Error.Kind, last.Outcome.Error.Scope)
}
