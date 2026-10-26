package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/logging"
	recipeops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestResumedJobLogsOnlyNewTaskExecution(t *testing.T) {
	base, hash, cleanup := setupGitRepo(t)
	defer cleanup()
	var out bytes.Buffer
	logger, err := logging.New(&out, logging.Options{Format: "json", Debug: []logging.Selector{{Field: "component", Value: "worker"}}})
	require.NoError(t, err)
	var calls [2]atomic.Int32
	var workers []jobworkflow.TaskWorker
	for i, name := range []string{"first", "second"} {
		reg := ActivityRegistration{TaskType: name, Metadata: recipeops.OpMetadata{Type: name}, Step: recipeops.TaskStep{
			Invoke: func(recipeops.OpDependencies, context.Context, map[string]interface{}) (map[string]interface{}, error) {
				calls[i].Add(1)
				return map[string]interface{}{}, nil
			},
		}}
		workers = append(workers, &taskWorker{name: name, reg: reg, doer: &opExecutor{deps: recipeops.NewServiceDepsBuilder().Build(), reg: reg, controller: gitstate.NewController(nil)}})
	}
	input := jobdb.NewTaskDataOrPanic(ActivityInvocationRequest{Const: true, Input: map[string]interface{}{}, GitTaskContext: gitstate.GlobalGitTaskContext{BaseRepo: base, BaseRef: hash, NodePath: "root/probe"}})
	orchestrator := jobworkflow.ExecutionWorker{JobType: "logging-resume", Execute: func(c jobworkflow.ExecutionContext, data jobdb.JobData) (jobdb.JobData, error) {
		var result jobdb.TaskData
		for _, worker := range workers {
			var err error
			result, err = c.DoTask(jobworkflow.TaskCall{Site: worker.Name(), TaskType: worker.Name(), ContractVersion: 1, Command: json.RawMessage(`{}`)}, func(context.Context) (jobdb.TaskData, error) { return data, nil })
			if err != nil {
				return nil, err
			}
		}
		return result, nil
	}}
	rt := executionruntime.New(toy.New(), execution.Allocation{SchemaVersion: 1})
	job, err := rt.SubmitJob(context.Background(), jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "test", JobType: orchestrator.Name(), Data: input}})
	require.NoError(t, err)
	for i, worker := range workers {
		workers[i] = rt.WrapTaskWorker(worker)
	}
	for pass := 1; pass <= 2; pass++ {
		out.Reset()
		h, err := jobworkflow.GetJobForRun(context.Background(), rt, jobworkflow.GetJobForRunRequest{JobKey: job.JobKey, JobWorker: orchestrator, TaskWorkers: workers[:pass], WorkerID: "test", LeaseDuration: time.Minute, Logger: logger})
		require.NoError(t, err)
		require.NotNil(t, h)
		outcome, err := h.Run(nil)
		require.NoError(t, err)
		if pass == 1 {
			require.Equal(t, jobworkflow.JobRunSuspended, outcome.Status)
		} else {
			require.Equal(t, jobworkflow.JobRunCompleted, outcome.Status)
		}
		var events []string
		for _, line := range bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n")) {
			var record map[string]any
			require.NoError(t, json.Unmarshal(line, &record))
			require.Equal(t, "DEBUG", record["level"])
			require.Equal(t, workers[pass-1].Name(), record["task_type"], "completed prefix must not emit execution records on resume")
			events = append(events, record["event"].(string))
			if record["event"] == "task.attempt.finished" {
				require.Equal(t, float64(1), record["attempt"])
			}
		}
		require.Equal(t, []string{"task.attempt.started", "op.body.started", "task.attempt.finished"}, events)
	}
	require.EqualValues(t, 1, calls[0].Load())
	require.EqualValues(t, 1, calls[1].Load())
}

func TestTaskLoggingDistinguishesPreparationAndOpExecution(t *testing.T) {
	base, hash, cleanup := setupGitRepo(t)
	defer cleanup()
	for _, failRestore := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "restore failure"}[failRestore], func(t *testing.T) {
			var out bytes.Buffer
			logger, err := logging.New(&out, logging.Options{Format: "json", Debug: []logging.Selector{{Field: "component", Value: "worker"}}})
			require.NoError(t, err)
			called := false
			reg := ActivityRegistration{TaskType: "logging.probe", Metadata: recipeops.OpMetadata{Type: "logging_probe"}, Step: recipeops.TaskStep{
				Invoke: func(recipeops.OpDependencies, context.Context, map[string]interface{}) (map[string]interface{}, error) {
					called = true
					return map[string]interface{}{"value": "ok"}, nil
				},
			}}
			worker := &taskWorker{name: reg.TaskType, reg: reg, doer: &opExecutor{deps: recipeops.NewServiceDepsBuilder().Build(), reg: reg, controller: gitstate.NewController(nil)}}
			repo := base
			if failRestore {
				repo = filepath.Join(t.TempDir(), "missing-repo")
			}
			input := jobdb.NewTaskDataOrPanic(ActivityInvocationRequest{Const: true, Input: map[string]interface{}{}, GitTaskContext: gitstate.GlobalGitTaskContext{BaseRepo: repo, BaseRef: hash, NodePath: "root/probe"}})
			tc := jobworkflow.NewTaskContext(jobdb.JobKey{TenantId: "tenant", JobId: "job"}, 1, logger, nil, nil)
			_, err = worker.RunTask(deniedTaskContext(t, tc), tc, input)
			if failRestore {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, !failRestore, called)
			var events []string
			for _, line := range bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n")) {
				var rec map[string]any
				require.NoError(t, json.Unmarshal(line, &rec))
				require.Equal(t, "DEBUG", rec["level"])
				require.Equal(t, "job", rec["job_id"])
				events = append(events, rec["event"].(string))
				if rec["event"] == "op.processing.stopped" {
					require.Equal(t, "restore_git_state", rec["phase"])
				}
				if rec["event"] == "task.attempt.finished" {
					want := "succeeded"
					if failRestore {
						want = "failed"
					}
					require.Equal(t, want, rec["outcome"])
					require.Equal(t, "root/probe", rec["node_path"])
				}
			}
			require.Contains(t, events, "task.attempt.started")
			require.Contains(t, events, "task.attempt.finished")
			if failRestore {
				require.NotContains(t, events, "op.body.started")
			} else {
				require.Contains(t, events, "op.body.started")
			}
		})
	}
}
