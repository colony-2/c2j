package executionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
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
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func str(v string) *string { return &v }
func allocation(memory string) execution.Allocation {
	return execution.Allocation{SchemaVersion: 1, Resources: execution.Resources{Memory: str(memory)}}
}

type resultTask struct {
	name  string
	runs  *atomic.Int32
	patch *execution.Requirements
}

func (w resultTask) Name() string { return w.name }
func (w resultTask) Run(ctx jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	w.runs.Add(1)
	raw, err := input.GetData()
	if err != nil {
		return nil, err
	}
	var req workerops.ActivityInvocationRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	out := workerops.ActivityInvocationOutput{OpOutput: map[string]any{"manifest": true}, Execution: w.patch, GitResult: contextual.GitCommitContext{ParentRef: req.GitTaskContext.BaseRef}}
	env, err := task.NewOutputEnvelope(task.OutputKindActivityInvocationOutput, out)
	if err != nil {
		return nil, err
	}
	return jobdb.NewTaskData(env, jobdb.NewArtifactFromBytes("manifest.txt", []byte("durable")))
}

type fixture struct {
	runtime       jobdb.WorkflowRuntime
	key           jobdb.JobKey
	first, second atomic.Int32
	tasks         []jobworkflow.TaskWorker
}

func newFixture(t *testing.T, backend, memory string, patch *execution.Requirements) *fixture {
	t.Helper()
	ctx := context.Background()
	var rt jobdb.WorkflowRuntime = toy.New()
	if backend == "sqlite" || backend == "remote" {
		s, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, s.Close(ctx)) })
		rt = s
	}
	if backend == "remote" {
		server := httptest.NewServer(remote.NewServer(rt))
		t.Cleanup(server.Close)
		r, err := remote.New(server.URL, server.Client())
		require.NoError(t, err)
		rt = r
	}
	for _, name := range []string{"execution_discover", "execution_finish"} {
		op, err := coreops.NewOp().WithType(name).AddStep("work", coreops.NewStepWithDeps(func(coreops.OpDependencies, context.Context, struct{}) (map[string]any, error) { return nil, nil })).Build()
		require.NoError(t, err)
		coreops.Register(op.(coreops.RegisterableOp))
	}
	r, err := recipe.LoadRecipeFromString([]byte("id: resources\nexecution:\n  resources:\n    memory: " + memory + "\nsequence:\n  - id: discover\n    op: execution_discover\n  - id: finish\n    op: execution_finish\n"))
	require.NoError(t, err)
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	registry := rt.(jobdb.JobSchemaRegistry)
	jobContext, git := compiler.GenerateTestContext()
	key, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "tenant", RecipeName: "resources", JobContext: jobContext, GitRef: git.ParentRef}, jobdbschema.WorkflowEngine{Engine: engine, Registry: registry}, *r)
	require.NoError(t, err)
	f := &fixture{runtime: rt, key: key}
	f.tasks = []jobworkflow.TaskWorker{resultTask{"execution_discover:work", &f.first, patch}, resultTask{"execution_finish:work", &f.second, nil}}
	return f
}

func (f *fixture) run(t *testing.T, memory string, tasks []jobworkflow.TaskWorker) (jobworkflow.JobRunOutcome, *compiler.ExecutionHandoff) {
	t.Helper()
	var event *compiler.ExecutionHandoff
	callback := func(e compiler.ExecutionHandoff) { event = &e }
	rt := New(f.runtime, allocation(memory), callback)
	worker := compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{Allocation: allocation(memory), StageExecution: rt.Stage, OnExecutionHandoff: callback})
	runnable, err := jobworkflow.GetJobForRun(context.Background(), rt, jobworkflow.GetJobForRunRequest{JobKey: f.key, JobWorker: worker, TaskWorkers: tasks, WorkerID: "test", LeaseDuration: time.Minute})
	require.NoError(t, err)
	out, err := runnable.Run(nil)
	require.NoError(t, err)
	return out, event
}

func TestExecutionHandoffResumesDurableActivityAcrossRuntimes(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			patch := execution.Requirements{Resources: execution.Resources{Memory: str("16Gi")}}
			f := newFixture(t, backend, "2Gi", &patch)
			out, event := f.run(t, "2Gi", f.tasks)
			require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
			require.NotNil(t, event)
			require.True(t, event.Published)
			require.EqualValues(t, 1, f.first.Load())
			require.Zero(t, f.second.Load())
			info, err := f.runtime.GetJob(context.Background(), f.key)
			require.NoError(t, err)
			d, err := execution.PayloadDemand(info.ClientPayload)
			require.NoError(t, err)
			require.Equal(t, "16Gi", *d.Effective.Resources.Memory)
			_, event = f.run(t, "2Gi", f.tasks)
			require.NotNil(t, event)
			require.False(t, event.Published)
			after, err := f.runtime.GetJob(context.Background(), f.key)
			require.NoError(t, err)
			require.Equal(t, info.ClientPayloadRevision, after.ClientPayloadRevision)
			out, event = f.run(t, "16Gi", f.tasks)
			require.Nil(t, event)
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
			require.EqualValues(t, 1, f.first.Load())
			require.EqualValues(t, 1, f.second.Load())
		})
	}
}

