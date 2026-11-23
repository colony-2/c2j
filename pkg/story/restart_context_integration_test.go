package story_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/consumerfixture"
	"github.com/colony-2/c2j/internal/jsonexact"
	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/jobrestart"
	restartservice "github.com/colony-2/c2j/pkg/jobrestart/service"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/ops/sleepop"
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
	ctx          context.Context
	runtime      jobdb.ConsumerRuntime
	engine       jobworkflow.Engine
	service      story.Service
	worker       jobworkflow.ExecutionWorker
	task         *restartProbeTask
	key          jobdb.JobKey
	guard        *inspectionRuntime
	dbPath       string
	taskWorkers  []jobworkflow.TaskWorker
	builtinCalls *atomic.Int32
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
	return newRestartFixtureConfigured(t, backend, recipe, inputs, jobContext, failValue, false, false, failFirst...)
}

func newRestartFixtureConfigured(t *testing.T, backend, recipe string, inputs map[string]any, jobContext contextual.JobContext, failValue string, allowFailure, builtin bool, failFirst ...bool) *restartFixture {
	t.Helper()
	coreops.Register(coreops.NewActivityMappedOpV2[restartProbeInput, map[string]any](coreops.OpMetadata{Type: restartProbeOp}, func(coreops.OpDependencies, context.Context, restartProbeInput) (map[string]any, error) {
		panic("inspection must not execute operations")
	}))
	ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), 30*time.Second)
	t.Cleanup(cancel)
	dbPath := filepath.Join(t.TempDir(), "jobs.db")
	db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: dbPath})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
	guard := &inspectionRuntime{Runtime: consumerfixture.New(db)}
	var rt jobdb.ConsumerRuntime = guard
	if backend == "remote" {
		server := httptest.NewServer(celltest.Handler(remote.NewConsumerServer(guard)))
		t.Cleanup(server.Close)
		rt, err = remote.New(server.URL, server.Client())
		require.NoError(t, err)
	}
	var admission *executionruntime.Runtime
	opts := compiler.RecipeJobWorkerOptions{}
	if builtin {
		admission = executionruntime.New(rt, execution.Allocation{SchemaVersion: execution.SchemaVersion})
		rt = admission
		opts.StageExecution = admission.Stage
	}
	w := compiler.NewRecipeJobWorker(opts)
	task := &restartProbeTask{failValue: failValue}
	if len(failFirst) > 0 {
		task.fail.Store(failFirst[0])
	}
	taskWorkers := []jobworkflow.TaskWorker{compiler.NewCellContextTaskWorker(), task}
	builtinCalls := &atomic.Int32{}
	if builtin {
		coreops.Register(sleepop.GetOp())
		registry, err := workerops.NewActivityRegistry()
		require.NoError(t, err)
		for _, worker := range registry.GetTaskWorkers(coreops.NewServiceDepsBuilder().Build()) {
			if worker.Name() == "sleep:sleep" {
				taskWorkers = append(taskWorkers, countedRestartTask{admission.WrapTaskWorker(worker), builtinCalls})
			}
		}
		require.Len(t, taskWorkers, 3)
	}
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId("restart-tests").PlusWorkers(w, taskWorkers...).BuildEngine()
	require.NoError(t, err)
	svc, err := story.New(story.ServiceConfig{Engine: engine, SchemaRegistry: guard})
	require.NoError(t, err)
	var refs []artifacts.Ref
	for name, ref := range jobContext.Artifacts {
		ref.Name = name
		refs = append(refs, ref)
	}
	cellRepo := "example.test/test"
	if builtin {
		cellRepo = celltest.Repo(jobContext.GitBase.BaseRepo)
	}
	data, err := jobdb.NewTaskData(workflowctl.StartJob{GitRef: jobContext.GitBase.BaseRef, ArtifactRefs: refs, JobContext: jobContext, Inputs: inputs, TenantId: "restart-tests", RecipeName: "fixture", Cell: "test", CellRepo: cellRepo}, jobdb.NewArtifactFromBytes("fixture"+starter.RecipeArtifactSuffix, []byte(recipe)))
	require.NoError(t, err)
	celltest.Create(t, rt, "restart-tests", "test", cellRepo)
	keyHandle, err := engine.SubmitJob(ctx, jobdb.SubmitJob{TenantId: "restart-tests", JobType: "recipe", Data: data, Cell: "test", CellRepo: cellRepo})
	key := keyHandle.JobKey
	require.NoError(t, err)
	f := &restartFixture{ctx: ctx, runtime: rt, engine: engine, service: svc, worker: w, task: task, key: key, guard: guard, dbPath: dbPath, taskWorkers: taskWorkers, builtinCalls: builtinCalls}
	if task.fail.Load() || allowFailure {
		run, err := jobworkflow.GetJobForRun(ctx, rt, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: w, TaskWorkers: f.tasks(), WorkerID: "test", LeaseDuration: time.Minute})
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
	run, err := jobworkflow.GetJobForRun(f.ctx, f.runtime, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: f.worker, TaskWorkers: f.tasks(), WorkerID: "restart-test", LeaseDuration: time.Minute})
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

