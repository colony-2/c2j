package story_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/jsonexact"
	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
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
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const restartProbeOp = "restart_context_probe"

type restartProbeInput struct {
	Value string `json:"value"`
}
type restartProbeTask struct {
	calls     atomic.Int32
	fail      atomic.Bool
	failValue string
}

func (*restartProbeTask) Name() string { return restartProbeOp + ":" + restartProbeOp }
func (w *restartProbeTask) Run(_ jobworkflow.TaskContext, data jobdb.TaskData) (jobdb.TaskData, error) {
	w.calls.Add(1)
	if w.fail.Load() && w.failValue == "" {
		return nil, fmt.Errorf("candidate failed")
	}
	raw, err := data.GetData()
	if err != nil {
		return nil, err
	}
	var request workerops.ActivityInvocationRequest
	if err = json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	if w.fail.Load() && request.Input["value"] == w.failValue {
		return nil, fmt.Errorf("candidate failed")
	}
	output := map[string]any{"value": request.Input["value"], "unchanged": "keep", "api_key": "sensitive fixture"}
	if request.Input["value"] == "exact-number-fixture" {
		var numbers map[string]any
		if err := jsonexact.Unmarshal([]byte(exactValues), &numbers); err != nil {
			return nil, err
		}
		output["exact"] = numbers
	}
	envelope, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, map[string]any{
		"output": output, "git": map[string]any{}, "nextTaskType": "",
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

func newRestartFixture(t *testing.T, backend, recipe string, failFirst ...bool) *restartFixture {
	return newRestartFixtureInputs(t, backend, recipe, nil, failFirst...)
}
func newRestartFixtureInputs(t *testing.T, backend, recipe string, inputs map[string]any, failFirst ...bool) *restartFixture {
	return newRestartFixtureFailure(t, backend, recipe, inputs, "", failFirst...)
}

func newRestartFixtureFailure(t *testing.T, backend, recipe string, inputs map[string]any, failValue string, failFirst ...bool) *restartFixture {
	return newRestartFixtureContext(t, backend, recipe, inputs, contextual.JobContext{}, failValue, failFirst...)
}

func newRestartFixtureContext(t *testing.T, backend, recipe string, inputs map[string]any, jobContext contextual.JobContext, failValue string, failFirst ...bool) *restartFixture {
	t.Helper()
	coreops.Register(coreops.NewActivityMappedOpV2[restartProbeInput, map[string]any](coreops.OpMetadata{Type: restartProbeOp}, func(coreops.OpDependencies, context.Context, restartProbeInput) (map[string]any, error) {
		panic("inspection must not execute operations")
	}))
	ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), 30*time.Second)
	t.Cleanup(cancel)
	db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
	var rt jobdb.WorkflowRuntime = db
	if backend == "remote" {
		server := httptest.NewServer(celltest.Handler(remote.NewServer(db)))
		t.Cleanup(server.Close)
		rt, err = remote.New(server.URL, server.Client())
		require.NoError(t, err)
	}
	w := compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{})
	task := &restartProbeTask{failValue: failValue}
	if len(failFirst) > 0 {
		task.fail.Store(failFirst[0])
	}
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId("restart-tests").PlusWorkers(w, task).BuildEngine()
	require.NoError(t, err)
	svc, err := story.New(story.ServiceConfig{Engine: engine, SchemaRegistry: db})
	require.NoError(t, err)
	var refs []artifacts.Ref
	for name, ref := range jobContext.Artifacts {
		ref.Name = name
		refs = append(refs, ref)
	}
	data, err := jobdb.NewTaskData(workflowctl.StartJob{ArtifactRefs: refs, JobContext: jobContext, Inputs: inputs, TenantId: "restart-tests", RecipeName: "fixture", Cell: "test", CellRepo: celltest.Repo("")}, jobdb.NewArtifactFromBytes("fixture"+starter.RecipeArtifactSuffix, []byte(recipe)))
	require.NoError(t, err)
	celltest.Create(t, rt, "restart-tests", "test", "example.test/test")
	keyHandle, err := engine.SubmitJob(ctx, jobdb.SubmitJob{TenantId: "restart-tests", JobType: "recipe", Data: data, Cell: "test", CellRepo: "example.test/test"})
	key := keyHandle.JobKey
	require.NoError(t, err)
	f := &restartFixture{ctx, rt, engine, svc, w, task, key}
	if task.fail.Load() {
		run, err := jobworkflow.GetJobForRun(ctx, rt, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: w, TaskWorkers: []jobworkflow.TaskWorker{compiler.NewCellContextTaskWorker(), task}, WorkerID: "test", LeaseDuration: time.Minute})
		require.NoError(t, err)
		out, err := run.Run(nil)
		require.NoError(t, err)
		require.Equal(t, jobworkflow.JobRunFailed, out.Status)
	} else {
		f.run(t, key)
	}
	return f
}
func (f *restartFixture) run(t *testing.T, key jobdb.JobKey) {
	t.Helper()
	run, err := jobworkflow.GetJobForRun(f.ctx, f.runtime, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: f.worker, TaskWorkers: []jobworkflow.TaskWorker{compiler.NewCellContextTaskWorker(), f.task}, WorkerID: "restart-test", LeaseDuration: time.Minute})
	require.NoError(t, err)
	_, err = run.Run(nil)
	require.NoError(t, err)
	stored, err := f.engine.GetJobRun(f.ctx, jobdb.GetJobRunRequest{JobKey: key})
	require.NoError(t, err)
	require.Equal(t, "success", stored.Job.CompletionStatus, "%s", stored.Job.CompletionDetail)
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

			patch := &coretask.ContextPatch{{Op: "replace", Path: "/sequence/inner/outputs/value", Value: []byte(`"patched"`)}}
			restarted, err := f.service.RestartRecipeJob(f.ctx, story.RestartRecipeJobRequest{RequestID: uuid.NewString(), Goal: struct {
				Mode string `json:"mode"`
			}{Mode: "new"}, ProjectID: f.key.TenantId, JobID: f.key.JobId, ChapterOrdinal: &target, Patch: patch})
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
			again, err := f.service.RestartRecipeJob(f.ctx, story.RestartRecipeJobRequest{RequestID: uuid.NewString(), Goal: struct {
				Mode string `json:"mode"`
			}{Mode: "new"}, ProjectID: key.TenantId, JobID: key.JobId, ChapterOrdinal: newOps[2].RestartChapterOrdinal})
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
	require.Equal(t, json.Number("1"), prior["outputs"].(map[string]any)["count"])
	for _, scope := range st.RestartScopes {
		f.inspectScope(t, f.key, scope.ScopeID)
	}
	scopeID := states[1].RestartScopeID
	restarted, err := f.service.RestartRecipeJob(f.ctx, story.RestartRecipeJobRequest{RequestID: uuid.NewString(), Goal: struct {
		Mode string `json:"mode"`
	}{Mode: "new"}, ProjectID: f.key.TenantId, JobID: f.key.JobId, ScopeID: scopeID})
	require.NoError(t, err)
	key := jobdb.JobKey{TenantId: f.key.TenantId, JobId: restarted.JobID}
	f.run(t, key)
	require.EqualValues(t, 2, f.story(t, key).Root.Output.(map[string]any)["count"])
	patched, err := f.service.RestartRecipeJob(f.ctx, story.RestartRecipeJobRequest{RequestID: uuid.NewString(), ProjectID: f.key.TenantId, JobID: f.key.JobId, ScopeID: scopeID, Patch: &coretask.ContextPatch{{Op: "replace", Path: "/states/loop/outputs/count", Value: []byte(`10`)}}, Goal: struct {
		Mode string `json:"mode"`
	}{Mode: "new"}})
	require.NoError(t, err)
	patchedKey := jobdb.JobKey{TenantId: f.key.TenantId, JobId: patched.JobID}
	f.run(t, patchedKey)
	require.EqualValues(t, 11, f.story(t, patchedKey).Root.Output.(map[string]any)["count"], "state output patch applies before the revisited state's vars/inputs")
}

