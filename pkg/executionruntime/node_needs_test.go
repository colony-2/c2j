package executionruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type nodeTask struct {
	name  string
	runs  atomic.Int32
	next  string
	patch *execution.Requirements
}

func (w *nodeTask) Name() string { return w.name }
func (w *nodeTask) Run(_ jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	n := w.runs.Add(1)
	raw, err := input.GetData()
	if err != nil {
		return nil, err
	}
	var req workerops.ActivityInvocationRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	env, err := task.NewOutputEnvelope(task.OutputKindActivityInvocationOutput, workerops.ActivityInvocationOutput{
		OpOutput: map[string]any{"memory": "16Gi", "count": n}, NextTask: w.next, Execution: w.patch,
		GitResult: contextual.GitCommitContext{ParentRef: req.GitTaskContext.BaseRef},
	})
	if err != nil {
		return nil, err
	}
	return jobdb.NewTaskData(env)
}

func newNodeFixture(t *testing.T, backend, source string) (*fixture, []*nodeTask) {
	t.Helper()
	f := newFixture(t, backend, "2Gi", nil)
	for _, name := range []string{"needs_a", "needs_b", "needs_c"} {
		op, err := coreops.NewOp().WithType(name).AddStep("work", coreops.NewStepWithDeps(func(coreops.OpDependencies, context.Context, struct{}) (map[string]any, error) { return nil, nil })).Build()
		require.NoError(t, err)
		coreops.Register(op.(coreops.RegisterableOp))
	}
	r, err := recipe.LoadRecipeFromString([]byte(source))
	require.NoError(t, err)
	submitNodeRecipe(t, f, *r)
	tasks := []*nodeTask{{name: "needs_a:work"}, {name: "needs_b:work"}, {name: "needs_c:work"}}
	f.tasks = nil
	for _, tw := range tasks {
		f.tasks = append(f.tasks, tw)
	}
	return f, tasks
}

func submitNodeRecipe(t *testing.T, f *fixture, r recipe.Recipe) {
	t.Helper()
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(f.runtime).BuildEngine()
	require.NoError(t, err)
	jobContext, git := compiler.GenerateTestContext()
	f.key, err = starter.StartRecipeJob(context.Background(), workflowctl.StartJob{TenantId: "tenant", RecipeName: r.GetMetadata().ID, JobContext: jobContext, GitRef: git.ParentRef}, jobdbschema.WorkflowEngine{Engine: engine, Registry: f.runtime.(jobdb.JobSchemaRegistry)}, r)
	require.NoError(t, err)
}

func runNode(t *testing.T, f *fixture, alloc execution.Allocation, tasks []jobworkflow.TaskWorker) (jobworkflow.JobRunOutcome, *compiler.ExecutionHandoff) {
	t.Helper()
	var event *compiler.ExecutionHandoff
	callback := func(e compiler.ExecutionHandoff) { event = &e }
	rt := New(f.runtime, alloc, callback)
	worker := compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{Allocation: alloc, StageExecution: rt.Stage, WrapTaskWorker: rt.WrapTaskWorker, OnExecutionHandoff: callback})
	wrapped := make([]jobworkflow.TaskWorker, len(tasks))
	for i, tw := range tasks {
		wrapped[i] = rt.WrapTaskWorker(tw)
	}
	runnable, err := jobworkflow.GetJobForRun(context.Background(), rt, jobworkflow.GetJobForRunRequest{JobKey: f.key, JobWorker: worker, TaskWorkers: wrapped, WorkerID: "needs-test", LeaseDuration: time.Minute})
	require.NoError(t, err)
	out, err := runnable.Run(nil)
	require.NoError(t, err)
	return out, event
}

func nodeAllocation(memory, arch, image string) execution.Allocation {
	a := allocation(memory)
	a.Resources.CPU = str("4")
	a.Platform = str(arch)
	a.Image.Reference = image
	return a
}

