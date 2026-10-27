package story_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/story"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

const unfinishedOp = "story_unfinished_probe"

type unfinishedInput struct {
	Value string `json:"value"`
}
type unfinishedTask struct {
	entered chan struct{}
	release chan struct{}
	retry   bool
	calls   atomic.Int32
}

func (*unfinishedTask) Name() string { return unfinishedOp + ":" + unfinishedOp }
func (w *unfinishedTask) Run(c wf.TaskContext, _ jobdb.TaskData) (jobdb.TaskData, error) {
	n := w.calls.Add(1)
	if w.retry && n == 1 {
		env, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, map[string]any{"output": map[string]any{"value": "old diagnostic"}})
		if err != nil {
			return nil, err
		}
		return jobdb.NewTaskDataOrPanic(env, jobdb.NewArtifactFromBytes("old.txt", []byte("old"))), errors.New("retry")
	}
	close(w.entered)
	select {
	case <-w.release:
	case <-c.Context().Done():
		return nil, c.Context().Err()
	}
	env, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, map[string]any{"output": map[string]any{"value": "done"}, "git": map[string]any{}, "nextTaskType": ""})
	if err != nil {
		return nil, err
	}
	return jobdb.NewTaskData(env)
}

func TestStoryAPIExposesUnfinishedTask(t *testing.T) {
	coreops.Register(coreops.NewActivityMappedOpV2[unfinishedInput, map[string]any](coreops.OpMetadata{Type: unfinishedOp, RetrySteps: true}, func(coreops.OpDependencies, context.Context, unfinishedInput) (map[string]any, error) {
		panic("inspection executed operation")
	}))
	for _, backend := range []string{"sqlite", "remote"} {
		for _, mode := range []string{"local", "external", "retry", "external-retry"} {
			t.Run(backend+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
				require.NoError(t, err)
				defer db.Close(context.Background())
				var rt jobdb.WorkflowRuntime = db
				if backend == "remote" {
					server := httptest.NewServer(remote.NewServer(db))
					defer server.Close()
					rt, err = remote.New(server.URL, server.Client())
					require.NoError(t, err)
				}
				w := compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{})
				task := &unfinishedTask{entered: make(chan struct{}), release: make(chan struct{}), retry: mode == "retry" || mode == "external-retry"}
				unblock := sync.OnceFunc(func() { close(task.release) })
				defer unblock()
				engine, err := wf.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId("story").PlusWorkers(w).BuildEngine()
				require.NoError(t, err)
				svc, err := story.New(story.ServiceConfig{Engine: engine})
				require.NoError(t, err)
				yaml := "id: unfinished\ninput_schema: {value: {type: string}}\nsequence:\n - id: outer\n   inputs: {value: resolved}\n   sequence:\n    - id: work\n      op: " + unfinishedOp + "\n      inputs: {value: '${{ inputs.value }}'}\n      retry: {maximum_attempts: 2, initial_interval: 1ms}\n      timeout: 5m\n"
				data, err := jobdb.NewTaskData(workflowctl.StartJob{TenantId: "story", RecipeName: "unfinished", Inputs: map[string]any{"value": "resolved"}}, jobdb.NewArtifactFromBytes("unfinished"+starter.RecipeArtifactSuffix, []byte(yaml)))
				require.NoError(t, err)
				h, err := engine.SubmitJob(ctx, jobdb.SubmitJob{TenantId: "story", JobType: "recipe", Data: data})
				require.NoError(t, err)
				request := story.GetJobRunStoryRequest{ProjectID: h.JobKey.TenantId, JobID: h.JobKey.JobId}
				inspect := func(status string, attempt int) *story.JobRunStoryNode {
					t.Helper()
					before, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: h.JobKey})
					require.NoError(t, err)
					st, err := svc.GetJobRunStory(ctx, request)
					require.NoError(t, err)
					nodes := storyNodes(st.Root, "op", unfinishedOp)
					require.Len(t, nodes, 1)
					n := nodes[0]
					require.Empty(t, n.Children, "single task stays collapsed before and after completion")
					require.Equal(t, status, string(n.Status))
					require.Equal(t, attempt, n.Attempt)
					require.NotEmpty(t, n.InvocationID)
					require.NotEmpty(t, n.Path)
					require.Equal(t, map[string]any{"value": "resolved"}, n.Input)
					if status != "succeeded" {
						require.Nil(t, n.Output)
						require.Empty(t, n.ArtifactKeys)
						require.Empty(t, n.ArtifactRefs)
						require.Nil(t, n.FinishedAt)
						require.Nil(t, n.Error)
					}
					if attempt == 2 {
						require.Len(t, n.PriorAttempts, 1)
						require.Equal(t, map[string]any{"value": "old diagnostic"}, n.PriorAttempts[0].Output)
						require.NotEmpty(t, n.PriorAttempts[0].ArtifactKeys)
					}
					after, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: h.JobKey})
					require.NoError(t, err)
					require.Equal(t, before, after)
					return n
				}
				var pending *story.JobRunStoryNode
				if mode == "external" || mode == "external-retry" {
					run, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: h.JobKey, JobWorker: w, WorkerID: "queue", LeaseDuration: time.Minute})
					require.NoError(t, err)
					out, err := run.Run(nil)
					require.NoError(t, err)
					require.Equal(t, wf.JobRunSuspended, out.Status, "job error: %v", out.JobError)
					pending = inspect("pending", 1)
					require.Equal(t, pending, inspect("pending", 1))
					require.Zero(t, task.calls.Load())
				}
				run, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: h.JobKey, JobWorker: w, TaskWorkers: []wf.TaskWorker{task}, WorkerID: "live", LeaseDuration: time.Minute})
				require.NoError(t, err)
				done := make(chan error, 1)
				go func() { out, err := run.Run(nil); done <- errors.Join(err, out.JobError) }()
				select {
				case <-task.entered:
				case err := <-done:
					t.Fatalf("job exited before task started: %v", err)
				case <-ctx.Done():
					t.Fatal("task did not start")
				}
				attempt := 1
				if task.retry {
					attempt = 2
				}
				active := inspect("running", attempt)
				if pending != nil {
					require.Equal(t, pending.ID, active.ID)
					require.Equal(t, pending.InvocationID, active.InvocationID)
					require.Equal(t, pending.Path, active.Path)
				}
				again := inspect("running", attempt)
				require.Equal(t, active.ID, again.ID)
				require.Equal(t, active.InvocationID, again.InvocationID)
				require.EqualValues(t, attempt, task.calls.Load())
				unblock()
				select {
				case err := <-done:
					require.NoError(t, err)
				case <-ctx.Done():
					t.Fatal("task did not finish")
				}
				finished := inspect("succeeded", attempt)
				require.Equal(t, active.ID, finished.ID)
				require.Equal(t, active.InvocationID, finished.InvocationID)
				require.Equal(t, active.Path, finished.Path)
				require.Equal(t, map[string]any{"value": "done"}, finished.Output)
				require.EqualValues(t, attempt, task.calls.Load())
			})
		}
	}
}