func TestRestartContextRejectsInvalidTargets(t *testing.T) {
	f := newRestartFixture(t, "sqlite", restartSequenceRecipe)
	st := f.story(t, f.key)
	for _, ordinal := range []int64{-1, 999999, *storyNodes(st.Root, "op", restartProbeOp)[0].TaskOrdinal} {
		_, err := f.service.GetRestartContext(f.ctx, story.GetRestartContextRequest{ProjectID: f.key.TenantId, JobID: f.key.JobId, ChapterOrdinal: ordinal})
		require.ErrorIs(t, err, story.ErrInvalidRestartBoundary)
		_, err = f.service.RestartRecipeJob(f.ctx, story.RestartRecipeJobRequest{RequestID: uuid.NewString(), Goal: struct {
			Mode string `json:"mode"`
		}{Mode: "new"}, ProjectID: f.key.TenantId, JobID: f.key.JobId, ChapterOrdinal: &ordinal})
		require.ErrorIs(t, err, story.ErrInvalidRestartBoundary)
	}
	_, err := f.service.GetRestartContext(f.ctx, story.GetRestartContextRequest{ProjectID: "wrong-tenant", JobID: f.key.JobId, ChapterOrdinal: *st.Root.RestartChapterOrdinal})
	require.ErrorIs(t, err, story.ErrNotFound)
	_, err = f.service.RestartRecipeJob(f.ctx, story.RestartRecipeJobRequest{RequestID: uuid.NewString(), Goal: struct {
		Mode string `json:"mode"`
	}{Mode: "new"}, ProjectID: f.key.TenantId, JobID: f.key.JobId, ScopeID: st.RestartScopes[0].ScopeID, ChapterOrdinal: st.Root.RestartChapterOrdinal})
	require.ErrorIs(t, err, story.ErrInvalidRestartBoundary)
}