const scopedRecipe = `id: scoped
execution:
  resources: {memory: 32Gi}
execution_needs:
  resources: {cpu: 4, memory: 16Gi}
  image: example.com/runner:a
  platform: linux/amd64
sequence:
  - id: first
    op: needs_a
  - id: inner
    execution_needs:
      resources: {memory: 8Gi}
      image: example.com/runner:b
      platform: linux/arm64
    sequence:
      - id: second
        op: needs_b
  - id: last
    op: needs_c
`

func TestNodeNeedsScopeTransitionsAndCachedTaskReplay(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f, tasks := newNodeFixture(t, backend, scopedRecipe)
			listed, err := f.runtime.ListJobs(context.Background(), jobdb.ListJobsRequest{TenantIds: []string{"tenant"}, JobKeys: []jobdb.JobKey{f.key}})
			require.NoError(t, err)
			require.Len(t, listed.Jobs, 1)
			require.Equal(t, "unresolved", execution.Inspect(listed.Jobs[0].Metadata, listed.Jobs[0].ClientPayload).Status)
			a := nodeAllocation("16Gi", "linux/amd64", "example.com/runner:a")
			b := nodeAllocation("8Gi", "linux/arm64", "example.com/runner:b")
			out, event := runNode(t, f, a, f.tasks)
			require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
			require.Equal(t, "8Gi", *event.Demand.Effective.Resources.Memory)
			require.Equal(t, "4", *event.Demand.Effective.Resources.CPU)
			require.EqualValues(t, 1, tasks[0].runs.Load())
			require.Zero(t, tasks[1].runs.Load())
			// An identical insufficient attempt must not rewrite the snapshot.
			_, event = runNode(t, f, a, f.tasks)
			require.False(t, event.Published)
			out, event = runNode(t, f, b, f.tasks)
			require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
			require.Equal(t, "16Gi", *event.Demand.Effective.Resources.Memory)
			require.Equal(t, "linux/amd64", *event.Demand.Effective.Platform)
			require.True(t, event.Demand.JobRequirements.Empty(), "lexical needs must not become job overrides")
			out, event = runNode(t, f, a, f.tasks)
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
			require.Nil(t, event)
			for _, task := range tasks {
				require.EqualValues(t, 1, task.runs.Load())
			}
		})
	}
}

func TestNodeNeedsTemplatesAndOrdinaryTaskHandoff(t *testing.T) {
	f, tasks := newNodeFixture(t, "remote", `id: templated
execution_needs:
  resources: {memory: 2Gi}
sequence:
  - id: probe
    op: needs_a
  - id: process
    execution_needs:
      resources: {memory: '${{ sequence.probe.outputs.memory }}'}
    op: needs_b
`)
	out, event := runNode(t, f, allocation("32Gi"), f.tasks[:1])
	require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
	require.Nil(t, event, "ordinary unsupported-task handoff, not an environment change")
	info, err := f.runtime.GetJob(context.Background(), f.key)
	require.NoError(t, err)
	d, err := execution.PayloadDemand(info.ClientPayload)
	require.NoError(t, err)
	require.Equal(t, "16Gi", *d.Effective.Resources.Memory)
	_, event = runNode(t, f, allocation("2Gi"), f.tasks)
	require.NotNil(t, event)
	require.Zero(t, tasks[1].runs.Load())
	out, event = runNode(t, f, allocation("16Gi"), f.tasks)
	require.Nil(t, event)
	// A task-only lease can first return the job to recipe work.
	if out.Status != jobworkflow.JobRunCompleted {
		out, event = runNode(t, f, allocation("16Gi"), f.tasks)
	}
	require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
	require.Nil(t, event)
	require.EqualValues(t, 1, tasks[0].runs.Load())
	require.EqualValues(t, 1, tasks[1].runs.Load())
}

func TestNodeNeedsOversizedAllocationDoesNotYieldOrPublish(t *testing.T) {
	f, tasks := newNodeFixture(t, "sqlite", `id: oversized
execution_needs: {resources: {memory: 16Gi}}
sequence:
  - id: smaller
    execution_needs: {resources: {memory: 8Gi}}
    op: needs_a
  - id: restored
    op: needs_b
`)
	out, event := runNode(t, f, allocation("32Gi"), f.tasks)
	require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
	require.Nil(t, event)
	require.EqualValues(t, 1, tasks[0].runs.Load())
	require.EqualValues(t, 1, tasks[1].runs.Load())
	info, err := f.runtime.GetJob(context.Background(), f.key)
	require.NoError(t, err)
	require.Empty(t, info.ClientPayload, "live scope changes require no scheduler writes")
}