// These workers expose explicit barriers at each task boundary. Story inspection
// uses an engine without task workers, so it cannot accidentally finish a task.
type storyBarrierTask struct {
	name    string
	result  jobdb.TaskData
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (w *storyBarrierTask) Name() string { return w.name }
func (w *storyBarrierTask) Run(c wf.TaskContext, _ jobdb.TaskData) (jobdb.TaskData, error) {
	w.calls.Add(1)
	close(w.entered)
	select {
	case <-w.release:
		return w.result, nil
	case <-c.Context().Done():
		return nil, c.Context().Err()
	}
}

func newStoryBarrierTask(t *testing.T, name string, result jobdb.TaskData) (*storyBarrierTask, func()) {
	t.Helper()
	w := &storyBarrierTask{name: name, result: result, entered: make(chan struct{}), release: make(chan struct{})}
	unblock := sync.OnceFunc(func() { close(w.release) })
	t.Cleanup(unblock)
	return w, unblock
}

func storyTestRuntime(t *testing.T, ctx context.Context, backend string) jobdb.WorkflowRuntime {
	t.Helper()
	db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close(context.Background())) })
	if backend == "sqlite" {
		return db
	}
	server := httptest.NewServer(remote.NewServer(db))
	t.Cleanup(server.Close)
	rt, err := remote.New(server.URL, server.Client())
	require.NoError(t, err)
	return rt
}