func (f *restartFixture) inspectScope(t *testing.T, key jobdb.JobKey, id string) *story.RestartContext {
	t.Helper()
	result, err := f.service.GetRestartContext(f.ctx, story.GetRestartContextRequest{ProjectID: key.TenantId, JobID: key.JobId, ScopeID: id})
	require.NoError(t, err)
	require.Equal(t, id, result.ScopeID)
	return result
}

func TestSubmissionRestartPreservesInputsAndArtifacts(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f := newRestartFixture(t, backend, restartSequenceRecipe)
			original, err := f.runtime.ListChapters(f.ctx, jobdb.ListChaptersRequest{JobKey: f.key})
			require.NoError(t, err)
			key := f.key
			// Exercise both existing selectors and a restart of a restart.
			for i, selector := range []string{"ordinal", "scope"} {
				st := f.story(t, key)
				require.Equal(t, "Job submission", st.RestartScopes[0].Title)
				require.Zero(t, st.RestartScopes[0].ChapterOrdinal)
				require.Equal(t, starter.SubmissionRestartScopeID, st.RestartScopes[0].ScopeID)
				byOrdinal := f.inspect(t, key, 0)
				byScope := f.inspectScope(t, key, st.RestartScopes[0].ScopeID)
				require.Equal(t, byOrdinal, byScope)
				require.Empty(t, byScope.Visible["inputs"])
				require.NotContains(t, byScope.Visible, "sequence", "no recipe scope exists at submission")
				req := story.RestartRecipeJobRequest{ProjectID: key.TenantId, JobID: key.JobId, RequestID: uuid.NewString()}
				req.Goal.Mode = "new"
				if selector == "ordinal" {
					ordinal := int64(0)
					req.ChapterOrdinal = &ordinal
				} else {
					req.ScopeID = st.RestartScopes[0].ScopeID
				}
				restarted, err := f.service.RestartRecipeJob(f.ctx, req)
				require.NoError(t, err)
				again, err := f.service.RestartRecipeJob(f.ctx, req)
				require.NoError(t, err)
				require.Equal(t, restarted, again, "request retries preserve identity")
				key = jobdb.JobKey{TenantId: restarted.TenantID, JobId: restarted.JobID}
				retained, err := f.runtime.ListChapters(f.ctx, jobdb.ListChaptersRequest{JobKey: key})
				require.NoError(t, err)
				require.Len(t, retained, 1, "only the submission is retained")
				require.Equal(t, original[0].Body, retained[0].Body, "original input and recipe artifact references are preserved")
				f.run(t, key)
				require.EqualValues(t, 4*(i+2), f.task.calls.Load(), "every operation executes again")
				require.Equal(t, "original", f.story(t, key).Root.Output.(map[string]any)["value"])
			}
			after, err := f.runtime.ListChapters(f.ctx, jobdb.ListChaptersRequest{JobKey: f.key})
			require.NoError(t, err)
			require.Equal(t, original, after, "inspection and restart leave the source history unchanged")
		})
	}
}

