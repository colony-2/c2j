package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	recipeops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/swfutil"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestTaskWorkerRunReturnsTaskDataOnFailure(t *testing.T) {
	t.Parallel()

	baseRepo, baseHash, cleanup := setupGitRepo(t)
	defer cleanup()

	reg := ActivityRegistration{
		TaskType: "test.failure",
		Step: recipeops.TaskStep{
			Invoke: func(deps recipeops.OpDependencies, ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
				require.NoError(t, deps.AddOutputArtifact(&mockArtifact{name: "stderr.txt", data: []byte("boom")}))
				target := filepath.Join(deps.WorktreePath(), "mutated.txt")
				require.NoError(t, os.WriteFile(target, []byte("changed"), 0o644))
				return map[string]interface{}{"status": "failed", "detail": "boom"}, fmt.Errorf("step failed")
			},
		},
	}

	worker := &taskWorker{
		name: "test.failure",
		reg:  reg,
		doer: &opExecutor{
			deps:       recipeops.NewServiceDepsBuilder().Build(),
			reg:        reg,
			controller: gitstate.NewController(nil),
		},
	}

	input := jobdb.NewTaskDataOrPanic(ActivityInvocationRequest{
		Input: map[string]interface{}{},
		GitTaskContext: gitstate.GlobalGitTaskContext{
			BaseRepo: baseRepo,
			BaseRef:  baseHash,
		},
	})

	tc := jobworkflow.NewTaskContext(jobdb.JobKey{TenantId: "tenant", JobId: "job"}, 1, nil, nil, nil)
	td, err := worker.RunTask(deniedTaskContext(t, tc), tc, input)
	require.Error(t, err)
	require.NotNil(t, td)

	artifacts, err := td.GetArtifacts()
	require.NoError(t, err)
	require.NotEmpty(t, artifacts)

	raw, err := td.GetData()
	require.NoError(t, err)

	var env coretask.OutputEnvelope
	require.NoError(t, json.Unmarshal(raw, &env))
	require.Equal(t, coretask.OutputKindActivityInvocationOutput, env.Kind)

	var output ActivityInvocationOutput
	require.NoError(t, env.DecodePayload(&output))
	require.Equal(t, "failed", output.OpOutput["status"])
	require.Equal(t, "boom", output.OpOutput["detail"])
	requireArtifactNamed(t, artifacts, "stderr.txt")

	t.Run("persist_and_replay", func(t *testing.T) {
		ctx := context.Background()
		rt := executionruntime.New(toy.New(), execution.Allocation{SchemaVersion: 1})
		orchestrator := jobworkflow.ExecutionWorker{JobType: "diagnostics", Execute: func(c jobworkflow.ExecutionContext, data jobdb.JobData) (jobdb.JobData, error) {
			return c.DoTask(jobworkflow.TaskCall{Site: "failure", TaskType: worker.Name(), ContractVersion: 1, Command: json.RawMessage(`{}`), Policy: jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 1}}}, func(context.Context) (jobdb.TaskData, error) { return data, nil })
		}}
		job, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "test", JobType: orchestrator.Name(), Data: input, RunPolicy: jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 1}}}})
		require.NoError(t, err)
		h, err := jobworkflow.GetJobForRun(ctx, rt, jobworkflow.GetJobForRunRequest{JobKey: job.JobKey, JobWorker: orchestrator, TaskWorkers: []jobworkflow.TaskWorker{rt.WrapTaskWorker(worker)}, WorkerID: "test", LeaseDuration: time.Minute})
		require.NoError(t, err)
		outcome, err := h.Run(nil)
		require.NoError(t, err)
		require.Equal(t, jobworkflow.JobRunFailed, outcome.Status)
		run, err := jobdb.GetJobRun(ctx, rt, jobdb.GetJobRunRequest{JobKey: job.JobKey, IncludeOutputs: true, IncludeArtifacts: true})
		require.NoError(t, err)
		tasks, err := swfutil.ExecutionTasks(run)
		require.NoError(t, err)
		require.Len(t, tasks, 1)
		require.Len(t, tasks[0].Attempts, 1)
		stored := tasks[0].Attempts[0]
		require.Equal(t, jobdb.TaskAttemptStateFailed, stored.State)
		require.NotNil(t, stored.Output, "failed outcomes must retain diagnostics")
		require.Contains(t, string(stored.Output.Data), `"detail":"boom"`)
		var stderr *jobdb.ArtifactInfo
		for i := range stored.Output.Artifacts {
			if stored.Output.Artifacts[i].Name == "stderr.txt" {
				stderr = &stored.Output.Artifacts[i]
			}
		}
		require.NotNil(t, stderr)
		require.NotNil(t, stderr.Key, "diagnostic artifacts need durable storage references")
		engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(job.JobKey.TenantId).PlusWorkers(orchestrator).BuildEngine()
		require.NoError(t, err)
		observer := &diagnosticObserver{}
		_, err = engine.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: job.JobKey, Observer: observer})
		require.Error(t, err, "a failed outcome must still replay as failure")
		require.Len(t, observer.ends, 1)
		require.Error(t, observer.ends[0].Err)
		require.NotNil(t, observer.ends[0].Output)
		replayed, err := observer.ends[0].Output.GetData()
		require.NoError(t, err)
		require.JSONEq(t, string(stored.Output.Data), string(replayed))
		replayedArtifacts, err := observer.ends[0].Output.GetArtifacts()
		require.NoError(t, err)
		requireArtifactNamed(t, replayedArtifacts, "stderr.txt")
	})
}

