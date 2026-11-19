package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/jsonexact"
	"github.com/colony-2/c2j/pkg/metrics"
	"github.com/colony-2/c2j/pkg/metricshistory"
	"github.com/colony-2/c2j/pkg/recipe"
	coretask "github.com/colony-2/c2j/pkg/task"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// Deterministic committed reports exercise template consumption independently of
// clocks/providers. The registered-worker fixture covers actual instrumentation.
type metricViewTask struct {
	mu    sync.Mutex
	calls map[string]int
}

func (*metricViewTask) Name() string { return "scope_probe:run" }
func (m *metricViewTask) Run(_ wf.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	var request workerops.ActivityInvocationRequest
	if err := jsonexact.Unmarshal(input.GetDataOrPanic(), &request); err != nil {
		return nil, err
	}
	request.Input = jsonexact.Native(request.Input).(map[string]any)
	name, _ := request.Input["name"].(string)
	m.mu.Lock()
	m.calls[name]++
	count := m.calls[name]
	m.mu.Unlock()
	n, ok := request.Input["value"].(int64)
	if !ok {
		return nil, fmt.Errorf("fixture requires an exact integer value")
	}
	env, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, workerops.ActivityInvocationOutput{OpOutput: request.Input})
	if err != nil {
		return nil, err
	}
	env.Metrics = &metrics.Channels{Runtime: metrics.Channel{Status: "absent"}, Producer: metrics.Channel{Status: "valid", Report: &metrics.Report{Version: 1, Mutations: []metrics.Mutation{{Descriptor: metrics.Descriptor{Name: "tokens", Kind: "counter", NumberType: "int64", Unit: "{token}"}, Op: "add", Value: n}}}}}
	out := jobdb.NewTaskDataOrPanic(env)
	if name == "fail" || (name == "retry" && count == 1) {
		return out, fmt.Errorf("fixture failure")
	}
	return out, nil
}
func metricViewRecipe(t *testing.T, rt jobdb.ConsumerRuntime, yaml string) (wf.ExecutionWorker, jobdb.JobKey, *metricViewTask) {
	t.Helper()
	rec, err := recipe.LoadRecipeFromString([]byte(yaml))
	require.NoError(t, err)
	worker := probeWorker(t, *rec)
	worker.ObserveTaskResults = ObserveTaskMetrics
	task := &metricViewTask{calls: map[string]int{}}
	return worker, probeSubmit(t, rt, worker), task
}
func metricViewOutput(t *testing.T, out wf.JobRunOutcome) map[string]any {
	t.Helper()
	require.Equal(t, wf.JobRunCompleted, out.Status, "%v", out.JobError)
	var value map[string]any
	require.NoError(t, jsonexact.Unmarshal(out.Output.GetDataOrPanic(), &value))
	return jsonexact.Native(value).(map[string]any)
}
func metricViewSnapshot(t *testing.T, value any) metrics.Snapshot {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	var s metrics.Snapshot
	require.NoError(t, json.Unmarshal(raw, &s))
	return s
}
func TestMetricTemplateFrontiersAndNestedOccurrences(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			rt := newTimeoutReproRuntime(t, backend).ConsumerRuntime
			worker, key, task := metricViewRecipe(t, rt, `id: root
version: "1.0.0"
input_schema: {}
sequence:
- id: a
  op: scope_probe
  inputs:
    name: a
    value: 3
    before: '${{ "tokens" in context.metrics.series ? context.metrics.series["tokens"][0].value : -1 }}'
- id: metrics
  sequence:
  - id: large
    op: scope_probe
    inputs:
      name: large
      value: '${{ 9007199254740993 }}'
      before: '${{ context.metrics.series["tokens"][0].value }}'
      scope_empty: '${{ !("tokens" in scope.metrics.series) }}'
  - id: b
    op: scope_probe
    inputs:
      name: b
      value: 5
      before: '${{ context.metrics.series["tokens"][0].value }}'
  outputs:
    job: '${{ context.metrics.series["tokens"][0].value }}'
    scope: '${{ scope.metrics.series["tokens"][0].value }}'
    first: '${{ sequence.large.metrics }}'
    first_before: '${{ sequence.large.outputs.before }}'
    first_scope_empty: '${{ sequence.large.outputs.scope_empty }}'
    second_before: '${{ sequence.b.outputs.before }}'
- id: metrics_extra
  op: scope_probe
  inputs: {name: last, value: 7}
outputs:
  job: '${{ context.metrics }}'
  scope: '${{ scope.metrics }}'
  a: '${{ sequence.a.metrics }}'
  first_before: '${{ sequence.a.outputs.before }}'
  nested: '${{ sequence.metrics.metrics }}'
  nested_output: '${{ sequence.metrics.outputs }}'
  last: '${{ sequence.metrics_extra.metrics }}'
  go_total: '{{ (index context.metrics.series.tokens 0).value }}'
`)
			out := probeRun(t, rt, key, worker, task)
			value := metricViewOutput(t, out)
			const large = int64(9007199254740993)
			for field, want := range map[string]int64{"job": large + 15, "scope": large + 15, "a": 3, "nested": large + 5, "last": 7} {
				require.Equal(t, want, metricViewSnapshot(t, value[field]).Series["tokens"][0].Value, field)
			}
			require.Equal(t, int64(-1), value["first_before"])
			nested := value["nested_output"].(map[string]any)
			require.Equal(t, int64(3), nested["first_before"])
			require.Equal(t, true, nested["first_scope_empty"])
			require.Equal(t, large+3, nested["second_before"])
			require.Equal(t, large+5, nested["scope"])
			require.Equal(t, large+8, nested["job"])
			require.Equal(t, fmt.Sprint(large+15), value["go_total"])
			snapshot := metricViewSnapshot(t, value["job"])
			inspected, err := metricshistory.Read(celltest.Context(context.Background()), rt, key, metrics.Query{ThroughOrdinal: &snapshot.Cutoff})
			require.NoError(t, err)
			require.Equal(t, snapshot.Series, inspected.Snapshot.Series)
			require.Equal(t, snapshot.Coverage, inspected.Snapshot.Coverage)
			before, err := rt.ListChapters(celltest.Context(context.Background()), jobdb.ListChaptersRequest{JobKey: key})
			require.NoError(t, err)
			require.NoError(t, probeReplay(t, rt, key, worker))
			require.NoError(t, probeReplay(t, rt, key, worker))
			after, err := rt.ListChapters(celltest.Context(context.Background()), jobdb.ListChaptersRequest{JobKey: key})
			require.NoError(t, err)
			require.Equal(t, before, after)
			require.Equal(t, map[string]int{"a": 1, "large": 1, "b": 1, "last": 1}, task.calls)
		})
	}
}

