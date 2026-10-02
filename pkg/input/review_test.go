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

func reviewConfig() Config {
	return Config{Kind: "review", Title: "Design review", Fields: []FormField{
		{ID: "decision", Type: FieldTypeMultipleChoice, Question: "Proceed?", Required: true, Options: []Option{{Value: "approve"}, {Value: "revise"}}},
		{ID: "document", Type: FieldTypeFileUpload, Question: "Optional document"},
	}}
}

func TestReviewFormAndAutofill(t *testing.T) {
	deps := coreops.NewOpDependenciesBuilder().Build()
	cfg := reviewConfig()
	form, err := buildForm(deps, context.Background(), Input{Form: cfg})
	require.NoError(t, err)
	require.Equal(t, "review", form.Kind)
	require.NotEmpty(t, form.RequestID)
	require.Nil(t, form.ResponseSchema)
	cfg.Fields[0].Question = "changed"
	require.Equal(t, "Proceed?", form.Fields[0].Question)
	form.Output = &Output{Fields: map[string]any{"decision": "approve"}}
	out, err := autoFillInputWithDeps(deps, context.Background(), form)
	require.NoError(t, err)
	require.Equal(t, "approve", out.Fields["decision"])
	require.NotContains(t, out.Fields, "document")
	require.Equal(t, "automation", out.Receipt.Actor.Kind)
	raw, err := jsonValue(form)
	require.NoError(t, err)
	mapped, err := GetAutoFillOp().TaskChain()[0].Invoke(deps, context.Background(), raw.(map[string]any))
	require.NoError(t, err)
	encoded, err := json.Marshal(mapped)
	require.NoError(t, err)
	var decoded Output
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.False(t, decoded.Receipt.SubmittedAt.IsZero())
	for _, fields := range []map[string]any{
		{}, {"decision": "invalid"}, {"decision": nil}, {"decision": "approve", "unknown": "value"},
		{"decision": "revise", "document": "/tmp/file.md"},
		{"decision": "approve", "document": recipeartifacts.NewExternalRef("x", "https://example.invalid/x", false)},
	} {
		form.Output = &Output{Fields: fields}
		_, err = autoFillInputWithDeps(deps, context.Background(), form)
		require.Error(t, err)
	}
}

func TestReviewConfigurationValidation(t *testing.T) {
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Kind = "unknown" },
		func(c *Config) { c.ResponseSchema = map[string]any{} },
		func(c *Config) { c.Question = "mixed" },
		func(c *Config) { c.Fields[1].ID = c.Fields[0].ID },
		func(c *Config) { c.Documents = map[string]any{"design": "/tmp/design.md"} },
		func(c *Config) {
			c.Documents = map[string]any{"design": recipeartifacts.NewExternalRef("x", "https://example.invalid/x", false)}
		},
	} {
		cfg := reviewConfig()
		mutate(&cfg)
		_, err := buildForm(coreops.NewOpDependenciesBuilder().Build(), context.Background(), Input{Form: cfg})
		require.Error(t, err)
	}
}

func TestReviewSQLiteAttachmentsReplayAndStaleSubmission(t *testing.T) {
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
	first, err := buildForm(deps, ctx, Input{Form: reviewConfig()})
	require.NoError(t, err)
	second, err := buildForm(deps, ctx, Input{Form: reviewConfig()})
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
	pageBefore, err := r.ListPendingInputsPage(ctx, "tenant", PendingInputOptions{PageSize: 1})
	require.NoError(t, err)
	require.Len(t, pageBefore.Inputs, 1)
	require.Equal(t, first.RequestedAt, pageBefore.Inputs[0].RequestedAt)
	require.NoError(t, rt.Close(ctx))
	rt, err = sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: dbPath})
	require.NoError(t, err)
	r, engine := makeRuntime()
	restored, err := r.GetForm(ctx, "tenant", "review")
	require.NoError(t, err)
	require.Equal(t, form, restored)
	pageAfter, err := r.ListPendingInputsPage(ctx, "tenant", PendingInputOptions{PageSize: 1})
	require.NoError(t, err)
	require.Equal(t, pageBefore, pageAfter)
	actor := Actor{ID: "reviewer", Kind: "human"}
	sub := FormSubmission{RequestID: form.RequestID, SubmissionID: "submit-1", Fields: map[string]any{"decision": "invalid"}}
	_, err = r.SubmitFormResponse(ctx, "tenant", "review", sub, actor)
	require.ErrorContains(t, err, "decision")
	_, err = r.GetForm(ctx, "tenant", "review")
	require.NoError(t, err)
	_, err = r.GetForm(ctx, "another-tenant", "review")
	require.Error(t, err)
	missing := recipeartifacts.NewStoredRef(jobdb.ArtifactKey{JobId: "missing-child", TaskOrdinal: 1, Name: "missing.md", SizeBytes: 1})
	sub.Fields = map[string]any{"decision": "revise", "document": missing}
	_, err = r.SubmitFormResponse(ctx, "tenant", "review", sub, actor)
	require.Error(t, err)
	_, err = r.GetForm(ctx, "tenant", "review")
	require.NoError(t, err)
	require.Error(t, r.SubmitResponse(ctx, "tenant", "review", FormResponse{Fields: map[string]any{}}))

	content := []byte("# Plan\n{++Explain recovery++}\n")
	sub.Fields = map[string]any{"decision": "revise", "document": jobdb.NewArtifactFromBytes("design.md", content)}
	out, err := r.SubmitFormResponse(ctx, "tenant", "review", sub, actor)
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
	nextPage, err := r.ListPendingInputsPage(ctx, "tenant", PendingInputOptions{PageSize: 1})
	require.NoError(t, err)
	require.Len(t, nextPage.Inputs, 1)
	require.Equal(t, second.RequestID, nextPage.Inputs[0].RequestID)
	require.NotEqual(t, pageBefore.Inputs[0].TaskOrdinal, nextPage.Inputs[0].TaskOrdinal)
	_, err = r.SubmitFormResponse(ctx, "tenant", "review", sub, actor)
	require.ErrorContains(t, err, "request_id")
	formAfter, err := r.GetForm(ctx, "tenant", "review")
	require.NoError(t, err)
	require.Equal(t, form, formAfter)
	// Reuse the previous checkpoint as a stored attachment; keep its producer key.
	sub = FormSubmission{RequestID: form.RequestID, SubmissionID: "submit-2", Fields: map[string]any{"decision": "revise", "document": ref}}
	out2, err := r.SubmitFormResponse(ctx, "tenant", "review", sub, actor)
	require.NoError(t, err)
	require.Equal(t, ref, out2.ArtifactRefs["design.md"])
	run()
}
