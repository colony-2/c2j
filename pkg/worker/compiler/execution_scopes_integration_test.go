package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/swfutil"
	coretask "github.com/colony-2/c2j/pkg/task"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type scopeProbeTask struct {
	mu       sync.Mutex
	calls    map[string]int
	failOnce string
	git      contextual.GitCommitContext
}

func (*scopeProbeTask) Name() string { return "scope_probe:run" }
func (p *scopeProbeTask) Run(ctx jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	raw, err := input.GetData()
	if err != nil {
		return nil, err
	}
	var request workerops.ActivityInvocationRequest
	if err = json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	name, _ := request.Input["name"].(string)
	p.mu.Lock()
	p.calls[name]++
	fail := p.failOnce != "" && name == p.failOnce && p.calls[name] == 1
	p.mu.Unlock()
	if fail {
		return nil, appFailure("probe failed", "probe")
	}
	if err = ctx.Context().Err(); err != nil {
		return nil, err
	}
	return scopeProbeOutput(request.Input, p.git)
}
func scopeProbeOutput(input map[string]any, git contextual.GitCommitContext) (jobdb.TaskData, error) {
	env, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, workerops.ActivityInvocationOutput{OpOutput: input, GitResult: git})
	if err != nil {
		return nil, err
	}
	return jobdb.NewTaskData(env)
}
func probeNode(id string) recipe.Node {
	return recipe.Node{NodeImpl: &recipe.NodeOp{NodeMetadata: recipe.NodeMetadata{ID: id, Inputs: map[string]any{"name": id}}, OpData: recipe.OpData{Op: "scope_probe"}}}
}
func probeRecipe(nodes ...recipe.Node) recipe.Recipe {
	return recipe.Recipe{RecipeImpl: &recipe.RecipeSequence{RecipeMetadata: recipe.RecipeMetadata{NodeMetadata: recipe.NodeMetadata{ID: "root", Inputs: map[string]any{}}}, SequenceData: recipe.SequenceData{Sequence: nodes, Outputs: map[string]any{"ok": true}}}}
}
func probeWorker(t *testing.T, rec recipe.Recipe) jobworkflow.ExecutionWorker {
	t.Helper()
	job, git := GenerateTestContext()
	return jobworkflow.ExecutionWorker{JobType: "scope-probe", Execute: func(exec jobworkflow.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
		out, artifacts, err := ExecuteRecipe(catchWorkflowContext(exec), rec, map[string]any{}, job, git)
		if err != nil {
			return nil, err
		}
		return jobdb.NewTaskData(out, artifacts...)
	}}
}
func probeRun(t *testing.T, rt jobdb.WorkflowRuntime, key jobdb.JobKey, w jobworkflow.ExecutionWorker, tasks ...jobworkflow.TaskWorker) jobworkflow.JobRunOutcome {
	t.Helper()
	h, err := jobworkflow.GetJobForRun(context.Background(), rt, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: w, TaskWorkers: tasks, WorkerID: "scope-test", LeaseDuration: time.Minute})
	require.NoError(t, err)
	out, err := h.Run(nil)
	require.NoError(t, err)
	return out
}
func probeSubmit(t *testing.T, rt jobdb.WorkflowRuntime, w jobworkflow.ExecutionWorker) jobdb.JobKey {
	t.Helper()
	h, err := rt.SubmitJob(context.Background(), jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "scope-tests", JobType: w.Name(), Data: jobdb.NewTaskDataOrPanic(nil), RunPolicy: jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 1}}}})
	require.NoError(t, err)
	return h.JobKey
}
func probeReplay(t *testing.T, rt jobdb.WorkflowRuntime, key jobdb.JobKey, w jobworkflow.ExecutionWorker) error {
	t.Helper()
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(key.TenantId).PlusWorkers(w).BuildEngine()
	require.NoError(t, err)
	_, err = engine.ReplayJobRun(context.Background(), jobworkflow.ReplayRunRequest{JobKey: key})
	return err
}
func TestExecutionScopesRetryNestedBlockAndReplay(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			rt := newTimeoutReproRuntime(t, backend)
			_, git := GenerateTestContext()
			task := &scopeProbeTask{calls: map[string]int{}, failOnce: "b", git: git}
			block := recipe.Node{NodeImpl: &recipe.NodeSequence{NodeMetadata: recipe.NodeMetadata{ID: "block", Retry: &recipe.RetryPolicy{MaximumAttempts: 2}, Inputs: map[string]any{}}, SequenceData: recipe.SequenceData{Sequence: []recipe.Node{probeNode("a"), probeNode("b")}, Outputs: map[string]any{"name": "${{ sequence.b.outputs.name }}"}}}}
			w := probeWorker(t, probeRecipe(probeNode("prefix"), block, probeNode("suffix")))
			key := probeSubmit(t, rt, w)
			outcome := probeRun(t, rt, key, w, task)
			require.Equal(t, jobworkflow.JobRunCompleted, outcome.Status, "%v", outcome.JobError)
			require.Equal(t, map[string]int{"prefix": 1, "a": 2, "b": 2, "suffix": 1}, task.calls)
			before, err := rt.ListChapters(context.Background(), jobdb.ListChaptersRequest{JobKey: key})
			require.NoError(t, err)
			require.NoError(t, probeReplay(t, rt, key, w))
			after, err := rt.ListChapters(context.Background(), jobdb.ListChaptersRequest{JobKey: key})
			require.NoError(t, err)
			require.Equal(t, before, after, "read-only replay must not append history")
			require.Equal(t, map[string]int{"prefix": 1, "a": 2, "b": 2, "suffix": 1}, task.calls)
			run, err := jobdb.GetJobRun(context.Background(), rt, jobdb.GetJobRunRequest{JobKey: key})
			require.NoError(t, err)
			var aInvocations []string
			for _, event := range run.Execution {
				if event.Record.Kind == "task" && strings.Contains(event.Record.ID, `scope:"op:a"`) {
					aInvocations = append(aInvocations, event.Record.ID)
				}
			}
			require.Len(t, aInvocations, 2)
			require.NotEqual(t, aInvocations[0], aInvocations[1])
		})
	}
}
func TestExecutionScopesCompletedTaskSkipsChangedInputBuilder(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	rt := newTimeoutReproRuntime(t, "sqlite")
	_, git := GenerateTestContext()
	task := &scopeProbeTask{calls: map[string]int{}, git: git}
	rec := probeRecipe(probeNode("a"))
	w := probeWorker(t, rec)
	key := probeSubmit(t, rt, w)
	require.Equal(t, jobworkflow.JobRunCompleted, probeRun(t, rt, key, w, task).Status)
	// The invocation declaration stays identical. A new worker has a stricter
	// input schema; completed work must never run its builder/validation again.
	type newInput struct {
		Required string `json:"required" validate:"required"`
	}
	op, err := coreops.NewOp().WithType("scope_probe").AddStep("run", coreops.NewStepWithDeps(func(coreops.OpDependencies, context.Context, newInput) (map[string]any, error) {
		return nil, fmt.Errorf("must not execute")
	})).Build()
	require.NoError(t, err)
	withRegisteredOps(t, op.(coreops.RegisterableOp))
	require.NoError(t, probeReplay(t, rt, key, w))
	require.Equal(t, 1, task.calls["a"])
	changed := probeRecipe(probeNode("different"))
	require.ErrorIs(t, probeReplay(t, rt, key, probeWorker(t, changed)), jobdb.ErrWorkflowNotDeterministic)
}