func TestExactJobStoryIndependentRestartAndReconciliation(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f := newRestartFixture(t, backend, `id: fixture
sequence:
 - id: work
   op: restart_context_probe
   inputs: {value: '${{ context.workflow.job_id }}'}
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
			require.Empty(t, restarted.GoalID)
			require.Equal(t, f.key.TenantId, restarted.TenantID)
			require.NotEmpty(t, restarted.JobID)
			require.NotEmpty(t, info.CellID)
			accepted, err := f.runtime.GetJob(f.ctx, jobdb.JobKey{TenantId: restarted.TenantID, JobId: restarted.JobID})
			require.NoError(t, err)
			require.Equal(t, info.CellID, accepted.CellID)
			require.Equal(t, info.Cell, accepted.Cell)
			require.Equal(t, info.CellRepo, accepted.CellRepo)
			require.NotEqual(t, info.GoalID, accepted.GoalID)
			f.task.fail.Store(false)
			f.run(t, jobdb.JobKey{TenantId: restarted.TenantID, JobId: restarted.JobID})
			goal, err = f.runtime.GetGoal(f.ctx, goalKey)
			require.NoError(t, err)
			require.Equal(t, jobdb.GoalOpen, goal.State, "independent restart cannot settle the source legacy goal")
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
			req.Goal.Mode = "new"
			fresh, err := f.service.RestartRecipeJob(f.ctx, req)
			require.NoError(t, err)
			require.NotEqual(t, restarted.JobID, fresh.JobID)
			freshKey := jobdb.JobKey{TenantId: fresh.TenantID, JobId: fresh.JobID}
			f.run(t, freshKey)
			freshStory := f.story(t, freshKey)
			require.Empty(t, freshStory.GoalID)
			require.Equal(t, fresh.JobID, freshStory.Root.Output.(map[string]any)["value"])
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

// Exercise the real broker/service/starter chain, including the complete native
// receipt which the older story response projects down to tenant/job/goal.
func TestGoalBrokerRestartReceiptReplay(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f := newRestartFixture(t, backend, restartSequenceRecipe, true)
			original, err := f.runtime.GetJob(f.ctx, f.key)
			require.NoError(t, err)
			require.Equal(t, jobdb.JobStatusCompleted, original.Status)
			engine := jobdbschema.WorkflowEngine{Engine: f.engine, Registry: f.runtime.(jobdb.JobSchemaRegistry)}
			api := &restartservice.Service{Engine: f.engine, Tenant: f.key.TenantId,
				Submit: func(ctx context.Context, key jobdb.JobKey, scope string, patch *coretask.ContextPatch, opts starter.RestartOptions) (jobref.Ref, error) {
					return starter.RestartRecipeJob(ctx, engine, key, scope, patch, opts)
				},
			}
			server, err := childbroker.Start(jobrestart.WithAPI(f.ctx, api), childbroker.Options{Submitter: engine, Current: jobcontext.Current{TenantID: f.key.TenantId, JobID: f.key.JobId, GoalID: original.GoalID}})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, server.Close()) })
			broker, found, err := jobcontext.ChildJobBrokerFromEnv(func(k string) string { return server.Env()[k] })
			require.NoError(t, err)
			require.True(t, found)
			client := childbroker.JobClient{Broker: broker}
			request := jobrestart.Request{Operation: "submit", JobID: f.key.JobId, Selector: jobrestart.Selector{Scope: starter.SubmissionRestartScopeID}, RequestID: "broker-repair"}
			first, err := client.Restart(f.ctx, request)
			require.NoError(t, err)
			require.NotNil(t, first.Ref)
			ref := *first.Ref
			require.NotEmpty(t, ref.JobID)
			require.Equal(t, jobref.Ref{TenantID: f.key.TenantId, JobID: ref.JobID, GoalID: ref.GoalID, Cell: original.Cell, CellID: original.CellID, CellRepo: original.CellRepo}, ref)
			require.NotEqual(t, original.GoalID, ref.GoalID)
			f.task.fail.Store(false)
			f.run(t, ref.Key())
			goal, err := f.runtime.GetGoal(f.ctx, jobdb.GoalKey{TenantId: ref.TenantID, GoalID: ref.GoalID})
			require.NoError(t, err)
			require.Equal(t, jobdb.GoalSucceeded, goal.State)
			replay, err := client.Restart(f.ctx, request)
			require.NoError(t, err)
			require.Equal(t, first, replay)
			started := server.StartedJobs()
			require.Len(t, started.Items, 2, "the helper records both accepted observations")
			for _, item := range started.Items {
				require.Equal(t, ref.CellID, item.CellID)
				require.Equal(t, ref.CellRepo, item.CellRepo)
				require.Equal(t, ref.Cell, item.Cell)
				require.Empty(t, item.GoalID)
				require.Equal(t, ref.JobID, item.JobID)
				require.Equal(t, ref.TenantID, item.TenantID)
			}
		})
	}
}

// Keep submission and all intermediate results small. Only the root output
// exceeds JobDB's unchanged default limit; the large literal lives in the
// retained recipe artifact, not in submission or task application data.
func newPayloadRejectedFixture(t *testing.T, backend string, probes bool) *restartFixture {
	t.Helper()
	recipe := "id: rejected_context\ninput_schema:\n submitted: {type: string}\nsequence:\n"
	for _, id := range []string{"prior", "target", "later"} {
		recipe += " - id: " + id + "\n"
		if probes {
			recipe += "   op: restart_context_probe\n   inputs: {value: accepted-" + id + "}\n"
		} else {
			recipe += "   sequence:\n    - id: persist\n      op: sleep\n      inputs: {duration: 0s}\n   outputs: {value: accepted-" + id + "}\n"
		}
	}
	recipe += "outputs:\n oversized: '" + strings.Repeat("rejected-root-", 24000) + "'\n"
	jobContext := contextual.JobContext{}
	if !probes {
		repo := t.TempDir()
		for _, args := range [][]string{{"init", "-b", "main"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "--allow-empty", "-m", "fixture"}} {
			cmd := exec.Command("git", args...)
			cmd.Dir = repo
			raw, err := cmd.CombinedOutput()
			require.NoError(t, err, string(raw))
		}
		jobContext.GitBase = contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}
	}
	f := newRestartFixtureConfigured(t, backend, recipe, map[string]any{"submitted": "retained-input"}, jobContext, "", true, !probes)
	assertPayloadRejection(t, f)
	if probes {
		require.EqualValues(t, 3, f.task.calls.Load())
	} else {
		require.EqualValues(t, 3, f.builtinCalls.Load())
	}
	return f
}

func assertPayloadRejection(t *testing.T, f *restartFixture) {
	t.Helper()
	chapters, err := f.runtime.ListChapters(f.ctx, jobdb.ListChaptersRequest{JobKey: f.key})
	require.NoError(t, err)
	require.NotEmpty(t, chapters)
	terminal := chapters[len(chapters)-1]
	require.True(t, jobdb.ChapterPayloadFailure(terminal), "must be a genuine oversized application write, not ordinary failure: %+v", terminal.Body)
	body, ok := terminal.Body.(jobdb.JobAttemptOutcomeChapter)
	require.True(t, ok)
	failure, ok := body.Outcome.(jobdb.AppErrorOutcome)
	require.True(t, ok, "error-only terminal marker cannot be accepted application output")
	require.Equal(t, jobdb.ChapterApplicationPayloadTooLargeCode, failure.Error.Attrs["code"])
	require.Contains(t, failure.Error.Message, "Application payload limit exceeded")
	raw, err := json.Marshal(failure.Error.Attrs["payloadSize"])
	require.NoError(t, err)
	var size jobdb.ChapterApplicationPayloadError
	require.NoError(t, json.Unmarshal(raw, &size))
	require.Equal(t, jobdb.DefaultMaxChapterApplicationPayloadBytes, size.LimitBytes)
	require.Greater(t, size.ActualBytes, size.LimitBytes)
	require.Equal(t, size.ActualBytes-size.LimitBytes, size.ExcessBytes)
	require.Equal(t, terminal.Ordinal, size.Ordinal)
	require.Equal(t, "result", size.RecordKind)
	require.Equal(t, `/scope:"$job"#1/attempt:1`, size.LogicalID)
	historyJSON, err := json.Marshal(chapters)
	require.NoError(t, err)
	require.NotContains(t, string(historyJSON), strings.Repeat("rejected-root-", 4), "rejected application bytes were not retained")
	require.NotEmpty(t, size.Contributors)
	run, err := f.engine.GetJobRun(f.ctx, jobdb.GetJobRunRequest{JobKey: f.key, IncludeOutputs: true})
	require.NoError(t, err)
	require.Equal(t, jobdb.JobStatusCompleted, run.Job.Status)
	require.Equal(t, "failed_app", run.Job.CompletionStatus)
	require.Equal(t, jobdb.TaskOutcomeStatusFailed, run.Attempts[len(run.Attempts)-1].Outcome.Status)
	// Native attempt reads retain diagnostic bytes in Output on errors. They
	// are classified by Outcome, and GetOutput must never accept them as data.
	diagnostic := run.Attempts[len(run.Attempts)-1].Output
	require.NotNil(t, diagnostic)
	var marker map[string]any
	require.NoError(t, json.Unmarshal(diagnostic.Data, &marker))
	require.NotContains(t, marker, "oversized")
	require.Equal(t, failure.Error.Message, marker["message"])
	require.NotNil(t, run.Attempts[len(run.Attempts)-1].Outcome.Error)
	output, err := run.GetOutput(nil, f.key.TenantId)
	require.Error(t, err)
	require.True(t, jobdb.IsChapterPayloadFailure(err), "%v", err)
	require.Nil(t, output)
	t.Logf("terminal chapter=%d code=%s actual=%d limit=%d excess=%d; completion=%s; final application output absent", terminal.Ordinal, failure.Error.Attrs["code"], size.ActualBytes, size.LimitBytes, size.ExcessBytes, run.Job.CompletionStatus)
}

func discoveredRestartScope(t *testing.T, points *jobrestart.Points, suffix string) jobrestart.RestartScope {
	t.Helper()
	var matches []jobrestart.RestartScope
	for _, point := range points.RestartScopes {
		parts := strings.Split(point.ScopeID, "/scope:")
		if strings.HasPrefix(parts[len(parts)-1], `"`+suffix+`"#`) {
			matches = append(matches, point)
		}
	}
	require.Len(t, matches, 1, "choose an exact unique ID from discovery")
	require.Empty(t, matches[0].UnavailableReason)
	return matches[0]
}

func assertAcceptedPrior(t *testing.T, visible map[string]any, probes bool) {
	t.Helper()
	seq := visible["sequence"].(map[string]any)
	require.Len(t, seq, 1)
	require.NotContains(t, seq, "target")
	require.NotContains(t, seq, "later")
	outputs := seq["prior"].(map[string]any)["outputs"].(map[string]any)
	if probes {
		require.Equal(t, map[string]any{"value": "accepted-prior", "unchanged": "keep", "api_key": "[REDACTED]"}, outputs)
	} else {
		require.Equal(t, map[string]any{"value": "accepted-prior"}, outputs)
	}
}

func TestRestartContextAfterPayloadRejection(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f := newPayloadRejectedFixture(t, backend, true)
			require.EqualValues(t, 3, f.task.calls.Load(), "prior, selected and later tasks completed before root rejection")
			accepted, err := f.engine.GetJobRun(f.ctx, jobdb.GetJobRunRequest{JobKey: f.key, IncludeOutputs: true})
			require.NoError(t, err)
			values := []string{}
			for _, entry := range accepted.Execution {
				if entry.Record.Kind != "result" || entry.Record.Output == nil || !strings.Contains(entry.Record.ID, `task:"`+restartProbeOp) {
					continue
				}
				var envelope coretask.OutputEnvelope
				require.NoError(t, json.Unmarshal(entry.Record.Output.Data, &envelope))
				var output workerops.ActivityInvocationOutput
				require.NoError(t, envelope.DecodePayload(&output))
				values = append(values, output.OpOutput["value"].(string))
			}
			require.Equal(t, []string{"accepted-prior", "accepted-target", "accepted-later"}, values)
			before := restartStoredState(t, f)
			f.guard.watch()
			counter := &restartReplayCounter{Engine: f.engine}
			svc := &restartservice.Service{Engine: counter, Tenant: f.key.TenantId}
			points, err := svc.Restart(f.ctx, jobrestart.Request{Operation: "points", JobID: f.key.JobId})
			require.NoError(t, err)
			require.Zero(t, counter.replays.Load(), "discovery uses stored metadata")
			point := discoveredRestartScope(t, points.Points, "op:target")
			snapshot := f.inspectScope(t, f.key, point.ScopeID)
			require.Equal(t, f.key.TenantId, snapshot.ProjectID)
			require.Equal(t, f.key.JobId, snapshot.JobID)
			require.Equal(t, point.ChapterOrdinal, snapshot.ChapterOrdinal)
			assertAcceptedPrior(t, snapshot.Visible, true)
			require.Contains(t, snapshot.RedactedPaths, "/visible/sequence/prior/outputs/api_key")
			// Accepted selected output is retained, but absent before that scope.
			later := discoveredRestartScope(t, points.Points, "op:later")
			laterContext := f.inspectScope(t, f.key, later.ScopeID)
			require.Equal(t, "accepted-target", laterContext.Visible["sequence"].(map[string]any)["target"].(map[string]any)["outputs"].(map[string]any)["value"])
			submission := f.inspect(t, f.key, 0)
			require.Equal(t, map[string]any{"submitted": "retained-input"}, submission.Visible["inputs"])
			require.NotContains(t, submission.Visible, "sequence")
			for _, req := range []story.GetRestartContextRequest{
				{ScopeID: "unknown-scope"}, {ChapterOrdinal: -1}, {ChapterOrdinal: 999999},
				{ScopeID: point.ScopeID, ChapterOrdinal: point.ChapterOrdinal},
			} {
				req.ProjectID, req.JobID = f.key.TenantId, f.key.JobId
				result, err := f.service.GetRestartContext(f.ctx, req)
				require.ErrorIs(t, err, story.ErrInvalidRestartBoundary)
				require.Nil(t, result)
			}
			taskOrdinal := firstTaskResultOrdinal(t, f)
			result, err := f.service.GetRestartContext(f.ctx, story.GetRestartContextRequest{ProjectID: f.key.TenantId, JobID: f.key.JobId, ChapterOrdinal: taskOrdinal})
			require.ErrorIs(t, err, story.ErrInvalidRestartBoundary)
			require.Nil(t, result)
			assertPayloadRejection(t, f)
			require.EqualValues(t, 3, f.task.calls.Load(), "no additional prior, selected, later or missing callbacks")
			f.guard.assertReadOnly(t)
			require.Equal(t, before, restartStoredState(t, f), "all persisted state and artifact bytes remain identical")
			t.Logf("GetRestartContext scope=%s chapter=%d: accepted-prior only; callback delta=0; mutation attempts=0; stored state unchanged", point.ScopeID, point.ChapterOrdinal)
		})
	}
}

func firstTaskResultOrdinal(t *testing.T, f *restartFixture) int64 {
	t.Helper()
	run, err := f.engine.GetJobRun(f.ctx, jobdb.GetJobRunRequest{JobKey: f.key, IncludeOutputs: true})
	require.NoError(t, err)
	for _, entry := range run.Execution {
		if entry.Record.Kind == "result" && strings.Contains(entry.Record.ID, `task:"`+restartProbeOp) {
			return entry.Ordinal
		}
	}
	t.Fatal("no accepted probe task result in execution metadata")
	return 0
}

type countedRestartTask struct {
	jobworkflow.TaskWorker
	calls *atomic.Int32
}

func (w countedRestartTask) Run(ctx jobworkflow.TaskContext, data jobdb.TaskData) (jobdb.TaskData, error) {
	w.calls.Add(1)
	return w.TaskWorker.Run(ctx, data)
}
func (f *restartFixture) tasks() []jobworkflow.TaskWorker {
	if f.taskWorkers != nil {
		return f.taskWorkers
	}
	return []jobworkflow.TaskWorker{compiler.NewCellContextTaskWorker(), f.task}
}
