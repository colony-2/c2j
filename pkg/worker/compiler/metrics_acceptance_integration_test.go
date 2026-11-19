package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/metrics"
	"github.com/colony-2/c2j/pkg/metricshistory"
	"github.com/colony-2/c2j/pkg/metricsruntime"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	coretask "github.com/colony-2/c2j/pkg/task"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// W11: run registered native steps through the actual op executor, stop at a
// missing second worker, and reconstruct the first step on another activation.
func TestMultistepMetricDeltasAcrossResumeAndReplay(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			calls := [2]int{}
			type intermediate struct {
				First bool `json:"first"`
			}
			first := coreops.NewStepWithDeps(func(_ coreops.OpDependencies, ctx context.Context, _ struct{}) (intermediate, error) {
				calls[0]++
				metrics.FromContext(ctx).AddInt("step_tokens", "{token}", nil, 3)
				return intermediate{First: true}, nil
			})
			second := coreops.NewStepWithDeps(func(_ coreops.OpDependencies, ctx context.Context, in intermediate) (map[string]any, error) {
				calls[1]++
				if !in.First {
					return nil, fmt.Errorf("first step output missing")
				}
				if calls[1] == 1 {
					metrics.FromContext(ctx).AddInt("step_tokens", "{token}", nil, 5)
					return nil, fmt.Errorf("retry second step")
				}
				metrics.FromContext(ctx).AddInt("step_tokens", "{token}", nil, 7)
				return map[string]any{"done": true}, nil
			})
			op, err := coreops.NewOp().WithType("metrics_multi").AddStep("first", first).AddStep("second", second).Build()
			require.NoError(t, err)
			ws := workspaceTestWorker(t, op.(coreops.RegisterableOp))
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "source")
			rt := newTimeoutReproRuntime(t, backend).ConsumerRuntime
			key := submitGitLifecycle(t, rt, repo, fmt.Sprintf(`id: git-lifecycle
version: "1.0.0"
input_schema: {}
workspace: {cell: test, repo: %q, ref: main}
sequence:
 - id: multi
   op: metrics_multi
   retry: {maximum_attempts: 2, initial_interval: 1ms}
outputs:
 done: '${{ sequence.multi.outputs.done }}'
 node: '${{ sequence.multi.metrics }}'
 job: '${{ context.metrics }}'
`, celltest.Repo(repo)))
			ctx := celltest.Context(context.Background())
			run := func(tasks []wf.TaskWorker) wf.JobRunOutcome {
				guard := executionruntime.New(rt, execution.Allocation{SchemaVersion: 1})
				var wrapped []wf.TaskWorker
				for _, task := range tasks {
					wrapped = append(wrapped, guard.WrapTaskWorker(task))
				}
				h, e := wf.GetJobForRun(ctx, guard, wf.GetJobForRunRequest{JobKey: key, JobWorker: ws.JobWorker, TaskWorkers: wrapped, WorkerID: "multistep", LeaseDuration: time.Minute})
				require.NoError(t, e)
				out, e := h.Run(nil)
				require.NoError(t, e)
				return out
			}
			var available, allTasks []wf.TaskWorker
			for _, task := range ws.TaskWorkers {
				allTasks = append(allTasks, task)
				if task.Name() != "metrics_multi:second" {
					available = append(available, task)
				}
			}
			initial := run(available)
			require.Equal(t, wf.JobRunSuspended, initial.Status, "%v", initial.JobError)
			require.Equal(t, [2]int{1, 0}, calls)
			prefix, err := metricshistory.Read(ctx, rt, key, metrics.Query{Name: "step_tokens", Deltas: true})
			require.NoError(t, err)
			require.Equal(t, int64(3), prefix.Snapshot.Series["step_tokens"][0].Value)
			out := run(allTasks)
			values := metricViewOutput(t, out)
			require.Equal(t, true, values["done"])
			require.Equal(t, [2]int{2, 2}, calls, "operation retry reruns both steps; resume reuses the first committed step")
			for _, name := range []string{"node", "job"} {
				require.Equal(t, int64(18), metricViewSnapshot(t, values[name]).Series["step_tokens"][0].Value)
			}
			api := &jobinspect.Service{Runtime: rt, Tenant: key.TenantId}
			result, err := api.Inspect(ctx, jobinspect.Request{Operation: "metrics", JobID: key.JobId, Metrics: &metrics.Query{Name: "step_tokens", Deltas: true}})
			require.NoError(t, err)
			require.Equal(t, int64(18), result.Metrics.Snapshot.Series["step_tokens"][0].Value)
			var steps []metrics.Record
			for _, r := range result.Metrics.Reports {
				if r.Channel == "producer" && r.ChannelReport.Report != nil && len(r.ChannelReport.Report.Mutations) > 0 {
					steps = append(steps, r)
				}
			}
			require.Len(t, steps, 4)
			for i, want := range []int64{3, 5, 3, 7} {
				require.Equal(t, 1, steps[i].Attempt)
				require.Contains(t, steps[i].Task, fmt.Sprintf(`/scope:"op:multi"#1/attempt:%d/`, 1+i/2))
				require.Len(t, steps[i].ChannelReport.Report.Mutations, 1)
				require.Equal(t, want, steps[i].ChannelReport.Report.Mutations[0].Value)
				if i > 0 {
					require.Less(t, steps[i-1].Ordinal, steps[i].Ordinal)
				}
			}
			require.NotEqual(t, steps[0].Task, steps[1].Task)
			require.NotEqual(t, steps[0].Task, steps[2].Task)
			engine, err := wf.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(key.TenantId).PlusWorkers(ws.JobWorker).BuildEngine()
			require.NoError(t, err)
			replayed, err := engine.ReplayJobRun(ctx, wf.ReplayRunRequest{JobKey: key})
			require.NoError(t, err)
			require.JSONEq(t, string(out.Output.GetDataOrPanic()), string(replayed.GetDataOrPanic()))
			after, err := api.Inspect(ctx, jobinspect.Request{Operation: "metrics", JobID: key.JobId, Metrics: &metrics.Query{Name: "step_tokens", Deltas: true}})
			require.NoError(t, err)
			require.Equal(t, result.Metrics, after.Metrics)
			require.Equal(t, [2]int{2, 2}, calls)
		})
	}
}

