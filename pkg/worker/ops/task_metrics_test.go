package ops

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/preparedtest"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/gitcommit"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/metrics"
	"github.com/colony-2/c2j/pkg/metricsruntime"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/ops/process"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/toolenv"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type metricClock struct {
	seconds int64
	file    string
}

func (c *metricClock) now() time.Time {
	if c.file != "" {
		b, err := os.ReadFile(c.file)
		if err != nil {
			panic(err)
		}
		c.seconds, err = strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		if err != nil {
			panic(err)
		}
	}
	return time.Unix(c.seconds, 0)
}
func (c *metricClock) add(n int64) {
	c.now()
	c.seconds += n
	if c.file != "" {
		if err := os.WriteFile(c.file, []byte(strconv.FormatInt(c.seconds, 10)), 0600); err != nil {
			panic(err)
		}
	}
}

type timedInput struct {
	jobdb.TaskData
	clock *metricClock
}

func (in timedInput) GetData() (json.RawMessage, error) {
	in.clock.add(2)
	return in.TaskData.GetData()
}

type timedInputArtifact struct {
	jobdb.Artifact
	clock *metricClock
}

func (a timedInputArtifact) Bytes(ctx context.Context) ([]byte, error) {
	a.clock.add(3)
	return a.Artifact.Bytes(ctx)
}

type timedWorkspace struct {
	*gitstate.Controller
	clock      *metricClock
	restoreErr error
	persist    func(*gitstate.GitTaskContext)
}

func (w timedWorkspace) Restore(ctx context.Context, task *gitstate.GitTaskContext, art jobdb.Artifact) error {
	w.clock.add(7)
	if w.restoreErr != nil {
		return w.restoreErr
	}
	return w.Controller.Restore(ctx, task, art)
}
func (w timedWorkspace) PersistWithDiffs(ctx context.Context, task *gitstate.GitTaskContext) (*gitcommit.PersistWithDiffsOutput, []jobdb.Artifact, error) {
	w.persist(task)
	w.clock.add(11)
	return w.Controller.PersistWithDiffs(ctx, task)
}

type checkedMetricOutput struct {
	clock *metricClock
	calls *int
}

func (v checkedMetricOutput) MarshalJSON() ([]byte, error) {
	*v.calls++
	v.clock.add(5)
	return []byte(`"checked"`), nil
}

func metricEnvelope(t *testing.T, data jobdb.TaskData) coretask.OutputEnvelope {
	t.Helper()
	require.NotNil(t, data)
	var env coretask.OutputEnvelope
	require.NoError(t, json.Unmarshal(data.GetDataOrPanic(), &env))
	require.NotNil(t, env.Metrics)
	require.Equal(t, "valid", env.Metrics.Runtime.Status)
	return env
}
func metricSpan(t *testing.T, channel metrics.Channel, name string, labels map[string]string, seconds float64) {
	t.Helper()
	reduced, err := metrics.Reduce([]metrics.Record{{Task: `/scope:"$job"#1/attempt:1/task:"fixture"#1`, Attempt: 1, Ordinal: 1, Channel: "runtime", ChannelReport: channel}}, metrics.Query{}, 1)
	require.NoError(t, err)
	var found []metrics.Point
	for _, m := range reduced.Snapshot.Series[name] {
		match := true
		for k, v := range labels {
			if m.Labels[k] != v {
				match = false
			}
		}
		if match {
			found = append(found, m)
		}
	}
	require.Len(t, found, 1, "%s %v: %+v", name, labels, channel.Report.Mutations)
	require.Equal(t, int64(1), found[0].Count)
	require.EqualValues(t, seconds, found[0].Sum)
}