func waitForStoryTask(t *testing.T, ctx context.Context, task *storyBarrierTask, done <-chan error) {
	t.Helper()
	select {
	case <-task.entered:
	case err := <-done:
		t.Fatalf("job exited before %s started: %v", task.name, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func finishStoryRun(t *testing.T, ctx context.Context, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestStoryAPIPreservesMultiStepLeaves(t *testing.T) {
	const opType = "story_unfinished_chain"
	step := func(coreops.OpDependencies, context.Context, unfinishedInput) (unfinishedInput, error) {
		panic("inspection executed operation")
	}
	op, err := coreops.NewOp().WithType(opType).
		AddStep(opType, coreops.NewStepWithDeps(step)).
		AddStep("second", coreops.NewStepWithDeps(step)).Build()
	require.NoError(t, err)
	coreops.Register(op)
	for _, backend := range []string{"sqlite", "remote"} {
		for _, earlyStop := range []bool{false, true} {
			name := backend + "/complete-chain"
			if earlyStop {
				name = backend + "/stop-after-first"
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				rt := storyTestRuntime(t, ctx, backend)
				w := compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{})
				engine, err := wf.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId("story").PlusWorkers(w).BuildEngine()
				require.NoError(t, err)
				svc, err := story.New(story.ServiceConfig{Engine: engine})
				require.NoError(t, err)
				output := func(value, next string) jobdb.TaskData {
					env, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, map[string]any{"output": map[string]any{"value": value}, "nextTaskType": next})
					require.NoError(t, err)
					return jobdb.NewTaskDataOrPanic(env)
				}
				next := opType + ":second"
				if earlyStop {
					next = ""
				}
				first, releaseFirst := newStoryBarrierTask(t, opType+":"+opType, output("first", next))
				second, releaseSecond := newStoryBarrierTask(t, opType+":second", output("second", ""))
				yaml := "id: chain\nsequence:\n - id: work\n   op: " + opType + "\n   inputs: {value: resolved}\n"
				data := jobdb.NewTaskDataOrPanic(workflowctl.StartJob{TenantId: "story", RecipeName: "chain"}, jobdb.NewArtifactFromBytes("chain"+starter.RecipeArtifactSuffix, []byte(yaml)))
				h, err := engine.SubmitJob(ctx, jobdb.SubmitJob{TenantId: "story", JobType: "recipe", Data: data})
				require.NoError(t, err)
				inspect := func() *story.JobRunStoryNode {
					t.Helper()
					before, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: h.JobKey})
					require.NoError(t, err)
					st, err := svc.GetJobRunStory(ctx, story.GetJobRunStoryRequest{ProjectID: "story", JobID: h.JobKey.JobId})
					require.NoError(t, err)
					nodes := storyNodes(st.Root, "op", opType)
					require.Len(t, nodes, 1)
					require.NotEmpty(t, nodes[0].Children, "a declared multi-step operation must never collapse")
					after, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: h.JobKey})
					require.NoError(t, err)
					require.Equal(t, before, after)
					return nodes[0]
				}
				run, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: h.JobKey, JobWorker: w, TaskWorkers: []wf.TaskWorker{first, second}, WorkerID: "live", LeaseDuration: time.Minute})
				require.NoError(t, err)
				done := make(chan error, 1)
				go func() { out, err := run.Run(nil); done <- errors.Join(err, out.JobError) }()
				waitForStoryTask(t, ctx, first, done)
				active := inspect()
				require.Len(t, active.Children, 1)
				leaf := active.Children[0]
				require.Equal(t, opType, leaf.StepID)
				require.Equal(t, "running", string(leaf.Status))
				require.Equal(t, map[string]any{"value": "resolved"}, leaf.Input)
				require.Nil(t, leaf.Output)
				require.Nil(t, leaf.Error)
				require.Nil(t, leaf.FinishedAt)
				require.Equal(t, leaf, inspect().Children[0])
				releaseFirst()
				if !earlyStop {
					waitForStoryTask(t, ctx, second, done)
					middle := inspect()
					require.Len(t, middle.Children, 2)
					require.Equal(t, leaf.ID, middle.Children[0].ID)
					require.Equal(t, leaf.InvocationID, middle.Children[0].InvocationID)
					require.Equal(t, leaf.Path, middle.Children[0].Path)
					require.Equal(t, "succeeded", string(middle.Children[0].Status))
					require.Equal(t, "running", string(middle.Children[1].Status))
					require.Equal(t, map[string]any{"value": "first"}, middle.Children[1].Input)
					require.Nil(t, middle.Children[1].Error)
					require.Equal(t, middle.Children[1], inspect().Children[1])
					releaseSecond()
				}
				finishStoryRun(t, ctx, done)
				finished := inspect()
				require.Equal(t, "succeeded", string(finished.Status))
				require.Equal(t, leaf.ID, finished.Children[0].ID)
				require.Equal(t, leaf.InvocationID, finished.Children[0].InvocationID)
				require.Equal(t, leaf.Path, finished.Children[0].Path)
				require.Equal(t, map[string]any{"value": "first"}, finished.Children[0].Output)
				require.EqualValues(t, 1, first.calls.Load())
				if earlyStop {
					require.Len(t, finished.Children, 1)
					require.Zero(t, second.calls.Load())
				} else {
					require.Len(t, finished.Children, 2)
					require.EqualValues(t, 1, second.calls.Load())
				}
			})
		}
	}
}

