package story_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/story"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

const restartProbeOp = "restart_context_probe"

type restartProbeInput struct {
	Value string `json:"value"`
}
type restartProbeTask struct{ calls atomic.Int32 }

func (*restartProbeTask) Name() string { return restartProbeOp + ":" + restartProbeOp }
func (w *restartProbeTask) Run(_ jobworkflow.TaskContext, data jobdb.TaskData) (jobdb.TaskData, error) {
	w.calls.Add(1)
	raw, err := data.GetData()
	if err != nil {
		return nil, err
	}
	var request workerops.ActivityInvocationRequest
	if err = json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	envelope, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, map[string]any{
		"output": map[string]any{"value": request.Input["value"], "unchanged": "keep", "api_key": "sensitive fixture"}, "git": map[string]any{}, "nextTaskType": "",
	})
	if err != nil {
		return nil, err
	}
	return jobdb.NewTaskData(envelope)
}

type restartFixture struct {
	ctx     context.Context
	runtime jobdb.WorkflowRuntime
	engine  jobworkflow.Engine
	service story.Service
	worker  jobworkflow.ExecutionWorker
	task    *restartProbeTask
	key     jobdb.JobKey
}

func newRestartFixture(t *testing.T, backend, recipe string) *restartFixture {
	t.Helper()
	coreops.Register(coreops.NewActivityMappedOpV2[restartProbeInput, map[string]any](coreops.OpMetadata{Type: restartProbeOp}, func(coreops.OpDependencies, context.Context, restartProbeInput) (map[string]any, error) {
		panic("inspection must not execute operations")
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
	var rt jobdb.WorkflowRuntime = db
	if backend == "remote" {
		server := httptest.NewServer(remote.NewServer(db))
		t.Cleanup(server.Close)
		rt, err = remote.New(server.URL, server.Client())
		require.NoError(t, err)
	}
	w := compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{})
	task := &restartProbeTask{}
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId("restart-tests").PlusWorkers(w, task).BuildEngine()
	require.NoError(t, err)
	svc, err := story.New(story.ServiceConfig{Engine: engine, SchemaRegistry: db})
	require.NoError(t, err)
	data, err := jobdb.NewTaskData(workflowctl.StartJob{TenantId: "restart-tests", RecipeName: "fixture"}, jobdb.NewArtifactFromBytes("fixture"+starter.RecipeArtifactSuffix, []byte(recipe)))
	require.NoError(t, err)
	key, err := engine.SubmitJob(ctx, jobdb.SubmitJob{TenantId: "restart-tests", JobType: "recipe", Data: data})
	require.NoError(t, err)
	f := &restartFixture{ctx, rt, engine, svc, w, task, key}
	f.run(t, key)
	return f
}
func (f *restartFixture) run(t *testing.T, key jobdb.JobKey) {
	t.Helper()
	run, err := jobworkflow.GetJobForRun(f.ctx, f.runtime, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: f.worker, TaskWorkers: []jobworkflow.TaskWorker{f.task}, WorkerID: "restart-test", LeaseDuration: time.Minute})
	require.NoError(t, err)
	_, err = run.Run(nil)
	require.NoError(t, err)
	stored, err := f.engine.GetJobRun(f.ctx, jobdb.GetJobRunRequest{JobKey: key})
	require.NoError(t, err)
	require.Equal(t, "success", stored.Job.CompletionStatus)
}
func (f *restartFixture) inspect(t *testing.T, key jobdb.JobKey, ordinal int64) *story.RestartContext {
	t.Helper()
	result, err := f.service.GetRestartContext(f.ctx, story.GetRestartContextRequest{ProjectID: key.TenantId, JobID: key.JobId, ChapterOrdinal: ordinal})
	require.NoError(t, err)
	return result
}
func (f *restartFixture) story(t *testing.T, key jobdb.JobKey) *story.JobRunStory {
	t.Helper()
	result, err := f.service.GetJobRunStory(f.ctx, story.GetJobRunStoryRequest{ProjectID: key.TenantId, JobID: key.JobId})
	require.NoError(t, err)
	return result
}
func storyNodes(root *story.JobRunStoryNode, kind, id string) []*story.JobRunStoryNode {
	var out []*story.JobRunStoryNode
	var visit func(*story.JobRunStoryNode)
	visit = func(n *story.JobRunStoryNode) {
		if n == nil {
			return
		}
		if string(n.Kind) == kind && (n.OpID == id || n.StateID == id || n.SequenceID == id || n.StateMachineID == id) {
			out = append(out, n)
		}
		for _, child := range n.Children {
			visit(child)
		}
	}
	visit(root)
	return out
}

const restartSequenceRecipe = `id: inspect
sequence:
  - id: first
    op: restart_context_probe
    inputs: {value: original}
  - id: nested
    inputs: {value: '${{ sequence.first.outputs.value }}'}
    sequence:
      - id: inner
        op: restart_context_probe
        inputs: {value: '${{ inputs.value }}'}
      - id: target
        op: restart_context_probe
        inputs: {value: '${{ sequence.inner.outputs.value }}'}
    outputs: {value: '${{ sequence.target.outputs.value }}'}
  - id: last
    op: restart_context_probe
    inputs: {value: '${{ sequence.nested.outputs.value }}'}
outputs: {value: '${{ sequence.last.outputs.value }}'}
`

func TestRestartContextAndOrdinalAPI(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f := newRestartFixture(t, backend, restartSequenceRecipe)
			before, err := f.runtime.ListChapters(f.ctx, jobdb.ListChaptersRequest{JobKey: f.key})
			require.NoError(t, err)
			st := f.story(t, f.key)
			require.NotNil(t, st.Root.RestartChapterOrdinal)
			// OpID is the registered op type; distinguish occurrences by their path.
			ops := storyNodes(st.Root, "op", restartProbeOp)
			require.Len(t, ops, 4)
			ordinals := map[int64]bool{}
			for _, op := range ops {
				require.NotNil(t, op.RestartChapterOrdinal)
				require.NotEmpty(t, op.InvocationID, "collapsed operation retains invocation identity")
				require.Empty(t, op.Children, "single task is collapsed")
				require.Equal(t, "exact", op.RestartRelationship)
				ordinals[*op.RestartChapterOrdinal] = true
			}
			require.Len(t, ordinals, 4)
			target := *ops[2].RestartChapterOrdinal
			snapshot := f.inspect(t, f.key, target)
			seq := snapshot.Visible["sequence"].(map[string]any)
			require.Len(t, seq, 1, "only local prior sibling is visible")
			outputs := seq["inner"].(map[string]any)["outputs"].(map[string]any)
			require.Equal(t, "original", outputs["value"])
			require.Equal(t, "keep", outputs["unchanged"])
			require.Equal(t, "[REDACTED]", outputs["api_key"])
			require.Contains(t, snapshot.RedactedPaths, "/visible/sequence/inner/outputs/api_key")
			require.Equal(t, "original", snapshot.Visible["inputs"].(map[string]any)["value"])
			require.NotEmpty(t, snapshot.Job)
			for _, scope := range st.RestartScopes {
				f.inspectScope(t, f.key, scope.ScopeID)
			}
			require.Equal(t, int32(4), f.task.calls.Load())
			after, err := f.runtime.ListChapters(f.ctx, jobdb.ListChaptersRequest{JobKey: f.key})
			require.NoError(t, err)
			require.Equal(t, before, after, "inspection never writes history")

			patch := &coretask.ContextPatch{Scopes: []coretask.ScopePatch{{Container: "sequence", ID: "inner", Outputs: map[string]any{"value": "patched"}}}}
			restarted, err := f.service.RestartRecipeJob(f.ctx, story.RestartRecipeJobRequest{ProjectID: f.key.TenantId, JobID: f.key.JobId, ChapterOrdinal: &target, Patch: patch})
			require.NoError(t, err)
			key := jobdb.JobKey{TenantId: f.key.TenantId, JobId: restarted.JobID}
			f.run(t, key)
			require.Equal(t, int32(6), f.task.calls.Load(), "restart retains the first two completed tasks")
			restartedStory := f.story(t, key)
			require.Equal(t, "patched", restartedStory.Root.Output.(map[string]any)["value"])
			newOps := storyNodes(restartedStory.Root, "op", restartProbeOp)
			last := f.inspect(t, key, *newOps[3].RestartChapterOrdinal)
			nested := last.Visible["sequence"].(map[string]any)["nested"].(map[string]any)
			require.Equal(t, "patched", nested["outputs"].(map[string]any)["value"], "earlier restart patches are replayed")
			// Inspecting the original source still returns its original context.
			require.Equal(t, snapshot.Visible["sequence"], f.inspect(t, f.key, target).Visible["sequence"])
			// A restart at the same boundary replaces that boundary's old patch.
			same := f.inspect(t, key, *newOps[2].RestartChapterOrdinal)
			require.Equal(t, snapshot.Visible["sequence"], same.Visible["sequence"])
			again, err := f.service.RestartRecipeJob(f.ctx, story.RestartRecipeJobRequest{ProjectID: key.TenantId, JobID: key.JobId, ChapterOrdinal: newOps[2].RestartChapterOrdinal})
			require.NoError(t, err)
			againKey := jobdb.JobKey{TenantId: key.TenantId, JobId: again.JobID}
			f.run(t, againKey)
			require.Equal(t, "original", f.story(t, againKey).Root.Output.(map[string]any)["value"])
		})
	}
}