// Actual activity worker: private input hydration, workspace restore, package
// activation, sentinel hydration, output checks, outbox collection and persistence.
// Only the clock and workspace latency are fake; no provider or installer runs.
func TestTaskMetricsPreparationAndExecutionBoundaries(t *testing.T) {
	repo, hash, cleanup := setupGitRepo(t)
	defer cleanup()
	t.Setenv("C2J_TOOL_CACHE_DIR", t.TempDir())
	clock := &metricClock{file: filepath.Join(t.TempDir(), "clock")}
	require.NoError(t, os.WriteFile(clock.file, []byte("0"), 0600))
	t.Setenv("METRIC_WORKER_CLOCK", clock.file)
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "uv"), []byte(`#!/bin/sh
set -eu
n=$(cat "$METRIC_WORKER_CLOCK"); echo $((n+4)) > "$METRIC_WORKER_CLOCK"
mkdir -p "$UV_TOOL_BIN_DIR"
printf '#!/bin/sh\nexit 0\n' > "$UV_TOOL_BIN_DIR/fixture"
chmod +x "$UV_TOOL_BIN_DIR/fixture"
`), 0700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	checked, bodyCalls, persisted := 0, 0, false
	reg := ActivityRegistration{TaskType: "metric.boundary", Step: coreops.TaskStep{
		InputType: reflect.TypeFor[map[string]any](),
		Invoke: func(deps coreops.OpDependencies, ctx context.Context, input map[string]any) (map[string]any, error) {
			bodyCalls++
			require.Equal(t, int64(16), clock.seconds, "decode + private hydration + restore + environment")
			require.Equal(t, deps.WorktreePath(), input["path"])
			activation, stderr, err := process.ExecuteProcess(ctx, process.RunRequest{Command: []string{"sh", "-c", `printf '%s' "$PATH"`}})
			require.NoError(t, err, string(stderr))
			require.Contains(t, string(activation), "/bindings/")
			clock.add(13)
			paths := deps.(coreops.OperationPathProvider).OperationPaths()
			require.NoError(t, os.WriteFile(filepath.Join(paths.Outbox, "result.txt"), []byte("result"), 0600))
			require.NoError(t, os.WriteFile(filepath.Join(deps.WorktreePath(), "changed"), []byte("persisted"), 0600))
			return map[string]any{"value": checkedMetricOutput{clock, &checked}}, nil
		},
	}}
	controller := timedWorkspace{Controller: gitstate.NewController(nil), clock: clock, persist: func(task *gitstate.GitTaskContext) {
		require.Positive(t, checked, "output validation precedes persistence")
		require.FileExists(t, filepath.Join(filepath.Dir(task.WorktreePath), "outbox/result.txt"))
		persisted = true
	}}
	worker := &taskWorker{name: reg.TaskType, reg: reg, doer: &opExecutor{deps: coreops.NewServiceDepsBuilder().Build(), reg: reg, controller: controller}}
	// The scripted manager advances four seconds and binds a local environment.
	payload := ActivityInvocationRequest{Input: map[string]any{"path": contextual.WorktreePathSentinel}, Preparation: PreparationRequest{Scopes: []toolenv.Scope{{ID: "fixture", Packages: []string{"uv:fixture"}}}}}
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	inv := ActivityInvocationRequest{InputArtifact: "bulk.json", GitTaskContext: gitstate.GlobalGitTaskContext{BaseRepo: repo, BaseRef: hash}}
	in := timedInput{jobdb.NewTaskDataOrPanic(inv, timedInputArtifact{jobdb.NewArtifactFromBytes("bulk.json", raw), clock}), clock}
	tc := wf.NewTaskContext(jobdb.JobKey{TenantId: "test", JobId: "metric-boundary"}, 1, nil, nil, nil)
	ctx := preparedtest.WithClock(deniedTaskContext(t, tc), clock.now)
	wrapped := metricsruntime.WrapWorker(worker, coretask.OutputKindActivityInvocationOutput)
	data, err := coretask.RunLocal(wrapped, ctx, tc, in)
	require.NoError(t, err)
	atReturn := clock.seconds
	env := metricEnvelope(t, data)
	require.Equal(t, 1, bodyCalls)
	require.True(t, persisted)
	require.Positive(t, checked)
	require.Equal(t, int64(16+13+11+checked*5), atReturn)
	metricSpan(t, env.Metrics.Runtime, "c2j_task_duration_seconds", map[string]string{"phase": "preparation", "outcome": "succeeded"}, 16)
	metricSpan(t, env.Metrics.Runtime, "c2j_task_duration_seconds", map[string]string{"phase": "execution", "outcome": "succeeded"}, float64(13+11+checked*5))
	metricSpan(t, env.Metrics.Runtime, "c2j_task_preparation_component_seconds", map[string]string{"component": "workspace", "outcome": "prepared"}, 7)
	metricSpan(t, env.Metrics.Runtime, "c2j_task_preparation_component_seconds", map[string]string{"component": "input", "outcome": "prepared"}, 0)
	metricSpan(t, env.Metrics.Runtime, "c2j_task_preparation_component_seconds", map[string]string{"component": "environment", "environment": "uv", "outcome": "prepared"}, 4)
	var out ActivityInvocationOutput
	require.NoError(t, env.DecodePayload(&out))
	// Existing diagnostics use wall time and end before sentinel hydration. The
	// virtual admitted duration must not overwrite those diagnostic fields.
	require.Less(t, out.PreparationMS, int64(16000))
	artifacts, err := data.GetArtifacts()
	require.NoError(t, err)
	requireArtifactNamed(t, artifacts, "result.txt")
	requireArtifactNamed(t, artifacts, gitstate.ThinPackArtifactName)
	// Queueing before the wrapper and append/storage after its return cannot
	// change an already captured admitted duration.
	clock.add(1000)
	again := metricEnvelope(t, data)
	require.Equal(t, env.Metrics, again.Metrics)
}

