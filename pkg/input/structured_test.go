package input

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	coreops "github.com/colony-2/c2j/pkg/ops"
	coretask "github.com/colony-2/c2j/pkg/task"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	c2workflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	sqliteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	workflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func structuredConfig() Config {
	return Config{Request: map[string]any{"title": "Review"}, ResponseSchema: map[string]any{
		"type": "object", "required": []any{"decision"}, "additionalProperties": false,
		"properties": map[string]any{"decision": map[string]any{"enum": []any{"approve", "revise"}}, "artifact": map[string]any{"type": "object"}},
	}}
}

func TestStructuredPublicationAndAutofill(t *testing.T) {
	ctx := context.Background()
	deps := coreops.NewOpDependenciesBuilder().Build()
	cfg := structuredConfig()
	form, err := buildForm(deps, ctx, Input{Form: cfg})
	require.NoError(t, err)
	require.NotEmpty(t, form.RequestID)
	cfg.Request.(map[string]any)["title"] = "changed"
	cfg.ResponseSchema["type"] = "string"
	require.Equal(t, "Review", form.Request.(map[string]any)["title"])
	require.Equal(t, "object", form.ResponseSchema["type"])
	other, err := buildForm(deps, ctx, Input{Form: structuredConfig()})
	require.NoError(t, err)
	require.NotEqual(t, form.RequestID, other.RequestID)

	form.Output = &Output{Response: map[string]any{"decision": "approve"}}
	out, err := autoFillInputWithDeps(deps, ctx, form)
	require.NoError(t, err)
	require.Equal(t, form.RequestID, out.Receipt.RequestID)
	require.Equal(t, "automation", out.Receipt.Actor.Kind)
	form.Output.Response = map[string]any{"decision": "unknown"}
	_, err = autoFillInputWithDeps(deps, ctx, form)
	require.ErrorContains(t, err, "response")
	form.Output.Response = map[string]any{"decision": "approve", "artifact": recipeartifacts.NewExternalRef("x", "https://example.invalid/x", false)}
	_, err = autoFillInputWithDeps(deps, ctx, form)
	require.ErrorContains(t, err, "stored artifacts")
}

func TestStructuredSchemasAndModeValidation(t *testing.T) {
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Question = "question" },
		func(c *Config) { c.Default = "approve" },
		func(c *Config) { c.ResponseSchema = map[string]any{"type": "invalid"} },
		func(c *Config) { c.ResponseSchema = map[string]any{"$ref": "file:///etc/passwd"} },
		func(c *Config) { c.RequestSchema = map[string]any{"type": "string"} },
	} {
		cfg := structuredConfig()
		mutate(&cfg)
		_, err := buildForm(coreops.NewOpDependenciesBuilder().Build(), context.Background(), Input{Form: cfg})
		require.Error(t, err)
	}
	_, err := buildForm(coreops.NewOpDependenciesBuilder().Build(), context.Background(), Input{Form: Config{Request: "missing schema"}})
	require.ErrorContains(t, err, "response_schema")
}

type structuredPrepareTask struct{}

func (structuredPrepareTask) Name() string { return "prepare" }
func (structuredPrepareTask) Run(_ workflow.TaskContext, data jobdb.TaskData) (jobdb.TaskData, error) {
	return data, nil
}

type structuredWorker struct {
	forms           []InputForm
	retainArtifacts bool
}

func (structuredWorker) Name() string { return "recipe" }
func (w structuredWorker) executionWorker() workflow.ExecutionWorker {
	return workflow.ExecutionWorker{JobType: "recipe", Execute: func(ctx workflow.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
		var result jobdb.TaskData
		for i, form := range w.forms {
			raw, err := json.Marshal(form)
			if err != nil {
				return nil, err
			}
			result, err = ctx.DoTask(workflow.TaskCall{Site: fmt.Sprint("input-", i), TaskType: "input:collect_user_input", ContractVersion: 1, Command: raw}, func(context.Context) (jobdb.TaskData, error) {
				value, err := jsonValue(form)
				if err != nil {
					return nil, err
				}
				req := workerops.ActivityInvocationRequest{Input: value.(map[string]any)}
				req.GitTaskContext.Workspace = &contextual.WorkspaceContext{ScopeID: "cell-b-workspace"}
				if w.retainArtifacts {
					req.Artifacts = map[string]recipeartifacts.Ref{"application": recipeartifacts.NewExternalRef("application", "https://example.invalid/application", false)}
				}
				td, err := workerops.NewInvocationTaskData(req)
				if err != nil || !w.retainArtifacts {
					return td, err
				}
				raw, err := td.GetData()
				if err != nil {
					return nil, err
				}
				artifacts, err := td.GetArtifacts()
				if err != nil {
					return nil, err
				}
				return &jobdb.SimpleTaskData{Data: raw, Artifacts: append(artifacts, jobdb.NewArtifactFromBytes("unrelated.txt", []byte("retain")))}, nil
			})
			if err != nil {
				return nil, err
			}
		}
		return result, nil
	}}
}