func TestExecutionScopesRestartNestedBlockKeepsPrefix(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			rt := newTimeoutReproRuntime(t, backend)
			_, git := GenerateTestContext()
			task := &scopeProbeTask{calls: map[string]int{}, git: git}
			rec, err := recipe.LoadRecipeFromString([]byte(`id: root
sequence:
- id: prefix
  op: scope_probe
  inputs: {name: prefix}
- id: block
  sequence:
  - id: a
    op: scope_probe
    inputs: {name: "${{ context.git.author }}"}
  - id: b
    op: scope_probe
    inputs: {name: b}
- id: suffix
  op: scope_probe
  inputs: {name: suffix}
`))
			require.NoError(t, err)
			w := probeWorker(t, *rec)
			key := probeSubmit(t, rt, w)
			first := probeRun(t, rt, key, w, task)
			require.Equal(t, jobworkflow.JobRunCompleted, first.Status, "%v", first.JobError)
			run, err := jobdb.GetJobRun(context.Background(), rt, jobdb.GetJobRunRequest{JobKey: key})
			require.NoError(t, err)
			var scope string
			for _, e := range run.Execution {
				if e.Record.Kind == "scope" && strings.HasSuffix(e.Record.ID, `scope:"sequence:block"#1`) {
					scope = e.Record.ID
					break
				}
			}
			require.NotEmpty(t, scope)
			payload := json.RawMessage(`{"job":{"git":{"author":"patched"}}}`)
			restarted, err := rt.SubmitRestartJob(context.Background(), jobdb.SubmitRestartJobRequest{Job: jobdb.SubmitRestartJob{PriorJobKey: key, ExecutionRestart: &jobdb.ExecutionRestart{ScopeID: scope, Payload: payload}}})
			require.NoError(t, err)
			outcome := probeRun(t, rt, restarted.JobKey, w, task)
			require.Equal(t, jobworkflow.JobRunCompleted, outcome.Status, "%v", outcome.JobError)
			require.Equal(t, 1, task.calls["prefix"], "prefix must replay")
			require.Equal(t, 2, task.calls["b"])
			require.Equal(t, 2, task.calls["suffix"])
			require.Equal(t, 1, task.calls["patched"], "restart patch must apply before block inputs")
			require.NoError(t, probeReplay(t, rt, restarted.JobKey, w))
			require.Equal(t, 1, task.calls["patched"])
		})
	}
}