func TestTaskMetricsPreparationFailures(t *testing.T) {
	repo, hash, cleanup := setupGitRepo(t)
	defer cleanup()
	for _, failure := range []string{"decode", "restore", "construction", "environment", "canceled"} {
		t.Run(failure, func(t *testing.T) {
			clock := &metricClock{seconds: 1000} // queue delay before admitted instrumentation
			reg := ActivityRegistration{TaskType: "metric.failure", Step: coreops.TaskStep{Invoke: func(coreops.OpDependencies, context.Context, map[string]any) (map[string]any, error) {
				t.Fatal("body entered after preparation failure")
				return nil, nil
			}}}
			controller := timedWorkspace{Controller: gitstate.NewController(nil), clock: clock}
			if failure == "restore" {
				controller.restoreErr = errors.New("restore fixture")
			}
			if failure == "canceled" {
				controller.restoreErr = context.Canceled
			}
			inv := ActivityInvocationRequest{Input: map[string]any{}, GitTaskContext: gitstate.GlobalGitTaskContext{BaseRepo: repo, BaseRef: hash}}
			if failure == "environment" {
				inv.Preparation.Scopes = []toolenv.Scope{{ID: "fixture", Packages: []string{"unsupported:secret-path"}}}
				t.Setenv("C2J_TOOL_CACHE_DIR", t.TempDir())
			}
			var input jobdb.TaskData = jobdb.NewTaskDataOrPanic(inv)
			if failure == "decode" {
				input = &jobdb.SimpleTaskData{Data: json.RawMessage(`{`)}
			}
			var constructionDir string
			if failure == "construction" {
				file := filepath.Join(t.TempDir(), "file")
				require.NoError(t, os.WriteFile(file, []byte("not a directory"), 0600))
				constructionDir = file
			}
			worker := &taskWorker{name: reg.TaskType, reg: reg, doer: &opExecutor{deps: coreops.NewServiceDepsBuilder().Build(), reg: reg, controller: controller}}
			tc := wf.NewTaskContext(jobdb.JobKey{TenantId: "test", JobId: "metric-failure"}, 1, nil, nil, nil)
			ctx := preparedtest.WithClock(deniedTaskContext(t, tc), clock.now)
			if constructionDir != "" {
				t.Setenv("TMPDIR", constructionDir)
			}
			data, err := coretask.RunLocal(metricsruntime.WrapWorker(worker, coretask.OutputKindActivityInvocationOutput), ctx, tc, timedInput{input, clock})
			require.Error(t, err)
			env := metricEnvelope(t, data)
			status := "failed"
			if failure == "canceled" {
				status = "canceled"
				require.ErrorIs(t, err, context.Canceled)
			}
			require.Equal(t, map[string]string{"preparation": status, "execution": "not_started"}, env.Metrics.Runtime.Phases)
			want := float64(9)
			if failure == "decode" || failure == "construction" {
				want = 2
			}
			metricSpan(t, env.Metrics.Runtime, "c2j_task_duration_seconds", map[string]string{"phase": "preparation", "outcome": status}, want)
			for _, m := range env.Metrics.Runtime.Report.Mutations {
				require.NotEqual(t, "execution", m.Labels["phase"])
			}
			if failure == "restore" || failure == "canceled" || failure == "construction" {
				seconds := float64(7)
				if failure == "construction" {
					seconds = 0
				}
				metricSpan(t, env.Metrics.Runtime, "c2j_task_preparation_component_seconds", map[string]string{"component": "workspace", "outcome": status}, seconds)
			}
			if failure == "environment" {
				artifacts, e := data.GetArtifacts()
				require.NoError(t, e)
				requireArtifactNamed(t, artifacts, "_c2j/preparation.json")
				metricSpan(t, env.Metrics.Runtime, "c2j_task_preparation_component_seconds", map[string]string{"component": "environment", "environment": "none", "outcome": "failed"}, 0)
			}
		})
	}
}

