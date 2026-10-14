package compiler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/jobdbschema"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	toyruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestConfigurationResumeUsesLiveTaskReadsAndRecordedRouting(t *testing.T) {
	ctx := context.Background()
	type input struct {
		Value   bool `json:"value"`
		Missing any  `json:"missing"`
	}
	calls := map[string][]bool{}
	makeOp := func(name string) coreops.RegisterableOp {
		return coreops.NewActivityMappedOpV2[input, input](coreops.OpMetadata{Type: name}, func(_ coreops.OpDependencies, _ context.Context, in input) (input, error) {
			require.Nil(t, in.Missing)
			calls[name] = append(calls[name], in.Value)
			return in, nil
		})
	}
	withRegisteredOps(t, makeOp("config_a"), makeOp("config_b"))
	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	ws, err := NewRecipeWorker(coreops.NewServiceDepsBuilder().Build(), registry)
	require.NoError(t, err)
	rt := toyruntime.New()
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId("config").PlusWorkers(ws.JobWorker).BuildEngine()
	require.NoError(t, err)
	engine = jobdbschema.WorkflowEngine{Engine: engine, Registry: rt}
	set := func(value string) {
		_, err := rt.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: "config", Name: "flag", Value: json.RawMessage(value)})
		require.NoError(t, err)
	}
	rec, err := recipe.LoadRecipeFromString([]byte(`id: config
sequence:
  - id: a
    op: config_a
    inputs:
      value: '${{ context.config.?flag.orValue(true) }}'
      missing: '${{ context.config.absent }}'
  - id: b
    when: sequence.a.outputs.value
    op: config_b
    inputs:
      value: '${{ context.config.?flag.orValue(true) }}'
      missing: '${{ context.config.absent }}'
outputs:
  a: '${{ sequence.a.outputs.value }}'
  b: '${{ sequence.b.outputs.value }}'
`))
	require.NoError(t, err)
	job, git := GenerateTestContext()
	key, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "config", RecipeName: "config", JobContext: job, GitRef: git.ParentRef}, engine, *rec)
	require.NoError(t, err)
	var first, all []jobworkflow.TaskWorker
	for name, w := range ws.TaskWorkers {
		all = append(all, w)
		if name != "config_b:config_b" {
			first = append(first, w)
		}
	}
	run := func(workers []jobworkflow.TaskWorker) jobworkflow.JobRunOutcome {
		runnable, err := jobworkflow.GetJobForRun(ctx, rt, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: ws.JobWorker, TaskWorkers: workers, WorkerID: "test", LeaseDuration: time.Minute})
		require.NoError(t, err)
		outcome, err := runnable.Run(nil)
		require.NoError(t, err)
		return outcome
	}
	require.Equal(t, jobworkflow.JobRunSuspended, run(first).Status)
	require.Equal(t, []bool{true}, calls["config_a"])
	require.Empty(t, calls["config_b"])
	set("false")
	outcome := run(all)
	require.Equal(t, jobworkflow.JobRunCompleted, outcome.Status, "%v", outcome.JobError)
	require.Equal(t, []bool{true}, calls["config_a"], "cached A never executes or seeds B")
	require.Equal(t, []bool{false}, calls["config_b"])
	result, err := swfutil.JobResult(ctx, engine, key)
	require.NoError(t, err)
	raw, err := result.GetData()
	require.NoError(t, err)
	require.JSONEq(t, `{"a":true,"b":false}`, string(raw))
	history, err := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: key, IncludeOutputs: true})
	require.NoError(t, err)
	seen := map[string]string{}
	for _, tasks := range [][]jobdb.TaskRun{executionTaskRuns(history)} {
		for _, task := range tasks {
			require.NotContains(t, []string{"configuration_snapshot", "tool_setup", "secret_setup", "extension_resolution"}, task.TaskType)
			if task.TaskType != "config_a:config_a" && task.TaskType != "config_b:config_b" {
				continue
			}
			for _, call := range task.Attempts {
				if call.Output == nil {
					continue
				}
				decoded, err := decodeTaskOutput(call.Output.Data)
				require.NoError(t, err)
				require.NotNil(t, decoded.Activity.Configuration)
				diag := decoded.Activity.Configuration
				if task.TaskType == "config_a:config_a" {
					require.Empty(t, diag.Values)
					require.Equal(t, []string{"absent", "flag"}, diag.Missing)
					seen[task.TaskType] = "missing"
				} else {
					require.Len(t, diag.Values, 1)
					require.Equal(t, []string{"absent"}, diag.Missing)
					seen[task.TaskType] = string(diag.Values["flag"])
				}
			}
		}
	}
	require.Equal(t, map[string]string{"config_a:config_a": "missing", "config_b:config_b": "false"}, seen)
	// No provider value can affect offline replay, including one of the wrong type.
	set(`"wrong-type"`)
	replay, err := engine.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
	require.NoError(t, err)
	replayRaw, err := replay.GetData()
	require.NoError(t, err)
	require.JSONEq(t, string(raw), string(replayRaw))
}