func TestRestartContinuesGoalAndReconcilesAfterWinning(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f := newRestartFixture(t, backend, `id: fixture
sequence:
 - id: work
   op: restart_context_probe
   inputs: {value: '${{ context.workflow.goal_id }}'}
outputs:
 value: ${{ sequence.work.outputs.value }}
`, true)
			info, err := f.runtime.GetJob(f.ctx, f.key)
			require.NoError(t, err)
			goalKey := jobdb.GoalKey{TenantId: f.key.TenantId, GoalID: info.GoalID}
			goal, err := f.runtime.GetGoal(f.ctx, goalKey)
			require.NoError(t, err)
			require.Equal(t, jobdb.GoalOpen, goal.State)
			st := f.story(t, f.key)
			require.NotEmpty(t, st.RestartScopes)
			req := story.RestartRecipeJobRequest{ProjectID: f.key.TenantId, JobID: f.key.JobId, ScopeID: st.Root.RestartScopeID, RequestID: "repair-attempt"}
			restarted, err := f.service.RestartRecipeJob(f.ctx, req)
			require.NoError(t, err)
			require.Equal(t, info.GoalID, restarted.GoalID)
			require.Equal(t, f.key.TenantId, restarted.TenantID)
			f.task.fail.Store(false)
			f.run(t, jobdb.JobKey{TenantId: restarted.TenantID, JobId: restarted.JobID})
			goal, err = f.runtime.GetGoal(f.ctx, goalKey)
			require.NoError(t, err)
			require.Equal(t, restarted.JobID, goal.SuccessfulJobID)
			again, err := f.service.RestartRecipeJob(f.ctx, req)
			require.NoError(t, err)
			require.Equal(t, restarted, again)
			// Reconcile duplicate operator/network retries even after the new
			// candidate has closed the goal. Every caller must get one identity.
			var retries sync.WaitGroup
			for i := 0; i < 8; i++ {
				retries.Go(func() {
					same, e := f.service.RestartRecipeJob(f.ctx, req)
					if e != nil {
						t.Errorf("concurrent restart retry: %v", e)
						return
					}
					if same == nil || *same != *restarted {
						t.Errorf("restart retry changed identity: got %+v want %+v", same, restarted)
					}
				})
			}
			retries.Wait()
			req.RequestID = "different-attempt"
			_, err = f.service.RestartRecipeJob(f.ctx, req)
			require.ErrorIs(t, err, jobdb.ErrGoalConflict)
			req.Goal.Mode = "new"
			fresh, err := f.service.RestartRecipeJob(f.ctx, req)
			require.NoError(t, err)
			require.NotEqual(t, restarted.GoalID, fresh.GoalID)
			freshKey := jobdb.JobKey{TenantId: fresh.TenantID, JobId: fresh.JobID}
			f.run(t, freshKey)
			freshStory := f.story(t, freshKey)
			require.Equal(t, fresh.GoalID, freshStory.GoalID)
			require.Equal(t, fresh.GoalID, freshStory.Root.Output.(map[string]any)["value"])
		})
	}
}

func TestRestartNumericNestedLifecycle(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f := newRestartFixtureFailure(t, backend, `id: numeric_restart
sequence:
 - id: prior
   op: restart_context_probe
   inputs: {value: prior}
 - id: nested
   inputs: {count: '${{ inputs.?count.orValue(0) }}'}
   state:
     initial: work
     states:
       work:
         inputs: {count: '${{ inputs.count }}'}
         sequence:
          - id: inner
            op: restart_context_probe
            inputs: {value: inner}
          - id: target
            op: restart_context_probe
            inputs: {value: target}
         outputs: {count: '${{ inputs.count + 1 }}'}
   outputs: {count: '${{ states.work.outputs.count + inputs.count }}'}
outputs: {count: '${{ sequence.nested.outputs.count + 1 }}'}
`, nil, "target", true)
			before, err := f.runtime.ListChapters(f.ctx, jobdb.ListChaptersRequest{JobKey: f.key})
			require.NoError(t, err)
			source := f.story(t, f.key)
			ops := storyNodes(source.Root, "op", restartProbeOp)
			require.Len(t, ops, 3)
			require.Equal(t, int32(3), f.task.calls.Load())
			req := story.RestartRecipeJobRequest{ProjectID: f.key.TenantId, JobID: f.key.JobId, ChapterOrdinal: ops[2].RestartChapterOrdinal, RequestID: uuid.NewString()}
			req.Goal.Mode = "new"
			restarted, err := f.service.RestartRecipeJob(f.ctx, req)
			require.NoError(t, err)
			f.task.fail.Store(false)
			key := jobdb.JobKey{TenantId: restarted.TenantID, JobId: restarted.JobID}
			f.run(t, key)
			require.Equal(t, int32(4), f.task.calls.Load(), "completed prior tasks are retained")
			st := f.story(t, key)
			require.EqualValues(t, 2, st.Root.Output.(map[string]any)["count"])
			nested := storyNodes(st.Root, "stateMachine", "nested")
			require.Len(t, nested, 1)
			require.EqualValues(t, 1, nested[0].Output.(map[string]any)["count"])
			after, err := f.runtime.ListChapters(f.ctx, jobdb.ListChaptersRequest{JobKey: f.key})
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}