func TestExecutionScopesBlockCatchSeesCompletedChildren(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			rt := newTimeoutReproRuntime(t, backend)
			_, git := GenerateTestContext()
			task := &scopeProbeTask{calls: map[string]int{}, git: git, failOnce: "b"}
			rec, err := recipe.LoadRecipeFromString([]byte(`id: root
sequence:
- id: block
  sequence:
  - id: a
    op: scope_probe
    inputs: {name: a}
  - id: b
    op: scope_probe
    inputs: {name: b}
  - id: skipped
    op: scope_probe
    inputs: {name: skipped}
  catch:
  - when: "true"
    continue:
      outputs:
        name: "${{ sequence.a.outputs.name }}"
- id: suffix
  op: scope_probe
  inputs: {name: "${{ sequence.block.outputs.name }}"}
`))
			require.NoError(t, err)
			w := probeWorker(t, *rec)
			key := probeSubmit(t, rt, w)
			outcome := probeRun(t, rt, key, w, task)
			require.Equal(t, jobworkflow.JobRunCompleted, outcome.Status, "%v", outcome.JobError)
			require.Equal(t, map[string]int{"a": 2, "b": 1}, task.calls)
			require.NoError(t, probeReplay(t, rt, key, w))
			require.Equal(t, map[string]int{"a": 2, "b": 1}, task.calls)
		})
	}
}

func TestExecutionScopesResumeNestedBlock(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	op, err := coreops.NewOp().WithType("scope_wait").AddStep("run", coreops.NewNoTaskStep[map[string]any, map[string]any]()).Build()
	require.NoError(t, err)
	coreops.Register(op.(coreops.RegisterableOp))
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			rt := newTimeoutReproRuntime(t, backend)
			_, git := GenerateTestContext()
			task := &scopeProbeTask{calls: map[string]int{}, git: git}
			rec, err := recipe.LoadRecipeFromString([]byte(`id: root
sequence:
- id: prefix
  op: scope_probe
  inputs: {name: prefix}
- id: block
  sequence:
  - id: a
    op: scope_probe
    inputs: {name: a}
  - id: wait
    op: scope_wait
    inputs: {name: wait}
  - id: b
    op: scope_probe
    inputs: {name: b}
- id: suffix
  op: scope_probe
  inputs: {name: suffix}
`))
			require.NoError(t, err)
			w := probeWorker(t, *rec)
			key := probeSubmit(t, rt, w)
			require.Equal(t, jobworkflow.JobRunSuspended, probeRun(t, rt, key, w, task).Status)
			require.Equal(t, map[string]int{"prefix": 1, "a": 1}, task.calls)
			engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
			require.NoError(t, err)
			pending, err := engine.GetWaitingTask(context.Background(), key)
			require.NoError(t, err)
			data, err := pending.Data()
			require.NoError(t, err)
			raw, err := data.GetData()
			require.NoError(t, err)
			var invocation workerops.ActivityInvocationRequest
			require.NoError(t, json.Unmarshal(raw, &invocation))
			require.Equal(t, "wait", invocation.Input["name"])
			output, err := scopeProbeOutput(invocation.Input, git)
			require.NoError(t, err)
			require.NoError(t, pending.Finish(context.Background(), output))
			// A new worker resumes the inner block, using the recorded prefix outcomes.
			outcome := probeRun(t, rt, key, probeWorker(t, *rec), task)
			require.Equal(t, jobworkflow.JobRunCompleted, outcome.Status, "%v", outcome.JobError)
			require.Equal(t, map[string]int{"prefix": 1, "a": 1, "b": 1, "suffix": 1}, task.calls)
			require.NoError(t, probeReplay(t, rt, key, w))
		})
	}
}