func TestInitialPreflightAndOrdinaryTaskHandoff(t *testing.T) {
	f := newFixture(t, "sqlite", "16Gi", nil)
	out, event := f.run(t, "2Gi", f.tasks)
	require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
	require.NotNil(t, event)
	require.Zero(t, f.first.Load())
	out, event = f.run(t, "16Gi", f.tasks)
	require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
	require.Nil(t, event)

	// A sufficient executor publishes its resolved demand on an ordinary task
	// handoff, so the task's next executor is checked too.
	f = newFixture(t, "remote", "16Gi", nil)
	out, event = f.run(t, "16Gi", nil)
	require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
	require.Nil(t, event)
	info, err := f.runtime.GetJob(context.Background(), f.key)
	require.NoError(t, err)
	require.NotEmpty(t, info.ClientPayload)
	_, event = f.run(t, "2Gi", f.tasks)
	require.NotNil(t, event)
	require.Zero(t, f.first.Load())
	out, event = f.run(t, "16Gi", f.tasks)
	require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
	require.Nil(t, event)
}

func TestExplicitChangeYieldsEvenWhenAlreadySufficient(t *testing.T) {
	f := newFixture(t, "toy", "2Gi", &execution.Requirements{Resources: execution.Resources{Memory: str("16Gi")}})
	out, event := f.run(t, "32Gi", f.tasks)
	require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
	require.NotNil(t, event)
	require.Empty(t, event.Mismatches)
	out, event = f.run(t, "32Gi", f.tasks)
	require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
	require.Nil(t, event)
	require.EqualValues(t, 1, f.first.Load())
}

func TestExecutionReplayDoesNotRestoreEarlierHigherRequirements(t *testing.T) {
	f := newFixture(t, "sqlite", "2Gi", &execution.Requirements{Resources: execution.Resources{Memory: str("16Gi")}})
	f.tasks[1] = resultTask{"execution_finish:work", &f.second, &execution.Requirements{Resources: execution.Resources{Memory: str("4Gi")}}}
	out, event := f.run(t, "2Gi", f.tasks)
	require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
	require.Equal(t, "16Gi", *event.Demand.Effective.Resources.Memory)
	out, event = f.run(t, "16Gi", f.tasks)
	require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
	require.Equal(t, "4Gi", *event.Demand.Effective.Resources.Memory)
	require.EqualValues(t, 2, event.Demand.Revision)
	out, event = f.run(t, "4Gi", f.tasks)
	require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
	require.Nil(t, event)
	require.EqualValues(t, 1, f.first.Load())
	require.EqualValues(t, 1, f.second.Load())
}

type lostResponseRuntime struct{ jobdb.WorkflowRuntime }

func (r lostResponseRuntime) GetJobLease(ctx context.Context, req jobdb.GetJobLeaseRequest) (jobdb.ExecutionLease, error) {
	l, err := r.WorkflowRuntime.GetJobLease(ctx, req)
	if err != nil || l == nil {
		return l, err
	}
	return lostResponseLease{l}, nil
}

type lostResponseLease struct{ jobdb.ExecutionLease }

func (l lostResponseLease) Reschedule(ctx context.Context, req jobdb.RescheduleExecutionRequest) error {
	if err := l.ExecutionLease.Reschedule(ctx, req); err != nil {
		return err
	}
	return errors.New("simulated lost handoff response")
}

func TestLostHandoffResponseStopsAndRecoversWithoutRepeatingActivity(t *testing.T) {
	f := newFixture(t, "toy", "2Gi", &execution.Requirements{Resources: execution.Resources{Memory: str("16Gi")}})
	var events int
	callback := func(compiler.ExecutionHandoff) { events++ }
	rt := New(lostResponseRuntime{f.runtime}, allocation("2Gi"), callback)
	worker := compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{Allocation: allocation("2Gi"), StageExecution: rt.Stage, OnExecutionHandoff: callback})
	runnable, err := jobworkflow.GetJobForRun(context.Background(), rt, jobworkflow.GetJobForRunRequest{JobKey: f.key, JobWorker: worker, TaskWorkers: f.tasks, WorkerID: "test", LeaseDuration: time.Minute})
	require.NoError(t, err)
	_, _ = runnable.Run(nil) // Finalization can also fail because the lease was surrendered.
	require.Zero(t, events, "an uncertain response must not report an acknowledged handoff")
	require.EqualValues(t, 1, f.first.Load())
	require.Zero(t, f.second.Load())
	info, err := f.runtime.GetJob(context.Background(), f.key)
	require.NoError(t, err)
	demand, err := execution.PayloadDemand(info.ClientPayload)
	require.NoError(t, err)
	require.NotNil(t, demand)
	require.Equal(t, "16Gi", *demand.Effective.Resources.Memory)
	out, event := f.run(t, "16Gi", f.tasks)
	require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
	require.Nil(t, event)
	require.EqualValues(t, 1, f.first.Load())
	require.EqualValues(t, 1, f.second.Load())
}