func TestRestartContextRepeatedStateVisits(t *testing.T) {
	f := newRestartFixture(t, "sqlite", `id: repeated
state:
  initial: loop
  states:
    loop:
      vars: {count: '${{ int(state_output("loop", "count", 0)) + 1 }}'}
      sequence: []
      outputs: {count: '${{ vars.count }}'}
      transitions:
        - to: loop
          when: states.loop.outputs.count < 2
        - to: done
    done:
      sequence: []
outputs: {count: '${{ states.loop.outputs.count }}'}
`)
	st := f.story(t, f.key)
	states := storyNodes(st.Root, "state", "loop")
	require.Len(t, states, 2)
	require.NotNil(t, states[0].RestartChapterOrdinal)
	require.NotNil(t, states[1].RestartChapterOrdinal)
	require.NotEqual(t, states[0].RestartScopeID, states[1].RestartScopeID)
	first := f.inspectScope(t, f.key, states[0].RestartScopeID)
	second := f.inspectScope(t, f.key, states[1].RestartScopeID)
	require.Empty(t, first.Visible["states"])
	prior := second.Visible["states"].(map[string]any)["loop"].(map[string]any)
	require.EqualValues(t, 1, prior["outputs"].(map[string]any)["count"])
	for _, scope := range st.RestartScopes {
		f.inspectScope(t, f.key, scope.ScopeID)
	}
	scopeID := states[1].RestartScopeID
	restarted, err := f.service.RestartRecipeJob(f.ctx, story.RestartRecipeJobRequest{ProjectID: f.key.TenantId, JobID: f.key.JobId, ScopeID: scopeID})
	require.NoError(t, err)
	key := jobdb.JobKey{TenantId: f.key.TenantId, JobId: restarted.JobID}
	f.run(t, key)
	require.EqualValues(t, 2, f.story(t, key).Root.Output.(map[string]any)["count"])
}