func TestStoryAPIExposesUnfinishedSourceResolution(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			rt := storyTestRuntime(t, ctx, backend)
			resolver := compiler.NewRecipeSourceResolver(compiler.RecipeSourceResolverOptions{
				RecipeRefResolver: compiler.NewProviderBackedRecipeRefResolver(func(string, string) (*recipe.Recipe, error) {
					panic("inspection invoked source resolver")
				}),
			})
			w := compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{RootSourceResolver: resolver})
			engine, err := wf.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId("story").PlusWorkers(w).BuildEngine()
			require.NoError(t, err)
			svc, err := story.New(story.ServiceConfig{Engine: engine, RootSourceResolver: resolver})
			require.NoError(t, err)
			result := compiler.ResolvedRecipeSource{RecipeSourceResolution: compiler.RecipeSourceResolution{SourceKind: compiler.RecipeSourceKindServerRef, SubmittedSelector: "source-ref", ResolvedSelector: "source-ref@v1"}, RecipeYAML: "id: resolved\nsequence: []\n"}
			task, release := newStoryBarrierTask(t, compiler.RootSourceResolutionTaskType, jobdb.NewTaskDataOrPanic(result))
			h, err := engine.SubmitJob(ctx, jobdb.SubmitJob{TenantId: "story", JobType: "recipe", Data: jobdb.NewTaskDataOrPanic(workflowctl.StartJob{TenantId: "story", RecipeName: "source-ref"})})
			require.NoError(t, err)
			inspect := func(status string) *story.JobRunStoryNode {
				t.Helper()
				before, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: h.JobKey})
				require.NoError(t, err)
				st, err := svc.GetJobRunStory(ctx, story.GetJobRunStoryRequest{ProjectID: "story", JobID: h.JobKey.JobId})
				require.NoError(t, err)
				nodes := storyNodes(st.Root, "recipeSourceResolution", "")
				require.Len(t, nodes, 1)
				n := nodes[0]
				require.Equal(t, status, string(n.Status))
				require.NotEmpty(t, n.InvocationID)
				require.Equal(t, 1, n.Attempt)
				require.Subset(t, n.Input, map[string]any{"project_id": "story", "selector": "source-ref"})
				if status != "succeeded" {
					require.Nil(t, n.Output)
					require.Empty(t, n.ArtifactKeys)
					require.Empty(t, n.ArtifactRefs)
					require.Nil(t, n.Error)
					require.Nil(t, n.FinishedAt)
				}
				after, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: h.JobKey})
				require.NoError(t, err)
				require.Equal(t, before, after)
				return n
			}
			queued, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: h.JobKey, JobWorker: w, WorkerID: "queue", LeaseDuration: time.Minute})
			require.NoError(t, err)
			out, err := queued.Run(nil)
			require.NoError(t, err)
			require.Equal(t, wf.JobRunSuspended, out.Status, "%v", out.JobError)
			pending := inspect("pending")
			require.Equal(t, pending, inspect("pending"))
			require.Zero(t, task.calls.Load())
			run, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: h.JobKey, JobWorker: w, TaskWorkers: []wf.TaskWorker{task}, WorkerID: "live", LeaseDuration: time.Minute})
			require.NoError(t, err)
			done := make(chan error, 1)
			go func() { out, err := run.Run(nil); done <- errors.Join(err, out.JobError) }()
			waitForStoryTask(t, ctx, task, done)
			active := inspect("running")
			require.Equal(t, pending.ID, active.ID)
			require.Equal(t, pending.InvocationID, active.InvocationID)
			require.Equal(t, active, inspect("running"))
			release()
			finishStoryRun(t, ctx, done)
			finished := inspect("succeeded")
			require.Equal(t, active.ID, finished.ID)
			require.Equal(t, active.InvocationID, finished.InvocationID)
			require.Equal(t, active.Path, finished.Path)
			require.Equal(t, "source-ref@v1", finished.Output.(map[string]any)["resolved_selector"])
			require.EqualValues(t, 1, task.calls.Load())
		})
	}
}