func TestMetricFailedScopesAndCatchFrontier(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			rt := newTimeoutReproRuntime(t, backend).ConsumerRuntime
			worker, key, task := metricViewRecipe(t, rt, `id: root
sequence:
- id: prefix
  op: scope_probe
  inputs: {name: prefix, value: 3}
- id: retried
  retry: {maximum_attempts: 2, initial_interval: 1ms}
  sequence:
  - id: a
    op: scope_probe
    inputs: {name: a, value: 5}
  - id: b
    op: scope_probe
    inputs: {name: retry, value: 7}
  outputs:
    scope: '${{ scope.metrics.series["tokens"][0].value }}'
- id: caught
  sequence:
  - id: a
    op: scope_probe
    inputs: {name: c, value: 11}
  - id: bad
    op: scope_probe
    inputs: {name: fail, value: 13}
  catch:
  - when: 'context.metrics.series["tokens"][0].value == 51 && scope.metrics.series["tokens"][0].value == 24'
    continue:
      outputs:
        metrics: {version: 1, mutations: [{name: tokens, kind: counter, number_type: int64, op: add, value: 1000}]}
        job: '${{ context.metrics.series["tokens"][0].value }}'
        scope: '${{ scope.metrics.series["tokens"][0].value }}'
        unpublished: '${{ !("bad" in sequence) }}'
outputs:
  job: '${{ context.metrics.series["tokens"][0].value }}'
  retry: '${{ sequence.retried.metrics.series["tokens"][0].value }}'
  retry_output: '${{ sequence.retried.outputs.scope }}'
  caught: '${{ sequence.caught.metrics.series["tokens"][0].value }}'
  catch_output: '${{ sequence.caught.outputs }}'
`)
			value := metricViewOutput(t, probeRun(t, rt, key, worker, task))
			require.Equal(t, int64(51), value["job"])
			require.Equal(t, int64(24), value["retry"])
			require.Equal(t, int64(24), value["retry_output"])
			require.Equal(t, int64(24), value["caught"])
			caught := value["catch_output"].(map[string]any)
			require.Equal(t, int64(51), caught["job"])
			require.Equal(t, int64(24), caught["scope"])
			require.Equal(t, true, caught["unpublished"])
			require.NoError(t, probeReplay(t, rt, key, worker))
			require.Equal(t, 2, task.calls["retry"])
		})
	}
}