func TestRestartContextRejectsInvalidTargets(t *testing.T) {
	f := newRestartFixture(t, "sqlite", restartSequenceRecipe)
	st := f.story(t, f.key)
	for _, ordinal := range []int64{-1, 0, 999999, *storyNodes(st.Root, "op", restartProbeOp)[0].TaskOrdinal} {
		_, err := f.service.GetRestartContext(f.ctx, story.GetRestartContextRequest{ProjectID: f.key.TenantId, JobID: f.key.JobId, ChapterOrdinal: ordinal})
		require.ErrorIs(t, err, story.ErrInvalidRestartBoundary)
		_, err = f.service.RestartRecipeJob(f.ctx, story.RestartRecipeJobRequest{ProjectID: f.key.TenantId, JobID: f.key.JobId, ChapterOrdinal: &ordinal})
		require.ErrorIs(t, err, story.ErrInvalidRestartBoundary)
	}
	_, err := f.service.GetRestartContext(f.ctx, story.GetRestartContextRequest{ProjectID: "wrong-tenant", JobID: f.key.JobId, ChapterOrdinal: *st.Root.RestartChapterOrdinal})
	require.ErrorIs(t, err, story.ErrNotFound)
	_, err = f.service.RestartRecipeJob(f.ctx, story.RestartRecipeJobRequest{ProjectID: f.key.TenantId, JobID: f.key.JobId, ScopeID: st.RestartScopes[0].ScopeID, ChapterOrdinal: st.Root.RestartChapterOrdinal})
	require.ErrorIs(t, err, story.ErrInvalidRestartBoundary)
}

func (f *restartFixture) inspectScope(t *testing.T, key jobdb.JobKey, id string) *story.RestartContext {
	t.Helper()
	result, err := f.service.GetRestartContext(f.ctx, story.GetRestartContextRequest{ProjectID: key.TenantId, JobID: key.JobId, ScopeID: id})
	require.NoError(t, err)
	require.Equal(t, id, result.ScopeID)
	return result
}
