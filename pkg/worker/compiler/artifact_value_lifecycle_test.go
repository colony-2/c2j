package compiler

import (
	"context"
	"encoding/json"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"testing"

	"github.com/colony-2/c2j/pkg/artifacts"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	coretask "github.com/colony-2/c2j/pkg/task"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestValueArtifactLargeInputLifecycle(t *testing.T) {
	op, err := coreops.NewOp().WithType("scope_probe").WithAcceptsArtifacts(true).
		AddStep("run", coreops.NewStepWithDeps(func(coreops.OpDependencies, context.Context, map[string]any) (map[string]any, error) { return nil, nil })).Build()
	require.NoError(t, err)
	withRegisteredOps(t, op.(coreops.RegisterableOp))
	rt := newTimeoutReproRuntime(t, "sqlite")
	_, git := GenerateTestContext()
	task := &scopeProbeTask{calls: map[string]int{}, git: git}
	rec, err := recipe.LoadRecipeFromString([]byte(`id: root
sequence:
 - id: consume
   op: scope_probe
   inputs: {name: consume}
   artifacts:
    context.txt:
     kind: value
     format: text
     value: '${{ jq(0, "\"x\" * 524288") }}'
outputs: {ok: true}
`))
	require.NoError(t, err)
	w := probeWorker(t, *rec)
	key := probeSubmit(t, rt, w)
	outcome := probeRun(t, rt, key, w, &valueProbeTask{scopeProbeTask: task, t: t})
	require.Equal(t, jobworkflow.JobRunCompleted, outcome.Status, "%v", outcome.JobError)
	require.NoError(t, outcome.JobError)
	require.NoError(t, probeReplay(t, rt, key, w))
	require.Equal(t, 1, task.calls["consume"])
}

// Simulate a fresh worker loading the recorded task-input attachment.
type valueProbeTask struct {
	*scopeProbeTask
	t *testing.T
}

func (p *valueProbeTask) Run(ctx jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	raw, err := input.GetData()
	require.NoError(p.t, err)
	require.Less(p.t, len(raw), 4096)
	var req workerops.ActivityInvocationRequest
	require.NoError(p.t, json.Unmarshal(raw, &req))
	require.NotEmpty(p.t, req.InputArtifact)
	attached, err := input.GetArtifacts()
	require.NoError(p.t, err)
	attached, err = req.RestoreInput(ctx.Context(), attached)
	require.NoError(p.t, err)
	require.Empty(p.t, attached)
	content, err := req.Artifacts["context.txt"].ValueBytes()
	require.NoError(p.t, err)
	require.Len(p.t, content, 524288)
	restored, err := jobdb.NewTaskData(req)
	require.NoError(p.t, err)
	return p.scopeProbeTask.Run(ctx, restored)
}

type cachedValueStepContext struct {
	countingJobContext
	t *testing.T
}

func (c *cachedValueStepContext) DoScope(_ string, _ jobworkflow.ScopeOptions, body func(jobworkflow.ExecutionContext) (jobdb.JobData, error)) (jobdb.JobData, error) {
	return body(c)
}
func (c *cachedValueStepContext) DoTask(call jobworkflow.TaskCall, build func(context.Context) (jobdb.TaskData, error)) (jobdb.TaskData, error) {
	if call.TaskType == "value_steps:first" {
		// Cached output: deliberately do not call the first step's input builder.
		ref, _, err := artifacts.ParseValue(map[string]any{"kind": "value", "name": "later.txt", "format": "text", "value": "from recorded output"})
		require.NoError(c.t, err)
		env, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, workerops.ActivityInvocationOutput{OpOutput: map[string]any{"document": ref}, NextTask: "value_steps:second"})
		require.NoError(c.t, err)
		return jobdb.NewTaskData(env)
	}
	require.Equal(c.t, "value_steps:second", call.TaskType)
	data, err := build(c.Context())
	require.NoError(c.t, err)
	raw, err := data.GetData()
	require.NoError(c.t, err)
	var req workerops.ActivityInvocationRequest
	require.NoError(c.t, json.Unmarshal(raw, &req))
	content, err := req.Artifacts["later.txt"].ValueBytes()
	require.NoError(c.t, err)
	require.Equal(c.t, "from recorded output", string(content))
	c.calls++
	_, git := GenerateTestContext()
	return scopeProbeOutput(map[string]any{"ok": true}, git)
}
func TestValueArtifactResumedStepDerivesBindingsFromRecordedInput(t *testing.T) {
	type input struct {
		Document artifacts.Ref `json:"document"`
	}
	op, err := coreops.NewOp().WithType("value_steps").WithAcceptsArtifacts(true).
		AddStep("first", coreops.NewStepWithDeps(func(coreops.OpDependencies, context.Context, map[string]any) (input, error) { return input{}, nil })).
		AddStep("second", coreops.NewStepWithDeps(func(coreops.OpDependencies, context.Context, input) (map[string]any, error) { return nil, nil })).Build()
	require.NoError(t, err)
	withRegisteredOps(t, op.(coreops.RegisterableOp))
	rec, err := recipe.LoadRecipeFromString([]byte("id: root\nop: value_steps\ninputs: {}\n"))
	require.NoError(t, err)
	ctx := &cachedValueStepContext{t: t}
	job, git := GenerateTestContext()
	_, _, err = ExecuteRecipe(newWorkflowContext(ctx), *rec, nil, job, git)
	require.NoError(t, err)
	require.Equal(t, 1, ctx.calls)
}
