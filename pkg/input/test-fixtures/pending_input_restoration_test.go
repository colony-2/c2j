package test_fixtures_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/input"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/starter"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// Fault injection wraps only the stored payload view; durable task completion
// still belongs to the original handle and must never be attempted on failure.
type restorationFaultControl struct {
	workflowctl.WorkflowControl
	task workflowctl.TaskHandle
}

func (c restorationFaultControl) GetWaitingTask(context.Context, jobdb.JobKey) (workflowctl.TaskHandle, error) {
	return c.task, nil
}

type restorationFaultTask struct {
	workflowctl.TaskHandle
	data     jobdb.TaskData
	finishes int
}

func (t *restorationFaultTask) Data() (jobdb.TaskData, error) { return t.data, nil }
func (t *restorationFaultTask) Finish(context.Context, jobdb.TaskData) error {
	t.finishes++
	return errors.New("unexpected Finish")
}

type restorationUnreadableArtifact struct{ jobdb.Artifact }

func (restorationUnreadableArtifact) Bytes(context.Context) ([]byte, error) {
	return nil, errors.New("stored payload read failed")
}

// This runs the real input op, compiler, activity registry and SQLite worker.
// The two waits model send and finish without importing consumer policy.
func TestPendingInputRestorationRealWorker(t *testing.T) {
	ensureFixtureOps()
	ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), 45*time.Second)
	defer cancel()
	source := `id: pending-restoration
version: "1.0"
input_schema:
  history: {type: string, required: true}
inputs:
  history: "${{ inputs.history }}"
sequence:
  - id: send
    op: input
    inputs:
      form:
        request:
          history: "${{ inputs.history }}"
        response_schema:
          type: object
          required: [action]
          additionalProperties: false
          properties:
            action: {enum: [send, finish]}
  - id: finish
    op: input
    inputs:
      form:
        request:
          history: "${{ inputs.history }}"
        response_schema:
          type: object
          required: [action]
          additionalProperties: false
          properties:
            action: {enum: [send, finish]}
outputs:
  send: "${{ sequence.send.outputs }}"
  finish: "${{ sequence.finish.outputs }}"
`
	path := filepath.Join(t.TempDir(), "pending-restoration.yaml")
	require.NoError(t, os.WriteFile(path, []byte(source), 0600))
	primary, err := loadRecipeFromPath(path)
	require.NoError(t, err)
	repo, hash := createFixtureRepo(t, path, nil)
	provider, err := buildRecipeRegistry(path, primary, nil, repo, hash)
	require.NoError(t, err)
	wf := &workflow.SWFWorkflowControl{PreferRuntimeRecipeResolution: true, Registry: provider}
	deps := coreops.NewServiceDepsBuilder().WithWorkflowControl(wf).WithSSEManager(input.NewSimpleSSEManager()).Build()
	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	resolver := compiler.NewRecipeSourceResolver(compiler.RecipeSourceResolverOptions{RecipeRefResolver: compiler.NewProviderBackedRecipeRefResolver(provider)})
	workers, err := compiler.NewRecipeWorkerWithOptions(deps, registry, compiler.RecipeJobWorkerOptions{RootSourceResolver: resolver})
	require.NoError(t, err)
	db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db"), MaxChapterApplicationPayloadBytes: 4 * 1024 * 1024})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, db.Close(celltest.Context(context.Background())))
	}()
	guard := executionruntime.New(db, execution.Allocation{SchemaVersion: 1})
	taskWorkers := make([]jobworkflow.TaskWorker, 0, len(workers.TaskWorkers))
	for _, tw := range workers.TaskWorkers {
		taskWorkers = append(taskWorkers, guard.WrapTaskWorker(tw))
	}
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(guard).WithWorkerTenantId("test-tenant").WithAwaitRecycleThreshold(20*time.Millisecond).WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))).PlusWorkers(workers.JobWorker, taskWorkers...).BuildEngine()
	require.NoError(t, err)
	engine = jobdbschema.WorkflowEngine{Engine: engine, Registry: db}
	wf.Engine = engine
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); engine.Run(runCtx) }()
	defer func() { stop(); <-done }()
	runtime, err := input.NewRuntime(wf, nil)
	require.NoError(t, err)
	history := strings.Repeat("<", 65536)
	jobCtx, gitCtx := generateTestContext(repo, hash, nil, nil)
	ref, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "test-tenant", RecipeName: primary.GetMetadata().ID, Inputs: map[string]any{"history": history}, JobContext: jobCtx, GitRef: gitCtx.ParentRef, Cell: "test", CellRepo: celltest.Repo(jobCtx.GitBase.BaseRepo)}, engine, *primary)
	require.NoError(t, err)
	key := ref.Key()
	actor := input.Actor{ID: "trusted", Kind: "human"}
	var previous input.StructuredSubmission
	var previousOrdinal int64
	for _, action := range []string{"send", "finish"} {
		var form input.InputForm
		require.Eventually(t, func() bool {
			var e error
			form, e = runtime.GetForm(ctx, key.TenantId, key.JobId)
			return e == nil && form.RequestID != "" && form.RequestID != previous.RequestID
		}, 20*time.Second, 20*time.Millisecond)
		require.Equal(t, map[string]any{"history": history}, form.Request)
		task, err := wf.GetWaitingTask(ctx, key)
		require.NoError(t, err)
		require.Greater(t, task.TaskOrdinalToComplete(), previousOrdinal)
		data, err := task.Data()
		require.NoError(t, err)
		raw, err := data.GetData()
		require.NoError(t, err)
		var request workerops.ActivityInvocationRequest
		require.NoError(t, json.Unmarshal(raw, &request))
		require.Equal(t, "c2j-invocation-input.json", request.InputArtifact)
		attachments, err := data.GetArtifacts()
		require.NoError(t, err)
		require.Len(t, attachments, 1)
		_, err = attachments[0].ArtifactKey()
		require.NoError(t, err)
		if action == "send" {
			for _, mode := range []string{"missing", "unreadable"} {
				var faultArtifacts []jobdb.Artifact
				if mode == "unreadable" {
					faultArtifacts = []jobdb.Artifact{restorationUnreadableArtifact{attachments[0]}}
				}
				faultTask := &restorationFaultTask{TaskHandle: task, data: &jobdb.SimpleTaskData{Data: raw, Artifacts: faultArtifacts}}
				faulty, err := input.NewRuntime(restorationFaultControl{WorkflowControl: wf, task: faultTask}, nil)
				require.NoError(t, err)
				_, err = faulty.SubmitStructuredResponse(ctx, key.TenantId, key.JobId, input.StructuredSubmission{RequestID: form.RequestID, SubmissionID: "failure", Response: map[string]any{"action": "send"}}, actor)
				require.ErrorIs(t, err, input.ErrStorage)
				require.Zero(t, faultTask.finishes)
				waiting, err := wf.GetWaitingTask(ctx, key)
				require.NoError(t, err)
				require.Equal(t, task.TaskOrdinalToComplete(), waiting.TaskOrdinalToComplete())
			}
		}
		page, err := runtime.ListPendingInputsPage(ctx, key.TenantId, input.PendingInputOptions{PageSize: 1})
		require.NoError(t, err)
		require.Equal(t, []input.PendingInputOccurrence{{JobID: key.JobId, TaskOrdinal: task.TaskOrdinalToComplete(), RequestID: form.RequestID, RequestedAt: form.RequestedAt}}, page.Inputs)
		if previous.RequestID != "" {
			_, err = runtime.SubmitStructuredResponse(ctx, key.TenantId, key.JobId, previous, actor)
			require.ErrorIs(t, err, input.ErrStaleRequest)
		}
		sub := input.StructuredSubmission{RequestID: form.RequestID, SubmissionID: action, Response: map[string]any{"action": action}}
		out, err := runtime.SubmitStructuredResponse(ctx, key.TenantId, key.JobId, sub, actor)
		require.NoError(t, err)
		require.Equal(t, form.RequestID, out.Receipt.RequestID)
		require.Equal(t, action, out.Receipt.SubmissionID)
		require.Equal(t, actor, out.Receipt.Actor)
		chapter, err := db.GetChapter(ctx, jobdb.ChapterRef{JobKey: key, Ordinal: task.TaskOrdinalToComplete()})
		require.NoError(t, err)
		record, err := jobdb.DecodeExecutionRecord(chapter.Body.(jobdb.ExecutionChapter).Record)
		require.NoError(t, err)
		require.Empty(t, record.Output.Artifacts)
		var envelope coretask.OutputEnvelope
		require.NoError(t, json.Unmarshal(record.Output.Data, &envelope))
		var output workerops.ActivityInvocationOutput
		require.NoError(t, envelope.DecodePayload(&output))
		var accepted input.Output
		encoded, err := json.Marshal(output.OpOutput)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(encoded, &accepted))
		require.Equal(t, out, accepted)
		previous = sub
		previousOrdinal = task.TaskOrdinalToComplete()
	}
	require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, 10*time.Second, key, engine))
	result, err := wf.JobResult(ctx, key)
	require.NoError(t, err)
	values, err := readJobOutputAsMap(result)
	require.NoError(t, err)
	require.Equal(t, "send", values["send"].(map[string]any)["response"].(map[string]any)["action"])
	require.Equal(t, "finish", values["finish"].(map[string]any)["response"].(map[string]any)["action"])
}