func TestNodeNeedsRecoveryIgnoresStalePublishedScope(t *testing.T) {
	f, tasks := newNodeFixture(t, "sqlite", scopedRecipe)
	a := nodeAllocation("16Gi", "linux/amd64", "example.com/runner:a")
	b := nodeAllocation("8Gi", "linux/arm64", "example.com/runner:b")
	_, _ = runNode(t, f, a, f.tasks[:1])
	old, err := f.runtime.GetJob(context.Background(), f.key)
	require.NoError(t, err)
	_, _ = runNode(t, f, b, f.tasks[:2])
	// Model recovery with a saved B snapshot and durable B result, but no newer
	// published scope. Scheduler data is deliberately not a live progress mirror.
	l, err := f.runtime.GetJobLease(context.Background(), jobdb.GetJobLeaseRequest{JobKey: f.key, Routes: []jobdb.Route{{JobType: "recipe", TaskType: "needs_c:work"}}, WorkerID: "recovery-fixture", LeaseDuration: time.Minute})
	require.NoError(t, err)
	require.NotNil(t, l)
	revision := l.ClientPayloadRevision()
	require.NoError(t, l.Reschedule(context.Background(), jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "recipe", TaskType: "needs_b:work"}, TaskWait: old.ExecutionState.TaskWait, ClientPayloadUpdate: &jobdb.ClientPayloadUpdate{Mode: "reset", Value: old.ClientPayload, ExpectedRevision: &revision}}))
	out, event := runNode(t, f, a, f.tasks)
	require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
	require.Nil(t, event, "completed B must not demand arm64 during replay")
	for _, task := range tasks {
		require.EqualValues(t, 1, task.runs.Load())
	}
}

func TestNodeNeedsRepeatedStateOccurrences(t *testing.T) {
	f, tasks := newNodeFixture(t, "toy", `id: loop
execution_needs: {resources: {memory: 1Gi}}
state:
  initial: {to: work, payload: {memory: 2Gi}}
  states:
    work:
      execution_needs:
        resources: {memory: '${{ transition.payload.memory }}'}
      op: needs_a
      transitions:
        - to: work
          when: outputs.count < 2
          payload: {memory: 16Gi}
        - to: done
    done:
      sequence: []
    unreachable:
      execution_needs: {resources: {memory: 1Ti}}
      op: needs_b
`)
	out, event := runNode(t, f, allocation("2Gi"), f.tasks)
	require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
	require.Equal(t, "16Gi", *event.Demand.Effective.Resources.Memory)
	require.EqualValues(t, 1, tasks[0].runs.Load())
	out, event = runNode(t, f, allocation("16Gi"), f.tasks)
	require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
	require.Nil(t, event)
	require.EqualValues(t, 2, tasks[0].runs.Load())
	require.Zero(t, tasks[1].runs.Load())
}

func TestNodeNeedsInlineScopeRestoresParent(t *testing.T) {
	f, tasks := newNodeFixture(t, "toy", "id: seed\nsequence: []\n")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "child.yaml"), []byte("id: child\nexecution_needs: {resources: {memory: 16Gi}}\nsequence:\n  - id: inside\n    op: needs_a\n"), 0600))
	r, err := recipe.LoadRecipeFromString([]byte("id: parent\nexecution_needs: {resources: {memory: 2Gi}}\nsequence:\n  - id: inline\n    include: ./child.yaml\n  - id: sibling\n    op: needs_b\n"))
	require.NoError(t, err)
	expanded, err := compiler.ResolveInlineRecipes(context.Background(), *r, compiler.InlineResolutionOptions{RootFile: filepath.Join(dir, "root.yaml")})
	require.NoError(t, err)
	submitNodeRecipe(t, f, expanded.Recipe)
	out, event := runNode(t, f, allocation("2Gi"), f.tasks)
	require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
	require.Equal(t, "16Gi", *event.Demand.Effective.Resources.Memory)
	// Missing sibling worker causes an ordinary handoff showing restored 2Gi.
	out, event = runNode(t, f, allocation("16Gi"), f.tasks[:1])
	require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
	require.Nil(t, event)
	info, err := f.runtime.GetJob(context.Background(), f.key)
	require.NoError(t, err)
	d, err := execution.PayloadDemand(info.ClientPayload)
	require.NoError(t, err)
	require.Equal(t, "2Gi", *d.Effective.Resources.Memory)
	out, event = runNode(t, f, allocation("2Gi"), f.tasks)
	require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
	require.Nil(t, event)
	require.EqualValues(t, 1, tasks[0].runs.Load())
	require.EqualValues(t, 1, tasks[1].runs.Load())
}