func TestStructuredInputSQLiteAttachmentsReplayAndStaleSubmission(t *testing.T) {
	for _, offloaded := range []bool{false, true} {
		t.Run(fmt.Sprintf("offloaded=%t", offloaded), func(t *testing.T) { testStructuredInputSQLite(t, offloaded) })
	}
}

func testStructuredInputSQLite(t *testing.T, offloaded bool) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "jobs.db")
	rt, err := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: dbPath, MaxChapterApplicationPayloadBytes: 4 * 1024 * 1024})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rt.Close(ctx) })
	makeRuntime := func() (*Runtime, workflow.Engine) {
		engine, err := workflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
		require.NoError(t, err)
		runtime, err := NewRuntime(&c2workflow.SWFWorkflowControl{Engine: engine}, nil)
		require.NoError(t, err)
		return runtime, engine
	}
	r, _ := makeRuntime()
	deps := coreops.NewOpDependenciesBuilder().Build()
	cfg := structuredConfig()
	if offloaded {
		cfg.Request = map[string]any{"history": strings.Repeat("<", 65536)}
	}
	first, err := buildForm(deps, ctx, Input{Form: cfg})
	require.NoError(t, err)
	second, err := buildForm(deps, ctx, Input{Form: cfg})
	require.NoError(t, err)
	w := structuredWorker{forms: []InputForm{first, second}, retainArtifacts: true}
	handle, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobID: "review", JobType: "recipe", Data: jobdb.NewTaskDataOrPanic(map[string]any{}), RunPolicy: jobdb.DefaultRunPolicy()}})
	require.NoError(t, err)
	run := func() {
		runnable, err := workflow.GetJobForRun(ctx, rt, workflow.GetJobForRunRequest{JobKey: handle.JobKey, JobWorker: w.executionWorker(), TaskWorkers: []workflow.TaskWorker{structuredPrepareTask{}}, WorkerID: "test", LeaseDuration: time.Minute})
		require.NoError(t, err)
		_, err = runnable.Run(nil)
		require.NoError(t, err)
	}
	run()
	form, err := r.GetForm(ctx, "tenant", "review")
	require.NoError(t, err)
	require.Equal(t, first.RequestID, form.RequestID)
	require.NoError(t, rt.Close(ctx))
	rt, err = sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: dbPath, MaxChapterApplicationPayloadBytes: 4 * 1024 * 1024})
	require.NoError(t, err)
	r, engine := makeRuntime()
	restored, err := r.GetForm(ctx, "tenant", "review")
	require.NoError(t, err)
	require.Equal(t, form, restored)
	task, err := r.ctl.GetWaitingTask(ctx, handle.JobKey)
	require.NoError(t, err)
	td, err := task.Data()
	require.NoError(t, err)
	raw, err := td.GetData()
	require.NoError(t, err)
	var invocation workerops.ActivityInvocationRequest
	require.NoError(t, json.Unmarshal(raw, &invocation))
	require.Equal(t, offloaded, invocation.InputArtifact != "")
	if offloaded {
		attachments, err := td.GetArtifacts()
		require.NoError(t, err)
		require.Len(t, attachments, 2)
		key, err := attachments[0].ArtifactKey()
		require.NoError(t, err)
		require.Equal(t, handle.JobKey.JobId, key.JobId)
	}
	page, err := r.ListPendingInputsPage(ctx, "tenant", PendingInputOptions{PageSize: 1})
	require.NoError(t, err)
	require.Equal(t, []PendingInputOccurrence{{JobID: handle.JobKey.JobId, TaskOrdinal: task.TaskOrdinalToComplete(), RequestID: form.RequestID, RequestedAt: form.RequestedAt}}, page.Inputs)
	// Simulate transport failures on the persisted lazy view without rewriting
	// its durable wait. Submission must not call Finish or advance the worker.
	if offloaded {
		original, err := td.GetArtifacts()
		require.NoError(t, err)
		for _, mode := range []string{"missing", "unreadable"} {
			artifacts := []jobdb.Artifact(nil)
			if mode == "unreadable" {
				artifacts = []jobdb.Artifact{restorationArtifact{Artifact: original[0], read: func(context.Context) ([]byte, error) { return nil, fmt.Errorf("stored payload unavailable") }}}
			}
			finishes := 0
			brokenTask := &clientTask{key: task.JobKey(), ordinal: task.TaskOrdinalToComplete(), data: &jobdb.SimpleTaskData{Data: raw, Artifacts: artifacts}, finish: func(jobdb.TaskData) error { finishes++; return nil }}
			brokenControl := &clientControl{waiting: func(jobdb.JobKey) (workflowctl.TaskHandle, error) { return brokenTask, nil }}
			broken, err := NewRuntime(brokenControl, nil)
			require.NoError(t, err)
			_, err = broken.SubmitStructuredResponse(ctx, "tenant", "review", StructuredSubmission{RequestID: form.RequestID, SubmissionID: "broken", Response: map[string]any{"decision": "approve"}}, Actor{ID: "trusted", Kind: "human"})
			require.ErrorIs(t, err, ErrStorage)
			require.Zero(t, finishes)
			unchanged, err := r.GetForm(ctx, "tenant", "review")
			require.NoError(t, err)
			require.Equal(t, form, unchanged)
			waiting, err := r.ctl.GetWaitingTask(ctx, handle.JobKey)
			require.NoError(t, err)
			require.Equal(t, task.TaskOrdinalToComplete(), waiting.TaskOrdinalToComplete())
		}
	}
	actor := Actor{ID: "reviewer", Kind: "human"}
	sub := StructuredSubmission{RequestID: form.RequestID, SubmissionID: "submit-1", Response: map[string]any{"decision": "invalid"}}
	_, err = r.SubmitStructuredResponse(ctx, "tenant", "review", sub, actor)
	require.ErrorContains(t, err, "response")
	_, err = r.GetForm(ctx, "tenant", "review")
	require.NoError(t, err)
	_, err = r.GetForm(ctx, "another-tenant", "review")
	require.Error(t, err)
	missing := recipeartifacts.NewStoredRef(jobdb.ArtifactKey{JobId: "missing-child", TaskOrdinal: 1, Name: "missing.md", SizeBytes: 1})
	sub.Response = map[string]any{"decision": "revise", "artifact": missing}
	_, err = r.SubmitStructuredResponse(ctx, "tenant", "review", sub, actor)
	require.Error(t, err)
	_, err = r.GetForm(ctx, "tenant", "review")
	require.NoError(t, err)
	require.Error(t, r.SubmitResponse(ctx, "tenant", "review", FormResponse{Fields: map[string]any{}}))

	content := []byte("# Plan\n{++Explain recovery++}\n")
	sub.Response = map[string]any{"decision": "revise", "artifact": jobdb.NewArtifactFromBytes("design.md", content)}
	out, err := r.SubmitStructuredResponse(ctx, "tenant", "review", sub, actor)
	require.NoError(t, err)
	ref := out.ArtifactRefs["design.md"]
	key, ok := ref.StoredKey()
	require.True(t, ok)
	a := (&c2workflow.SWFWorkflowControl{Engine: engine}).GetArtifactLazy(ctx, "tenant", key)
	bytes, err := a.Bytes(ctx)
	require.NoError(t, err)
	require.Equal(t, content, bytes)
	chapter, err := rt.GetChapter(ctx, jobdb.ChapterRef{JobKey: handle.JobKey, Ordinal: key.TaskOrdinal})
	require.NoError(t, err)
	body, ok := chapter.Body.(jobdb.ExecutionChapter)
	require.True(t, ok)
	persisted, err := jobdb.DecodeExecutionRecord(body.Record)
	require.NoError(t, err)
	encoded := persisted.Output.Data
	var persistedEnvelope coretask.OutputEnvelope
	require.NoError(t, json.Unmarshal(encoded, &persistedEnvelope))
	var persistedActivity workerops.ActivityInvocationOutput
	require.NoError(t, persistedEnvelope.DecodePayload(&persistedActivity))
	normalized, err := jsonValue(out)
	require.NoError(t, err)
	require.Equal(t, normalized, any(persistedActivity.OpOutput))
	require.Equal(t, form.RequestID, out.Receipt.RequestID)
	require.Equal(t, "submit-1", out.Receipt.SubmissionID)
	require.Equal(t, actor, out.Receipt.Actor)
	require.NotContains(t, persistedActivity.ArtifactRefs, "c2j-invocation-input.json")
	require.Contains(t, persistedActivity.ArtifactRefs, "application")
	require.Len(t, persisted.Output.Artifacts, 2)
	retained := false
	for _, artifact := range persisted.Output.Artifacts {
		if artifact.Name == "unrelated.txt" {
			retained = true
			require.NotNil(t, artifact.Key)
			bytes, err := r.ctl.GetArtifactLazy(ctx, "tenant", *artifact.Key).Bytes(ctx)
			require.NoError(t, err)
			require.Equal(t, []byte("retain"), bytes)
		}
	}
	require.True(t, retained)
	for _, artifact := range persisted.Output.Artifacts {
		require.NotEqual(t, "c2j-invocation-input.json", artifact.Name)
	}
	require.Contains(t, string(encoded), "cell-b-workspace")
	require.NotContains(t, string(encoded), "Explain recovery")
	run()
	form, err = r.GetForm(ctx, "tenant", "review")
	require.NoError(t, err)
	require.Equal(t, second.RequestID, form.RequestID)
	_, err = r.SubmitStructuredResponse(ctx, "tenant", "review", sub, actor)
	require.ErrorContains(t, err, "request_id")
	formAfter, err := r.GetForm(ctx, "tenant", "review")
	require.NoError(t, err)
	require.Equal(t, form, formAfter)
	// Reuse the previous checkpoint as a stored attachment; keep its producer key.
	sub = StructuredSubmission{RequestID: form.RequestID, SubmissionID: "submit-2", Response: map[string]any{"decision": "revise", "artifact": ref}}
	out2, err := r.SubmitStructuredResponse(ctx, "tenant", "review", sub, actor)
	require.NoError(t, err)
	require.Equal(t, ref, out2.ArtifactRefs["design.md"])
	run()
}