// JobDB writes and queue waits dwarf the task but occur outside the admission
// wrapper. Persist a real failed preparation result and compare its envelope.
type metricAppendDelay struct {
	jobdb.WorkflowRuntime
	seconds *atomic.Int64
	writes  atomic.Int64
}

func (r *metricAppendDelay) PutChapter(ctx context.Context, req jobdb.PutChapterRequest) error {
	r.seconds.Add(1000)
	r.writes.Add(1)
	return r.WorkflowRuntime.PutChapter(ctx, req)
}
func TestAdmittedTaskMetricsExcludeQueueAndAppend(t *testing.T) {
	var seconds atomic.Int64
	source := &metricAppendDelay{WorkflowRuntime: toy.New(), seconds: &seconds}
	rt := executionruntime.New(source, execution.Allocation{SchemaVersion: 1})
	clock := func() time.Time { return time.Unix(seconds.Load(), 0) }
	ctx := preparedtest.WithClock(celltest.Context(context.Background()), clock)
	// A malformed invocation fails after admission, without body or Git activity.
	input := jobdb.NewTaskDataOrPanic(map[string]any{"input": map[string]any{}})
	worker := &taskWorker{name: "metric.admitted"}
	orchestration := wf.ExecutionWorker{JobType: "metric-admitted", Execute: func(c wf.ExecutionContext, data jobdb.JobData) (jobdb.JobData, error) {
		seconds.Add(1000) // queueing / deterministic orchestration before dispatch
		return c.DoTask(wf.TaskCall{Site: "admitted", TaskType: worker.Name(), ContractVersion: 1, Command: json.RawMessage(`{}`), Policy: jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 1}}}, func(context.Context) (jobdb.TaskData, error) { return data, nil })
	}}
	celltest.Create(t, rt, "test", "test", "example.test/test")
	job, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "test", JobType: orchestration.Name(), Data: input, Cell: "test", CellRepo: "example.test/test", RunPolicy: jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 1}}}})
	require.NoError(t, err)
	seconds.Add(1000)
	// Delay input decoding inside the admitted worker, after middleware's own
	// decode. The private invocation attachment is the actual hydration boundary.
	payload := jobdb.NewArtifactFromBytes("broken.json", []byte(`{`))
	delayed := admittedMetricInputWorker{TaskWorker: worker, seconds: &seconds, artifact: payload}
	wrapped := metricsruntime.WrapWorker(rt.WrapTaskWorker(delayed), coretask.OutputKindActivityInvocationOutput)
	handle, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: job.JobKey, JobWorker: orchestration, TaskWorkers: []wf.TaskWorker{wrapped}, WorkerID: "fixture", LeaseDuration: time.Minute})
	require.NoError(t, err)
	outcome, err := handle.Run(nil)
	require.NoError(t, err)
	require.Equal(t, wf.JobRunFailed, outcome.Status)
	run, err := jobdb.GetJobRun(ctx, rt, jobdb.GetJobRunRequest{JobKey: job.JobKey, IncludeOutputs: true})
	require.NoError(t, err)
	tasks, err := swfutil.ExecutionTasks(run)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Len(t, tasks[0].Attempts, 1)
	stored := tasks[0].Attempts[0].Output
	require.NotNil(t, stored)
	env := metricEnvelope(t, &jobdb.SimpleTaskData{Data: stored.Data})
	require.Equal(t, "not_started", env.Metrics.Runtime.Phases["execution"])
	metricSpan(t, env.Metrics.Runtime, "c2j_task_duration_seconds", map[string]string{"phase": "preparation", "outcome": "failed"}, 17)
	require.Positive(t, source.writes.Load())
	require.Greater(t, seconds.Load(), int64(2000))
}

type admittedMetricInputWorker struct {
	wf.TaskWorker
	seconds  *atomic.Int64
	artifact jobdb.Artifact
}

func (w admittedMetricInputWorker) RunTask(local context.Context, tc wf.TaskContext, _ jobdb.TaskData) (jobdb.TaskData, error) {
	w.seconds.Add(17)
	return coretask.RunLocal(w.TaskWorker, local, tc, jobdb.NewTaskDataOrPanic(ActivityInvocationRequest{InputArtifact: "broken.json"}, w.artifact))
}