func TestNodeNeedsPartialOperationReplaysCompletedStep(t *testing.T) {
	f, _ := newNodeFixture(t, "toy", "id: seed\nsequence: []\n")
	step := coreops.NewStepWithDeps(func(coreops.OpDependencies, context.Context, struct{}) (map[string]any, error) { return nil, nil })
	secondStep := coreops.NewStepWithDeps(func(coreops.OpDependencies, context.Context, map[string]any) (map[string]any, error) { return nil, nil })
	op, err := coreops.NewOp().WithType("needs_multi").AddStep("first", step).AddStep("second", secondStep).Build()
	require.NoError(t, err)
	coreops.Register(op.(coreops.RegisterableOp))
	r, err := recipe.LoadRecipeFromString([]byte("id: partial\nexecution_needs: {resources: {memory: 2Gi}}\nop: needs_multi\n"))
	require.NoError(t, err)
	submitNodeRecipe(t, f, *r)
	first := &nodeTask{name: "needs_multi:first", next: "needs_multi:second", patch: &execution.Requirements{Resources: execution.Resources{Memory: str("16Gi")}}}
	second := &nodeTask{name: "needs_multi:second"}
	f.tasks = []jobworkflow.TaskWorker{first, second}
	out, event := runNode(t, f, allocation("2Gi"), f.tasks)
	require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
	require.NotNil(t, event)
	require.EqualValues(t, 1, first.runs.Load())
	require.Zero(t, second.runs.Load())
	out, event = runNode(t, f, allocation("16Gi"), f.tasks)
	require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
	require.Nil(t, event)
	require.EqualValues(t, 1, first.runs.Load())
	require.EqualValues(t, 1, second.runs.Load())
}

func TestNodeNeedsUnknownAllocationAndLostHandoffResponse(t *testing.T) {
	f, tasks := newNodeFixture(t, "toy", `id: unknown
execution_needs: {resources: {memory: 16Gi}}
op: needs_a
`)
	out, event := runNode(t, f, execution.Allocation{SchemaVersion: 1}, f.tasks)
	require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
	require.NotEmpty(t, event.Mismatches)
	require.Zero(t, tasks[0].runs.Load())
	// This second, incompatible invocation commits a reschedule but loses the
	// response. It must not invoke the task or claim an acknowledged handoff.
	base := f.runtime
	rt := New(lostResponseRuntime{base}, allocation("2Gi"), func(compiler.ExecutionHandoff) { t.Error("uncertain yield reported success") })
	worker := compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{Allocation: allocation("2Gi"), StageExecution: rt.Stage, WrapTaskWorker: rt.WrapTaskWorker})
	runnable, err := jobworkflow.GetJobForRun(context.Background(), rt, jobworkflow.GetJobForRunRequest{JobKey: f.key, JobWorker: worker, TaskWorkers: []jobworkflow.TaskWorker{rt.WrapTaskWorker(tasks[0])}, WorkerID: "lost-response", LeaseDuration: time.Minute})
	require.NoError(t, err)
	_, _ = runnable.Run(nil)
	require.Zero(t, tasks[0].runs.Load())
	out, event = runNode(t, f, allocation("16Gi"), f.tasks)
	require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
	require.Nil(t, event)
	require.EqualValues(t, 1, tasks[0].runs.Load())
}