type scopeCancelTask struct{ done chan struct{} }

func (scopeCancelTask) Name() string { return "scope_probe:run" }
func (w scopeCancelTask) Run(ctx jobworkflow.TaskContext, _ jobdb.TaskData) (jobdb.TaskData, error) {
	<-ctx.Context().Done()
	close(w.done)
	return nil, context.Cause(ctx.Context())
}
func TestExecutionScopesBlockBudgetCancelsWorker(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	synctest.Test(t, func(t *testing.T) {
		rt := toy.New()
		rec := probeRecipe(probeNode("blocked"), probeNode("unreachable"))
		// GetMetdata returns a value; set the concrete scope policy explicitly.
		rec.RecipeImpl.(*recipe.RecipeSequence).Timeout = recipe.Duration(time.Minute)
		w := probeWorker(t, rec)
		key := probeSubmit(t, rt, w)
		done := make(chan struct{})
		out := probeRun(t, rt, key, w, scopeCancelTask{done})
		require.Equal(t, jobworkflow.JobRunFailed, out.Status)
		require.ErrorContains(t, out.JobError, "timed out")
		<-done
		require.Error(t, probeReplay(t, rt, key, w), "recorded failure remains a failure on replay")
		run, err := jobdb.GetJobRun(context.Background(), rt, jobdb.GetJobRunRequest{JobKey: key})
		require.NoError(t, err)
		tasks, err := swfutil.ExecutionTasks(run)
		require.NoError(t, err)
		require.Len(t, tasks, 1, "expired blocks must not start the next child")
	})
}

func TestExecutionScopesStepRetryTimeoutDoesNotRestartParent(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	coreops.Replace(coreops.NewOp().WithType("scope_probe").WithStepRetries(true).
		AddStep("run", coreops.NewStepWithDeps(func(_ coreops.OpDependencies, _ context.Context, input map[string]any) (map[string]any, error) {
			return input, nil
		})).BuildOrPanic())
	synctest.Test(t, func(t *testing.T) {
		rt := toy.New()
		node := probeNode("blocked")
		node.NodeImpl.(*recipe.NodeOp).Timeout = recipe.Duration(time.Minute)
		rec := probeRecipe(node)
		rec.RecipeImpl.(*recipe.RecipeSequence).Retry = &recipe.RetryPolicy{MaximumAttempts: 3}
		w := probeWorker(t, rec)
		key := probeSubmit(t, rt, w)
		done := make(chan struct{})
		out := probeRun(t, rt, key, w, scopeCancelTask{done})
		require.Equal(t, jobworkflow.JobRunFailed, out.Status)
		require.ErrorContains(t, out.JobError, "timed out")
		<-done
		require.Error(t, probeReplay(t, rt, key, w))
		run, err := jobdb.GetJobRun(context.Background(), rt, jobdb.GetJobRunRequest{JobKey: key})
		require.NoError(t, err)
		tasks, err := swfutil.ExecutionTasks(run)
		require.NoError(t, err)
		require.Len(t, tasks, 1, "an op timeout must not restart completed publication steps")
	})
}

func TestExecutionScopesExhaustedStepTimeoutDoesNotRestartParent(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	coreops.Replace(coreops.NewOp().WithType("scope_probe").WithStepRetries(true).
		AddStep("run", coreops.NewStepWithDeps(func(_ coreops.OpDependencies, _ context.Context, input map[string]any) (map[string]any, error) {
			return input, nil
		})).BuildOrPanic())
	synctest.Test(t, func(t *testing.T) {
		rt := toy.New()
		node := probeNode("blocked")
		node.NodeImpl.(*recipe.NodeOp).Retry = &recipe.RetryPolicy{MaximumAttempts: 2}
		rec := probeRecipe(node)
		rec.RecipeImpl.(*recipe.RecipeSequence).Retry = &recipe.RetryPolicy{MaximumAttempts: 3}
		_, git := GenerateTestContext()
		probe := &scopeProbeTask{calls: map[string]int{}, git: git}
		task := publicationFaultWorker{TaskWorker: probe, after: func(jobdb.TaskData) error {
			return jobdb.NewTimeoutError("task", time.Minute, jobdb.TimeoutScopeInvocation, nil, true)
		}}
		w := probeWorker(t, rec)
		key := probeSubmit(t, rt, w)
		out := probeRun(t, rt, key, w, task)
		require.Equal(t, jobworkflow.JobRunFailed, out.Status)
		require.ErrorContains(t, out.JobError, "timed out")
		require.Equal(t, 2, probe.calls["blocked"], "only the task's own retry policy may repeat the step")
		require.Error(t, probeReplay(t, rt, key, w))
	})
}