func TestStructuredEmptySchemaAndOutputNormalization(t *testing.T) {
	cfg := Config{Request: map[string]any{"question": "Anything"}, ResponseSchema: map[string]any{}}
	deps := coreops.NewOpDependenciesBuilder().Build()
	// Go op mapping must preserve the explicit empty schema, too.
	mapped, err := GetOp().TaskChain()[0].Invoke(deps, context.Background(), map[string]any{"form": map[string]any{"request": cfg.Request, "response_schema": cfg.ResponseSchema}})
	require.NoError(t, err)
	require.Contains(t, mapped, "response_schema")
	var form InputForm
	require.NoError(t, coreops.DecodeWithJsonTags(mapped, &form))
	require.NotNil(t, form.ResponseSchema)
	form.Output = &Output{Response: false}
	accepted, err := autoFillInputWithDeps(deps, context.Background(), form)
	require.NoError(t, err)
	raw, err := jsonValue(accepted)
	require.NoError(t, err)
	normalized, err := NormalizeOutputMap(map[string]any{"form": map[string]any{"request": cfg.Request, "response_schema": cfg.ResponseSchema}}, raw.(map[string]any))
	require.NoError(t, err)
	require.Equal(t, raw, any(normalized))
}

func TestStructuredNullResponseIsPreserved(t *testing.T) {
	deps := coreops.NewOpDependenciesBuilder().Build()
	form, err := buildForm(deps, context.Background(), Input{Form: Config{Request: "Optional response", ResponseSchema: map[string]any{"type": "null"}}})
	require.NoError(t, err)
	form.Output = &Output{Response: nil}
	out, err := autoFillInputWithDeps(deps, context.Background(), form)
	require.NoError(t, err)
	raw, err := jsonValue(out)
	require.NoError(t, err)
	require.Contains(t, raw, "response")
	require.Nil(t, raw.(map[string]any)["response"])
}