func TestTaskWorkerRunCancelsStepWithExecutionContext(t *testing.T) {
	t.Parallel()

	baseRepo, baseHash, cleanup := setupGitRepo(t)
	defer cleanup()

	executionCtx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	started := make(chan struct{})
	reg := ActivityRegistration{
		TaskType: "test.timeout",
		Step: recipeops.TaskStep{
			Invoke: func(deps recipeops.OpDependencies, ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
				close(started)
				cancel(context.DeadlineExceeded)
				<-ctx.Done()
				if cause := context.Cause(ctx); cause != nil {
					return map[string]interface{}{"status": "timeout"}, cause
				}
				return map[string]interface{}{"status": "timeout"}, ctx.Err()
			},
		},
	}

	worker := &taskWorker{
		name: "test.timeout",
		reg:  reg,
		doer: &opExecutor{
			deps:       recipeops.NewServiceDepsBuilder().Build(),
			reg:        reg,
			controller: gitstate.NewController(nil),
		},
	}

	input := jobdb.NewTaskDataOrPanic(ActivityInvocationRequest{
		Input: map[string]interface{}{},
		Const: true,
		GitTaskContext: gitstate.GlobalGitTaskContext{
			BaseRepo: baseRepo,
			BaseRef:  baseHash,
		},
	})

	rt := executionruntime.New(toy.New(), execution.Allocation{SchemaVersion: 1})
	job, err := rt.SubmitJob(executionCtx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobType: "cancel-step", Data: input}})
	require.NoError(t, err)
	finished := make(chan taskWorkerResult, 1)
	wrapped := observingTaskWorker{TaskWorker: worker, finished: finished}
	orchestrator := jobworkflow.ExecutionWorker{JobType: "cancel-step", Execute: func(ctx jobworkflow.ExecutionContext, data jobdb.JobData) (jobdb.JobData, error) {
		return ctx.DoTask(jobworkflow.TaskCall{Site: "step", TaskType: worker.Name(), ContractVersion: 1, Command: json.RawMessage(`{}`)}, func(context.Context) (jobdb.TaskData, error) { return data, nil })
	}}
	h, err := jobworkflow.GetJobForRun(executionCtx, rt, jobworkflow.GetJobForRunRequest{JobKey: job.JobKey, JobWorker: orchestrator, TaskWorkers: []jobworkflow.TaskWorker{rt.WrapTaskWorker(wrapped)}, WorkerID: "test", LeaseDuration: time.Minute})
	require.NoError(t, err)
	outcome, runErr := h.Run(nil)
	select {
	case <-started:
	default:
		t.Fatalf("worker never started: outcome=%+v error=%v", outcome, runErr)
	}
	result := <-finished
	require.ErrorIs(t, result.err, context.DeadlineExceeded)
	require.NotNil(t, result.data)

	select {
	case <-started:
	default:
		t.Fatal("expected step to start before timeout")
	}
}

func requireArtifactNamed(t *testing.T, artifacts []jobdb.Artifact, name string) {
	t.Helper()
	for _, artifact := range artifacts {
		if artifact.Name() == name {
			return
		}
	}
	var names []string
	for _, artifact := range artifacts {
		names = append(names, artifact.Name())
	}
	t.Fatalf("expected artifact %q in %#v", name, names)
}

// Observe completion explicitly: engine cancellation may return before worker cleanup.
type taskWorkerResult struct {
	data jobdb.TaskData
	err  error
}
type observingTaskWorker struct {
	jobworkflow.TaskWorker
	finished chan<- taskWorkerResult
}

func (w observingTaskWorker) Run(ctx jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(ctx.Context(), ctx, input)
}
func (w observingTaskWorker) RunTask(local context.Context, ctx jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	out, err := coretask.RunLocal(w.TaskWorker, local, ctx, input)
	w.finished <- taskWorkerResult{out, err}
	return out, err
}

type diagnosticObserver struct{ ends []jobworkflow.TaskEndEvent }

func (*diagnosticObserver) OnJobStart(jobworkflow.JobStartEvent)   {}
func (*diagnosticObserver) OnJobEnd(jobworkflow.JobEndEvent)       {}
func (*diagnosticObserver) OnTaskStart(jobworkflow.TaskStartEvent) {}
func (o *diagnosticObserver) OnTaskEnd(e jobworkflow.TaskEndEvent) { o.ends = append(o.ends, e) }
