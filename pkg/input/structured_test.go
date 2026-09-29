package input

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	coreops "github.com/colony-2/c2j/pkg/ops"
	coretask "github.com/colony-2/c2j/pkg/task"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	c2workflow "github.com/colony-2/c2j/pkg/worker/workflow"
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

type structuredWorker struct{ forms []InputForm }

func (structuredWorker) Name() string { return "recipe" }
func (w structuredWorker) Run(ctx workflow.JobContext, _ jobdb.JobData) (jobdb.JobData, error) {
	var result jobdb.TaskData
	for _, form := range w.forms {
		value, err := jsonValue(form)
		if err != nil {
			return nil, err
		}
		env, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, workerops.ActivityInvocationOutput{
			WorkspaceScopeID: "cell-b-workspace", OpOutput: value.(map[string]any),
		})
		if err != nil {
			return nil, err
		}
		prepared, err := ctx.DoTask(jobdb.DefaultRunPolicy(), "prepare", jobdb.NewTaskDataOrPanic(env))
		if err != nil {
			return nil, err
		}
		result, err = ctx.DoTask(jobdb.DefaultRunPolicy(), "input:collect_user_input", prepared)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func TestStructuredInputSQLiteAttachmentsReplayAndStaleSubmission(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "jobs.db")
	rt, err := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: dbPath})
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
	first, err := buildForm(deps, ctx, Input{Form: structuredConfig()})
	require.NoError(t, err)
	second, err := buildForm(deps, ctx, Input{Form: structuredConfig()})
	require.NoError(t, err)
	w := structuredWorker{forms: []InputForm{first, second}}
	handle, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobID: "review", JobType: "recipe", Data: jobdb.NewTaskDataOrPanic(map[string]any{}), RunPolicy: jobdb.DefaultRunPolicy()}})
	require.NoError(t, err)
	run := func() {
		runnable, err := workflow.GetJobForRun(ctx, rt, workflow.GetJobForRunRequest{JobKey: handle.JobKey, JobWorker: w, TaskWorkers: []workflow.TaskWorker{structuredPrepareTask{}}, WorkerID: "test", LeaseDuration: time.Minute})
		require.NoError(t, err)
		_, err = runnable.Run(nil)
		require.NoError(t, err)
	}
	run()
	form, err := r.GetForm(ctx, "tenant", "review")
	require.NoError(t, err)
	require.Equal(t, first.RequestID, form.RequestID)
	require.NoError(t, rt.Close(ctx))
	rt, err = sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: dbPath})
	require.NoError(t, err)
	r, engine := makeRuntime()
	restored, err := r.GetForm(ctx, "tenant", "review")
	require.NoError(t, err)
	require.Equal(t, form, restored)
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
	body, ok := chapter.Body.(jobdb.TaskAttemptOutcomeChapter)
	require.True(t, ok)
	persisted, ok := body.Outcome.(jobdb.ApplicationOutputOutcome)
	require.True(t, ok)
	encoded := persisted.Output.Data
	var persistedEnvelope coretask.OutputEnvelope
	require.NoError(t, json.Unmarshal(encoded, &persistedEnvelope))
	var persistedActivity workerops.ActivityInvocationOutput
	require.NoError(t, persistedEnvelope.DecodePayload(&persistedActivity))
	normalized, err := jsonValue(out)
	require.NoError(t, err)
	require.Equal(t, normalized, any(persistedActivity.OpOutput))
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