func TestMetricLoopRunsAndRestartPrefix(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			rt := newTimeoutReproRuntime(t, backend).ConsumerRuntime
			worker, key, task := metricViewRecipe(t, rt, `id: root
state:
  initial: metrics
  states:
    metrics:
      op: scope_probe
      inputs: {name: loop, value: 2}
      transitions:
      - to: metrics
        when: 'context.metrics.series["tokens"][0].value < 6'
outputs:
  job: '${{ context.metrics.series["tokens"][0].value }}'
  latest: '${{ states.metrics.metrics.series["tokens"][0].value }}'
  first: '${{ states.metrics.runs[0].metrics.series["tokens"][0].value }}'
  second: '${{ states.metrics.runs[1].metrics.series["tokens"][0].value }}'
`)
			value := metricViewOutput(t, probeRun(t, rt, key, worker, task))
			require.Equal(t, int64(6), value["job"])
			for _, field := range []string{"latest", "first", "second"} {
				require.Equal(t, int64(2), value[field], field)
			}
			require.NoError(t, probeReplay(t, rt, key, worker))
			require.Equal(t, 3, task.calls["loop"])
			// A restart reconstructs the retained prefix; it cannot import source totals
			// or metrics hidden in patched business output.
			worker, key, task = metricViewRecipe(t, rt, `id: root
sequence:
- id: prefix
  op: scope_probe
  inputs: {name: prefix, value: 3}
- id: block
  sequence:
  - id: value
    op: scope_probe
    inputs:
      name: body
      value: '${{ context.git.author == "patched" ? 11 : 5 }}'
  outputs: {}
- id: suffix
  op: scope_probe
  inputs: {name: suffix, value: 7}
outputs:
  total: '${{ context.metrics.series["tokens"][0].value }}'
  prefix: '${{ sequence.prefix.metrics.series["tokens"][0].value }}'
  business: '${{ sequence.prefix.outputs.value }}'
`)
			require.Equal(t, int64(15), metricViewOutput(t, probeRun(t, rt, key, worker, task))["total"])
			ctx := celltest.Context(context.Background())
			run, err := jobdb.GetJobRun(ctx, rt, jobdb.GetJobRunRequest{JobKey: key})
			require.NoError(t, err)
			var scope string
			for _, e := range run.Execution {
				if e.Record.Kind == "scope" && strings.HasSuffix(e.Record.ID, `scope:"sequence:block"#1`) {
					scope = e.Record.ID
					break
				}
			}
			require.NotEmpty(t, scope)
			restarted, err := rt.SubmitRestartJob(ctx, jobdb.SubmitRestartJobRequest{Job: jobdb.SubmitRestartJob{PriorJobKey: key, Cell: "test", ExecutionRestart: &jobdb.ExecutionRestart{ScopeID: scope, Payload: json.RawMessage(`[{"op":"add","path":"/context/git/author","value":"patched"},{"op":"replace","path":"/sequence/prefix/outputs/value","value":99}]`)}}})
			require.NoError(t, err)
			value = metricViewOutput(t, probeRun(t, rt, restarted.JobKey, worker, task))
			require.Equal(t, int64(21), value["total"])
			require.Equal(t, int64(3), value["prefix"])
			require.Equal(t, int64(99), value["business"])
			require.Equal(t, 1, task.calls["prefix"])
			require.NoError(t, probeReplay(t, rt, restarted.JobKey, worker))
			inspected, err := metricshistory.Read(ctx, rt, restarted.JobKey, metrics.Query{})
			require.NoError(t, err)
			require.Equal(t, int64(21), inspected.Snapshot.Series["tokens"][0].Value)
		})
	}
}