type mutableResolver struct {
	rec   recipe.Recipe
	loads atomic.Int32
}

func (r *mutableResolver) Resolve(_ context.Context, _ string, selector string) (compiler.RecipeSourceResolution, error) {
	return compiler.RecipeSourceResolution{SourceKind: compiler.RecipeSourceKindServerRef, SubmittedSelector: selector, ResolvedSelector: selector}, nil
}
func (r *mutableResolver) Load(context.Context, string, compiler.RecipeSourceResolution) (recipe.Recipe, error) {
	r.loads.Add(1)
	return r.rec, nil
}

func TestDeferredResolutionPinsRequirementsWithoutProvisionerResolution(t *testing.T) {
	for _, initialMemory := range []string{"2Gi", "16Gi"} {
		t.Run(initialMemory, func(t *testing.T) {
			ctx := context.Background()
			rt := toy.New()
			engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
			require.NoError(t, err)
			r, err := recipe.LoadRecipeFromString([]byte("id: deferred\nexecution:\n  resources:\n    memory: 16Gi\nsequence: []\n"))
			require.NoError(t, err)
			resolver := &mutableResolver{rec: *r}
			key, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "tenant", RecipeName: "deferred"}, jobdbschema.WorkflowEngine{Engine: engine, Registry: rt})
			require.NoError(t, err)
			require.Zero(t, resolver.loads.Load())
			run := func(memory string) (jobworkflow.JobRunOutcome, *compiler.ExecutionHandoff) {
				var event *compiler.ExecutionHandoff
				callback := func(e compiler.ExecutionHandoff) { event = &e }
				wrapped := New(rt, allocation(memory), callback)
				registry, err := workerops.NewActivityRegistry()
				require.NoError(t, err)
				ws, err := compiler.NewRecipeWorkerWithOptions(coreops.NewServiceDepsBuilder().Build(), registry, compiler.RecipeJobWorkerOptions{RootSourceResolver: resolver, Allocation: allocation(memory), StageExecution: wrapped.Stage, OnExecutionHandoff: callback})
				require.NoError(t, err)
				var tasks []jobworkflow.TaskWorker
				for _, tw := range ws.TaskWorkers {
					tasks = append(tasks, tw)
				}
				runnable, err := jobworkflow.GetJobForRun(ctx, wrapped, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: ws.JobWorker, TaskWorkers: tasks, WorkerID: "test", LeaseDuration: time.Minute})
				require.NoError(t, err)
				out, err := runnable.Run(nil)
				require.NoError(t, err)
				return out, event
			}
			out, event := run(initialMemory)
			require.EqualValues(t, 1, resolver.loads.Load())
			if initialMemory == "16Gi" {
				require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
				require.Nil(t, event)
				info, err := rt.GetJob(ctx, key)
				require.NoError(t, err)
				require.Empty(t, info.ClientPayload, "compatible resolution does not force publication")
			} else {
				require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
				require.NotNil(t, event)
				changed, err := recipe.LoadRecipeFromString([]byte("id: changed\nexecution:\n  resources:\n    memory: 32Gi\nsequence: []\n"))
				require.NoError(t, err)
				resolver.rec = *changed
				out, event = run("16Gi")
				require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
				require.Nil(t, event)
				require.EqualValues(t, 1, resolver.loads.Load())
			}
		})
	}
}

func TestInlineDeclarationIsRetainedAndYieldsBeforeIncludedBody(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	childPath := filepath.Join(dir, "child.yaml")
	require.NoError(t, os.WriteFile(childPath, []byte("id: child\nexecution:\n  resources:\n    memory: 16Gi\nsequence: []\n"), 0600))
	root, err := recipe.LoadRecipeFromString([]byte("id: parent\nexecution:\n  resources:\n    memory: 2Gi\nsequence:\n  - id: child\n    include: ./child.yaml\n"))
	require.NoError(t, err)
	expanded, err := compiler.ResolveInlineRecipes(ctx, *root, compiler.InlineResolutionOptions{RootFile: filepath.Join(dir, "root.yaml")})
	require.NoError(t, err)
	inline := expanded.Recipe.RecipeImpl.(*recipe.RecipeSequence).Sequence[0].GetMetadata().Internal.Inline
	require.Equal(t, "16Gi", *inline.Execution.Resources.Memory)
	rt := toy.New()
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	key, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "tenant", RecipeName: "parent"}, jobdbschema.WorkflowEngine{Engine: engine, Registry: rt}, expanded.Recipe)
	require.NoError(t, err)
	f := &fixture{runtime: rt, key: key}
	out, event := f.run(t, "2Gi", nil)
	require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
	require.NotNil(t, event)
	require.Equal(t, "16Gi", *event.Demand.Effective.Resources.Memory)
	out, event = f.run(t, "16Gi", nil)
	require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
	require.Nil(t, event)
}