type interruptScopeRuntime struct {
	jobdb.WorkflowRuntime
	lease jobdb.ExecutionLease
	kind  string
	fired bool
}

func (r *interruptScopeRuntime) GetJobLease(ctx context.Context, req jobdb.GetJobLeaseRequest) (jobdb.ExecutionLease, error) {
	lease, err := r.WorkflowRuntime.GetJobLease(ctx, req)
	r.lease = lease
	return lease, err
}
func (r *interruptScopeRuntime) PutChapter(ctx context.Context, req jobdb.PutChapterRequest) error {
	if r.fired {
		return r.WorkflowRuntime.PutChapter(ctx, req)
	}
	matches := func(record jobdb.ExecutionRecord) bool {
		return record.Kind == r.kind && strings.Contains(record.ID, `scope:"sequence:block"`) && (r.kind != "decision" || record.Retry)
	}
	interrupt := func() error {
		r.fired = true
		if err := r.lease.Reschedule(ctx, jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "scope-probe"}}); err != nil {
			return err
		}
		return fmt.Errorf("injected interruption at %s", r.kind)
	}
	if body, ok := req.Chapter.Body.(jobdb.ExecutionChapter); ok {
		record, err := jobdb.DecodeExecutionRecord(body.Record)
		if err != nil {
			return err
		}
		if matches(record) {
			return interrupt()
		}
	}
	// JobDB v0.0.49 compacts catch/handled records into the following durable
	// outcome. Interrupt after committing that checkpoint: the buffered event
	// by itself is no longer a durable recovery boundary.
	if raw := req.Chapter.Metadata.Fields["metadata"].Map["__execution_prefix_v3"].String; raw != "" {
		var records []json.RawMessage
		if err := json.Unmarshal([]byte(raw), &records); err != nil {
			return err
		}
		for _, raw := range records {
			record, err := jobdb.DecodeExecutionRecord(raw)
			if err != nil {
				return err
			}
			if matches(record) {
				if err := r.WorkflowRuntime.PutChapter(ctx, req); err != nil {
					return err
				}
				return interrupt()
			}
		}
	}
	return r.WorkflowRuntime.PutChapter(ctx, req)
}

func TestExecutionScopesRecoverCatchAndRetryDecision(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, backend := range []string{"sqlite", "remote"} {
		for _, kind := range []string{"catch", "handled", "decision"} {
			t.Run(backend+"/"+kind, func(t *testing.T) {
				base := newTimeoutReproRuntime(t, backend)
				rt := &interruptScopeRuntime{WorkflowRuntime: base, kind: kind}
				_, git := GenerateTestContext()
				task := &scopeProbeTask{calls: map[string]int{}, git: git, failOnce: "b"}
				clause := "  catch:\n  - when: 'true'\n    continue: {outputs: {name: recovered}}\n"
				if kind == "decision" {
					clause = "  retry: {maximum_attempts: 2}\n"
				}
				rec, err := recipe.LoadRecipeFromString([]byte(`id: root
sequence:
- id: prefix
  op: scope_probe
  inputs: {name: prefix}
- id: block
  sequence:
  - id: a
    op: scope_probe
    inputs: {name: a}
  - id: b
    op: scope_probe
    inputs: {name: b}
` + clause + `- id: suffix
  op: scope_probe
  inputs: {name: suffix}
`))
				require.NoError(t, err)
				w := probeWorker(t, *rec)
				key := probeSubmit(t, rt, w)
				h, err := jobworkflow.GetJobForRun(context.Background(), rt, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: w, TaskWorkers: []jobworkflow.TaskWorker{task}, WorkerID: "interrupted", LeaseDuration: time.Minute})
				require.NoError(t, err)
				_, err = h.Run(nil)
				require.Error(t, err)
				require.True(t, rt.fired)
				out := probeRun(t, base, key, probeWorker(t, *rec), task)
				require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
				require.Equal(t, 1, task.calls["prefix"])
				require.Equal(t, 1, task.calls["suffix"])
				if kind == "decision" {
					require.Equal(t, 2, task.calls["a"])
					require.Equal(t, 2, task.calls["b"])
				} else {
					require.Equal(t, 1, task.calls["a"])
					require.Equal(t, 1, task.calls["b"])
				}
				require.NoError(t, probeReplay(t, base, key, w))
			})
		}
	}
}