type cpuSelectionTask struct{ channels map[string]metrics.Channel }

func (*cpuSelectionTask) Name() string { return "scope_probe:run" }
func (w *cpuSelectionTask) Run(_ wf.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	var request workerops.ActivityInvocationRequest
	if err := json.Unmarshal(in.GetDataOrPanic(), &request); err != nil {
		return nil, err
	}
	env, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, workerops.ActivityInvocationOutput{OpOutput: request.Input})
	if err != nil {
		return nil, err
	}
	env.Metrics = &metrics.Channels{Runtime: w.channels[request.Input["name"].(string)], Producer: metrics.Channel{Status: "absent"}}
	return jobdb.NewTaskData(env)
}

// C09: real sampler windows feed persisted runtime reports. A and B overlap for
// twenty seconds, so their excluded task-window sum is forty, not unique time.
func TestMetricCPUSelectionsWithOverlap(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			now, cpu := time.Unix(0, 0), 0.
			sampler := metricsruntime.NewSampler(func() time.Time { return now }, func() (float64, error) { return cpu, nil })
			a := sampler.Start()
			now, cpu = now.Add(10*time.Second), 10
			b := sampler.Start()
			now, cpu = now.Add(20*time.Second), 20
			bc := b()
			now, cpu = now.Add(10*time.Second), 25
			ac := a()
			c := sampler.Start()
			now, cpu = now.Add(60*time.Second), 55
			cc := c()
			channels := map[string]metrics.Channel{}
			for name, coverage := range map[string]metrics.CPUCoverage{"a": ac, "b": bc, "c": cc} {
				collector := metrics.NewRuntimeCollector(nil)
				if coverage.MeasuredSeconds > 0 {
					labels := map[string]string{"source": "worker_process"}
					collector.AddFloat("c2j_process_cpu_seconds_total", "s", labels, coverage.ProcessSeconds)
					collector.AddFloat("c2j_process_cpu_observed_seconds_total", "s", labels, coverage.MeasuredSeconds)
				}
				channel := collector.Snapshot()
				channel.CPU = &coverage
				channels[name] = channel
			}
			rt := newTimeoutReproRuntime(t, backend).ConsumerRuntime
			rec, err := recipe.LoadRecipeFromString([]byte(`id: root
version: "1.0.0"
input_schema: {}
sequence:
- id: pair
  sequence:
  - id: a
    op: scope_probe
    inputs: {name: a}
  - id: b
    op: scope_probe
    inputs: {name: b}
  outputs: {}
- id: c
  op: scope_probe
  inputs: {name: c}
outputs:
 pair: '${{ sequence.pair.metrics }}'
 c: '${{ sequence.c.metrics }}'
 job: '${{ context.metrics }}'
`))
			require.NoError(t, err)
			worker := probeWorker(t, *rec)
			worker.ObserveTaskResults = ObserveTaskMetrics
			key := probeSubmit(t, rt, worker)
			values := metricViewOutput(t, probeRun(t, rt, key, worker, &cpuSelectionTask{channels}))
			assertCPU := func(snapshot metrics.Snapshot, process, measured, excluded float64) {
				t.Helper()
				require.Equal(t, process, snapshot.Coverage.CPU.ProcessSeconds)
				require.Equal(t, measured, snapshot.Coverage.CPU.MeasuredSeconds)
				require.Equal(t, excluded, snapshot.Coverage.CPU.ExcludedSeconds["concurrent_tasks"])
				if measured == 0 {
					require.NotContains(t, snapshot.Series, "c2j_process_cpu_seconds_total")
					require.NotContains(t, snapshot.Series, "c2j_process_cpu_observed_seconds_total")
				} else {
					require.Equal(t, process, snapshot.Series["c2j_process_cpu_seconds_total"][0].Value)
					require.Equal(t, measured, snapshot.Series["c2j_process_cpu_observed_seconds_total"][0].Value)
				}
			}
			assertCPU(metricViewSnapshot(t, values["pair"]), 15, 20, 40)
			assertCPU(metricViewSnapshot(t, values["c"]), 30, 60, 0)
			assertCPU(metricViewSnapshot(t, values["job"]), 45, 80, 40)
			api := &jobinspect.Service{Runtime: rt, Tenant: key.TenantId}
			ctx := celltest.Context(context.Background())
			all, err := api.Inspect(ctx, jobinspect.Request{Operation: "metrics", JobID: key.JobId, Metrics: &metrics.Query{Deltas: true}})
			require.NoError(t, err)
			selectedTasks := 0
			for _, report := range all.Metrics.Reports {
				if report.Channel != "runtime" || report.ChannelReport.CPU == nil {
					continue
				}
				selectedTasks++
				selected, err := api.Inspect(ctx, jobinspect.Request{Operation: "metrics", JobID: key.JobId, Metrics: &metrics.Query{Task: report.Task, Attempt: report.Attempt, Deltas: true}})
				require.NoError(t, err)
				switch {
				case strings.Contains(report.Task, `op:a"`):
					assertCPU(selected.Metrics.Snapshot, 15, 20, 20)
				case strings.Contains(report.Task, `op:b"`):
					assertCPU(selected.Metrics.Snapshot, 0, 0, 20)
				case strings.Contains(report.Task, `op:c"`):
					assertCPU(selected.Metrics.Snapshot, 30, 60, 0)
				default:
					t.Fatalf("unexpected CPU task %s", report.Task)
				}
				require.Equal(t, report.ChannelReport.CPU.Availability, selected.Metrics.Snapshot.Coverage.CPU.Availability)
			}
			require.Equal(t, 3, selectedTasks)
			var pairID string
			for _, report := range all.Metrics.Reports {
				if i := strings.Index(report.Task, `/scope:"sequence:pair"#1`); i >= 0 {
					pairID = report.Task[:i] + `/scope:"sequence:pair"#1`
				}
			}
			require.NotEmpty(t, pairID)
			selected, err := api.Inspect(ctx, jobinspect.Request{Operation: "metrics", JobID: key.JobId, Metrics: &metrics.Query{Scope: pairID}})
			require.NoError(t, err)
			assertCPU(selected.Metrics.Snapshot, 15, 20, 40)
			require.NoError(t, probeReplay(t, rt, key, worker))
		})
	}
}
